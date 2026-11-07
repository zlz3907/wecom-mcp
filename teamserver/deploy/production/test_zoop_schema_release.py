import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location('schema_release',Path(__file__).with_name('zoop_schema_release.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class TargetSchemaReleaseTests(unittest.TestCase):
    def test_unit_is_target_only_and_admin_is_disabled(self):
        unit=m.unit_content()
        self.assertIn(b'127.0.0.1:7707',unit)
        self.assertIn(b'127.0.0.1:7504',unit)
        self.assertIn(b'-u GNAS_SCHEMA_ADMIN_BINDING_DIGEST -u GNAS_SCHEMA_ADMIN_USERID',unit)
        self.assertIn(b'Restart=on-failure',unit)
        self.assertNotIn(b'127.0.0.1:7703',unit)
        self.assertNotIn(b'gnas-server.service',unit)

    def test_exact_target_route_and_reversible_replacement(self):
        old=b'prefix '+m.OLD_PORT+b' suffix '+m.OLD_PORT+b' unchanged'
        new=old.replace(m.OLD_PORT,m.NEW_PORT)
        self.assertEqual(new.count(m.NEW_PORT),2)
        self.assertEqual(new.replace(m.NEW_PORT,m.OLD_PORT),old)
        with tempfile.TemporaryDirectory() as d:
            p=Path(d)/'route';p.write_bytes(old)
            m.replace(p,new,0o600);self.assertEqual(p.read_bytes(),new)
            m.replace(p,old,0o600);self.assertEqual(p.read_bytes(),old)

    def test_receipt_binds_exact_action_binary_and_baseline(self):
        with tempfile.TemporaryDirectory() as d:
            path=Path(d);controller=path/'controller';controller.write_text('controller')
            a='APR-20261107T010101Z-test00';now=dt.datetime.now(dt.timezone.utc)
            fields={'old_route_sha256':'baseline','old_mcp_pid':4}
            receipt={'schema_version':1,'approval_id':a,'action':'cutover-schema-target','target':'zhycit.com','source_commit':m.SOURCE,
                     'binary_sha256':m.BIN_SHA,'controller_sha256':m.sha(controller),**fields,
                     'approved_at':(now-dt.timedelta(seconds=1)).isoformat(),'expires_at':(now+dt.timedelta(minutes=5)).isoformat(),'approved_by':'owner-delegated-root'}
            p=path/(a+'.json');p.write_text(json.dumps(receipt));p.chmod(0o400)
            with mock.patch.object(m,'APPROVALS',path),mock.patch.object(m,'SELF',controller),mock.patch.object(m,'root_owned_regular',return_value=True):
                m.verify_receipt(a,'cutover-schema-target',fields)
                with self.assertRaises(ValueError):m.verify_receipt(a,'cutover-schema-target',{**fields,'old_mcp_pid':5})
                with self.assertRaises(ValueError):m.verify_receipt(a,'rollback-schema-target',fields)

if __name__=='__main__':unittest.main()
