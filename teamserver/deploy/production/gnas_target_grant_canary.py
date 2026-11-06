#!/usr/bin/python3 -I
"""Apply one authenticated member grant to an unrouted OAuth canary."""
import datetime as dt
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import types

HERE=Path('/usr/local/sbin')
ADAPTER=HERE/'gnas-isolated-canary-prod.py'
ENV_HELPER=HERE/'gnas-isolated-env-exec.py'
SELF=HERE/'gnas-target-grant-canary.py'
UNIT='gnas-oauth-zhycit-isolated.service'
UNIT_PATH=Path('/etc/systemd/system')/UNIT
ROOT=Path('/var/lib/gnas-oauth-isolated')
APPROVALS=Path('/etc/gnas/release-approvals')
GRANT=Path('/etc/gnas/oauth21/isolated-target-grant.json')
RID='20261106T203247Z-932ca8583c69'
SHA='d5b8cb5c98e74f45e7cc54b645ae9d686fa7003e9391bcaa85317101f3d531df'
OLD='20261106T040805Z-1acaa4269334'
OLD_SHA='f80fa1116cebd1b55523782259f626450deef7e2c23818c4bec92840b0d173a0'


def require(v,msg):
    if not v: raise ValueError(msg)


def digest(path):
    h=hashlib.sha256()
    with path.open('rb') as f:
        for b in iter(lambda:f.read(1048576),b''): h.update(b)
    return h.hexdigest()


def trusted(path, directory=False):
    st=path.lstat()
    require(path.resolve()==path and st.st_uid==0 and not st.st_mode&0o022, 'untrusted control path')
    require(path.is_dir() if directory else path.is_file(), 'invalid control path')


def load(name,path):
    trusted(path)
    spec=importlib.util.spec_from_file_location(name,path)
    module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
    return module


def run(*args,timeout=20):
    return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=timeout).stdout


def plan(grant):
    return types.SimpleNamespace(release_id=RID,binary_sha256=SHA,baseline_release_id=OLD,
                                 baseline_binary_sha256=OLD_SHA,host='zhycit.com',listen='127.0.0.1:7503',grant_sha256=grant)


def route_unexposed():
    data=run('/usr/sbin/nginx','-T',timeout=20)
    return not re.search(rb'(?<![0-9])(?:7503|7705)(?![0-9])',data)


def unit_properties():
    raw=run('/usr/bin/systemctl','show',UNIT,'-p','MainPID','-p','ActiveState','-p','NRestarts').decode()
    return dict(x.split('=',1) for x in raw.splitlines() if '=' in x)


def check_receipt(approval, grant_sha, adapter, before_pid):
    require(re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[a-zA-Z0-9._-]{6,64}',approval), 'invalid receipt')
    path=APPROVALS/(approval+'.json');trusted(path)
    require(path.stat().st_mode&0o777==0o400, 'unsafe receipt mode')
    data=json.loads(path.read_text())
    expected={'schema_version':1,'approval_id':approval,'environment':'zhycit-prod-01',
      'action':'grant-isolated-oauth','release_id':RID,'binary_sha256':SHA,'grant_sha256':grant_sha,
      'baseline_release_id':OLD,'baseline_binary_sha256':OLD_SHA,'gnas_canary_pid':before_pid,
      'old_unit_sha256':hashlib.sha256(adapter.unit_content(plan('none'))).hexdigest(),
      'new_unit_sha256':hashlib.sha256(adapter.unit_content(plan(grant_sha))).hexdigest(),
      'controller_sha256':digest(SELF),'adapter_sha256':digest(ADAPTER),'helper_sha256':digest(ENV_HELPER)}
    require(set(data)==set(expected)|{'approved_at','expires_at','approved_by'}
            and all(data.get(k)==v for k,v in expected.items()), 'receipt does not bind grant')
    approved=dt.datetime.fromisoformat(data['approved_at']);expires=dt.datetime.fromisoformat(data['expires_at']);now=dt.datetime.now(dt.timezone.utc)
    require(approved.tzinfo and expires.tzinfo and approved<=now<expires and expires-approved<=dt.timedelta(hours=4)
            and bool(data['approved_by']), 'receipt expired')


def replace_unit(raw):
    # The directory is root-owned and the exact old unit was checked already.
    fd,name=tempfile.mkstemp(prefix='.gnas-oauth-isolated-',dir=UNIT_PATH.parent)
    try:
        with os.fdopen(fd,'wb') as f:f.write(raw);f.flush();os.fsync(f.fileno())
        os.chmod(name,0o644);os.replace(name,UNIT_PATH)
    finally:
        if os.path.exists(name):os.unlink(name)


def ready(adapter):
    adapter.ProductionOps(plan('none'),'').verify_canary_health(plan('none'))


def main(approval, grant_sha):
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF and re.fullmatch(r'[a-f0-9]{64}',grant_sha), 'invalid arguments')
    for p in (Path('/usr'),Path('/usr/local'),HERE,Path('/etc'),Path('/etc/gnas'),APPROVALS,
              Path('/etc/systemd'),UNIT_PATH.parent,Path('/var/lib'),ROOT,ROOT/'used',Path('/etc/gnas/oauth21')):
        trusted(p,True)
    for p in (SELF,ADAPTER,ENV_HELPER,UNIT_PATH,ROOT/'release.lock',GRANT):trusted(p)
    require(GRANT.stat().st_mode&0o777==0o400 and digest(GRANT)==grant_sha,'target grant digest mismatch')
    adapter=load('isolated_adapter',ADAPTER);helper=load('isolated_environment_helper',ENV_HELPER)
    helper.load_target_grant({},grant_sha) # validates exact member, target resource and tool allowlist
    with (ROOT/'release.lock').open('r+') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        before=adapter.ProductionOps(plan('none'),'').snapshot_shared()
        require(before.matches(OLD,OLD_SHA),'shared baseline changed')
        require(route_unexposed(),'isolated service is already exposed')
        state=unit_properties();pid=int(state.get('MainPID','0'))
        require(state.get('ActiveState')=='active' and state.get('NRestarts')=='0' and pid>1
                and digest(Path('/proc')/str(pid)/'exe')==SHA,'canary changed')
        oldunit=adapter.unit_content(plan('none'));newunit=adapter.unit_content(plan(grant_sha))
        require(UNIT_PATH.read_bytes()==oldunit,'canary unit changed')
        check_receipt(approval,grant_sha,adapter,pid)
        fd=os.open(ROOT/'used'/approval,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o440)
        with os.fdopen(fd,'w') as f:f.write(dt.datetime.now(dt.timezone.utc).isoformat()+'\n');f.flush();os.fsync(f.fileno())
        try:
            require(route_unexposed(),'isolated service became exposed')
            run('/usr/bin/systemctl','stop',UNIT,timeout=35)
            replace_unit(newunit);run('/usr/bin/systemctl','daemon-reload',timeout=30)
            run('/usr/bin/systemctl','start',UNIT,timeout=50);ready(adapter)
            require(adapter.ProductionOps(plan('none'),'').snapshot_shared()==before,'shared baseline changed')
        except Exception:
            # Roll back only while neither isolated listener is public.
            if route_unexposed():
                run('/usr/bin/systemctl','stop',UNIT,timeout=35)
                replace_unit(oldunit);run('/usr/bin/systemctl','daemon-reload',timeout=30)
                run('/usr/bin/systemctl','start',UNIT,timeout=50);ready(adapter)
            raise
        print(json.dumps({'state':'target_grant_loaded_in_isolated_oauth','grant_sha256':grant_sha,
                          'public_route_changed':False,'shared_restarted':False,'approval_id':approval},sort_keys=True))


if __name__=='__main__':
    try:
        require(len(sys.argv)==3,'exact approval and grant digest required');main(sys.argv[1],sys.argv[2])
    except Exception as e:
        print('isolated grant refused or failed: '+(str(e) if isinstance(e,ValueError) else type(e).__name__),file=sys.stderr)
        sys.exit(1)
