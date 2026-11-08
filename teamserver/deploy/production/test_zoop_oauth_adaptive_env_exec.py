import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

path=Path(__file__).with_name('zoop_oauth_adaptive_env_exec.py')
spec=importlib.util.spec_from_file_location('adaptive',path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class AdaptiveTests(unittest.TestCase):
    def test_current_baseline_reads_immutable_manifest_and_rejects_wrong_environment(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp).resolve(); releases=root/'releases';releases.mkdir();rid='20261107T193406Z-4c9c55635682'
            directory=releases/rid;directory.mkdir();(root/'current').symlink_to(directory)
            manifest={'schema_version':2,'environment':'zhycit-prod-01','artifact':'gnas-service-gin','release_id':rid,
                      'binary':{'name':'GNASServer','sha256':'a'*64}}
            p=directory/'manifest.json';p.write_text(json.dumps(manifest))
            real_lstat=Path.lstat
            def root_owner(p):
                s=real_lstat(p)
                return types.SimpleNamespace(st_uid=0,st_mode=s.st_mode)
            with patch.object(m,'ROOT',root),patch.object(Path,'lstat',root_owner):
                self.assertEqual(m.current_baseline(),(rid,'a'*64))
                manifest['environment']='another';p.write_text(json.dumps(manifest))
                with self.assertRaisesRegex(ValueError,'manifest'):m.current_baseline()
    def test_prepare_uses_current_validated_baseline_and_scoped_loopback(self):
        with tempfile.TemporaryDirectory() as tmp:
            grant=Path(tmp)/'grant';grant.write_bytes(b'grant');grant.chmod(0o400)
            digest=hashlib.sha256(grant.read_bytes()).hexdigest();calls=[];env={}
            def prep(*args):calls.append(args);return Path('/release/GNASServer'),env
            helper=types.SimpleNamespace(prepare=prep,add_target_grant=lambda e,r:e.update({'GRANT_TEST':'present'}))
            spec=types.SimpleNamespace(loader=types.SimpleNamespace(exec_module=lambda _:None))
            with patch.object(m,'GRANT',grant),patch.object(m,'trusted'),patch.object(m,'current_baseline',return_value=('20261107T193406Z-4c9c55635682','b'*64)),patch.object(m.importlib.util,'spec_from_file_location',return_value=spec),patch.object(m.importlib.util,'module_from_spec',return_value=helper):
                args=['20261108T144914Z-e3620ffda6aa','a'*64,digest,'live']
                _,result=m.prepare(args);self.assertEqual(result['GNAS_OAUTH21_ISOLATED_LISTEN'],'127.0.0.1:7504')
                self.assertEqual(calls[0][2:],('20261107T193406Z-4c9c55635682','b'*64))
                _,result=m.prepare(args[:-1]+['probe']);self.assertEqual(result['GNAS_OAUTH21_ISOLATED_LISTEN'],'127.0.0.1:7505')
                with self.assertRaisesRegex(ValueError,'arguments'):m.prepare(args[:-1]+['0.0.0.0:7504'])
