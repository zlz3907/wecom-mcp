#!/usr/bin/python3 -I
"""Read-only preflight of the exact Zhonghe isolated OAuth release.

This is not a switch controller and cannot install, start, or change services.
It prints no environment values or protected configuration.
"""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import socket
import stat
import subprocess

RID='20261108T144914Z-e3620ffda6aa'
BINARY='cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1'
MANIFEST='0c6903f7f2161c190a1a7ec4af5d6dc976d8a271f58210e13d7422f5ff919e7f'
SHARED='20261107T193406Z-4c9c55635682'
SHARED_SHA='a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e'
OLD='20261106T203247Z-932ca8583c69'
OLD_SHA='d5b8cb5c98e74f45e7cc54b645ae9d686fa7003e9391bcaa85317101f3d531df'
UNIT_SHA='acacb97d639155f35986650eee3b51c583424326ef1611b95f6349b5d5c2e098'
GRANT_SHA='d647a79758f25323ca187c2a0d44ab0a08ae3ca2535e2d8a5f583d0ebc97f6ca'


def require(value, message):
    if not value: raise ValueError(message)


def sha(path):
    h=hashlib.sha256()
    with path.open('rb') as f:
        for part in iter(lambda:f.read(1024*1024),b''):h.update(part)
    return h.hexdigest()


def run(*args):
    return subprocess.run(args,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,timeout=30).stdout


def props(unit):
    raw=run('/usr/bin/systemctl','show',unit,'-p','ActiveState','-p','MainPID','-p','NRestarts').decode()
    return dict(row.split('=',1) for row in raw.splitlines() if '=' in row)


def trusted(path):
    info=path.lstat()
    require(path.resolve()==path and stat.S_ISREG(info.st_mode) and info.st_uid==0 and not info.st_mode&0o022,'untrusted control path')


def main():
    require(os.geteuid()==0,'root read access required')
    shared=dict(row.split('=',1) for row in run('/usr/local/sbin/gnas-release-controller','status').decode().splitlines() if '=' in row)
    require(shared.get('release_id')==SHARED and shared.get('runtime_sha256')==SHARED_SHA and
            shared.get('service_state')=='active' and shared.get('restarts')=='0','shared baseline changed')
    unit=Path('/etc/systemd/system/gnas-oauth-zhycit-next.service');trusted(unit)
    require(sha(unit)==UNIT_SHA,'isolated unit changed')
    isolated=props('gnas-oauth-zhycit-next.service');pid=int(isolated.get('MainPID','0'))
    require(isolated.get('ActiveState')=='active' and isolated.get('NRestarts')=='0' and pid>1,'isolated service unavailable')
    exe=Path('/proc')/str(pid)/'exe'
    require(exe.resolve()==Path('/var/lib/gnas-oauth-isolated/releases')/OLD/'GNASServer' and sha(exe)==OLD_SHA,'isolated process changed')
    mcp=props('wecom-mcp-zhycit-migration.service')
    require(mcp.get('ActiveState')=='active' and mcp.get('NRestarts')=='0' and int(mcp.get('MainPID','0'))>1,'target MCP unavailable')
    grant=Path('/etc/gnas/oauth21/isolated-target-grant-v2.json');trusted(grant)
    require(stat.S_IMODE(grant.stat().st_mode)==0o400 and sha(grant)==GRANT_SHA,'grant changed')
    incoming=Path('/var/lib/gnas-deployer/incoming')/RID
    manifest=incoming/'manifest.json'; binary=incoming/'GNASServer'
    for path in (manifest,binary): trusted(path)
    require(sha(manifest)==MANIFEST and sha(binary)==BINARY,'candidate changed')
    data=json.loads(manifest.read_bytes())
    require(data.get('release_id')==RID and data.get('previous_release_id')==SHARED and
            data.get('previous_binary_sha256')==SHARED_SHA and data.get('binary',{}).get('sha256')==BINARY,'manifest baseline changed')
    s=socket.socket()
    try:s.bind(('127.0.0.1',7505))
    finally:s.close()
    nginx=run('/usr/sbin/nginx','-T')
    require(re.search(rb'(?<![0-9])7505(?![0-9])',nginx) is None,'probe port is publicly referenced')
    for path in (Path('/usr/local/sbin/gnas-isolated-canary-prod.py'),Path('/usr/local/sbin/zoop-target-cutover.py')): trusted(path)
    def load(name,path):
        spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
    g=load('isolated_health','/usr/local/sbin/gnas-isolated-canary-prod.py')
    route=load('target_health','/usr/local/sbin/zoop-target-cutover.py')
    require(g.oauth_fingerprint(7501)==g.oauth_fingerprint(7504),'OAuth keys or issuer changed')
    vector=route.other_public_vector()
    require(vector==(('mcp.wesiyu.com',401,200),('mcp.rtyouth.com',401,200),('mcp.jianpinke.com',503,503)),'other enterprise baseline changed')
    require(route.tls_probe('zhycit.com','/mcp')==401 and route.tls_probe('zhycit.com','/.well-known/oauth-protected-resource/mcp')==200,'target public endpoint unavailable')
    print(json.dumps({'result':'read_only_preflight_passed','candidate':RID,'shared':SHARED,'old_isolated':OLD,
                      'oauth_issuer_keys_match':True,'probe_port_free':True,'other_enterprise_baseline_unchanged':True},sort_keys=True))


if __name__=='__main__':
    try:main()
    except Exception as error:
        print(json.dumps({'result':'read_only_preflight_failed','error':str(error) if isinstance(error,ValueError) else type(error).__name__}))
        raise SystemExit(1)
