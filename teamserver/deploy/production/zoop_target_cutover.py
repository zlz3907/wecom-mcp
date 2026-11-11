#!/usr/bin/python3 -I
"""Switch only zhycit MCP/OAuth Nginx locations to verified loopback canaries."""
import contextlib
import datetime as dt
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import time

HERE=Path('/usr/local/sbin')
SELF=HERE/'zoop-target-cutover.py'
MCP_CONTROL=HERE/'mcp-target-canary.py'
GNAS_CONTROL=HERE/'gnas-isolated-canary-prod.py'
HELPER=HERE/'gnas-isolated-env-exec.py'
ROOT=Path('/var/lib/wecom-mcp-isolated')
APPROVALS=Path('/etc/gnas/release-approvals')
NGINX_DIR=Path('/root/nginx/snippets')
TARGET=NGINX_DIR/'zhycit-mcp-routes.conf'
COMMON=NGINX_DIR/'gnas-oauth21-proxy.conf'
ISOLATED=NGINX_DIR/'gnas-oauth21-zhycit-isolated.conf'
GRANT=Path('/etc/gnas/oauth21/isolated-target-grant.json')
GNAS_LOCK=Path('/var/lib/gnas-oauth-isolated/release.lock')
ORIGINAL_SHA='9bfc22b102ea2a927f88bcd33424310614f9a0e955d2f11538ad981c9df8f75f'
COMMON_SHA='c6ffc62cb818e77d5e20ffe9a11f04e381483795f3d8e46f69247ac39007ef05'
MCP_SHA='48fd78e9e344554ad35f8c65107a62407b0691d2dabb42bdf1d81b60e8b3bfef'
GNAS_SHA='d5b8cb5c98e74f45e7cc54b645ae9d686fa7003e9391bcaa85317101f3d531df'
HOSTS=('mcp.wesiyu.com','mcp.rtyouth.com','mcp.jianpinke.com')


def require(v,msg):
    if not v:raise ValueError(msg)


def sha_bytes(raw):return hashlib.sha256(raw).hexdigest()

def sha(path):return sha_bytes(path.read_bytes())


def trusted(p,directory=False):
    s=p.lstat()
    require(p.resolve()==p and s.st_uid==0 and not s.st_mode&0o022 and (p.is_dir() if directory else p.is_file()),'untrusted path')


def trusted_nginx_tree(root=Path('/root'), parent=Path('/root/nginx'), snippets=NGINX_DIR):
    # Production /root/nginx is owned by the maintenance account.  /root and
    # the snippets directory both exclude group/other traversal and writes.
    # Verify these enclosing protections instead of changing live ownership.
    trusted(root,True)
    require(stat.S_IMODE(root.lstat().st_mode)&0o077==0,'root directory is traversable')
    info=parent.lstat()
    require(parent.resolve()==parent and stat.S_ISDIR(info.st_mode) and not info.st_mode&0o022,
            'untrusted nginx parent')
    trusted(snippets,True)
    require(stat.S_IMODE(snippets.lstat().st_mode)&0o077==0,'snippet directory is accessible')


def load(name,path):
    trusted(path);spec=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m


def run(*args,timeout=20):
    return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=timeout).stdout


def unit(unit):
    x=run('/usr/bin/systemctl','show',unit,'-p','MainPID','-p','ActiveState','-p','NRestarts').decode()
    return dict(line.split('=',1) for line in x.splitlines() if '=' in line)


def expected_files(original,common):
    a=b'proxy_pass http://127.0.0.1:7703;';b=b'proxy_pass http://127.0.0.1:7705;'
    c=b'include /root/nginx/snippets/gnas-oauth21-proxy.conf;'
    d=b'include /root/nginx/snippets/gnas-oauth21-zhycit-isolated.conf;'
    e=b'proxy_pass http://127.0.0.1:7501;';f=b'proxy_pass http://127.0.0.1:7503;'
    require(original.count(a)==2 and original.count(c)==3 and common.count(e)==1,'unexpected target route shape')
    return original.replace(a,b).replace(c,d),common.replace(e,f)


def tls_probe(host,path):
    out=run('/usr/bin/curl','--noproxy','*','--silent','--show-error','--max-time','7','--insecure',
            '--resolve',host+':443:127.0.0.1','--output','/dev/null','--write-out','%{http_code}',
            'https://'+host+path,timeout=10).decode()
    require(re.fullmatch(r'[1-5][0-9]{2}',out),'invalid local TLS probe');return int(out)


def other_public_vector():
    return tuple((h,tls_probe(h,'/mcp'),tls_probe(h,'/.well-known/oauth-protected-resource/mcp')) for h in HOSTS)


def active_canaries(mcp,gnas,helper,grant_sha):
    grants=helper.load_target_grant({},grant_sha)
    env_unit=unit('gnas-oauth-zhycit-isolated.service');mcp_unit=unit('wecom-mcp-zhycit-isolated.service')
    gp=int(env_unit.get('MainPID','0'));mp=int(mcp_unit.get('MainPID','0'))
    require(env_unit.get('ActiveState')=='active' and env_unit.get('NRestarts')=='0' and gp>1
            and sha(Path('/proc')/str(gp)/'exe')==GNAS_SHA,'OAuth canary unavailable')
    require(mcp_unit.get('ActiveState')=='active' and mcp_unit.get('NRestarts')=='0' and mp>1
            and sha(Path('/proc')/str(mp)/'exe')==MCP_SHA,'MCP canary unavailable')
    env=dict(x.split(b'=',1) for x in (Path('/proc')/str(gp)/'environ').read_bytes().split(b'\0') if b'=' in x)
    require(all(env.get(k.encode())==v.encode() for k,v in grants.items()),'target member grant not loaded')
    menv=dict(x.split(b'=',1) for x in (Path('/proc')/str(mp)/'environ').read_bytes().split(b'\0') if b'=' in x)
    require(menv.get(b'GNAS_BASE_URL')==b'http://127.0.0.1:7503','MCP canary authority changed')
    for port in (7501,7503):gnas.oauth_fingerprint(port)
    require(gnas.oauth_fingerprint(7501)==gnas.oauth_fingerprint(7503),'OAuth authority mismatch')
    code,raw=mcp.probe(7705,'zhycit.com','/.well-known/oauth-protected-resource/mcp');meta=json.loads(raw)
    require(code==200 and meta.get('resource')=='https://zhycit.com/mcp' and
            {'zoop.read','zoop.write'}.issubset(meta.get('scopes_supported',[])), 'target metadata unavailable')
    return gp,mp


def check_receipt(a,grant_sha,old,new,include,nginx_sha,nginx_pid,canaries,controllers):
    require(re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[a-zA-Z0-9._-]{6,64}',a),'invalid receipt')
    path=APPROVALS/(a+'.json');trusted(path);require(path.stat().st_mode&0o777==0o400,'unsafe receipt mode')
    x=json.loads(path.read_text())
    expected={'schema_version':1,'approval_id':a,'environment':'zhycit-prod-01','action':'cutover-target-oauth-mcp',
      'target':'zhycit.com','grant_sha256':grant_sha,'old_target_sha256':sha_bytes(old),
      'new_target_sha256':sha_bytes(new),'isolated_include_sha256':sha_bytes(include),'nginx_config_sha256':nginx_sha,
      'nginx_main_pid':nginx_pid,'oauth_canary_pid':canaries[0],'mcp_canary_pid':canaries[1],
      'mcp_binary_sha256':MCP_SHA,'gnas_binary_sha256':GNAS_SHA,
      'controller_sha256':sha(SELF),'dependencies_sha256':controllers}
    require(set(x)==set(expected)|{'approved_at','expires_at','approved_by'} and all(x.get(k)==v for k,v in expected.items()),'receipt does not bind cutover')
    now=dt.datetime.now(dt.timezone.utc);a1=dt.datetime.fromisoformat(x['approved_at']);e=dt.datetime.fromisoformat(x['expires_at'])
    require(a1.tzinfo and e.tzinfo and a1<=now<e and e-a1<=dt.timedelta(hours=4) and bool(x['approved_by']),'receipt expired')


def replace(path,raw,mode):
    fd,name=tempfile.mkstemp(prefix='.zoop-cutover-',dir=path.parent)
    try:
        with os.fdopen(fd,'wb') as f:f.write(raw);f.flush();os.fsync(f.fileno())
        os.chmod(name,mode);os.replace(name,path)
    finally:
        if os.path.exists(name):os.unlink(name)


def write_new(path,raw,mode):
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,mode)
    with os.fdopen(fd,'wb') as f:f.write(raw);f.flush();os.fsync(f.fileno())
    os.chmod(path,mode)


def main(approval,grant_sha):
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF and re.fullmatch(r'[a-f0-9]{64}',grant_sha),'invalid arguments')
    for p in (Path('/usr'),Path('/usr/local'),HERE,Path('/etc'),Path('/etc/gnas'),APPROVALS,Path('/root'),
              Path('/var/lib'),ROOT,ROOT/'used',Path('/etc/gnas/oauth21')):trusted(p,True)
    trusted_nginx_tree()
    for p in (SELF,MCP_CONTROL,GNAS_CONTROL,HELPER,TARGET,COMMON,GRANT,ROOT/'release.lock',GNAS_LOCK):trusted(p)
    require(GRANT.stat().st_mode&0o777==0o400 and sha(GRANT)==grant_sha,'grant changed')
    mcp=load('target_mcp_control',MCP_CONTROL);gnas=load('target_gnas_control',GNAS_CONTROL);helper=load('target_grant_helper',HELPER)
    controls={p.name:sha(p) for p in (MCP_CONTROL,GNAS_CONTROL,HELPER)}
    with contextlib.ExitStack() as stack:
        for lock_path in (GNAS_LOCK,ROOT/'release.lock'):
            lock=stack.enter_context(lock_path.open('r+'))
            fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        shared=mcp.snapshot(); canaries=active_canaries(mcp,gnas,helper,grant_sha)
        old=TARGET.read_bytes();common=COMMON.read_bytes()
        require(sha_bytes(old)==ORIGINAL_SHA and sha_bytes(common)==COMMON_SHA and not ISOLATED.exists() and not ISOLATED.is_symlink(), 'routing baseline changed')
        new,include=expected_files(old,common)
        nginx=unit('nginx.service');pid=int(nginx.get('MainPID','0'))
        require(nginx.get('ActiveState')=='active' and pid>1,'Nginx unavailable')
        config=run('/usr/sbin/nginx','-T',timeout=20);config_sha=sha_bytes(config)
        require(not re.search(rb'(?<![0-9])(?:7503|7705)(?![0-9])',config),'isolated routes already exposed')
        public=other_public_vector()
        require(public==(('mcp.wesiyu.com',401,200),('mcp.rtyouth.com',401,200),('mcp.jianpinke.com',503,503)), 'other enterprise public baseline changed')
        check_receipt(approval,grant_sha,old,new,include,config_sha,pid,canaries,controls)
        write_new(ROOT/'used'/approval,(dt.datetime.now(dt.timezone.utc).isoformat()+'\n').encode(),0o440)
        write_new(ROOT/'target-route-original.conf',old,0o440)
        write_new(ISOLATED,include,0o644)
        touched=False
        try:
            require(TARGET.read_bytes()==old and sha_bytes(run('/usr/sbin/nginx','-T',timeout=20))==config_sha,'routing changed before cutover')
            replace(TARGET,new,0o644);touched=True
            run('/usr/sbin/nginx','-t',timeout=20)
            run('/usr/bin/systemctl','reload','nginx.service',timeout=30)
            require(unit('nginx.service').get('MainPID')==str(pid),'Nginx master changed')
            require(mcp.snapshot()==shared and active_canaries(mcp,gnas,helper,grant_sha)==canaries,'service changed during cutover')
            require(other_public_vector()==public,'other enterprise public health changed')
            require(tls_probe('zhycit.com','/mcp')==401 and tls_probe('zhycit.com','/.well-known/oauth-protected-resource/mcp')==200,
                    'target public route unhealthy')
            # Graceful reload may briefly leave old workers accepting requests.
            deadline=time.monotonic()+15
            while True:
                raw=run('/usr/bin/curl','--noproxy','*','--silent','--show-error','--max-time','7','--insecure',
                        '--resolve','zhycit.com:443:127.0.0.1','https://zhycit.com/.well-known/oauth-protected-resource/mcp',timeout=10)
                metadata=json.loads(raw)
                if metadata.get('resource')=='https://zhycit.com/mcp' and {'zoop.read','zoop.write'}.issubset(metadata.get('scopes_supported',[])):
                    break
                require(time.monotonic()<deadline,'public scope metadata mismatch')
                time.sleep(0.5)
            write_new(ROOT/'target-route-activation.json',json.dumps({'approval_id':approval,'old_sha256':sha_bytes(old),
              'new_sha256':sha_bytes(new),'include_sha256':sha_bytes(include),'grant_sha256':grant_sha,'nginx_pid':pid,
              'oauth_canary_pid':canaries[0],'mcp_canary_pid':canaries[1]},sort_keys=True).encode()+b'\n',0o440)
        except Exception:
            if touched:
                # CAS prevents overwriting a concurrent owner change.
                require(TARGET.read_bytes()==new,'target changed during recovery')
                replace(TARGET,old,0o644);run('/usr/sbin/nginx','-t',timeout=20)
                run('/usr/bin/systemctl','reload','nginx.service',timeout=30)
                require(TARGET.read_bytes()==old and other_public_vector()==public,'route rollback did not verify')
            raise
        print(json.dumps({'state':'target_route_cutover_verified','target':'zhycit.com','shared_restarted':False,
                          'other_enterprise_public_vector_unchanged':True,'approval_id':approval},sort_keys=True))


def rollback(approval):
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF and
            re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[a-zA-Z0-9._-]{6,64}',approval),'invalid rollback arguments')
    for p in (Path('/usr'),Path('/usr/local'),HERE,Path('/etc'),Path('/etc/gnas'),APPROVALS,Path('/root'),
              Path('/var/lib'),ROOT,ROOT/'used'):trusted(p,True)
    trusted_nginx_tree()
    backup=ROOT/'target-route-original.conf';activation=ROOT/'target-route-activation.json'
    for p in (SELF,MCP_CONTROL,TARGET,COMMON,ISOLATED,backup,activation,ROOT/'release.lock'):trusted(p)
    mcp=load('rollback_mcp_control',MCP_CONTROL)
    with (ROOT/'release.lock').open('r+') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        state=json.loads(activation.read_bytes());old=backup.read_bytes();common=COMMON.read_bytes();new,include=expected_files(old,common)
        require(sha_bytes(old)==ORIGINAL_SHA and sha_bytes(common)==COMMON_SHA and
                TARGET.read_bytes()==new and ISOLATED.read_bytes()==include and
                state.get('old_sha256')==sha_bytes(old) and state.get('new_sha256')==sha_bytes(new) and
                state.get('include_sha256')==sha_bytes(include),'rollback baseline changed')
        shared=mcp.snapshot();public=other_public_vector();nginx=unit('nginx.service');pid=int(nginx.get('MainPID','0'))
        require(nginx.get('ActiveState')=='active' and pid>1,'Nginx unavailable')
        receipt_path=APPROVALS/(approval+'.json');trusted(receipt_path)
        require(receipt_path.stat().st_mode&0o777==0o400,'unsafe receipt mode')
        x=json.loads(receipt_path.read_bytes())
        expected={'schema_version':1,'approval_id':approval,'environment':'zhycit-prod-01',
          'action':'rollback-target-oauth-mcp','target':'zhycit.com','activation_sha256':sha(activation),
          'old_target_sha256':sha_bytes(old),'new_target_sha256':sha_bytes(new),'nginx_main_pid':pid,
          'controller_sha256':sha(SELF),'mcp_controller_sha256':sha(MCP_CONTROL)}
        require(set(x)==set(expected)|{'approved_at','expires_at','approved_by'} and all(x.get(k)==v for k,v in expected.items()),'receipt does not bind rollback')
        now=dt.datetime.now(dt.timezone.utc);a=dt.datetime.fromisoformat(x['approved_at']);e=dt.datetime.fromisoformat(x['expires_at'])
        require(a.tzinfo and e.tzinfo and a<=now<e and e-a<=dt.timedelta(hours=4) and bool(x['approved_by']),'receipt expired')
        write_new(ROOT/'used'/approval,(now.isoformat()+'\n').encode(),0o440)
        try:
            require(TARGET.read_bytes()==new,'target changed before rollback')
            replace(TARGET,old,0o644);run('/usr/sbin/nginx','-t',timeout=20);run('/usr/bin/systemctl','reload','nginx.service',timeout=30)
            require(unit('nginx.service').get('MainPID')==str(pid) and mcp.snapshot()==shared and
                    other_public_vector()==public and tls_probe('zhycit.com','/mcp')==401,'rollback health mismatch')
            write_new(ROOT/'target-route-rolled-back.json',json.dumps({'approval_id':approval,'activation_sha256':sha(activation)},sort_keys=True).encode()+b'\n',0o440)
        except Exception:
            if TARGET.read_bytes()==old:
                replace(TARGET,new,0o644);run('/usr/sbin/nginx','-t',timeout=20);run('/usr/bin/systemctl','reload','nginx.service',timeout=30)
            raise
        print(json.dumps({'state':'target_route_rollback_verified','shared_restarted':False,'approval_id':approval},sort_keys=True))


if __name__=='__main__':
    try:
        if len(sys.argv)==3 and sys.argv[1]=='rollback':rollback(sys.argv[2])
        elif len(sys.argv)==3:main(sys.argv[1],sys.argv[2])
        else:raise ValueError('exact action and receipt required')
    except Exception as e:
        print('target route action refused or failed: '+(str(e) if isinstance(e,ValueError) else type(e).__name__),file=sys.stderr)
        sys.exit(1)
