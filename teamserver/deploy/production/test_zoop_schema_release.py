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


class RollbackTests(unittest.TestCase):
    def test_rollback_does_not_require_failed_candidate(self):
        from contextlib import redirect_stdout
        from io import StringIO
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory).resolve()
            route=root/'route'; old=b'x '+m.OLD_PORT+b' y '+m.OLD_PORT+b' z'; new=old.replace(m.OLD_PORT,m.NEW_PORT)
            route.write_bytes(new);route.chmod(0o644)
            (root/'release.lock').touch(); (root/'gnas.lock').touch()
            baseline=(('123',9,()),(11,12),())
            common={'old_route_sha256':m.digest(old),'old_oauth_pid':11,'old_mcp_pid':12,'shared_gnas_pid':'123','shared_mcp_pid':9,'route_mode':0o644}
            prep=root/'prepared';prep.write_text(json.dumps({**common,'candidate_pid':99}))
            activation=root/'activation';activation.write_text(json.dumps({'old_route':old.decode(),'old_route_sha256':m.digest(old),'new_route_sha256':m.digest(new)}))
            route_api=SimpleNamespace(tls_probe=lambda _host,path:401 if path=='/mcp' else 200)
            modules={'mcp_schema':object(),'route_schema':route_api,'next_schema':object(),'env_schema':object(),'gnas_schema':object()}
            with mock.patch.object(m,'ROOT',root),mock.patch.object(m,'GLOCK',root/'gnas.lock'),mock.patch.object(m,'TARGET',route),mock.patch.object(m,'PREP',prep),mock.patch.object(m,'ACTIVATION',activation),mock.patch.object(m,'SELF',Path(m.__file__).resolve()),mock.patch.object(m.os,'geteuid',return_value=0),mock.patch.object(m,'trusted_directory',return_value=True),mock.patch.object(m,'valid_target',return_value=True),mock.patch.object(m,'load',side_effect=lambda name,_path:modules[name]),mock.patch.object(m,'baseline',return_value=baseline),mock.patch.object(m,'candidate',side_effect=AssertionError('must not need candidate')),mock.patch.object(m,'verify_receipt'),mock.patch.object(m,'consume'),mock.patch.object(m,'run',return_value=b'config'),mock.patch.object(m.time,'sleep'),redirect_stdout(StringIO()):
                m.main('rollback','APR-20261107T010101Z-test00')
            self.assertEqual(route.read_bytes(),old)

if __name__=='__main__':unittest.main()
