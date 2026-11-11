#!/usr/bin/python3 -I
"""Target-only MCP candidate activation; old instances remain available for rollback."""
import contextlib, datetime as dt, fcntl, hashlib, importlib.util, json, os, re, socket, stat, subprocess, sys, tempfile, time
from pathlib import Path

HERE=Path('/usr/local/sbin'); SELF=HERE/'zoop-schema-enable.py'
ROOT=Path('/var/lib/wecom-mcp-isolated'); APPROVALS=Path('/etc/gnas/release-approvals'); GLOCK=Path('/var/lib/gnas-oauth-isolated/release.lock')
UNIT='wecom-mcp-zhycit-admin.service'; UNIT_PATH=Path('/etc/systemd/system')/UNIT
RELEASE='20261107T-schema-admin-b605951'; SOURCE='b605951aad28f607009623c4d4809f3ade767368'
BIN_SHA='84bada3fd403b932e0c316c22aa90d161800ae4550ae482a138b5c710693e6a6'; POLICY_SHA='7d45dd145b1c4f604619046e305e031d42e807e5c7ee32450015d04123ccbba6'
CANDIDATE=ROOT/'releases'/RELEASE; PREP=ROOT/'schema-admin-enabled-prepared.json'; ACTIVATION=ROOT/'schema-admin-enabled-activation.json'; SCHEMA_ACTIVATION=ROOT/'schema-target-activation.json'
SCOPE=Path('/etc/wecom-mcp/zoop-schema-admin.env'); POLICY=Path('/etc/gnas/oauth21/zoop-doc-member-policy.json'); MEMBER_RECEIPT=ROOT/'target-document-admin-receipt-retry-20261107.json'; MEMBER_JOURNAL=ROOT/'target-document-admin-journal-retry-20261107.json'
TARGET=Path('/root/nginx/snippets/zhycit-mcp-routes.conf'); INCLUDE=Path('/root/nginx/snippets/gnas-oauth21-zhycit-isolated.conf')
OLD_PORT=b'proxy_pass http://127.0.0.1:7707;'; NEW_PORT=b'proxy_pass http://127.0.0.1:7708;'
MCP=HERE/'mcp-target-canary.py'; ROUTE=HERE/'zoop-target-cutover.py'; NEXT=HERE/'zoop-next-release.py'; ENV=HERE/'gnas-isolated-env-exec.py'; GNAS=HERE/'gnas-isolated-canary-prod.py'

def require(ok,msg):
    if not ok: raise ValueError(msg)
def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def digest(b): return hashlib.sha256(b).hexdigest()
def run(*args,timeout=25): return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=timeout).stdout

def load(name,path):
    st=path.lstat();require(path.resolve()==path and st.st_uid==0 and stat.S_ISREG(st.st_mode) and not st.st_mode&0o022,'untrusted controller')
    s=importlib.util.spec_from_file_location(name,path);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m

def unit_content():
    binary=str(CANDIDATE/'wecom-mcp-team'); policy=str(CANDIDATE/'discovery-policy.json')
    args=('/usr/bin/env GNAS_BASE_URL=http://127.0.0.1:7504 '
          '"TEAM_MCP_ADVERTISED_SCOPES=zoop.read zoop.write" TEAM_MCP_ADVERTISED_SCOPES_RESOURCE=https://zhycit.com/mcp '+binary+
          ' --gnas-discovery-policy '+policy+' --gnas-state-root /home/product/services/mcp/wecom/state/discovery --listen 127.0.0.1:7708 '
          '--gnas-ready-worker-isolation --gnas-ready-worker-timeout 5s')
    return (('[Unit]\nDescription=Isolated Zoop Schema candidate\nAfter=network-online.target gnas-oauth-zhycit-next.service\n'
        '[Service]\nType=simple\nUser=wecom-mcp-gmzoop\nGroup=wecom-mcp-gmzoop\nEnvironmentFile=/etc/wecom-mcp/gmzoop.env\nEnvironmentFile=/etc/wecom-mcp/zoop-schema-admin.env\n'
        'ExecStartPre='+args+' --check-config\nExecStart='+args+' --gnas-fleet-refresh 30s\n'
        'Restart=on-failure\nRestartSec=5s\nTimeoutStopSec=30s\nOOMPolicy=stop\nNoNewPrivileges=true\nPrivateTmp=true\nProtectHome=read-only\n'
        'ProtectSystem=strict\nProtectKernelTunables=true\nProtectKernelModules=true\nProtectControlGroups=true\nRestrictSUIDSGID=true\n'
        'LockPersonality=true\nMemoryDenyWriteExecute=true\nReadWritePaths=/home/product/services/mcp/wecom/state/discovery\n'
        'KillMode=control-group\nMemoryHigh=96M\nMemoryMax=128M\nTasksMax=96\nLimitNOFILE=4096\nCPUQuota=20%\n'
        '[Install]\nWantedBy=multi-user.target\n').encode())

def file_new(path,data,mode):
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,mode)
    with os.fdopen(fd,'wb') as f: f.write(data);f.flush();os.fsync(f.fileno())
    os.chmod(path,mode)
def replace(path,data,mode):
    fd,name=tempfile.mkstemp(prefix='.schema-target-',dir=path.parent)
    try:
        with os.fdopen(fd,'wb') as f:f.write(data);f.flush();os.fsync(f.fileno())
        os.chmod(name,mode);os.replace(name,path)
    finally:
        if os.path.exists(name): os.unlink(name)

def trusted_directory(p):
    st=p.lstat()
    return p.resolve()==p and st.st_uid==0 and stat.S_ISDIR(st.st_mode) and not st.st_mode&0o022

def valid_target(p):
    st=p.lstat()
    return p.resolve()==p and st.st_uid==0 and stat.S_ISREG(st.st_mode) and not st.st_mode&0o022

def root_owned_regular(p):
    st=p.lstat()
    return p.resolve()==p and st.st_uid==0 and stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o400

def verify_receipt(a,action,fields):
    require(re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[a-zA-Z0-9._-]{6,64}',a),'invalid receipt')
    p=APPROVALS/(a+'.json');require(root_owned_regular(p),'untrusted receipt')
    x=json.loads(p.read_bytes());expect={'schema_version':1,'approval_id':a,'action':action,'scope_sha256':sha(SCOPE),'target':'zhycit.com','source_commit':SOURCE,'binary_sha256':BIN_SHA,'controller_sha256':sha(SELF),**fields}
    require(set(x)==set(expect)|{'approved_at','expires_at','approved_by'} and all(x.get(k)==v for k,v in expect.items()),'receipt mismatch')
    now=dt.datetime.now(dt.timezone.utc);start=dt.datetime.fromisoformat(x['approved_at']);end=dt.datetime.fromisoformat(x['expires_at'])
    require(start.tzinfo and end.tzinfo and start<=now<end and end-start<=dt.timedelta(hours=1) and x['approved_by']=='owner-delegated-root','receipt expired or invalid')

def consume(a): file_new(ROOT/'used'/a,(dt.datetime.now(dt.timezone.utc).isoformat()+'\n').encode(),0o440)

def baseline(mcp,route,nextmod,helper,gnas,expected_route_sha=None):
    route.trusted_nginx_tree();shared=mcp.snapshot()
    prep=json.loads(nextmod.PREP.read_bytes());active=json.loads(SCHEMA_ACTIVATION.read_bytes()); oauth_active=json.loads(nextmod.ACTIVATION.read_bytes())
    require(sha(TARGET)==(expected_route_sha or active['new_route_sha256']) and sha(INCLUDE)==oauth_active['new_include_sha256'],'target baseline changed')
    nextp=nextmod.next_ready(gnas,mcp,helper,prep['next_grant_sha256'])
    old=mcp.properties('wecom-mcp-zhycit-schema.service');oldpid=int(old.get('MainPID','0'))
    require(old.get('ActiveState')=='active' and old.get('NRestarts')=='0' and oldpid>1 and sha(Path('/proc')/str(oldpid)/'exe')==BIN_SHA,'previous target unavailable')
    nextp=(nextp[0],oldpid)
    vector=route.other_public_vector();require(vector==(('mcp.wesiyu.com',401,200),('mcp.rtyouth.com',401,200),('mcp.jianpinke.com',503,503)),'other enterprise baseline changed')
    return shared,nextp,vector

def protected_scope_file(path):
    st=path.lstat()
    return path.resolve()==path and st.st_uid==0 and stat.S_ISREG(st.st_mode) and stat.S_IMODE(st.st_mode)==0o400

def read_scope():
    for path in (SCOPE,POLICY,MEMBER_RECEIPT,MEMBER_JOURNAL):
        require(protected_scope_file(path),'untrusted target admin scope')
    policy=json.loads(POLICY.read_bytes());receipt=json.loads(MEMBER_RECEIPT.read_bytes());journal=json.loads(MEMBER_JOURNAL.read_bytes())
    require(receipt.get('phase')=='verified' and receipt.get('target_is_admin') is True and receipt.get('other_permissions_unchanged') is True and receipt.get('plan')==journal.get('plan') and journal.get('member_user_id')==policy.get('user_id') and journal.get('document_id')==policy.get('document_id'),'document admin grant not verified')
    lines=SCOPE.read_text().splitlines();require(len(lines)==2,'invalid target admin scope')
    values=dict(line.split('=',1) for line in lines if '=' in line)
    require(set(values)=={'GNAS_SCHEMA_ADMIN_BINDING_DIGEST','GNAS_SCHEMA_ADMIN_USERID'} and re.fullmatch(r'[a-f0-9]{64}',values['GNAS_SCHEMA_ADMIN_BINDING_DIGEST']) and values['GNAS_SCHEMA_ADMIN_USERID']==policy.get('user_id'),'target admin scope mismatch')
    return values

def candidate(mcp):
    for p,expected in ((CANDIDATE/'wecom-mcp-team',BIN_SHA),(CANDIDATE/'discovery-policy.json',POLICY_SHA)):
        st=p.lstat();require(p.resolve()==p and st.st_uid==0 and stat.S_ISREG(st.st_mode) and not st.st_mode&0o022 and sha(p)==expected,'candidate mismatch')
    x=mcp.properties(UNIT);pid=int(x.get('MainPID','0'))
    require(x.get('ActiveState')=='active' and x.get('NRestarts')=='0' and pid>1 and sha(Path('/proc')/str(pid)/'exe')==BIN_SHA,'candidate not healthy')
    env=dict(row.split(b'=',1) for row in (Path('/proc')/str(pid)/'environ').read_bytes().split(b'\0') if b'=' in row)
    scope=read_scope()
    require(all(env.get(k.encode())==v.encode() for k,v in scope.items()),'target admin scope mismatch')
    require(tuple(mcp.probe(7708,h,'/healthz')[0] for h in mcp.HOSTS)==(200,200,200,503),'candidate fleet mismatch')
    code,body=mcp.probe(7708,'zhycit.com','/.well-known/oauth-protected-resource/mcp');meta=json.loads(body)
    require(code==200 and meta.get('resource')=='https://zhycit.com/mcp' and {'zoop.read','zoop.write'}.issubset(meta.get('scopes_supported',[])),'candidate metadata mismatch')
    return pid

def main(action,a):
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF,'invalid controller')
    for path in (Path('/usr'),Path('/usr/local'),HERE,Path('/etc'),Path('/etc/gnas'),Path('/etc/wecom-mcp'),APPROVALS,Path('/var/lib'),ROOT,ROOT/'releases',ROOT/'used',CANDIDATE):
        require(trusted_directory(path),'untrusted release directory')
    require(valid_target(TARGET),'untrusted target route')
    route_mode=stat.S_IMODE(TARGET.lstat().st_mode)
    read_scope()
    mcp=load('mcp_schema',MCP);route=load('route_schema',ROUTE);nextmod=load('next_schema',NEXT);helper=load('env_schema',ENV);gnas=load('gnas_schema',GNAS)
    with contextlib.ExitStack() as stack:
        for path in (GLOCK,ROOT/'release.lock'):
            f=stack.enter_context(path.open('r+'));fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
        activation=json.loads(ACTIVATION.read_bytes()) if action=='rollback' else None
        current_sha=activation['new_route_sha256'] if activation else None
        shared,nextp,vector=baseline(mcp,route,nextmod,helper,gnas,current_sha)
        old=activation['old_route'].encode() if activation else TARGET.read_bytes()
        require(old.count(OLD_PORT)==2 and NEW_PORT not in old,'unexpected route shape')
        common={'old_route_sha256':digest(old),'old_oauth_pid':nextp[0],'old_mcp_pid':nextp[1],'shared_gnas_pid':str(shared[0]),'shared_mcp_pid':shared[1],'route_mode':route_mode}
        if action=='prepare':
            require(not UNIT_PATH.exists() and not PREP.exists(),'candidate already prepared')
            memory=re.search(r'^MemAvailable:\s+([0-9]+)\s+kB$',Path('/proc/meminfo').read_text(),re.MULTILINE)
            require(memory and int(memory.group(1))>=384*1024,'insufficient available memory')
            for p,expected in ((CANDIDATE/'wecom-mcp-team',BIN_SHA),(CANDIDATE/'discovery-policy.json',POLICY_SHA)):
                st=p.lstat();require(p.resolve()==p and st.st_uid==0 and stat.S_ISREG(st.st_mode) and not st.st_mode&0o022 and sha(p)==expected,'candidate mismatch')
            sock=socket.socket()
            try:sock.bind(('127.0.0.1',7708))
            finally:sock.close()
            require(not re.search(rb'(?<![0-9])7708(?![0-9])',run('/usr/sbin/nginx','-T',timeout=20)),'candidate already routed')
            fields={**common,'unit_sha256':digest(unit_content())};verify_receipt(a,'prepare-schema-admin',fields);consume(a)
            file_new(UNIT_PATH,unit_content(),0o644)
            try:
                run('/usr/bin/systemctl','daemon-reload');run('/usr/bin/systemctl','enable',UNIT);run('/usr/bin/systemctl','start',UNIT,timeout=50)
                deadline=time.monotonic()+40
                while True:
                    try:pid=candidate(mcp);break
                    except (OSError,ValueError):
                        require(time.monotonic()<deadline,'candidate health timeout');time.sleep(1)
                require(baseline(mcp,route,nextmod,helper,gnas)==(shared,nextp,vector),'baseline changed')
                file_new(PREP,json.dumps({**fields,'candidate_pid':pid},sort_keys=True).encode(),0o440)
            except Exception:
                if not re.search(rb'(?<![0-9])7708(?![0-9])',run('/usr/sbin/nginx','-T',timeout=20)):
                    subprocess.run(['/usr/bin/systemctl','disable','--now',UNIT],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
                    UNIT_PATH.unlink(missing_ok=True)
                    run('/usr/bin/systemctl','daemon-reload')
                raise
            print(json.dumps({'state':'schema_candidate_ready','schema_admin_enabled':True,'shared_restarted':False},sort_keys=True));return
        prepared=json.loads(PREP.read_bytes());pid=prepared['candidate_pid']
        require(all(prepared.get(k)==v for k,v in common.items()),'prepared state changed')
        # A broken new process must never prevent restoring the healthy old route.
        if action!='rollback': require(candidate(mcp)==pid,'candidate process changed')
        if action=='rollback':
            current=TARGET.read_bytes();require(digest(current)==activation['new_route_sha256'] and digest(old)==activation['old_route_sha256'],'rollback route drift')
            fields={**common,'candidate_pid':pid,'new_route_sha256':digest(current),'nginx_config_sha256':digest(run('/usr/sbin/nginx','-T',timeout=20))}
            verify_receipt(a,'rollback-schema-admin',fields);consume(a)
            require(TARGET.read_bytes()==current,'route changed during rollback');replace(TARGET,old,route_mode)
            try:
                run('/usr/sbin/nginx','-t');run('/usr/sbin/nginx','-s','reload');time.sleep(1)
                require(baseline(mcp,route,nextmod,helper,gnas)==(shared,nextp,vector),'rollback baseline mismatch')
                require(route.tls_probe('zhycit.com','/mcp')==401 and route.tls_probe('zhycit.com','/.well-known/oauth-protected-resource/mcp')==200,'rollback target unavailable')
            except Exception:
                if TARGET.read_bytes()==old:
                    replace(TARGET,current,route_mode);run('/usr/sbin/nginx','-t');run('/usr/sbin/nginx','-s','reload')
                raise
            print(json.dumps({'state':'schema_target_rollback_verified','shared_restarted':False},sort_keys=True));return
        require(action=='cutover','invalid action')
        new=old.replace(OLD_PORT,NEW_PORT);ngsha=digest(run('/usr/sbin/nginx','-T',timeout=20))
        fields={**common,'candidate_pid':pid,'new_route_sha256':digest(new),'nginx_config_sha256':ngsha}
        verify_receipt(a,'cutover-schema-admin',fields);consume(a)
        try:
            require(TARGET.read_bytes()==old,'route changed during cutover');replace(TARGET,new,route_mode)
            run('/usr/sbin/nginx','-t');run('/usr/sbin/nginx','-s','reload');time.sleep(1)
            require(sha(TARGET)==digest(new) and sha(INCLUDE)==json.loads(nextmod.ACTIVATION.read_bytes())['new_include_sha256'],'route readback failed')
            require(route.tls_probe('zhycit.com','/mcp')==401 and route.tls_probe('zhycit.com','/.well-known/oauth-protected-resource/mcp')==200,'target public probe failed')
            require(route.other_public_vector()==vector and mcp.snapshot()==shared and candidate(mcp)==pid,'fleet changed')
            file_new(ACTIVATION,json.dumps({**fields,'old_route':old.decode()},sort_keys=True).encode(),0o440)
        except Exception:
            if TARGET.read_bytes()==new:
                replace(TARGET,old,route_mode);run('/usr/sbin/nginx','-t');run('/usr/sbin/nginx','-s','reload')
                require(baseline(mcp,route,nextmod,helper,gnas)==(shared,nextp,vector) and route.tls_probe('zhycit.com','/mcp')==401,'automatic rollback readback failed')
            raise
        print(json.dumps({'state':'schema_target_cutover_verified','schema_admin_enabled':True,'shared_restarted':False,'other_enterprises_unchanged':True},sort_keys=True))

if __name__=='__main__':
    try:
        require(len(sys.argv)==3 and sys.argv[1] in ('prepare','cutover','rollback'),'action and receipt required');main(sys.argv[1],sys.argv[2])
    except Exception as e:
        print('schema target release failed: '+(str(e) if isinstance(e,ValueError) else type(e).__name__),file=sys.stderr);sys.exit(1)
