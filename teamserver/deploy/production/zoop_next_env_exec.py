#!/usr/bin/python3 -I
"""Run the already-reviewed OAuth binary with the next target-only grant."""
import hashlib
import importlib.util
import os
from pathlib import Path
import re
import stat
import sys

HERE=Path('/usr/local/sbin')
SELF=HERE/'zoop-next-env-exec.py'
BASE=HERE/'gnas-isolated-env-exec.py'
GRANT=Path('/etc/gnas/oauth21/isolated-target-grant-v2.json')


def require(v,msg):
    if not v:raise ValueError(msg)


def trusted(p,directory=False):
    s=p.lstat();require(p.resolve()==p and s.st_uid==0 and not s.st_mode&0o022 and
        (stat.S_ISDIR(s.st_mode) if directory else stat.S_ISREG(s.st_mode)),'untrusted next OAuth path')


def main():
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF and len(sys.argv)==6,'invalid next OAuth arguments')
    for p in (Path('/usr'),Path('/usr/local'),HERE,Path('/etc'),Path('/etc/gnas'),GRANT.parent):trusted(p,True)
    for p in (SELF,BASE,GRANT):trusted(p)
    require(stat.S_IMODE(GRANT.stat().st_mode)==0o400 and re.fullmatch(r'[a-f0-9]{64}',sys.argv[5]),'invalid grant')
    raw=GRANT.read_bytes();require(len(raw)<=16384 and hashlib.sha256(raw).hexdigest()==sys.argv[5],'grant mismatch')
    spec=importlib.util.spec_from_file_location('existing_isolated_env',BASE);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
    target,env=m.prepare(*sys.argv[1:5]);m.add_target_grant(env,raw)
    env['GNAS_OAUTH21_ISOLATED_LISTEN']='127.0.0.1:7504'
    os.execve(target,[str(target)],env)


if __name__=='__main__':
    try:main()
    except Exception:
        print('next isolated OAuth start refused',file=sys.stderr);sys.exit(1)
