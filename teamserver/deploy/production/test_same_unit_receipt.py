import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest

import same_unit_receipt as receipt


class ReceiptTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.plan = self.root / 'plan.json'
        self.ident = 'APR-20261109T010203Z-test123'
        self.path = self.root / (self.ident + '.json')
        self.used = self.root / 'used'
        self.used.mkdir(mode=0o700)
        self.now = dt.datetime(2026, 11, 9, 1, 30, tzinfo=dt.timezone.utc)
        self.plan.write_text('{"release":"candidate"}\n')
        self.plan.chmod(0o400)
        self.receipt = {'schema_version': 1, 'environment': 'zhycit-prod-01',
                        'action': 'deploy-mcp-open-bootstrap', 'approval_id': self.ident,
                        'plan_sha256': hashlib.sha256(self.plan.read_bytes()).hexdigest(),
                        'approved_by': 'owner', 'approved_at': '2026-11-09T01:00:00Z',
                        'expires_at': '2026-11-09T02:00:00Z'}
        self.write_receipt()

    def write_receipt(self):
        if self.path.exists(): self.path.chmod(0o600)
        self.path.write_text(json.dumps(self.receipt))
        self.path.chmod(0o400)

    def validate(self):
        return receipt.validate(self.path, self.plan, self.ident, 'deploy-mcp-open-bootstrap',
                                root=self.root, owner=os.getuid(), now=self.now)

    def test_exact_approval_is_single_use(self):
        plan, sha = self.validate()
        self.assertEqual(plan['release'], 'candidate')
        receipt.consume(self.used, self.ident, sha, root=self.root, owner=os.getuid())
        with self.assertRaises(FileExistsError):
            receipt.consume(self.used, self.ident, sha, root=self.root, owner=os.getuid())

    def test_wrong_plan_expired_or_wrong_action_are_rejected(self):
        for key, value in (('plan_sha256', '0' * 64), ('expires_at', '2026-11-09T01:15:00Z'),
                           ('action', 'recover-mcp-open-bootstrap')):
            with self.subTest(key=key):
                original = self.receipt[key]
                self.receipt[key] = value
                self.write_receipt()
                with self.assertRaises(ValueError): self.validate()
                self.receipt[key] = original

    def test_untrusted_or_duplicate_receipt_is_rejected(self):
        self.path.chmod(0o600)
        with self.assertRaisesRegex(ValueError, 'untrusted approval file'): self.validate()
        self.path.write_text('{"schema_version":1,"schema_version":1}')
        self.path.chmod(0o400)
        with self.assertRaisesRegex(ValueError, 'duplicate approval field'): self.validate()

    def test_symlink_plan_is_rejected(self):
        link = self.root / 'link.json'
        link.symlink_to(self.plan)
        with self.assertRaises(OSError):
            receipt.validate(self.path, link, self.ident, 'deploy-mcp-open-bootstrap',
                             root=self.root, owner=os.getuid(), now=self.now)

    def test_recovery_requires_consumed_original_deployment_receipt(self):
        _plan, sha = self.validate()
        with self.assertRaises(FileNotFoundError):
            receipt.verify_consumed(self.path, self.plan, self.used, self.ident, root=self.root, owner=os.getuid())
        receipt.consume(self.used, self.ident, sha, root=self.root, owner=os.getuid())
        saved, actual = receipt.verify_consumed(self.path, self.plan, self.used, self.ident, root=self.root, owner=os.getuid())
        self.assertEqual((saved['release'], actual), ('candidate', sha))


if __name__ == '__main__':
    unittest.main()
