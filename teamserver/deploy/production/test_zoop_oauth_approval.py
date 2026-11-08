import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

path=Path(__file__).with_name('zoop_oauth_approval.py');spec=importlib.util.spec_from_file_location('receipt',path)
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class ApprovalTests(unittest.TestCase):
    def test_exact_action_and_expiry_are_mandatory(self):
        now=dt.datetime(2026,11,8,15,tzinfo=dt.timezone.utc)
        aid='APR-20261108T150000Z-test001'; expected={'action':'deploy-isolated-oauth','candidate_sha256':'a'*64,'old_sha256':'b'*64}
        receipt={'schema_version':1,'approval_id':aid,'environment':'zhycit-prod-01',**expected,
            'approved_at':now.isoformat(),'expires_at':(now+dt.timedelta(minutes=20)).isoformat(),'approved_by':'operator'}
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/(aid+'.json');p.write_text(json.dumps(receipt));p.chmod(0o400)
            # Test ownership is supplied by the fixture; production requires root.
            with patch.object(m.stat,'S_ISREG',return_value=True),patch.object(Path,'resolve',lambda self: self),patch.object(Path,'lstat',return_value=type('S',(),{'st_mode':0o100400,'st_uid':0})()):
                self.assertEqual(m.validate(p,aid,expected,now=now)['action'],expected['action'])
                with self.assertRaisesRegex(ValueError,'bind action'):m.validate(p,aid,{**expected,'candidate_sha256':'c'*64},now=now)
                with self.assertRaisesRegex(ValueError,'expired'):m.validate(p,aid,expected,now=now+dt.timedelta(minutes=21))
                with self.assertRaisesRegex(ValueError,'expected approval fields'):m.validate(p,aid,{**expected,'environment':'other'},now=now)
