import contextlib
import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

path=Path(__file__).with_name('zoop_next_release.py')
spec=importlib.util.spec_from_file_location('next_release',path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

def module(name,p):
 s=importlib.util.spec_from_file_location(name,p);x=importlib.util.module_from_spec(s);s.loader.exec_module(x);return x

class NextReleaseTests(unittest.TestCase):
 def test_scoped_grant_preserves_identity_and_prior_tools(self):
  base=Path('/Users/zhonglizhi/.codex/worktrees/zoop-isolated-control-20261107/gnas-service-gin/deploy/production/gnas-isolated-env-exec.py')
  # On CI, validate the same contract through a small strict fake rather than depend on another repository.
  if base.exists():helper=module('old_env_local',base)
  else:
   def add(env,raw):
    x=json.loads(raw)
    if x.get('resource')!='https://zhycit.com/mcp' or not x.get('user_id') or '*' in x.get('tools',[]):raise ValueError('invalid grant')
    return env
   helper=types.SimpleNamespace(add_target_grant=add)
  original={'schema_version':1,'resource':'https://zhycit.com/mcp','binding_id':'example-binding','user_id':'example-user','tools':sorted(m.EXISTING)}
  with tempfile.TemporaryDirectory() as tmp:
   p=Path(tmp)/'grant';p.write_text(json.dumps(original));p.chmod(0o400)
   with patch.object(m,'OLD_GRANT',p),patch.object(m,'OLD_SHA',m.sha(p)),patch.object(m,'trusted'):
    result=json.loads(m.grant_bytes(helper))
    self.assertEqual(result['binding_id'],original['binding_id']);self.assertEqual(result['user_id'],original['user_id'])
    self.assertEqual(set(result['tools']),set(original['tools'])|set(m.ADDITIONS));self.assertNotIn('*',result['tools'])
    self.assertEqual(len(result['tools']),12)
 def test_next_units_preserve_boundaries_and_enable_recovery(self):
  mcp=module('mcp_unit',path.with_name('mcp_target_canary.py'))
  def unit(_):return b'[Unit]\nAfter=network-online.target\n[Service]\nExecStart=/usr/bin/python3 -I /usr/local/sbin/gnas-isolated-env-exec.py rid sha old oldsha grant\nRestart=no\n'
  g,c=m.plans(types.SimpleNamespace(unit_content=unit),mcp,'a'*64)
  self.assertIn(b'zoop-next-env-exec.py',g);self.assertIn(b'After=network-online.target gnas-server.service',g)
  self.assertIn(b'GNAS_BASE_URL=http://127.0.0.1:7504',c);self.assertIn(b'--listen 127.0.0.1:7706',c)
  self.assertIn(b'gnas-oauth-zhycit-next.service',c)
  for unit in (g,c):self.assertIn(b'Restart=on-failure\n',unit);self.assertIn(b'WantedBy=multi-user.target',unit)
 def test_receipt_binds_target_and_next_grant(self):
  a='APR-20261107T010646Z-0541a94db7ab';now=dt.datetime.now(dt.timezone.utc);fields={'next_grant_sha256':'a'*64,'old_oauth_pid':123}
  obj={'schema_version':1,'approval_id':a,'environment':'zhycit-prod-01','action':'prepare-zoop-next','target':'zhycit.com',
   'controller_sha256':'c'*64,'dependencies_sha256':{'dep':'d'*64},**fields,'approved_at':now.isoformat(),
   'expires_at':(now+dt.timedelta(minutes=5)).isoformat(),'approved_by':'delegated-root'}
  with tempfile.TemporaryDirectory() as tmp,patch.object(m,'APPROVALS',Path(tmp)),patch.object(m,'trusted'),patch.object(m,'sha',return_value='c'*64),patch.object(m,'deps',return_value={'dep':'d'*64}):
   p=Path(tmp)/(a+'.json');p.write_text(json.dumps(obj));p.chmod(0o400);m.check_receipt(a,'prepare-zoop-next',fields)
   with self.assertRaisesRegex(ValueError,'binding mismatch'):m.check_receipt(a,'prepare-zoop-next',dict(fields,old_oauth_pid=124))
 def test_prepare_failure_removes_only_unexposed_next_artifacts(self):
  with tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
   root=Path(tmp).resolve();(root/'used').mkdir();(root/'release.lock').touch();gl=root/'glock';gl.touch();w=root/'wants';w.mkdir()
   paths={'ROOT':root,'GLOCK':gl,'WANTS':w,'PREP':root/'prep','NEXT_GRANT':root/'grant','GUNIT':root/'gnas.service','MUNIT':root/'mcp.service',
          'ENV':root/'env','GNAS':root/'gnas-controller','MCP':root/'mcp-controller','ROUTE':root/'route-controller'}
   for k,v in paths.items():stack.enter_context(patch.object(m,k,v))
   modules={m.ENV:object(),m.GNAS:object(),m.MCP:object(),m.ROUTE:object()}
   stack.enter_context(patch.object(m,'verify_paths'));stack.enter_context(patch.object(m,'load',side_effect=lambda n,p:modules[p]))
   stack.enter_context(patch.object(m,'current',return_value=('shared',(1,2),'vector',3)))
   stack.enter_context(patch.object(m,'next_unexposed',return_value=True));stack.enter_context(patch.object(m,'port_free',return_value=True))
   stack.enter_context(patch.object(m,'available_memory_kib',return_value=600000));stack.enter_context(patch.object(m,'grant_bytes',return_value=b'grant'))
   stack.enter_context(patch.object(m,'plans',return_value=(b'gnas',b'mcp')));stack.enter_context(patch.object(m,'check_receipt'))
   def run(*args,**kw):
    if args[1]=='-T':return b'old config'
    if args[1]=='enable':(w/m.GUNIT.name).symlink_to(m.GUNIT);(w/m.MUNIT.name).symlink_to(m.MUNIT)
    if args[1]=='start':raise RuntimeError('simulated startup failure')
    return b''
   stack.enter_context(patch.object(m,'run',side_effect=run))
   with self.assertRaises(RuntimeError):m.prepare('approval')
   for p in (m.NEXT_GRANT,m.GUNIT,m.MUNIT,w/m.GUNIT.name,w/m.MUNIT.name):self.assertFalse(p.exists() or p.is_symlink())
 def test_cutover_failure_restores_both_target_routes(self):
  vector=(('mcp.wesiyu.com',401,200),('mcp.rtyouth.com',401,200),('mcp.jianpinke.com',503,503))
  for fail in (False,True):
   with self.subTest(fail=fail),tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
    root=Path(tmp).resolve();(root/'used').mkdir();(root/'release.lock').touch();gl=root/'glock';gl.touch()
    target=root/'target';target.write_bytes(b'proxy_pass http://127.0.0.1:7705;\n'*2)
    include=root/'include';include.write_bytes(b'proxy_pass http://127.0.0.1:7503;\n');oldtarget=target.read_bytes();oldinclude=include.read_bytes()
    raw=b'next-grant';grant=root/'grant';grant.write_bytes(raw);gu=root/'gnas.service';gu.touch();mu=root/'mcp.service';mu.touch()
    prep=root/'prep';prep.write_text(json.dumps({'next_grant_sha256':m.digest(raw),'next_oauth_pid':4,'next_mcp_pid':5}))
    paths={'ROOT':root,'GLOCK':gl,'PREP':prep,'ACTIVATION':root/'activation','NEXT_GRANT':grant,'GUNIT':gu,'MUNIT':mu,
           'ENV':root/'env','GNAS':root/'gnas-controller','MCP':root/'mcp-controller','ROUTE':root/'route-controller'}
    for k,v in paths.items():stack.enter_context(patch.object(m,k,v))
    mc=types.SimpleNamespace(snapshot=lambda:'shared');route=types.SimpleNamespace(TARGET=target,ISOLATED=include,unit=lambda n:{'MainPID':'3'},
        other_public_vector=lambda:vector,active_canaries=lambda *args:(1,2),tls_probe=lambda host,p:401 if p=='/mcp' else 200)
    mods={m.ENV:object(),m.GNAS:object(),m.MCP:mc,m.ROUTE:route}
    stack.enter_context(patch.object(m,'verify_paths'));stack.enter_context(patch.object(m,'trusted'));stack.enter_context(patch.object(m,'load',side_effect=lambda n,p:mods[p]))
    stack.enter_context(patch.object(m,'current',return_value=('shared',(1,2),vector,3)));stack.enter_context(patch.object(m,'grant_bytes',return_value=raw));stack.enter_context(patch.object(m,'check_receipt'))
    stack.enter_context(patch.object(m,'next_ready',side_effect=([(4,5),ValueError('failed')] if fail else [(4,5),(4,5)])))
    calls=[]
    def run(*args,**kw):calls.append(args);return b'original nginx' if args[1]=='-T' else b''
    stack.enter_context(patch.object(m,'run',side_effect=run))
    if fail:
     with self.assertRaisesRegex(ValueError,'failed'):m.cutover('approval')
     self.assertEqual(target.read_bytes(),oldtarget);self.assertEqual(include.read_bytes(),oldinclude)
     self.assertEqual(sum(x[1:3]==('reload','nginx.service') for x in calls),2)
    else:
     m.cutover('approval');self.assertIn(b'7706',target.read_bytes());self.assertIn(b'7504',include.read_bytes());self.assertTrue(m.ACTIVATION.exists())
    self.assertFalse(any('restart' in c for c in calls))

if __name__=='__main__':unittest.main()
