#!/usr/bin/python3 -I
"""Start and switch one target-only Zoop instance with expanded member tools."""
import contextlib
import datetime as dt
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import socket
import stat
import subprocess
import sys
import tempfile
import time
import types

HERE=Path('/usr/local/sbin');SELF=HERE/'zoop-next-release.py';NEXT_ENV=HERE/'zoop-next-env-exec.py'
GNAS=HERE/'gnas-isolated-canary-prod.py';ENV=HERE/'gnas-isolated-env-exec.py';MCP=HERE/'mcp-target-canary.py';ROUTE=HERE/'zoop-target-cutover.py'
ROOT=Path('/var/lib/wecom-mcp-isolated');GLOCK=Path('/var/lib/gnas-oauth-isolated/release.lock');APPROVALS=Path('/etc/gnas/release-approvals')
OLD_GRANT=Path('/etc/gnas/oauth21/isolated-target-grant.json');NEXT_GRANT=Path('/etc/gnas/oauth21/isolated-target-grant-v2.json')
GUNIT=Path('/etc/systemd/system/gnas-oauth-zhycit-next.service');MUNIT=Path('/etc/systemd/system/wecom-mcp-zhycit-next.service')
WANTS=Path('/etc/systemd/system/multi-user.target.wants');PREP=ROOT/'target-next-prepared.json';ACTIVATION=ROOT/'target-next-activation.json'
OLD_SHA='5dcf443f4fedf42f2c75513c5d2216e9c12b6b126472ac546dce45f6afcc3a93'
RID='20261106T203247Z-932ca8583c69';GSHA='d5b8cb5c98e74f45e7cc54b645ae9d686fa7003e9391bcaa85317101f3d531df'
OLD_RID='20261106T040805Z-1acaa4269334';OLD_GSHA='f80fa1116cebd1b55523782259f626450deef7e2c23818c4bec92840b0d173a0'
ADDITIONS=('wecom_schema_registry_update','wecom_schema_migration_preview','wecom_schema_migration_apply','wecom_send_app_message','wecom_send_app_media_message')
EXISTING=frozenset({'wecom_field_codec_lab_create','wecom_field_codec_lab_reference_write_probe','wecom_field_codec_lab_register',
 'wecom_field_codec_lab_replay_probe','wecom_field_codec_lab_write_probe','wecom_record_apply','wecom_requirement_progress_reconcile'})


def require(v,msg):
    if not v:raise ValueError(msg)


def sha(p):
    h=hashlib.sha256()
    with p.open('rb') as f:
        for b in iter(lambda:f.read(1048576),b''):h.update(b)
    return h.hexdigest()

def digest(b):return hashlib.sha256(b).hexdigest()


def trusted(p,directory=False):
    s=p.lstat();require(p.resolve()==p and s.st_uid==0 and not s.st_mode&0o022 and
       (stat.S_ISDIR(s.st_mode) if directory else stat.S_ISREG(s.st_mode)),'untrusted next control path')


def load(name,p):
    trusted(p);s=importlib.util.spec_from_file_location(name,p);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m


def run(*args,timeout=30):
    return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=timeout).stdout


def props(unit):
    x=run('/usr/bin/systemctl','show',unit,'-p','MainPID','-p','ActiveState','-p','NRestarts').decode()
    return dict(s.split('=',1) for s in x.splitlines() if '=' in s)


def grant_bytes(helper):
    trusted(OLD_GRANT);require(stat.S_IMODE(OLD_GRANT.stat().st_mode)==0o400 and sha(OLD_GRANT)==OLD_SHA,'current member grant changed')
    x=json.loads(OLD_GRANT.read_bytes());helper.add_target_grant({},json.dumps(x).encode())
    require(set(x['tools'])==EXISTING and len(x['tools'])==len(EXISTING),'current tool grant changed')
    x['tools']=list(x['tools'])+list(ADDITIONS);raw=(json.dumps(x,separators=(',',':'))+'\n').encode();helper.add_target_grant({},raw)
    return raw


def plans(gnas,mcp,next_sha):
    plan=types.SimpleNamespace(release_id=RID,binary_sha256=GSHA,baseline_release_id=OLD_RID,baseline_binary_sha256=OLD_GSHA,
                               host='zhycit.com',listen='127.0.0.1:7504',grant_sha256=next_sha)
    g=gnas.unit_content(plan);old=b'/usr/local/sbin/gnas-isolated-env-exec.py';new=b'/usr/local/sbin/zoop-next-env-exec.py'
    require(g.count(old)==1 and g.count(b'Restart=no\n')==1 and g.count(b'After=network-online.target\n')==1,'unexpected OAuth unit')
    g=g.replace(old,new).replace(b'Restart=no\n',b'Restart=on-failure\nRestartSec=5s\n').replace(
      b'After=network-online.target\n',b'After=network-online.target gnas-server.service\n')+b'[Install]\nWantedBy=multi-user.target\n'
    c=mcp.unit_content();require(c.count(b'127.0.0.1:7503')==2 and c.count(b'127.0.0.1:7705')==2
        and c.count(b'gnas-oauth-zhycit-isolated.service')==1 and c.count(b'Restart=no\n')==1,'unexpected MCP unit')
    c=c.replace(b'127.0.0.1:7503',b'127.0.0.1:7504').replace(b'127.0.0.1:7705',b'127.0.0.1:7706').replace(
      b'gnas-oauth-zhycit-isolated.service',b'gnas-oauth-zhycit-next.service').replace(
      b'Restart=no\n',b'Restart=on-failure\nRestartSec=5s\n')+b'[Install]\nWantedBy=multi-user.target\n'
    return g,c


def next_unexposed():
    return not re.search(rb'(?<![0-9])(?:7504|7706)(?![0-9])',run('/usr/sbin/nginx','-T',timeout=20))


def port_free(port):
    s=socket.socket()
    try:s.bind(('127.0.0.1',port));return True
    except OSError:return False
    finally:s.close()


def available_memory_kib():
    m=re.search(r'^MemAvailable:\s+([0-9]+)\s+kB$',Path('/proc/meminfo').read_text(),re.MULTILINE)
    require(m is not None,'memory availability unknown')
    return int(m.group(1))


def write_new(p,data,mode):
    fd=os.open(p,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,mode)
    with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fsync(f.fileno())
    os.chmod(p,mode)


def replace(p,data,mode):
    fd,name=tempfile.mkstemp(prefix='.zoop-next-',dir=p.parent)
    try:
        with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fsync(f.fileno())
        os.chmod(name,mode);os.replace(name,p)
    finally:
        if os.path.exists(name):os.unlink(name)


def check_receipt(a,action,fields):
    require(re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[a-zA-Z0-9._-]{6,64}',a),'invalid approval')
    p=APPROVALS/(a+'.json');trusted(p);require(stat.S_IMODE(p.stat().st_mode)==0o400,'unsafe approval')
    x=json.loads(p.read_bytes());expected={'schema_version':1,'approval_id':a,'environment':'zhycit-prod-01','action':action,
       'target':'zhycit.com','controller_sha256':sha(SELF),'dependencies_sha256':deps(),**fields}
    require(set(x)==set(expected)|{'approved_at','expires_at','approved_by'} and all(x.get(k)==v for k,v in expected.items()),'approval binding mismatch')
    now=dt.datetime.now(dt.timezone.utc);start=dt.datetime.fromisoformat(x['approved_at']);end=dt.datetime.fromisoformat(x['expires_at'])
    require(start.tzinfo and end.tzinfo and start<=now<end and end-start<=dt.timedelta(hours=4) and bool(x['approved_by']),'approval expired')


def deps():return {p.name:sha(p) for p in (NEXT_ENV,GNAS,ENV,MCP,ROUTE)}


def current(mcp,gnas,route,helper):
    shared=mcp.snapshot();oldp=route.active_canaries(mcp,gnas,helper,OLD_SHA)
    previous=json.loads((ROOT/'target-route-activation.json').read_bytes());route.trusted_nginx_tree()
    require(route.sha(route.TARGET)==previous['new_sha256'] and route.sha(route.ISOLATED)==previous['include_sha256']
        and previous['grant_sha256']==OLD_SHA,'current route baseline changed')
    vector=route.other_public_vector();require(vector==(('mcp.wesiyu.com',401,200),('mcp.rtyouth.com',401,200),('mcp.jianpinke.com',503,503)), 'other enterprise baseline changed')
    nginx=route.unit('nginx.service');pid=int(nginx.get('MainPID','0'));require(nginx.get('ActiveState')=='active' and pid>1,'Nginx unavailable')
    return shared,oldp,vector,pid


def next_ready(gnas,mcp,helper,next_sha):
    g=props(GUNIT.name);c=props(MUNIT.name);gp=int(g.get('MainPID','0'));mp=int(c.get('MainPID','0'))
    require(g.get('ActiveState')=='active' and g.get('NRestarts')=='0' and gp>1 and sha(Path('/proc')/str(gp)/'exe')==GSHA,'next OAuth unavailable')
    require(c.get('ActiveState')=='active' and c.get('NRestarts')=='0' and mp>1 and sha(Path('/proc')/str(mp)/'exe')==mcp.BINARY_SHA,'next MCP unavailable')
    expected=helper.add_target_grant({},NEXT_GRANT.read_bytes());env=dict(x.split(b'=',1) for x in (Path('/proc')/str(gp)/'environ').read_bytes().split(b'\0') if b'=' in x)
    require(all(env.get(k.encode())==v.encode() for k,v in expected.items()),'next member grant not loaded')
    mcp_env=dict(x.split(b'=',1) for x in (Path('/proc')/str(mp)/'environ').read_bytes().split(b'\0') if b'=' in x)
    require(mcp_env.get(b'GNAS_BASE_URL')==b'http://127.0.0.1:7504','next MCP authority mismatch')
    require(gnas.oauth_fingerprint(7503)==gnas.oauth_fingerprint(7504),'next OAuth keys differ')
    codes=tuple(mcp.probe(7706,h,'/healthz')[0] for h in mcp.HOSTS);require(codes==(200,200,200,503),'next tenant health mismatch')
    code,raw=mcp.probe(7706,'zhycit.com','/.well-known/oauth-protected-resource/mcp');meta=json.loads(raw)
    require(code==200 and meta.get('resource')=='https://zhycit.com/mcp' and {'zoop.read','zoop.write'}.issubset(meta.get('scopes_supported',[])),'next metadata mismatch')
    return gp,mp


def verify_paths():
    for p in (Path('/usr'),Path('/usr/local'),HERE,Path('/etc'),Path('/etc/gnas'),APPROVALS,Path('/etc/gnas/oauth21'),
              Path('/etc/systemd'),Path('/etc/systemd/system'),WANTS,Path('/var/lib'),ROOT,ROOT/'used'):trusted(p,True)
    for p in (SELF,NEXT_ENV,GNAS,ENV,MCP,ROUTE,OLD_GRANT,GLOCK,ROOT/'release.lock',ROOT/'target-route-activation.json'):trusted(p)


def lock_all(stack):
    for p in (GLOCK,ROOT/'release.lock'):
        f=stack.enter_context(p.open('r+'));fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)


def prepare_fields(raw,g,c,oldp,nginx_sha):
    return {'current_grant_sha256':OLD_SHA,'next_grant_sha256':digest(raw),'old_oauth_pid':oldp[0],'old_mcp_pid':oldp[1],
            'next_oauth_unit_sha256':digest(g),'next_mcp_unit_sha256':digest(c),'nginx_config_sha256':nginx_sha}


def prepare(a):
    verify_paths();helper=load('old_env',ENV);g=load('gnas_adapter',GNAS);m=load('mcp_adapter',MCP);r=load('route_adapter',ROUTE)
    with contextlib.ExitStack() as stack:
        lock_all(stack);shared,oldp,vector,ngpid=current(m,g,r,helper)
        require(next_unexposed() and port_free(7504) and port_free(7706),'next listeners unavailable')
        require(available_memory_kib()>=448*1024,'insufficient memory for parallel instances')
        for p in (NEXT_GRANT,GUNIT,MUNIT,PREP,WANTS/GUNIT.name,WANTS/MUNIT.name):require(not p.exists() and not p.is_symlink(),'next destination exists')
        raw=grant_bytes(helper);next_sha=digest(raw);gu,mu=plans(g,m,next_sha);nginx_sha=digest(run('/usr/sbin/nginx','-T',timeout=20))
        fields=prepare_fields(raw,gu,mu,oldp,nginx_sha);check_receipt(a,'prepare-zoop-next',fields)
        write_new(ROOT/'used'/a,(dt.datetime.now(dt.timezone.utc).isoformat()+'\n').encode(),0o440)
        started=False
        try:
            write_new(NEXT_GRANT,raw,0o400);write_new(GUNIT,gu,0o644);write_new(MUNIT,mu,0o644)
            run('/usr/bin/systemctl','daemon-reload');run('/usr/bin/systemctl','enable',GUNIT.name,MUNIT.name)
            run('/usr/bin/systemctl','start',GUNIT.name,timeout=50);started=True
            deadline=time.monotonic()+40
            while True:
                try:
                    if g.oauth_fingerprint(7504)==g.oauth_fingerprint(7503):break
                except Exception:pass
                require(time.monotonic()<deadline,'next OAuth not ready');time.sleep(1)
            run('/usr/bin/systemctl','start',MUNIT.name,timeout=50);deadline=time.monotonic()+60
            while True:
                try:nextp=next_ready(g,m,helper,next_sha);break
                except Exception:
                    require(time.monotonic()<deadline,'next MCP not ready');time.sleep(1)
            require(current(m,g,r,helper)==(shared,oldp,vector,ngpid) and next_unexposed(),'current services changed')
            for p in (GUNIT,MUNIT):
                require((WANTS/p.name).is_symlink() and (WANTS/p.name).resolve()==p and run('/usr/bin/systemctl','is-enabled',p.name).decode().strip()=='enabled','next boot enablement missing')
            data={**fields,'next_oauth_pid':nextp[0],'next_mcp_pid':nextp[1],'nginx_main_pid':ngpid,'approval_id':a}
            write_new(PREP,json.dumps(data,sort_keys=True).encode()+b'\n',0o440)
        except Exception:
            if next_unexposed():
                try:run('/usr/bin/systemctl','stop',MUNIT.name,GUNIT.name,timeout=50)
                except Exception:pass
                for unit,expected in ((GUNIT,gu),(MUNIT,mu)):
                    link=WANTS/unit.name
                    if link.is_symlink() and link.resolve()==unit:link.unlink()
                    if unit.exists() and unit.read_bytes()==expected:unit.unlink()
                if NEXT_GRANT.exists() and NEXT_GRANT.read_bytes()==raw:NEXT_GRANT.unlink()
                try:run('/usr/bin/systemctl','daemon-reload')
                except Exception:pass
            raise
        print(json.dumps({'state':'next_target_ready','public_route_changed':False,'shared_restarted':False,'new_tool_count':len(ADDITIONS),'approval_id':a},sort_keys=True))


def cutover_fields(prep,old_target,old_include,new_target,new_include,nginx_sha,nextp):
    return {'prepare_sha256':sha(PREP),'current_grant_sha256':OLD_SHA,'next_grant_sha256':prep['next_grant_sha256'],
      'old_target_sha256':digest(old_target),'old_include_sha256':digest(old_include),'new_target_sha256':digest(new_target),
      'new_include_sha256':digest(new_include),'nginx_config_sha256':nginx_sha,'next_oauth_pid':nextp[0],'next_mcp_pid':nextp[1]}


def cutover(a):
    verify_paths();trusted(PREP);trusted(NEXT_GRANT);trusted(GUNIT);trusted(MUNIT)
    helper=load('old_env',ENV);g=load('gnas_adapter',GNAS);m=load('mcp_adapter',MCP);r=load('route_adapter',ROUTE)
    with contextlib.ExitStack() as stack:
        lock_all(stack);shared,oldp,vector,ngpid=current(m,g,r,helper);prep=json.loads(PREP.read_bytes());next_sha=prep['next_grant_sha256']
        require(sha(NEXT_GRANT)==next_sha and NEXT_GRANT.read_bytes()==grant_bytes(helper),'next grant changed')
        nextp=next_ready(g,m,helper,next_sha);require(list(nextp)==[prep['next_oauth_pid'],prep['next_mcp_pid']],'next process changed')
        for path in (ACTIVATION,ROOT/'target-next-previous-route.conf',ROOT/'target-next-previous-oauth.conf'):
            require(not path.exists() and not path.is_symlink(),'next activation already exists')
        target=r.TARGET.read_bytes();include=r.ISOLATED.read_bytes()
        require(target.count(b'proxy_pass http://127.0.0.1:7705;')==2 and include.count(b'proxy_pass http://127.0.0.1:7503;')==1,'unexpected current route')
        newtarget=target.replace(b'proxy_pass http://127.0.0.1:7705;',b'proxy_pass http://127.0.0.1:7706;')
        newinclude=include.replace(b'proxy_pass http://127.0.0.1:7503;',b'proxy_pass http://127.0.0.1:7504;')
        nginx_sha=digest(run('/usr/sbin/nginx','-T',timeout=20));fields=cutover_fields(prep,target,include,newtarget,newinclude,nginx_sha,nextp)
        check_receipt(a,'cutover-zoop-next',fields);write_new(ROOT/'used'/a,(dt.datetime.now(dt.timezone.utc).isoformat()+'\n').encode(),0o440)
        write_new(ROOT/'target-next-previous-route.conf',target,0o440);write_new(ROOT/'target-next-previous-oauth.conf',include,0o440)
        touched=False
        try:
            require(r.TARGET.read_bytes()==target and r.ISOLATED.read_bytes()==include and digest(run('/usr/sbin/nginx','-T',timeout=20))==nginx_sha,'route changed before cutover')
            replace(r.ISOLATED,newinclude,0o644);touched=True;replace(r.TARGET,newtarget,0o644)
            run('/usr/sbin/nginx','-t',timeout=20);run('/usr/bin/systemctl','reload','nginx.service',timeout=30)
            require(r.unit('nginx.service').get('MainPID')==str(ngpid) and m.snapshot()==shared and r.other_public_vector()==vector
                    and r.active_canaries(m,g,helper,OLD_SHA)==oldp,'shared service changed')
            require(next_ready(g,m,helper,next_sha)==nextp and r.tls_probe('zhycit.com','/mcp')==401 and r.tls_probe('zhycit.com','/.well-known/oauth-protected-resource/mcp')==200,'next public route unavailable')
            data={**fields,'old_oauth_pid':oldp[0],'old_mcp_pid':oldp[1],'nginx_main_pid':ngpid,'approval_id':a}
            write_new(ACTIVATION,json.dumps(data,sort_keys=True).encode()+b'\n',0o440)
        except Exception:
            if touched:
                require(r.TARGET.read_bytes() in (target,newtarget) and r.ISOLATED.read_bytes() in (include,newinclude),'route changed during recovery')
                replace(r.ISOLATED,include,0o644);replace(r.TARGET,target,0o644);run('/usr/sbin/nginx','-t',timeout=20);run('/usr/bin/systemctl','reload','nginx.service',timeout=30)
                require(r.other_public_vector()==vector,'other enterprise health changed during recovery')
            raise
        print(json.dumps({'state':'next_target_cutover_verified','public_route_changed':True,'shared_restarted':False,'other_enterprises_unchanged':True,'approval_id':a},sort_keys=True))


def rollback(a):
    verify_paths();trusted(PREP);trusted(ACTIVATION);trusted(NEXT_GRANT)
    helper=load('old_env',ENV);g=load('gnas_adapter',GNAS);m=load('mcp_adapter',MCP);r=load('route_adapter',ROUTE)
    previous_target=ROOT/'target-next-previous-route.conf';previous_include=ROOT/'target-next-previous-oauth.conf'
    trusted(previous_target);trusted(previous_include)
    with contextlib.ExitStack() as stack:
        lock_all(stack);shared=m.snapshot();vector=r.other_public_vector();oldp=r.active_canaries(m,g,helper,OLD_SHA)
        prep=json.loads(PREP.read_bytes());active=json.loads(ACTIVATION.read_bytes());next_sha=prep['next_grant_sha256']
        nextp=next_ready(g,m,helper,next_sha);target=r.TARGET.read_bytes();include=r.ISOLATED.read_bytes()
        oldtarget=previous_target.read_bytes();oldinclude=previous_include.read_bytes()
        require(digest(target)==active['new_target_sha256'] and digest(include)==active['new_include_sha256'] and
                digest(oldtarget)==active['old_target_sha256'] and digest(oldinclude)==active['old_include_sha256'], 'rollback route changed')
        nginx=r.unit('nginx.service');pid=int(nginx.get('MainPID','0'));require(nginx.get('ActiveState')=='active' and pid>1,'Nginx unavailable')
        fields={'activation_sha256':sha(ACTIVATION),'old_target_sha256':digest(oldtarget),'old_include_sha256':digest(oldinclude),
          'new_target_sha256':digest(target),'new_include_sha256':digest(include),'old_oauth_pid':oldp[0],'old_mcp_pid':oldp[1],
          'next_oauth_pid':nextp[0],'next_mcp_pid':nextp[1],'nginx_main_pid':pid}
        check_receipt(a,'rollback-zoop-next',fields);write_new(ROOT/'used'/a,(dt.datetime.now(dt.timezone.utc).isoformat()+'\n').encode(),0o440)
        touched=False
        try:
            require(r.TARGET.read_bytes()==target and r.ISOLATED.read_bytes()==include,'route changed before rollback')
            replace(r.ISOLATED,oldinclude,0o644);touched=True;replace(r.TARGET,oldtarget,0o644)
            run('/usr/sbin/nginx','-t',timeout=20);run('/usr/bin/systemctl','reload','nginx.service',timeout=30)
            require(r.unit('nginx.service').get('MainPID')==str(pid) and m.snapshot()==shared and r.other_public_vector()==vector
                    and r.active_canaries(m,g,helper,OLD_SHA)==oldp,'rollback health mismatch')
            write_new(ROOT/'target-next-rolled-back.json',json.dumps({'approval_id':a,'activation_sha256':sha(ACTIVATION)},sort_keys=True).encode()+b'\n',0o440)
        except Exception:
            if touched and r.TARGET.read_bytes() in (target,oldtarget) and r.ISOLATED.read_bytes() in (include,oldinclude):
                replace(r.ISOLATED,include,0o644);replace(r.TARGET,target,0o644)
                run('/usr/sbin/nginx','-t',timeout=20);run('/usr/bin/systemctl','reload','nginx.service',timeout=30)
            raise
        print(json.dumps({'state':'next_target_rollback_verified','shared_restarted':False,'approval_id':a},sort_keys=True))


def main():
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF and len(sys.argv)==3,'invalid next release arguments')
    if sys.argv[1]=='prepare':prepare(sys.argv[2])
    elif sys.argv[1]=='cutover':cutover(sys.argv[2])
    elif sys.argv[1]=='rollback':rollback(sys.argv[2])
    else:raise ValueError('unknown next release action')


if __name__=='__main__':
    try:main()
    except Exception as e:
        print('next target release refused or failed: '+(str(e) if isinstance(e,ValueError) else type(e).__name__),file=sys.stderr);sys.exit(1)
