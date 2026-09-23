"""Independent negative regressions for release/upgrade safety boundaries."""
import unittest
from unittest.mock import patch

import test_recovery as fixtures


class UpgradeReviewTests(unittest.TestCase):
    def setUp(self):
        fixtures.UpgradeTests.setUp(self)

    def test_drift_after_receipt_consumption_never_overwrites_external_controller(self):
        u = fixtures.u
        original = u.fields
        calls = 0

        def fields():
            nonlocal calls
            calls += 1
            if calls == 3:
                self.target.write_bytes(b'external-admin-controller')
            return original()

        with patch.object(u, 'fields', side_effect=fields):
            with self.assertRaisesRegex(ValueError, 'before replacement'):
                u.upgrade('approval')
        self.assertEqual(self.target.read_bytes(), b'external-admin-controller')
        self.assertEqual((self.control / 'controller-upgrades/approval/controller.before').read_bytes(), b'old')
        self.assertFalse((self.control / 'controller-upgrades/approval/completed.json').exists())

    def test_post_replacement_external_controller_is_not_overwritten_by_compensation(self):
        u = fixtures.u
        calls = 0

        def status():
            nonlocal calls
            calls += 1
            if calls == 4:
                self.target.write_bytes(b'external-admin-controller')
                return dict(self.facts, main_pid=124)
            return self.facts

        self.status.side_effect = status
        with self.assertRaisesRegex(ValueError, 'changed concurrently'):
            u.upgrade('approval')
        self.assertEqual(self.target.read_bytes(), b'external-admin-controller')
        self.assertFalse((self.control / 'controller-upgrades/approval/completed.json').exists())


class RecoveryReviewTests(unittest.TestCase):
    def setUp(self):
        fixtures.RecoveryTests.setUp(self)

    def seal(self):
        fixtures.RecoveryTests.seal(self)

    def test_missing_ci_refuses_staging_and_deploy_before_mutation(self):
        c = fixtures.c
        with patch.object(c, 'verify', return_value=self.m), patch.object(c, 'status'), patch.object(c, 'baseline_matches', return_value=True), patch.object(c, 'check_gnas') as check_gnas, patch.object(c, 'approval') as approval, patch.object(c, 'atomic') as atomic:
            with self.assertRaisesRegex(ValueError, 'CI evidence'):
                c.stage(self.rid)
            with self.assertRaisesRegex(ValueError, 'CI evidence'):
                c.deploy(self.rid, 'approval')
            check_gnas.assert_not_called()
            approval.assert_not_called()
            atomic.assert_not_called()

    def test_recovery_preflight_failure_never_consumes_receipt_or_switches(self):
        c = fixtures.c
        self.override.write_bytes(c.dropin(self.rid))
        current = dict(runtime_path=str(self.directory / 'wecom-mcp-team'), binary_sha256=self.m['files']['wecom-mcp-team'], unit_fingerprint='unit', main_pid=0, active='failed', runtime_verified=False)
        with patch.object(c, 'status', return_value=current), patch.object(c, 'check_approval'), patch.object(c, 'preflight_recovery', side_effect=ValueError('check failed')), patch.object(c, 'approval') as approval, patch.object(c, 'restore') as restore:
            with self.assertRaisesRegex(ValueError, 'check failed'):
                c.rollback(self.rid, 'approval')
            approval.assert_not_called()
            restore.assert_not_called()
        self.assertEqual(self.override.read_bytes(), c.dropin(self.rid))


if __name__ == '__main__':
    unittest.main()
