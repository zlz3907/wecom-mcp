#!/usr/bin/python3 -I
"""Install immutable target OAuth controller files; does not change services."""
import contextlib
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile

SOURCE=Path(__file__).resolve().parent;DEST=Path('/usr/local/sbin')
ROOT=Path('/var/lib/gnas-oauth-isolated');MCP=Path('/var/lib/wecom-mcp-isolated')
MAIN=Path('/home/product/services/gnas-service-gin');APPROVALS=Path('/etc/gnas/release-approvals')
FILES={
 'zoop_oauth_swap_prod.py':'zoop-oauth-swap-prod.py',
 'zoop_oauth_adaptive_env_exec.py':'zoop-oauth-adaptive-env-exec.py',
 'zoop_oauth_units.py':'zoop-oauth-units.py',
 'zoop_oauth_approval.py':'zoop-oauth-approval.py',
 'zoop_oauth_swap_core.py':'zoop-oauth-swap-core.py',
 'zoop_oauth_unit_transaction.py':'zoop-oauth-unit-transaction.py',
 'zoop_oauth_release_preflight.py':'zoop-oauth-release-preflight.py',
}


def require(v,msg):
    if not v:raise ValueError(msg)
def sha(p):
    h=hashlib.sha256()
    with p.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''):h.update(b)
    return h.hexdigest()
def trusted(p,directory=False):
    s=p.lstat();require(p.resolve()==p and s.st_uid==0 and not s.st_mode&0o022 and
      (stat.S_ISDIR(s.st_mode) if directory else stat.S_ISREG(s.st_mode)),'untrusted installation path')
def run(*args):return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=30).stdout

def baseline():
    status=dict(x.split('=',1) for x in run(str(DEST/'gnas-release-controller'),'status').decode().splitlines() if '=' in x)
    require(status.get('release_id')=='20261107T193406Z-4c9c55635682' and
      status.get('runtime_sha256')=='a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e' and
      status.get('service_state')=='active' and status.get('restarts')=='0','shared service changed')
    out=run('/usr/bin/systemctl','show','gnas-oauth-zhycit-next.service','-p','ActiveState','-p','MainPID','-p','NRestarts').decode()
    isolated=dict(x.split('=',1) for x in out.splitlines() if '=' in x);pid=int(isolated.get('MainPID','0'))
    require(isolated.get('ActiveState')=='active' and isolated.get('NRestarts')=='0' and pid>1 and
      sha(Path('/proc')/str(pid)/'exe')=='d5b8cb5c98e74f45e7cc54b645ae9d686fa7003e9391bcaa85317101f3d531df','isolated service changed')
    unit=Path('/etc/systemd/system/gnas-oauth-zhycit-next.service');trusted(unit)
    require(sha(unit)=='acacb97d639155f35986650eee3b51c583424326ef1611b95f6349b5d5c2e098','isolated unit changed')
    return {'shared_release':status['release_id'],'shared_sha256':status['runtime_sha256'],
            'shared_pid':status['main_pid'],'isolated_pid':pid,'isolated_sha256':'d5b8cb5c98e74f45e7cc54b645ae9d686fa7003e9391bcaa85317101f3d531df',
            'isolated_unit_sha256':sha(unit)}

def component_hashes():return {target:sha(SOURCE/source) for source,target in FILES.items()}

def check_receipt(a,before):
    require(re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}',a),'invalid approval')
    p=APPROVALS/(a+'.json');trusted(p);require(stat.S_IMODE(p.stat().st_mode)==0o400,'invalid approval mode')
    x=json.loads(p.read_bytes());expected={'schema_version':1,'approval_id':a,'environment':'zhycit-prod-01',
      'action':'install-zoop-oauth-swap-controller','installer_sha256':sha(Path(__file__).resolve()),
      'component_sha256':component_hashes(),'baseline':before}
    require(set(x)==set(expected)|{'approved_at','expires_at','approved_by','ci_commit','ci_url'} and
      all(x.get(k)==v for k,v in expected.items()),'installation approval mismatch')
    require(re.fullmatch(r'[a-f0-9]{40}',x['ci_commit']) and x['ci_url'].startswith('https://github.com/'),'invalid CI evidence')
    start=dt.datetime.fromisoformat(x['approved_at'].replace('Z','+00:00'));end=dt.datetime.fromisoformat(x['expires_at'].replace('Z','+00:00'));now=dt.datetime.now(dt.timezone.utc)
    require(start.tzinfo and end.tzinfo and start<=now<end and end-start<=dt.timedelta(hours=4) and isinstance(x['approved_by'],str) and x['approved_by'].strip(),'approval expired')
    return x

def syncdir(p):
    fd=os.open(p,os.O_RDONLY|os.O_DIRECTORY)
    try:os.fsync(fd)
    finally:os.close(fd)

def install(a):
    require(os.geteuid()==0 and SOURCE.is_relative_to(Path('/root')) and SOURCE!=Path('/root'),'invalid installer location')
    for p in (Path('/root'),SOURCE,Path('/usr'),Path('/usr/local'),DEST,Path('/etc'),Path('/etc/gnas'),APPROVALS,Path('/var/lib'),ROOT,ROOT/'used',MCP):trusted(p,True)
    trusted(Path(__file__).resolve());trusted(DEST/'gnas-release-controller')
    for name in FILES:trusted(SOURCE/name)
    with contextlib.ExitStack() as stack:
        for p in (MAIN/'release.lock',ROOT/'release.lock',MCP/'release.lock'):
            trusted(p);f=stack.enter_context(p.open('r+'));fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
        before=baseline();approval=check_receipt(a,before)
        for source,target in FILES.items():
            p=DEST/target
            if p.exists() or p.is_symlink():
                trusted(p);require(sha(p)==approval['component_sha256'][target],'different controller already installed')
        require(baseline()==before,'baseline changed')
        marker=ROOT/'used'/a;fd=os.open(marker,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o440)
        with os.fdopen(fd,'w') as f:f.write(dt.datetime.now(dt.timezone.utc).isoformat()+'\n');f.flush();os.fchmod(f.fileno(),0o440);os.fsync(f.fileno())
        syncdir(marker.parent)
        for source,target in FILES.items():
            dest=DEST/target
            if dest.exists():continue
            fd,name=tempfile.mkstemp(prefix='.zoop-oauth-',dir=DEST)
            try:
                with os.fdopen(fd,'wb') as out,(SOURCE/source).open('rb') as inp:
                    shutil.copyfileobj(inp,out);out.flush();os.fchmod(out.fileno(),0o555);os.fsync(out.fileno())
                require(sha(Path(name))==approval['component_sha256'][target],'component copy mismatch')
                os.link(name,dest);syncdir(DEST)
            finally:
                if os.path.exists(name):os.unlink(name)
        require(baseline()==before,'baseline changed during install')
        print(json.dumps({'state':'oauth_controller_installed','shared_restarted':False,'isolated_restarted':False}))

if __name__=='__main__':
    try:
        require(len(sys.argv)==2,'exact approval id required');install(sys.argv[1])
    except Exception as e:
        print('controller installation failed: '+(str(e) if isinstance(e,ValueError) else type(e).__name__),file=sys.stderr);sys.exit(1)
