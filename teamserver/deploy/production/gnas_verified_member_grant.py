#!/usr/bin/python3 -I
"""Create a target-only grant after live GNAS verifies a real OAuth member.

Reads a short-lived token from stdin only; never prints or stores that token.
"""
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

ROOT=Path('/etc/gnas/oauth21')
GRANT=ROOT/'isolated-target-grant.json'
SELF=Path('/usr/local/sbin/gnas-verified-member-grant.py')
HELPER=Path('/usr/local/sbin/gnas-isolated-env-exec.py')
TOOLS=['wecom_record_apply','wecom_requirement_progress_reconcile','wecom_field_codec_lab_create',
       'wecom_field_codec_lab_register','wecom_field_codec_lab_write_probe','wecom_field_codec_lab_replay_probe',
       'wecom_field_codec_lab_reference_write_probe']


def require(ok,message):
    if not ok:raise ValueError(message)


def trusted(p,directory=False):
    s=p.lstat()
    require(p.resolve()==p and s.st_uid==0 and not s.st_mode&0o022 and (p.is_dir() if directory else p.is_file()), 'untrusted path')


def request(path,body,headers):
    c=http.client.HTTPConnection('127.0.0.1',7503,timeout=10)
    try:
        c.request('POST',path,body=body,headers=headers);r=c.getresponse();raw=r.read(1048577)
        require(r.status==200 and len(raw)<=1048576, 'GNAS verification rejected')
        x=json.loads(raw);require(x.get('code')==200 and isinstance(x.get('data'),dict),'GNAS verification failed')
        return x['data']
    finally:c.close()


def target_binding(bindings):
    rows=[r for r in bindings if r.get('public_resource')=='https://zhycit.com']
    require(len(rows)==1,'target binding not unique')
    return rows[0]


def binding_digest(binding):
    # Preserve the order of the JSON fields emitted by the GNAS Go struct.
    s=json.dumps(binding,ensure_ascii=False,separators=(',',':'))
    s=s.replace('&','\\u0026').replace('<','\\u003c').replace('>','\\u003e')
    s=s.replace('\u2028','\\u2028').replace('\u2029','\\u2029')
    return hashlib.sha256(s.encode()).hexdigest()


def validate_member(result,binding,client_id):
    user=result.get('wecom_userid');tenant=result.get('tenant');aud=result.get('aud')
    if isinstance(aud,str):aud=[aud]
    now=time.time()
    require(result.get('active') is True and result.get('iss')=='https://zhycit.com/gnas/oauth'
            and aud==['https://zhycit.com/mcp'] and result.get('token_use')=='access'
            and tenant==binding.get('binding_id') and isinstance(user,str)
            and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,127}',user)
            and 'openid' not in user and 'external_userid' not in user
            and result.get('sub')=='wecom:'+tenant+':'+user and result.get('client_id')==client_id
            and {'zoop.read','zoop.write'}.issubset(str(result.get('scope','')).split())
            and isinstance(result.get('iat'),(int,float)) and isinstance(result.get('exp'),(int,float))
            and result['iat']<=now<result['exp'] and result['exp']-result['iat']<=360
            and not result.get('ai_subject'), 'employee OAuth identity not verified')
    return user


def main():
    require(os.geteuid()==0 and Path(__file__).resolve()==SELF,'invalid verifier installation')
    for p in (Path('/usr'),Path('/usr/local'),SELF.parent,Path('/etc'),Path('/etc/gnas'),ROOT):trusted(p,True)
    for p in (SELF,HELPER):trusted(p)
    raw=sys.stdin.buffer.read(32769);require(len(raw)<=32768,'request too large')
    incoming=json.loads(raw);require(set(incoming)=={'token','client_id'} and
        isinstance(incoming['token'],str) and 0<len(incoming['token'])<=16384 and
        isinstance(incoming['client_id'],str) and 0<len(incoming['client_id'])<=256,'invalid request')
    pid=int(subprocess.check_output(['/usr/bin/systemctl','show','wecom-mcp-zhycit-isolated.service','-p','MainPID','--value'],text=True).strip())
    require(pid>1,'isolated MCP is unavailable')
    env=dict(x.split(b'=',1) for x in (Path('/proc')/str(pid)/'environ').read_bytes().split(b'\0') if b'=' in x)
    require(env.get(b'GNAS_BASE_URL')==b'http://127.0.0.1:7503','incorrect authority')
    credential=json.dumps({'app_id':env[b'GNAS_APP_ID'].decode(),'app_secret':env[b'GNAS_APP_SECRET'].decode()})
    service=request('/gnas/service/getJwtToken',credential,{'Content-Type':'application/json'})
    token=service.get('token');require(isinstance(token,str) and token,'service authentication failed')
    headers={'Authorization':'Bearer '+token,'X-Auth-Type':'service_jwt'}
    fleet=request('/gnas/service/resolveMCPBindingsV1',None,headers)
    binding=target_binding(fleet['bindings']);headers['Content-Type']='application/json'
    payload=json.dumps({'binding_id':binding['binding_id'],'binding_digest':binding_digest(binding),'token':incoming['token']})
    result=request('/gnas/service/introspectMCPTokenV1',payload,headers)
    user=validate_member(result,binding,incoming['client_id'])
    grant={'schema_version':1,'resource':'https://zhycit.com/mcp','binding_id':binding['binding_id'],'user_id':user,'tools':TOOLS}
    encoded=(json.dumps(grant,separators=(',',':'))+'\n').encode()
    spec=importlib.util.spec_from_file_location('safe_env_helper',HELPER);helper=importlib.util.module_from_spec(spec);spec.loader.exec_module(helper)
    helper.add_target_grant({},encoded)
    fd=os.open(GRANT,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o400)
    with os.fdopen(fd,'wb') as f:f.write(encoded);f.flush();os.fsync(f.fileno())
    print(json.dumps({'state':'verified_target_member_grant_prepared','grant_sha256':hashlib.sha256(encoded).hexdigest(),
                      'member_and_token_redacted':True,'public_route_changed':False},sort_keys=True))


if __name__=='__main__':
    try:main()
    except Exception as e:
        print('member grant not prepared: '+(str(e) if isinstance(e,ValueError) else type(e).__name__),file=sys.stderr)
        sys.exit(1)
