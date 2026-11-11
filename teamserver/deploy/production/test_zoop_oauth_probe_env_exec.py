import hashlib
import importlib.util
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

path = Path(__file__).with_name('zoop_oauth_probe_env_exec.py')
spec = importlib.util.spec_from_file_location('probe_env', path)
m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)

class ProbeEnvTests(unittest.TestCase):
    def test_only_verified_grant_and_explicit_loopback_port_are_used(self):
        with tempfile.TemporaryDirectory() as tmp:
            grant=Path(tmp)/'grant'; grant.write_bytes(b'grant'); grant.chmod(0o400)
            digest=hashlib.sha256(grant.read_bytes()).hexdigest()
            env={'PATH':'safe'}
            helper=types.SimpleNamespace(prepare=lambda *args:(Path('/release/GNASServer'),env),add_target_grant=lambda e,r:e.update({'GRANT_TEST':'ok'}))
            spec=types.SimpleNamespace(loader=types.SimpleNamespace(exec_module=lambda _:None))
            with patch.object(m,'GRANT',grant),patch.object(m,'trusted'),patch.object(m.importlib.util,'spec_from_file_location',return_value=spec),patch.object(m.importlib.util,'module_from_spec',return_value=helper):
                binary,result=m.prepare(['rid','sha','old','oldsha',digest,'7505'])
                self.assertEqual(binary,Path('/release/GNASServer'))
                self.assertEqual(result['GNAS_OAUTH21_ISOLATED_LISTEN'],'127.0.0.1:7505')
                self.assertEqual(result['GRANT_TEST'],'ok')
                for args in (['rid','sha','old','oldsha',digest,'7504'],['rid','sha','old','oldsha','a'*64,'7505']):
                    with self.assertRaises(ValueError): m.prepare(args)

if __name__ == '__main__': unittest.main()
