#!/usr/bin/python3 -I
"""Start the isolated OAuth process against the currently running authority.

Only root-owned immutable main releases are accepted; the existing loader then
verifies the current symlink, running binary and process twice before extracting
its allowlisted environment. No environment values or secrets are persisted.
"""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import stat
import sys

SELF=Path('/usr/local/sbin/zoop-oauth-adaptive-env-exec.py')
BASE=Path('/usr/local/sbin/gnas-isolated-env-exec.py')
ROOT=Path('/home/product/services/gnas-service-gin')
GRANT=Path('/etc/gnas/oauth21/isolated-target-grant-v2.json')
RID=re.compile(r'^[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]+$')
SHA=re.compile(r'^[0-9a-f]{64}$')


def require(value, message):
    if not value: raise ValueError(message)


def trusted(path, directory=False):
    s=path.lstat()
    require(path.resolve()==path and s.st_uid==0 and not s.st_mode&0o022 and
            (stat.S_ISDIR(s.st_mode) if directory else stat.S_ISREG(s.st_mode)), 'untrusted path')


def current_baseline():
    releases=ROOT/'releases';trusted(releases,True)
    current=ROOT/'current';s=current.lstat()
    require(stat.S_ISLNK(s.st_mode) and s.st_uid==0,'untrusted current link')
    directory=current.resolve(strict=True)
    require(directory.parent==releases and RID.fullmatch(directory.name),'invalid current release')
    trusted(directory,True)
    manifest=directory/'manifest.json';trusted(manifest)
    raw=manifest.read_bytes();require(len(raw)<=65536,'manifest too large')
    m=json.loads(raw);binary=m.get('binary',{})
    require(m.get('schema_version')==2 and m.get('environment')=='zhycit-prod-01' and
            m.get('artifact')=='gnas-service-gin' and m.get('release_id')==directory.name and
            binary.get('name')=='GNASServer' and SHA.fullmatch(binary.get('sha256','')),
            'invalid current manifest')
    return directory.name,binary['sha256']


def same_grant(left,right):
    def canonical(raw):
        value=json.loads(raw)
        require(isinstance(value,list) and len(value)==1 and isinstance(value[0],dict) and
          set(value[0])=={'userid','tools'},'invalid inherited target grant')
        user=value[0]['userid'];tools=value[0]['tools']
        require(isinstance(user,str) and isinstance(tools,list) and all(isinstance(x,str) for x in tools) and
          len(tools)==len(set(tools)),'invalid inherited target grant')
        return user,tuple(sorted(tools))
    return canonical(left)==canonical(right)


def prepare(argv):
    require(len(argv)==4 and RID.fullmatch(argv[0]) and SHA.fullmatch(argv[1]) and SHA.fullmatch(argv[2])
            and argv[3] in ('live','probe'),'invalid arguments')
    for p in (Path('/usr'),Path('/usr/local'),SELF.parent,Path('/etc'),Path('/etc/gnas'),GRANT.parent):trusted(p,True)
    for p in (SELF,BASE,GRANT):trusted(p)
    require(stat.S_IMODE(GRANT.stat().st_mode)==0o400,'invalid grant mode')
    raw=GRANT.read_bytes();require(len(raw)<=16384 and hashlib.sha256(raw).hexdigest()==argv[2],'grant digest changed')
    baseline,baseline_sha=current_baseline()
    spec=importlib.util.spec_from_file_location('isolated_loader',BASE)
    helper=importlib.util.module_from_spec(spec);spec.loader.exec_module(helper)
    binary,env=helper.prepare(argv[0],argv[1],baseline,baseline_sha)
    expected={};helper.add_target_grant(expected,raw)
    require(len(expected)==1,'invalid target grant')
    key,value=next(iter(expected.items()))
    if key in env:require(same_grant(env[key],value),'inherited target grant conflicts')
    else:env[key]=value
    env['GNAS_OAUTH21_ISOLATED_LISTEN']='127.0.0.1:'+('7504' if argv[3]=='live' else '7505')
    return binary,env


def main():
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF,'invalid launch context')
    binary,env=prepare(sys.argv[1:]);os.execve(binary,[str(binary)],env)


if __name__=='__main__':
    try:main()
    except Exception:
        print('adaptive isolated OAuth start refused',file=sys.stderr);sys.exit(1)
