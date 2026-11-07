import hashlib
import importlib.util
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

path=Path(__file__).with_name('zoop_next_env_exec.py');spec=importlib.util.spec_from_file_location('next_env',path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class NextEnvTests(unittest.TestCase):
 def test_exec_uses_verified_grant_and_loopback_without_exposing_other_variables(self):
  with tempfile.TemporaryDirectory() as tmp:
   g=Path(tmp)/'grant';g.write_bytes(b'valid-grant');g.chmod(0o400);digest=hashlib.sha256(g.read_bytes()).hexdigest();env={'PATH':'safe'}
   helper=types.SimpleNamespace(prepare=lambda *args:(Path('/release/GNASServer'),env),add_target_grant=lambda e,raw:e.update({'GNAS_OAUTH21_USER_TOOLS_TEST':'scoped'}))
   loader=types.SimpleNamespace(exec_module=lambda obj:None);spec=types.SimpleNamespace(loader=loader)
   with patch.object(m,'SELF',path.resolve()),patch.object(m,'GRANT',g),patch.object(m,'trusted'),patch.object(m.os,'geteuid',return_value=0),\
        patch.object(m.sys,'argv',['program','rid','sha','old','oldsha',digest]),patch.object(m.importlib.util,'spec_from_file_location',return_value=spec),\
        patch.object(m.importlib.util,'module_from_spec',return_value=helper),patch.object(m.os,'execve',side_effect=RuntimeError('exec intercepted')) as execute:
    with self.assertRaisesRegex(RuntimeError,'intercepted'):m.main()
    args=execute.call_args.args
    self.assertEqual(args[2]['GNAS_OAUTH21_ISOLATED_LISTEN'],'127.0.0.1:7504')
    self.assertEqual(args[2]['GNAS_OAUTH21_USER_TOOLS_TEST'],'scoped')
 def test_digest_mismatch_refuses_execution(self):
  with tempfile.TemporaryDirectory() as tmp:
   g=Path(tmp)/'grant';g.write_bytes(b'valid-grant');g.chmod(0o400)
   with patch.object(m,'SELF',path.resolve()),patch.object(m,'GRANT',g),patch.object(m,'trusted'),patch.object(m.os,'geteuid',return_value=0),\
        patch.object(m.sys,'argv',['program','rid','sha','old','oldsha','a'*64]),patch.object(m.os,'execve') as execute:
    with self.assertRaisesRegex(ValueError,'grant mismatch'):m.main()
    execute.assert_not_called()
if __name__=='__main__':unittest.main()
