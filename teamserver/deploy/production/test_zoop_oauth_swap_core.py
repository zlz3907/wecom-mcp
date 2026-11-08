import importlib.util
from pathlib import Path
import unittest

p = Path(__file__).with_name('zoop_oauth_swap_core.py')
spec = importlib.util.spec_from_file_location('zoop_oauth_swap_core', p)
m = importlib.util.module_from_spec(spec)
import sys
sys.modules[spec.name] = m
spec.loader.exec_module(m)


class Lock:
    def __enter__(self): return self
    def __exit__(self, *_): return False


class Ops:
    def __init__(self, fail=None): self.calls=[]; self.fail=fail
    def __getattr__(self, name):
        def call(*args):
            self.calls.append(name)
            if name == self.fail: raise RuntimeError(name)
            if name == 'snapshot': return {'old_pid': 42}
            if name == 'snapshot_for_recovery': return {'candidate_pid': 0}
        return call
    def release_lock(self): self.calls.append('release_lock'); return Lock()


class SwapTests(unittest.TestCase):
    def setUp(self): self.plan=m.Plan(*['a']*11)
    def test_switch_preflights_rollback_before_touching_live_service(self):
        o=Ops(); self.assertEqual(m.switch(self.plan,o),'isolated_oauth_switched')
        self.assertLess(o.calls.index('verify_old_recovery_candidate'),o.calls.index('consume_receipt'))
        self.assertLess(o.calls.index('verify_new_canary'),o.calls.index('install_and_restart_candidate'))
        self.assertLess(o.calls.index('record_prepared'),o.calls.index('install_and_restart_candidate'))
        self.assertNotIn('install_and_restart_rollback',o.calls)
    def test_preflight_failure_does_not_touch_live_service_or_consume_receipt(self):
        for point in ('verify_plan','verify_receipt','verify_old_recovery_candidate','verify_new_canary'):
            with self.subTest(point=point):
                o=Ops(point)
                with self.assertRaises(RuntimeError): m.switch(self.plan,o)
                self.assertNotIn('consume_receipt',o.calls)
                self.assertNotIn('install_and_restart_candidate',o.calls)
                self.assertNotIn('install_and_restart_rollback',o.calls)
    def test_each_post_touch_failure_performs_and_verifies_rollback(self):
        for point in ('install_and_restart_candidate','verify_candidate','verify_public_and_other_tenants','record_success'):
            with self.subTest(point=point):
                o=Ops(point)
                # A public failure is a candidate-only failure in this case.
                if point == 'verify_public_and_other_tenants':
                    def once(*args):
                        o.calls.append(point)
                        if o.calls.count(point)==1: raise RuntimeError(point)
                    o.verify_public_and_other_tenants=once
                with self.assertRaisesRegex(m.SwapError,'automatic rollback verified'): m.switch(self.plan,o)
                self.assertIn('install_and_restart_rollback',o.calls)
                self.assertIn('verify_rollback',o.calls)
                self.assertIn('record_rollback',o.calls)
    def test_published_success_marker_does_not_trigger_unsafe_rollback(self):
        o=Ops('record_success')
        def published(*args):o.calls.append('success_record_published');return True
        o.success_record_published=published
        with self.assertRaisesRegex(m.SwapError,'durability is uncertain'):m.switch(self.plan,o)
        self.assertNotIn('install_and_restart_rollback',o.calls)

    def test_unsettled_systemd_job_prevents_competing_rollback(self):
        o=Ops('verify_candidate')
        def busy(*args):o.calls.append('ensure_restart_job_settled');raise RuntimeError('job running')
        o.ensure_restart_job_settled=busy
        with self.assertRaisesRegex(m.SwapError,'rollback was not verified'):m.switch(self.plan,o)
        self.assertNotIn('install_and_restart_rollback',o.calls)

    def test_rollback_failure_is_reported_as_unverified(self):
        o=Ops('verify_candidate')
        def rollback(*args): o.calls.append('install_and_restart_rollback'); raise RuntimeError('rollback')
        o.install_and_restart_rollback=rollback
        with self.assertRaisesRegex(m.SwapError,'rollback was not verified'): m.switch(self.plan,o)
        self.assertNotIn('record_rollback',o.calls)


    def test_recovery_validates_journal_and_baseline_before_restart(self):
        o=Ops(); self.assertEqual(m.recover_incomplete(self.plan,o),'interrupted_swap_rolled_back')
        self.assertLess(o.calls.index('verify_recovery_journal'),o.calls.index('install_and_restart_rollback'))
        self.assertLess(o.calls.index('verify_recovery_preconditions'),o.calls.index('install_and_restart_rollback'))
        self.assertIn('verify_rollback',o.calls)
        self.assertIn('snapshot_for_recovery',o.calls)
        self.assertNotIn('snapshot',o.calls)
    def test_recovery_refuses_changed_or_completed_state(self):
        for point in ('verify_recovery_journal','verify_recovery_preconditions'):
            with self.subTest(point=point):
                o=Ops(point)
                with self.assertRaises(RuntimeError): m.recover_incomplete(self.plan,o)
                self.assertNotIn('install_and_restart_rollback',o.calls)


if __name__ == '__main__': unittest.main()
