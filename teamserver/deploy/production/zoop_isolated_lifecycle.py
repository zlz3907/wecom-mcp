#!/usr/bin/python3 -I
"""Make verified isolated units restartable and boot-persistent without restarting them."""
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
import types

HERE=Path('/usr/local/sbin')
SELF=HERE/'zoop-isolated-lifecycle.py'
MCP=HERE/'mcp-target-canary.py'
GNAS=HERE/'gnas-isolated-canary-prod.py'
ROUTE=HERE/'zoop-target-cutover.py'
HELPER=HERE/'gnas-isolated-env-exec.py'
GRANT=Path('/etc/gnas/oauth21/isolated-target-grant.json')
APPROVALS=Path('/etc/gnas/release-approvals')
GLOCK=Path('/var/lib/gnas-oauth-isolated/release.lock')
MROOT=Path('/var/lib/wecom-mcp-isolated')
GUNIT=Path('/etc/systemd/system/gnas-oauth-zhycit-isolated.service')
MUNIT=Path('/etc/systemd/system/wecom-mcp-zhycit-isolated.service')
WANTS=Path('/etc/systemd/system/multi-user.target.wants')


def require(v,msg):
    if not v:raise ValueError(msg)


def sha(p):return hashlib.sha256(p.read_bytes()).hexdigest()
def sha_bytes(b):return hashlib.sha256(b).hexdigest()


def trusted(p,directory=False):
    s=p.lstat();require(p.resolve()==p and s.st_uid==0 and not s.st_mode&0o022 and (p.is_dir() if directory else p.is_file()),'untrusted lifecycle path')


def load(name,p):
    trusted(p);s=importlib.util.spec_from_file_location(name,p);m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m


def run(*args,timeout=30):
    return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=timeout).stdout


def build_units(old_g,old_m):
    after=b'After=network-online.target\n';new_after=b'After=network-online.target gnas-server.service\n'
    restart=b'Restart=no\n';new_restart=b'Restart=on-failure\nRestartSec=5s\n'
    require(old_g.count(after)==1 and old_g.count(restart)==1 and old_m.count(restart)==1
            and b'[Install]' not in old_g and b'[Install]' not in old_m,'unexpected unit content')
    return (old_g.replace(after,new_after).replace(restart,new_restart)+b'[Install]\nWantedBy=multi-user.target\n',
            old_m.replace(restart,new_restart)+b'[Install]\nWantedBy=multi-user.target\n')


def replace(p,raw):
    fd,name=tempfile.mkstemp(prefix='.zoop-unit-',dir=p.parent)
    try:
        with os.fdopen(fd,'wb') as f:f.write(raw);f.flush();os.fsync(f.fileno())
        os.chmod(name,0o644);os.replace(name,p)
    finally:
        if os.path.exists(name):os.unlink(name)


def check_receipt(a,digest,canaries,old_g,old_m,new_g,new_m,deps):
    require(re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[a-zA-Z0-9._-]{6,64}',a),'invalid receipt')
    p=APPROVALS/(a+'.json');trusted(p);require(stat.S_IMODE(p.stat().st_mode)==0o400,'unsafe receipt mode')
    x=json.loads(p.read_bytes())
    expected={'schema_version':1,'approval_id':a,'environment':'zhycit-prod-01','action':'persist-isolated-zoop-units',
      'target':'zhycit.com','grant_sha256':digest,'oauth_canary_pid':canaries[0],'mcp_canary_pid':canaries[1],
      'old_gnas_unit_sha256':sha_bytes(old_g),'new_gnas_unit_sha256':sha_bytes(new_g),
      'old_mcp_unit_sha256':sha_bytes(old_m),'new_mcp_unit_sha256':sha_bytes(new_m),
      'controller_sha256':sha(SELF),'dependencies_sha256':deps}
    require(set(x)==set(expected)|{'approved_at','expires_at','approved_by'} and all(x.get(k)==v for k,v in expected.items()),'receipt does not bind lifecycle')
    now=dt.datetime.now(dt.timezone.utc);a1=dt.datetime.fromisoformat(x['approved_at']);end=dt.datetime.fromisoformat(x['expires_at'])
    require(a1.tzinfo and end.tzinfo and a1<=now<end and end-a1<=dt.timedelta(hours=4) and bool(x['approved_by']),'receipt expired')


def main(approval,grant_sha):
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF and re.fullmatch(r'[a-f0-9]{64}',grant_sha),'invalid arguments')
    for p in (Path('/usr'),Path('/usr/local'),HERE,Path('/etc'),Path('/etc/gnas'),APPROVALS,Path('/etc/systemd'),
              Path('/etc/systemd/system'),WANTS,Path('/var/lib'),MROOT,MROOT/'used',Path('/etc/gnas/oauth21')):trusted(p,True)
    for p in (SELF,MCP,GNAS,ROUTE,HELPER,GRANT,GLOCK,MROOT/'release.lock',GUNIT,MUNIT):trusted(p)
    require(stat.S_IMODE(GRANT.stat().st_mode)==0o400 and sha(GRANT)==grant_sha,'grant changed')
    m=load('mcp_lifecycle_control',MCP);g=load('gnas_lifecycle_control',GNAS);r=load('route_lifecycle_control',ROUTE);h=load('grant_lifecycle_helper',HELPER)
    deps={p.name:sha(p) for p in (MCP,GNAS,ROUTE,HELPER)}
    plan=types.SimpleNamespace(release_id='20261106T203247Z-932ca8583c69',binary_sha256=r.GNAS_SHA,
      baseline_release_id='20261106T040805Z-1acaa4269334',baseline_binary_sha256=m.OLD_GNAS_SHA,
      host='zhycit.com',listen='127.0.0.1:7503',grant_sha256=grant_sha)
    with contextlib.ExitStack() as stack:
        for p in (GLOCK,MROOT/'release.lock'):
            lock=stack.enter_context(p.open('r+'));fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        shared=m.snapshot();canaries=r.active_canaries(m,g,h,grant_sha)
        require(r.sha(r.TARGET)==r.ORIGINAL_SHA and not r.ISOLATED.exists(),'target is already switched')
        old_g=GUNIT.read_bytes();old_m=MUNIT.read_bytes()
        require(old_g==g.unit_content(plan) and old_m==m.unit_content(),'isolated unit baseline changed')
        new_g,new_m=build_units(old_g,old_m)
        for unit in (GUNIT,MUNIT):require(not (WANTS/unit.name).exists() and not (WANTS/unit.name).is_symlink(),'unit enablement already exists')
        check_receipt(approval,grant_sha,canaries,old_g,old_m,new_g,new_m,deps)
        fd=os.open(MROOT/'used'/approval,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o440)
        with os.fdopen(fd,'w') as f:f.write(dt.datetime.now(dt.timezone.utc).isoformat()+'\n');f.flush();os.fsync(f.fileno())
        changed=False
        try:
            require(GUNIT.read_bytes()==old_g and MUNIT.read_bytes()==old_m,'unit changed before persistence')
            replace(GUNIT,new_g);changed=True;replace(MUNIT,new_m)
            run('/usr/bin/systemctl','daemon-reload')
            run('/usr/bin/systemctl','enable',GUNIT.name,MUNIT.name)
            for unit in (GUNIT,MUNIT):
                link=WANTS/unit.name
                require(link.is_symlink() and link.resolve()==unit and run('/usr/bin/systemctl','is-enabled',unit.name).decode().strip()=='enabled',
                        'unit enablement not verified')
                require(run('/usr/bin/systemctl','show',unit.name,'-p','Restart','--value').decode().strip()=='on-failure','restart policy not effective')
            require(GUNIT.read_bytes()==new_g and MUNIT.read_bytes()==new_m and m.snapshot()==shared
                    and r.active_canaries(m,g,h,grant_sha)==canaries,'service changed during lifecycle update')
        except Exception:
            if changed:
                for unit,new,old in ((GUNIT,new_g,old_g),(MUNIT,new_m,old_m)):
                    link=WANTS/unit.name
                    if link.is_symlink() and link.resolve()==unit:link.unlink()
                    if unit.read_bytes()==new:replace(unit,old)
                run('/usr/bin/systemctl','daemon-reload')
            raise
        print(json.dumps({'state':'isolated_units_persisted','shared_restarted':False,'isolated_restarted':False,
                          'public_route_changed':False,'approval_id':approval},sort_keys=True))


if __name__=='__main__':
    try:
        require(len(sys.argv)==3,'exact approval and grant digest required');main(sys.argv[1],sys.argv[2])
    except Exception as e:
        print('isolated lifecycle refused or failed: '+(str(e) if isinstance(e,ValueError) else type(e).__name__),file=sys.stderr)
        sys.exit(1)
