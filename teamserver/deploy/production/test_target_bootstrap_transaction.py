import contextlib
import unittest

import target_bootstrap_recovery as routes
import target_bootstrap_transaction as transaction


class FakeOps:
    def __init__(self, fail=None):
        self.route = b'location /mcp { ' + routes.OLD_PORT + b' }\nlocation /auth { ' + routes.OLD_PORT + b' }\n'
        self.initial = self.route
        self.old_stopped = False
        self.intent = None
        self.events = []
        self.fail = fail
        self.failed = False
        self.ready = True
        self.committed = False
        self.closed = False

    @contextlib.contextmanager
    def lock(self):
        self.events.append('lock')
        yield

    def step(self, name):
        self.events.append(name)
        if self.fail == name and not self.failed:
            self.failed = True
            raise RuntimeError('injected failure')

    def read_route(self):
        return self.route

    def verify_baseline(self, _route):
        self.step('baseline')

    def verify_old_boot_guard(self):
        self.step('boot_guard')

    def candidate_ready(self):
        self.step('candidate')
        return self.ready

    def write_prepare(self, _intent):
        self.step('prepare')

    def fence_old_starts(self, intent):
        assert self.old_stopped
        self.step('intent')
        self.intent = intent

    def disable_old_and_confirm_stopped(self):
        self.step('stop_old')
        self.old_stopped = True

    def replace_route(self, route):
        assert self.old_stopped
        self.step('replace')
        self.route = route

    def test_and_reload(self):
        self.step('reload')

    def verify_candidate_public(self):
        self.step('verify_candidate')

    def verify_old_stopped(self):
        assert self.old_stopped
        self.step('verify_stopped')

    def verify_closed_public(self):
        self.step('verify_closed')

    def write_committed(self, _intent):
        self.step('commit')
        self.committed = True
        self.step('commit_after_publish')

    def committed_state(self, _intent):
        return 'exact' if self.committed else 'absent'

    def closed_state(self, _intent):
        return 'exact' if self.closed else 'absent'

    def write_incident(self, _intent):
        self.step('incident')

    def write_closed(self, _intent):
        self.step('closed')
        self.closed = True


class TargetBootstrapTransactionTest(unittest.TestCase):
    def test_success_stops_old_before_new_route(self):
        ops = FakeOps()
        transaction.switch(ops)
        self.assertTrue(ops.old_stopped)
        self.assertEqual(ops.route, routes.route_variants(ops.initial)[0])
        self.assertLess(ops.events.index('prepare'), ops.events.index('stop_old'))
        self.assertLess(ops.events.index('stop_old'), ops.events.index('intent'))
        self.assertLess(ops.events.index('intent'), ops.events.index('replace'))
        self.assertEqual(ops.events[-1], 'commit_after_publish')

    def test_failures_after_intent_close_without_restart(self):
        for step in ('intent', 'stop_old', 'replace', 'reload', 'verify_candidate', 'commit'):
            with self.subTest(step=step):
                ops = FakeOps(step)
                with self.assertRaises(RuntimeError):
                    transaction.switch(ops)
                self.assertTrue(ops.old_stopped)
                self.assertEqual(ops.route, routes.route_variants(ops.initial)[1])
                self.assertEqual(ops.events[-1], 'closed')

    def test_failure_before_intent_leaves_old_baseline(self):
        ops = FakeOps('candidate')
        with self.assertRaises(RuntimeError):
            transaction.switch(ops)
        self.assertFalse(ops.old_stopped)
        self.assertEqual(ops.route, ops.initial)

    def test_missing_boot_guard_prevents_any_cutover(self):
        ops = FakeOps('boot_guard')
        with self.assertRaises(RuntimeError):
            transaction.switch(ops)
        self.assertFalse(ops.old_stopped)
        self.assertEqual(ops.route, ops.initial)

    def test_recovery_uses_new_or_closed_never_old(self):
        for ready in (True, False):
            ops = FakeOps()
            ops.ready = ready
            transaction.recover(ops, routes.make_intent(ops.initial), ops.initial)
            self.assertTrue(ops.old_stopped)
            self.assertEqual(ops.route, routes.route_variants(ops.initial)[0 if ready else 1])

    def test_unknown_route_drift_is_not_overwritten(self):
        ops = FakeOps()
        ops.route = b'changed by another actor'
        with self.assertRaises(transaction.RecoveryUnconfirmed):
            transaction.recover(ops, routes.make_intent(ops.initial), ops.initial)
        self.assertTrue(ops.old_stopped)
        self.assertEqual(ops.route, b'changed by another actor')

    def test_recovery_reloads_even_if_file_was_already_replaced(self):
        for ready in (True, False):
            ops = FakeOps()
            ops.ready = ready
            ops.route = routes.route_variants(ops.initial)[0 if ready else 1]
            transaction.recover(ops, routes.make_intent(ops.initial), ops.initial)
            self.assertIn('reload', ops.events)
            self.assertIn('verify_candidate' if ready else 'verify_closed', ops.events)

    def test_published_commit_marker_is_not_followed_by_closed_marker(self):
        ops = FakeOps('commit_after_publish')
        with self.assertRaises(transaction.CommitPersistenceUncertain):
            transaction.switch(ops)
        self.assertEqual(ops.route, routes.route_variants(ops.initial)[0])
        self.assertTrue(ops.old_stopped)
        self.assertNotIn('closed', ops.events)

    def test_closed_terminal_is_not_reopened_or_committed(self):
        ops = FakeOps()
        ops.closed = True
        ops.ready = True
        ops.route = routes.route_variants(ops.initial)[1]
        transaction.recover(ops, routes.make_intent(ops.initial), ops.initial)
        self.assertEqual(ops.route, routes.route_variants(ops.initial)[1])
        self.assertFalse(ops.committed)
        self.assertNotIn('commit', ops.events)

    def test_committed_terminal_failing_candidate_closes_without_second_terminal(self):
        ops = FakeOps()
        ops.committed = True
        ops.ready = False
        ops.route = routes.route_variants(ops.initial)[0]
        transaction.recover(ops, routes.make_intent(ops.initial), ops.initial)
        self.assertEqual(ops.route, routes.route_variants(ops.initial)[1])
        self.assertTrue(ops.committed)
        self.assertFalse(ops.closed)
        self.assertIn('incident', ops.events)

    def test_recovery_public_probe_failure_closes_target(self):
        ops = FakeOps('verify_candidate')
        with self.assertRaises(RuntimeError):
            transaction.recover(ops, routes.make_intent(ops.initial), ops.initial)
        self.assertTrue(ops.old_stopped)
        self.assertEqual(ops.route, routes.route_variants(ops.initial)[1])
        self.assertTrue(ops.closed)


if __name__ == '__main__':
    unittest.main()
