import datetime as dt
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

path=Path(__file__).with_name('gnas_target_grant_canary.py')
spec=importlib.util.spec_from_file_location('grant_canary',path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class GrantCanaryTests(unittest.TestCase):
    def test_routing_detection_checks_both_ports(self):
        for body in (b'proxy_pass http://127.0.0.1:7503;',b'proxy_pass http://127.0.0.1:7705;'):
            with patch.object(m,'run',return_value=body):self.assertFalse(m.route_unexposed())
        with patch.object(m,'run',return_value=b'proxy_pass http://127.0.0.1:7703;'):
            self.assertTrue(m.route_unexposed())

    def test_receipt_binds_pid_grant_and_unit(self):
        a='APR-20261106T203409Z-4e6b6a0e50e2';g='a'*64;now=dt.datetime.now(dt.timezone.utc)
        adapter=types.SimpleNamespace(unit_content=lambda p:('grant='+p.grant_sha256).encode())
        x={'schema_version':1,'approval_id':a,'environment':'zhycit-prod-01','action':'grant-isolated-oauth',
           'release_id':m.RID,'binary_sha256':m.SHA,'grant_sha256':g,'baseline_release_id':m.OLD,
           'baseline_binary_sha256':m.OLD_SHA,'gnas_canary_pid':123,
           'old_unit_sha256':hashlib.sha256(adapter.unit_content(m.plan('none'))).hexdigest(),
           'new_unit_sha256':hashlib.sha256(adapter.unit_content(m.plan(g))).hexdigest(),
           'controller_sha256':'c'*64,'adapter_sha256':'c'*64,'helper_sha256':'c'*64,
           'approved_at':now.isoformat(),'expires_at':(now+dt.timedelta(minutes=5)).isoformat(),'approved_by':'delegated-root'}
        with tempfile.TemporaryDirectory() as tmp,patch.object(m,'APPROVALS',Path(tmp)),patch.object(m,'trusted'),patch.object(m,'digest',return_value='c'*64):
            p=Path(tmp)/(a+'.json')
            def write():p.write_text(json.dumps(x));p.chmod(0o400)
            write();m.check_receipt(a,g,adapter,123)
            with self.assertRaisesRegex(ValueError,'does not bind'):m.check_receipt(a,g,adapter,124)
            p.chmod(0o600);x['grant_sha256']='b'*64;write()
            with self.assertRaisesRegex(ValueError,'does not bind'):m.check_receipt(a,g,adapter,123)

    def test_unit_replacement_is_atomic_and_exact(self):
        with tempfile.TemporaryDirectory() as tmp,patch.object(m,'UNIT_PATH',Path(tmp)/'test.service'):
            m.UNIT_PATH.write_bytes(b'old')
            m.replace_unit(b'new')
            self.assertEqual(m.UNIT_PATH.read_bytes(),b'new')
            self.assertEqual(m.UNIT_PATH.stat().st_mode&0o777,0o644)
            self.assertEqual([x.name for x in Path(tmp).iterdir()],['test.service'])

if __name__=='__main__':unittest.main()
