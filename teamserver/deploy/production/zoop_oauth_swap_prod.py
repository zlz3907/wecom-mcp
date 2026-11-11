#!/usr/bin/python3 -I
"""Controlled target-only OAuth prepare, deploy, and interrupted recovery.

All mutable production actions require a root-owned exact receipt and the
original release locks. This program never changes Nginx or shared services.
"""
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
import time
import types

HERE=Path('/usr/local/sbin');SELF=HERE/'zoop-oauth-swap-prod.py'
ROOT=Path('/var/lib/gnas-oauth-isolated');MAIN=Path('/home/product/services/gnas-service-gin')
MCP_ROOT=Path('/var/lib/wecom-mcp-isolated');APPROVALS=Path('/etc/gnas/release-approvals')
INCOMING=Path('/var/lib/gnas-deployer/incoming');UNIT=Path('/etc/systemd/system/gnas-oauth-zhycit-next.service')
PROBE=Path('/etc/systemd/system/gnas-oauth-zhycit-recovery-probe.service');PREP=ROOT/'open-consent-prepared.json'
JOURNAL=ROOT/'open-consent-transaction.json';SUCCESS=ROOT/'open-consent-success.json';ROLLED=ROOT/'open-consent-rolled-back.json'
ROLLBACK_START=ROOT/'open-consent-manual-rollback-started.json'
RID='20261108T144914Z-e3620ffda6aa';SHA='cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1'
MANIFEST='0c6903f7f2161c190a1a7ec4af5d6dc976d8a271f58210e13d7422f5ff919e7f'
OLD='20261106T203247Z-932ca8583c69';OLD_SHA='d5b8cb5c98e74f45e7cc54b645ae9d686fa7003e9391bcaa85317101f3d531df'
MAIN_RID='20261107T193406Z-4c9c55635682';MAIN_SHA='a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e'
# Independent Reviewer audited every 443 server include for this exact dump.
# A different configuration requires a new review before this one-off release.
AUDITED_NGINX_SHA='150aeb9781623a6d87fe06e1db96d512d88ce877049bd246f3e4d1b9f028fdbf'
VERIFY_INCLUDE=Path('/root/nginx/global.verify.conf')
VERIFY_INCLUDE_SHA='a80aeecdf88e0425bc5da262b53e6de8b7d3ceb8aba6451f06b9866a48b25a65'
SITE_ROUTE=Path('/root/nginx/sites/portal-zhycit.conf')
TARGET_ROUTE=Path('/root/nginx/snippets/zhycit-mcp-routes.conf')
OAUTH_ROUTE=Path('/root/nginx/snippets/gnas-oauth21-zhycit-isolated.conf')
TARGET_ROUTE_SHA='9a7479072284ca9704cebbf67cf8dfd36e0292ec99df73dacb88e00dfe02fea0'
OAUTH_ROUTE_SHA='9c2df8795200171d0ca00c7f90e7e8819272f192d7dab86c44cb3538513acf88'
MCP_SHA='82fd10c16f5ccf72219818a9efc00e0b79f4e70a8726faf071059b6a6344fa03'
GRANT='d647a79758f25323ca187c2a0d44ab0a08ae3ca2535e2d8a5f583d0ebc97f6ca'
OLD_UNIT='acacb97d639155f35986650eee3b51c583424326ef1611b95f6349b5d5c2e098'
OLD_ARGS=[OLD,OLD_SHA,'20261106T040805Z-1acaa4269334','f80fa1116cebd1b55523782259f626450deef7e2c23818c4bec92840b0d173a0',GRANT]
DEPS={'units':'zoop-oauth-units.py','approval':'zoop-oauth-approval.py','core':'zoop-oauth-swap-core.py',
      'txn':'zoop-oauth-unit-transaction.py','adaptive':'zoop-oauth-adaptive-env-exec.py',
      'gnas':'gnas-isolated-canary-prod.py','base':'gnas-isolated-env-exec.py','route':'zoop-target-cutover.py',
      'mcp':'mcp-target-canary.py','preflight':'zoop-oauth-release-preflight.py'}


def require(v,msg):
    if not v:raise ValueError(msg)
def digest(b):return hashlib.sha256(b).hexdigest()
def sha(p):
    h=hashlib.sha256()
    with p.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
    return h.hexdigest()
def run(*args,timeout=30):return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=timeout).stdout

def trusted(p,directory=False):
    s=p.lstat();require(p.resolve()==p and s.st_uid==0 and not s.st_mode&0o022 and
      (stat.S_ISDIR(s.st_mode) if directory else stat.S_ISREG(s.st_mode)),'untrusted control path')
def load(name,p):
    trusted(p);s=importlib.util.spec_from_file_location(name,p);m=importlib.util.module_from_spec(s)
    sys.modules[name]=m;s.loader.exec_module(m);return m

def components():
    for p in (Path('/usr'),Path('/usr/local'),HERE,Path('/etc'),Path('/etc/gnas'),APPROVALS,Path('/etc/systemd'),UNIT.parent,
              Path('/var/lib'),ROOT,ROOT/'releases',ROOT/'used',MCP_ROOT):trusted(p,True)
    trusted(SELF)
    result={}
    for key,name in DEPS.items():
        path=HERE/name;trusted(path)
        if key not in ('adaptive','base'):result[key]=load('zoop_open_'+key,path)
    return result

def dependencies():return {name:sha(HERE/name) for name in sorted(DEPS.values())}

def properties(unit):
    out=run('/usr/bin/systemctl','show',unit,'-p','ActiveState','-p','MainPID','-p','NRestarts').decode()
    return dict(x.split('=',1) for x in out.splitlines() if '=' in x)

def unit_no_overrides(path):
    raw=run('/usr/bin/systemctl','show',path.name,'-p','FragmentPath','-p','DropInPaths').decode()
    value=dict(x.split('=',1) for x in raw.splitlines() if '=' in x)
    require(value=={'FragmentPath':str(path),'DropInPaths':''},'systemd unit override or fragment changed')
    return value


def ensure_unit_job_settled(unit,timeout=120):
    deadline=time.monotonic()+timeout
    while True:
        job=run('/usr/bin/systemctl','show',unit.name,'-p','Job','--value',timeout=10).decode().strip()
        if not job:return
        require(time.monotonic()<deadline,'systemd job still running')
        time.sleep(1)


def guarded_systemd(*args):
    if args==('/usr/bin/systemctl','restart',UNIT.name):
        unit_no_overrides(UNIT)
        return run(*args,timeout=120)
    return run(*args)


def runtime(unit,expected):
    x=properties(unit);pid=int(x.get('MainPID','0'))
    require(x.get('ActiveState')=='active' and x.get('NRestarts')=='0' and pid>1 and sha(Path('/proc')/str(pid)/'exe')==expected,'runtime unavailable')
    return pid

def mcp_authority_matches(pid,proc=Path('/proc')):
    raw=(proc/str(pid)/'environ').read_bytes()
    require(len(raw)<=1024*1024,'MCP environment too large')
    values=[entry.split(b'=',1)[1] for entry in raw.split(b'\0') if entry.startswith(b'GNAS_BASE_URL=')]
    return values==[b'http://127.0.0.1:7504']


def shared(allow_advanced=False):
    data=dict(x.split('=',1) for x in run('/usr/local/sbin/gnas-release-controller','status').decode().splitlines() if '=' in x)
    rid=data.get('release_id','');binary_sha=data.get('runtime_sha256','')
    require(re.fullmatch(r'[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]+',rid) and re.fullmatch(r'[0-9a-f]{64}',binary_sha) and
      data.get('service_state')=='active' and data.get('restarts')=='0' and
      data.get('runtime_path')==str(MAIN/'releases'/rid/'GNASServer'),'shared authority unavailable')
    if not allow_advanced:require(rid==MAIN_RID and binary_sha==MAIN_SHA,'shared authority changed')
    directory=MAIN/'releases'/rid;trusted(MAIN/'releases',True);trusted(directory,True)
    manifest=directory/'manifest.json';binary=directory/'GNASServer';trusted(manifest);trusted(binary)
    require(manifest.stat().st_size<=65536 and sha(binary)==binary_sha,'shared artifact changed')
    meta=json.loads(manifest.read_bytes())
    require(meta.get('schema_version')==2 and meta.get('environment')=='zhycit-prod-01' and
      meta.get('artifact')=='gnas-service-gin' and meta.get('release_id')==rid and
      meta.get('binary')=={'name':'GNASServer','sha256':binary_sha},'shared manifest mismatch')
    try:
        p=properties('wecom-mcp-zhycit-migration.service');pid=int(p.get('MainPID','0'))
        mcp_sha=sha(Path('/proc')/str(pid)/'exe') if pid>1 else None
        authority_ok=mcp_authority_matches(pid) if pid>1 else False
        mcp_ok=p.get('ActiveState')=='active' and p.get('NRestarts')=='0' and pid>1 and mcp_sha==MCP_SHA and authority_ok
    except Exception:
        if not allow_advanced:raise
        pid=0;mcp_sha=None;mcp_ok=False;authority_ok=False
    if not allow_advanced:require(mcp_ok,'target MCP changed')
    return {'main_release':rid,'main_sha256':binary_sha,'main_pid':data['main_pid'],
            'mcp_pid':pid,'mcp_sha256':mcp_sha,'mcp_ok':mcp_ok,'mcp_authority_ok':authority_ok}

def common(c,allow_advanced=False):
    # A separate tenant's outage must not block a necessary rollback. During
    # recovery its state is recorded best-effort, then compared after action.
    try:vector=[list(v) for v in c['route'].other_public_vector()]
    except Exception:
        if not allow_advanced:raise
        vector=None
    if not allow_advanced:
        require(vector==[['mcp.wesiyu.com',401,200],['mcp.rtyouth.com',401,200],['mcp.jianpinke.com',503,503]],'other enterprise baseline changed')
    return {'shared':shared(allow_advanced),'other_enterprises':vector}

@contextlib.contextmanager
def locks():
    # Shared controller locks only MAIN; older target controllers acquire ROOT
    # before MCP_ROOT. The ordering below is compatible with both.
    with contextlib.ExitStack() as stack:
        for p in (MAIN/'release.lock',ROOT/'release.lock',MCP_ROOT/'release.lock'):
            trusted(p);f=stack.enter_context(p.open('r+'));fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
        yield

def units(c,old):
    require(digest(old)==OLD_UNIT,'old unit changed')
    u=c['units'];return {
      'candidate':u.adaptive(old,OLD_ARGS,RID,SHA,GRANT),
      'rollback':u.adaptive(old,OLD_ARGS,OLD,OLD_SHA,GRANT),
      'probe_candidate':u.adaptive(old,OLD_ARGS,RID,SHA,GRANT,probe=True),
      'probe_rollback':u.adaptive(old,OLD_ARGS,OLD,OLD_SHA,GRANT,probe=True)}

def receipt(c,aid,action,fields):
    fixed={'action','target','release_id','binary_sha256','manifest_sha256','expected_current_release','expected_current_sha256','shared_release','shared_sha256','controller_sha256','dependencies_sha256'}
    require(not set(fields)&fixed,'receipt field collision')
    current,current_sha=(RID,SHA) if action=='rollback-isolated-oauth-open-consent' else (OLD,OLD_SHA)
    baseline=fields['shared_snapshot']['shared'] if action=='rollback-isolated-oauth-open-consent' else {'main_release':MAIN_RID,'main_sha256':MAIN_SHA}
    return c['approval'].validate(APPROVALS/(aid+'.json'),aid,{'action':action,'target':'zhycit.com',
      'release_id':RID,'binary_sha256':SHA,'manifest_sha256':MANIFEST,'expected_current_release':current,
      'expected_current_sha256':current_sha,'shared_release':baseline['main_release'],'shared_sha256':baseline['main_sha256'],
      'controller_sha256':sha(SELF),'dependencies_sha256':dependencies(),**fields})

def consume(c,aid):c['txn'].write_exclusive(ROOT/'used'/aid,(dt.datetime.now(dt.timezone.utc).isoformat()+'\n').encode(),0o440)

def fields(c,old,before):
    generated=units(c,old)
    return {'unit_sha256':digest(old),'generated_unit_sha256':{k:digest(v) for k,v in generated.items()},
            'grant_sha256':GRANT,'shared_snapshot':before,'target_routes':routes(c),'systemd_unit':unit_no_overrides(UNIT),'prepared_controller_sha256':sha(SELF),
            'prepared_dependencies_sha256':dependencies()}

def nginx_tokens(raw):
    """Tokenize nginx directives, preserving quoted/variable braces."""
    text=raw.decode('utf-8');tokens=[];word=[];i=0
    def flush():
        if word:tokens.append(''.join(word));word.clear()
    while i<len(text):
        ch=text[i]
        if ch.isspace():flush();i+=1;continue
        if ch=='#':
            flush();end=text.find('\n',i);i=len(text) if end<0 else end+1;continue
        if ch in ('"',"'"):
            quote=ch;i+=1
            while i<len(text) and text[i]!=quote:
                if text[i]=='\\':
                    i+=1;require(i<len(text),'invalid nginx escape')
                word.append(text[i]);i+=1
            require(i<len(text),'unterminated nginx string');i+=1;continue
        if ch=='\\':
            i+=1;require(i<len(text),'invalid nginx escape');word.append(text[i]);i+=1;continue
        if ch=='{' and word:
            close=text.find('}',i+1)
            if close>=0 and (word[-1]=='$' or re.fullmatch(r'[0-9]+(?:,[0-9]*)?',text[i+1:close])):
                word.extend(text[i:close+1]);i=close+1;continue
        if ch in '{};':flush();tokens.append(ch);i+=1;continue
        word.append(ch);i+=1
    flush();return tokens


def nginx_servers(raw):
    tokens=nginx_tokens(raw);stack=[];directive=[];servers=[];current=None;server_depth=0
    for token in tokens:
        if token=='{':
            if current is not None and len(stack)==server_depth and directive:
                current.append(tuple(directive)+('{',))
            if directive==['server']:
                require(current is None,'nested nginx server');current=[];servers.append(current);server_depth=len(stack)+1
            stack.append(tuple(directive));directive=[]
        elif token=='}':
            require(stack,'invalid nginx block nesting')
            if current is not None and len(stack)==server_depth:current=None
            stack.pop();directive=[]
        elif token==';':
            if current is not None and len(stack)==server_depth and directive:current.append(tuple(directive))
            directive=[]
        else:directive.append(token)
    require(not stack and not directive,'invalid nginx syntax')
    return servers


def listen_443(row):
    if not row or row[0]!='listen' or len(row)<2:return None
    value=row[1]
    if value in ('443','*:443','0.0.0.0:443'):return 'wildcard_v4'
    if value=='[::]:443':return 'wildcard_v6'
    if value.endswith(':443'):return 'specific_address'
    return None


def validate_target_server(server):
    allowed={'listen','server_name','ssl_certificate','ssl_certificate_key','gzip','gzip_http_version',
             'gzip_disable','gzip_comp_level','gzip_types','location','include'}
    require(all(x[0] in allowed for x in server),'target server has unreviewed top-level directive')
    includes=[x for x in server if x[0]=='include']
    require(sorted(includes)==sorted([('include','/root/nginx/snippets/zhycit-mcp-routes.conf'),
      ('include','/root/nginx/global.verify.conf')]),'target server has unreviewed include')
    exact={'/mcp','/.well-known/oauth-protected-resource/mcp','/zhycit/mcp',
      '/.well-known/oauth-protected-resource/zhycit/mcp','/.well-known/oauth-authorization-server/gnas/oauth',
      '/.well-known/openid-configuration/gnas/oauth'}
    for item in server:
        if item[0]!='location':continue
        require(item[-1]=='{' and len(item)>=3,'invalid target location')
        args=list(item[1:-1]);modifier=''
        if args[0] in ('=','^~','~','~*'):
            modifier=args.pop(0)
        elif args[0].startswith('^~'):
            modifier='^~';args[0]=args[0][2:]
        require(len(args)==1 and args[0],'invalid target location')
        if modifier in ('~','~*'):continue # Exact and ^~ OAuth locations win before regex.
        path=args[0]
        require(path not in exact and not path.startswith('/gnas/oauth/'),'target server contains competing MCP location')


def validate_site_topology(site):
    matching=[]
    for server in nginx_servers(site):
        has_name=any(x[0]=='server_name' and 'zhycit.com' in [v.lower().rstrip('.') for v in x[1:]] for x in server)
        has_tls=any(listen_443(x) is not None and 'ssl' in x[2:] for x in server)
        if has_name and has_tls:matching.append(server)
    require(len(matching)==1,'target HTTPS server ambiguous')
    include=('include','/root/nginx/snippets/zhycit-mcp-routes.conf')
    require(matching[0].count(include)==1,'target HTTPS server does not include MCP routes')
    validate_target_server(matching[0])


def validate_global_topology(config):
    servers=nginx_servers(config);matching=[];v6=False
    for server in servers:
        listeners=[x for x in server if listen_443(x) is not None]
        for row in listeners:
            kind=listen_443(row)
            require(kind!='specific_address','address-specific 443 server may shadow target')
            if kind=='wildcard_v6':v6=True
        has_name=any(x[0]=='server_name' and 'zhycit.com' in [v.lower().rstrip('.') for v in x[1:]] for x in server)
        if has_name and listeners:matching.append((server,listeners))
    require(len(matching)==1,'global target HTTPS server ambiguous')
    server,listeners=matching[0]
    require(any(listen_443(x)=='wildcard_v4' and 'ssl' in x[2:] for x in listeners),'target IPv4 HTTPS listener unavailable')
    if v6:require(any(listen_443(x)=='wildcard_v6' and 'ssl' in x[2:] for x in listeners),'target IPv6 listener may be shadowed')
    require(server.count(('include','/root/nginx/snippets/zhycit-mcp-routes.conf'))==1,'global target HTTPS server missing MCP routes')
    validate_target_server(server)


def routes(c):
    c['route'].trusted_nginx_tree()
    for path in (SITE_ROUTE,TARGET_ROUTE,OAUTH_ROUTE):trusted(path)
    v=VERIFY_INCLUDE.lstat()
    require(VERIFY_INCLUDE.resolve()==VERIFY_INCLUDE and stat.S_ISREG(v.st_mode) and not v.st_mode&0o022,'untrusted target include')
    site=SITE_ROUTE.read_bytes();target=TARGET_ROUTE.read_bytes();oauth=OAUTH_ROUTE.read_bytes();verify=VERIFY_INCLUDE.read_bytes()
    config=run('/usr/sbin/nginx','-T',timeout=20)
    require(len(config)<=8*1024*1024 and digest(config)==AUDITED_NGINX_SHA,'nginx configuration lacks matching independent review')
    validate_site_topology(site);validate_global_topology(config)
    require( digest(verify)==VERIFY_INCLUDE_SHA and digest(target)==TARGET_ROUTE_SHA and
      digest(oauth)==OAUTH_ROUTE_SHA and target.count(b'proxy_pass http://127.0.0.1:7709;')==2 and
      oauth.count(b'proxy_pass http://127.0.0.1:7504;')==1 and
      site.count(b'include /root/nginx/snippets/zhycit-mcp-routes.conf;')==1 and
      target.count(b'include /root/nginx/snippets/gnas-oauth21-zhycit-isolated.conf;')==3,
      'target route changed')
    for path in (SITE_ROUTE,TARGET_ROUTE,OAUTH_ROUTE,VERIFY_INCLUDE):
        marker=('# configuration file '+str(path)+':\n').encode()
        require(config.count(marker)==1,'target nginx include topology changed')
    require(config.count(b'include /root/nginx/snippets/zhycit-mcp-routes.conf;')==1 and
      config.count(b'include /root/nginx/snippets/gnas-oauth21-zhycit-isolated.conf;')==3,'target nginx include topology changed')
    return {'site_sha256':digest(site),'verification_include_sha256':VERIFY_INCLUDE_SHA,'mcp_route_sha256':TARGET_ROUTE_SHA,'oauth_route_sha256':OAUTH_ROUTE_SHA,
            'nginx_config_sha256':digest(config)}

def target_ready(c):
    require(c['route'].tls_probe('zhycit.com','/mcp')==401 and
      c['route'].tls_probe('zhycit.com','/.well-known/oauth-protected-resource/mcp')==200 and
      c['mcp'].probe(7709,'zhycit.com','/healthz')[0]==200,'target baseline unavailable')

def check_probe_absent(require_memory=True):
    require(not PROBE.exists() and not PROBE.is_symlink(),'probe unit exists')
    p=properties(PROBE.name);require(p.get('MainPID','0')=='0' and p.get('ActiveState') in ('inactive','failed'),'probe service active')
    s=socket.socket()
    try:s.bind(('127.0.0.1',7505))
    finally:s.close()
    require(not re.search(rb'(?<![0-9])7505(?![0-9])',run('/usr/sbin/nginx','-T',timeout=20)),'probe port routed')
    if require_memory:
        memory=re.search(r'^MemAvailable:\s+([0-9]+)\s+kB$',Path('/proc/meminfo').read_text(),re.M)
        require(memory and int(memory.group(1))>=384*1024,'insufficient memory for isolated probe')

def probe_version(c,unit_bytes,binary_sha):
    c['txn'].write_exclusive(PROBE,unit_bytes,0o644)
    try:
        run('/usr/bin/systemctl','daemon-reload');unit_no_overrides(PROBE);run('/usr/bin/systemctl','start',PROBE.name,timeout=50)
        end=time.monotonic()+45
        while True:
            try:
                pid=runtime(PROBE.name,binary_sha)
                require(c['gnas'].oauth_fingerprint(7505)==c['gnas'].oauth_fingerprint(7504),'probe OAuth keys differ')
                return pid
            except Exception:
                require(time.monotonic()<end,'probe did not become healthy');time.sleep(1)
    finally:
        require(PROBE.read_bytes()==unit_bytes,'probe unit drifted');unit_no_overrides(PROBE)
        run('/usr/bin/systemctl','stop',PROBE.name,timeout=50)
        p=properties(PROBE.name);require(p.get('MainPID')=='0' and p.get('ActiveState') in ('inactive','failed'),'probe did not stop')
        require(PROBE.read_bytes()==unit_bytes,'probe unit drifted');PROBE.unlink();run('/usr/bin/systemctl','daemon-reload')


def install_artifact(c):
    directory=ROOT/'releases'/RID
    if directory.exists():
        trusted(directory,True);trusted(directory/'GNASServer');trusted(directory/'manifest.json')
        require(sha(directory/'GNASServer')==SHA and sha(directory/'manifest.json')==MANIFEST,'installed candidate changed')
        return
    # Reuse the existing reviewed immutable artifact promotion implementation.
    plan=types.SimpleNamespace(release_id=RID,binary_sha256=SHA,baseline_release_id=MAIN_RID,baseline_binary_sha256=MAIN_SHA)
    ops=c['gnas'].ProductionOps(plan,'unused')
    ops.verify_artifact(plan)
    require(sha(INCOMING/RID/'manifest.json')==MANIFEST,'incoming manifest changed')
    ops.receipt={'candidate_manifest_sha256':MANIFEST};ops.install_immutable_artifact(plan)


def probe_record(aid):
    require(isinstance(aid,str) and re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}',aid),'invalid probe approval id')
    return ROOT/('open-consent-probe-'+aid+'.json')


def recover_probe(c,aid):
    with locks():
        require(not PREP.exists(),'probe preparation already completed')
        start=read_json_trusted(probe_record(aid));marker=ROOT/'used'/aid
        trusted(marker);require(stat.S_IMODE(marker.stat().st_mode)==0o440 and start.get('approval_id')==aid and
          start.get('controller_sha256')==sha(SELF) and start.get('dependencies_sha256')==dependencies(),'probe recovery evidence changed')
        old=start['old_unit'].encode();generated=units(c,old)
        require(start.get('probe_rollback_sha256')==digest(generated['probe_rollback']) and
          start.get('probe_candidate_sha256')==digest(generated['probe_candidate']),'probe recovery unit mismatch')
        require(not re.search(rb'(?<![0-9])7505(?![0-9])',run('/usr/sbin/nginx','-T',timeout=20)),'probe port routed')
        if PROBE.exists() or PROBE.is_symlink():
            trusted(PROBE);raw=PROBE.read_bytes()
            require(raw in (generated['probe_rollback'],generated['probe_candidate']),'probe unit changed')
            # The process may have died after writing the unit but before
            # systemd reloaded its cached not-found state.
            run('/usr/bin/systemctl','daemon-reload')
            unit_no_overrides(PROBE);ensure_unit_job_settled(PROBE);p=properties(PROBE.name);pid=int(p.get('MainPID','0'))
            expected=OLD_SHA if raw==generated['probe_rollback'] else SHA
            if pid>1:require(sha(Path('/proc')/str(pid)/'exe')==expected,'probe process changed')
            run('/usr/bin/systemctl','stop',PROBE.name,timeout=50)
            p=properties(PROBE.name);require(p.get('MainPID')=='0' and p.get('ActiveState') in ('inactive','failed'),'probe did not stop')
            require(PROBE.read_bytes()==raw,'probe unit drifted');PROBE.unlink();c['txn'].sync_directory(PROBE.parent)
            run('/usr/bin/systemctl','daemon-reload')
        check_probe_absent(require_memory=False)
        record=ROOT/('open-consent-probe-recovered-'+aid+'.json')
        c['txn'].write_exclusive(record,(json.dumps({'approval_id':aid,'probe_start_sha256':sha(probe_record(aid))},sort_keys=True)+'\n').encode(),0o440)
        print(json.dumps({'state':'isolated_probe_recovered','live_switched':False}))


def prepare(c,aid):
    with locks():
        require(not PREP.exists() and not JOURNAL.exists(),'release already prepared')
        before=common(c);target_ready(c);trusted(UNIT);old=UNIT.read_bytes();require(runtime(UNIT.name,OLD_SHA)>1,'old authority unavailable')
        require(c['gnas'].oauth_fingerprint(7501)==c['gnas'].oauth_fingerprint(7504),'old OAuth key mismatch')
        check_probe_absent();bindings=fields(c,old,before)
        receipt(c,aid,'prepare-isolated-oauth-open-consent',bindings);consume(c,aid)
        generated=units(c,old)
        start={'approval_id':aid,'old_unit':old.decode('utf-8'),'controller_sha256':sha(SELF),
          'dependencies_sha256':dependencies(),'probe_rollback_sha256':digest(generated['probe_rollback']),
          'probe_candidate_sha256':digest(generated['probe_candidate'])}
        c['txn'].write_exclusive(probe_record(aid),(json.dumps(start,sort_keys=True)+'\n').encode(),0o440)
        install_artifact(c)
        probe_version(c,generated['probe_rollback'],OLD_SHA)
        require(common(c)==before and runtime(UNIT.name,OLD_SHA)>1,'live baseline changed during probe')
        probe_version(c,generated['probe_candidate'],SHA)
        require(common(c)==before and runtime(UNIT.name,OLD_SHA)>1,'live baseline changed during probe')
        payload={**bindings,'prepare_approval_id':aid,'old_unit':old.decode('utf-8'),'candidate_sha256':SHA,'old_sha256':OLD_SHA}
        c['txn'].write_exclusive(PREP,(json.dumps(payload,sort_keys=True)+'\n').encode(),0o440)
        print(json.dumps({'state':'isolated_oauth_probes_passed','live_switched':False},sort_keys=True))


def read_json_trusted(p):
    # Ensure any previously linked marker is durable before acting on it.
    fd=os.open(p.parent,os.O_RDONLY|os.O_DIRECTORY)
    try:os.fsync(fd)
    finally:os.close(fd)
    trusted(p);require(stat.S_IMODE(p.stat().st_mode)==0o440 and p.stat().st_size<=65536,'untrusted release record')
    v=json.loads(p.read_bytes());require(isinstance(v,dict),'invalid release record');return v


class ProductionOps:
    def __init__(self,c,approval):self.c=c;self.approval=approval;self.prepared=None;self.record=None;self.restoring=False;self.mcp_ready=None
    def release_lock(self):return locks()
    def snapshot(self):
        before=common(self.c);before['old_pid']=runtime(UNIT.name,OLD_SHA)
        return before
    def snapshot_for_recovery(self):return common(self.c,allow_advanced=True)
    def verify_plan(self,plan,before):
        trusted(PREP);trusted(UNIT);target_ready(self.c);self.prepared=read_json_trusted(PREP)
        old=self.prepared['old_unit'].encode();generated=units(self.c,old)
        require(UNIT.read_bytes()==old and all(self.prepared.get(k)==v for k,v in fields(self.c,old,{'shared':before['shared'],'other_enterprises':before['other_enterprises']}).items()),'prepared baseline changed')
        require(plan.candidate_unit_sha256==digest(generated['candidate']) and plan.rollback_unit_sha256==digest(generated['rollback']),'unit plan changed')
        installed=ROOT/'releases'/RID
        trusted(installed,True);trusted(installed/'GNASServer');trusted(installed/'manifest.json')
        require(sha(installed/'GNASServer')==SHA and sha(installed/'manifest.json')==MANIFEST,'installed candidate changed')
        require(not JOURNAL.exists() and not SUCCESS.exists() and not ROLLED.exists(),'transaction already exists')
    def verify_receipt(self,plan,before):
        receipt(self.c,self.approval,'deploy-isolated-oauth-open-consent',{'prepared_sha256':sha(PREP),
          'old_oauth_pid':before['old_pid'],'candidate_unit_sha256':plan.candidate_unit_sha256,
          'rollback_unit_sha256':plan.rollback_unit_sha256,'shared_snapshot':{'shared':before['shared'],'other_enterprises':before['other_enterprises']}})
    def verify_old_recovery_candidate(self,plan,before):
        aid=self.prepared.get('prepare_approval_id','')
        require(self.prepared.get('old_sha256')==OLD_SHA and isinstance(aid,str) and
          re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}',aid),'old recovery probe evidence missing')
        marker=ROOT/'used'/aid;trusted(marker);require(stat.S_IMODE(marker.stat().st_mode)==0o440,'untrusted preparation evidence')
    def verify_new_canary(self,plan,before):
        require(self.prepared.get('candidate_sha256')==SHA,'candidate probe evidence missing')
    def consume_receipt(self):consume(self.c,self.approval)
    def record_prepared(self,plan,before):
        old=self.prepared['old_unit'].encode();generated=units(self.c,old)
        self.record={'prepared_sha256':sha(PREP),'approval_id':self.approval,'before':before,
          'old_unit':old.decode(),'candidate_unit':generated['candidate'].decode(),'rollback_unit':generated['rollback'].decode(),
          'candidate_sha256':SHA,'old_sha256':OLD_SHA,'grant_sha256':GRANT}
        self.c['txn'].write_exclusive(JOURNAL,(json.dumps(self.record,sort_keys=True)+'\n').encode(),0o440)
    def assert_unchanged(self,plan,before):
        unit_no_overrides(UNIT);require(routes(self.c)==self.prepared['target_routes'],'nginx configuration changed')
        require(common(self.c)=={'shared':before['shared'],'other_enterprises':before['other_enterprises']} and
                runtime(UNIT.name,OLD_SHA)==before['old_pid'] and UNIT.read_bytes()==self.record['old_unit'].encode(),'baseline changed before switch')
    def ensure_restart_job_settled(self):ensure_unit_job_settled(UNIT)
    def install_and_restart_candidate(self,plan,before):
        self.c['txn'].activate(UNIT,self.record['old_unit'].encode(),self.record['candidate_unit'].encode(),0o644,guarded_systemd)
    def verify_candidate(self,plan,before):self.verify_runtime(SHA,self.record['candidate_unit'].encode())
    def install_and_restart_rollback(self,plan,before):
        self.restoring=True
        current=UNIT.read_bytes();old=self.record['old_unit'].encode();new=self.record['candidate_unit'].encode();rollback=self.record['rollback_unit'].encode()
        require(current in (old,new,rollback),'unit drift during rollback')
        self.c['txn'].activate(UNIT,current,rollback,0o644,guarded_systemd)
    def verify_rollback(self,plan,before):self.verify_runtime(OLD_SHA,self.record['rollback_unit'].encode())
    def verify_runtime(self,binary_sha,unit):
        deadline=time.monotonic()+60
        while True:
            try:
                require(UNIT.read_bytes()==unit,'unit mismatch')
                runtime(UNIT.name,binary_sha)
                require(self.c['gnas'].oauth_fingerprint(7501)==self.c['gnas'].oauth_fingerprint(7504),'OAuth identity mismatch')
                return
            except Exception:
                require(time.monotonic()<deadline,'isolated runtime not ready');time.sleep(1)
    def verify_public_and_other_tenants(self,plan,before):
        expected=before if 'old_pid' not in before else {'shared':before['shared'],'other_enterprises':before['other_enterprises']}
        if self.restoring:
            actual=common(self.c,allow_advanced=True)
            for key in ('main_release','main_sha256','main_pid'):
                require(actual['shared'][key]==expected['shared'][key],'shared authority changed')
            try:
                target_ready(self.c);self.mcp_ready=True
            except Exception:
                self.mcp_ready=False
                print(json.dumps({'state':'oauth_rollback_mcp_unavailable'}))
            if actual['other_enterprises']!=expected['other_enterprises']:
                print(json.dumps({'state':'rollback_other_enterprise_observation_changed'}))
            return
        deadline=time.monotonic()+60
        while True:
            try:
                actual=common(self.c,allow_advanced=True)
                require(routes(self.c)==self.prepared['target_routes'],'nginx configuration changed')
                require(actual==expected,'shared service or other enterprise health changed')
                target_ready(self.c)
                return
            except Exception:
                require(time.monotonic()<deadline,'public or fleet health failed');time.sleep(2)
    def success_record_published(self,plan,before):
        if not SUCCESS.exists():return False
        trusted(SUCCESS);require(stat.S_IMODE(SUCCESS.stat().st_mode)==0o440,'untrusted success record')
        value=json.loads(SUCCESS.read_bytes())
        require(value=={'journal_sha256':sha(JOURNAL),'state':'switched'},'invalid success record')
        return True
    def record_success(self,plan,before):
        self.c['txn'].write_exclusive(SUCCESS,(json.dumps({'journal_sha256':sha(JOURNAL),'state':'switched'},sort_keys=True)+'\n').encode(),0o440)
    def record_rollback(self,plan,before,reason):
        self.c['txn'].write_exclusive(ROLLED,(json.dumps({'journal_sha256':sha(JOURNAL),'state':'oauth_rolled_back','reason':reason,'mcp_ready':self.mcp_ready},sort_keys=True)+'\n').encode(),0o440)
    def verify_recovery_journal(self,plan,before):
        require(not SUCCESS.exists() and not ROLLED.exists(),'transaction already completed')
        self.record=read_json_trusted(JOURNAL);self.prepared=read_json_trusted(PREP)
        require(self.record.get('prepared_sha256')==sha(PREP) and self.record.get('candidate_sha256')==SHA and
          self.record.get('old_sha256')==OLD_SHA and self.record.get('grant_sha256')==GRANT and
          re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}',self.record.get('approval_id','')) and
          (ROOT/'used'/self.record['approval_id']).exists(),'invalid recovery journal')
        marker=ROOT/'used'/self.record['approval_id'];trusted(marker);require(stat.S_IMODE(marker.stat().st_mode)==0o440,'untrusted consumed approval')
        old=self.record['old_unit'].encode();generated=units(self.c,old)
        require(self.record['candidate_unit'].encode()==generated['candidate'] and self.record['rollback_unit'].encode()==generated['rollback'],'journal unit mismatch')
    def verify_recovery_preconditions(self,plan,before):
        self.ensure_restart_job_settled()
        trusted(UNIT);require(UNIT.read_bytes() in tuple(self.record[k].encode() for k in ('old_unit','candidate_unit','rollback_unit')),'unit changed')
        trusted(ROOT/'releases'/OLD/'GNASServer');require(sha(ROOT/'releases'/OLD/'GNASServer')==OLD_SHA,'rollback binary changed')


def manual_rollback(c,aid=None,recover=False):
    with locks():
        require(not ROLLED.exists(),'already rolled back')
        journal=read_json_trusted(JOURNAL);success=read_json_trusted(SUCCESS)
        require(success.get('journal_sha256')==sha(JOURNAL) and success.get('state')=='switched' and journal.get('candidate_sha256')==SHA and journal.get('old_sha256')==OLD_SHA,'invalid activation evidence')
        old=journal['old_unit'].encode();generated=units(c,old)
        require(journal['candidate_unit'].encode()==generated['candidate'] and journal['rollback_unit'].encode()==generated['rollback'],'journal unit mismatch')
        before=common(c,allow_advanced=True)
        trusted(UNIT);current=UNIT.read_bytes()
        require(current in (generated['candidate'],generated['rollback']),'unexpected active unit')
        trusted(ROOT/'releases'/OLD/'GNASServer');require(sha(ROOT/'releases'/OLD/'GNASServer')==OLD_SHA,'rollback binary changed')
        if recover:
            started=read_json_trusted(ROLLBACK_START)
            aid=started.get('approval_id','')
            require(isinstance(aid,str) and re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}',aid),'invalid rollback approval id')
            used=ROOT/'used'/aid
            require(started.get('journal_sha256')==sha(JOURNAL),'invalid rollback recovery evidence')
            trusted(used);require(stat.S_IMODE(used.stat().st_mode)==0o440,'untrusted consumed approval')
        else:
            require(not ROLLBACK_START.exists() and current==generated['candidate'],'rollback already started or unit drifted')
            receipt(c,aid,'rollback-isolated-oauth-open-consent',{'journal_sha256':sha(JOURNAL),
              'success_sha256':sha(SUCCESS),'rollback_unit_sha256':digest(generated['rollback']),'shared_snapshot':before})
            consume(c,aid)
            c['txn'].write_exclusive(ROLLBACK_START,(json.dumps({'journal_sha256':sha(JOURNAL),'approval_id':aid},sort_keys=True)+'\n').encode(),0o440)
        op=ProductionOps(c,'');op.record=journal
        op.ensure_restart_job_settled()
        op.install_and_restart_rollback(None,before)
        op.verify_rollback(None,before)
        op.verify_public_and_other_tenants(None,before)
        op.record_rollback(None,before,'manual' if not recover else 'manual-interrupted')
        print(json.dumps({'state':'isolated_oauth_rollback_verified','mcp_ready':op.mcp_ready}))


def plan(c):
    x=read_json_trusted(PREP);old=x['old_unit'].encode();generated=units(c,old)
    return c['core'].Plan(RID,SHA,MANIFEST,OLD,OLD_SHA,MAIN_RID,MAIN_SHA,OLD_UNIT,digest(generated['candidate']),digest(generated['rollback']),GRANT)


def main():
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF and
      os.environ.get('GNAS_RELEASE_CONTROLLER_DISPATCH')=='zoop-oauth-v1','invalid controller context')
    c=components()
    require(len(sys.argv) in (2,3),'invalid controller arguments')
    action=sys.argv[1]
    if action=='prepare' and len(sys.argv)==3:prepare(c,sys.argv[2]);return
    if action=='recover-probe' and len(sys.argv)==3:recover_probe(c,sys.argv[2]);return
    if action=='deploy' and len(sys.argv)==3:
        result=c['core'].switch(plan(c),ProductionOps(c,sys.argv[2]));print(json.dumps({'state':result}));return
    if action=='rollback' and len(sys.argv)==3:manual_rollback(c,sys.argv[2]);return
    if action=='recover-rollback' and len(sys.argv)==2:manual_rollback(c,recover=True);return
    if action=='recover' and len(sys.argv)==2:
        result=c['core'].recover_incomplete(plan(c),ProductionOps(c,''));print(json.dumps({'state':result}));return
    raise ValueError('unsupported action')


if __name__=='__main__':
    try:main()
    except Exception as e:
        print('isolated OAuth control failed: '+(str(e) if isinstance(e,(ValueError,RuntimeError)) else type(e).__name__),file=sys.stderr)
        sys.exit(1)
