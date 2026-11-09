import contextlib
import unittest

import same_unit_bootstrap_transaction as tx


class FakeOps:
    def __init__(self, failure=None):
        self.failure = failure
        self.failed = False
        self.events = []
        self.terminal = 'none'
        self.closed = False

    @contextlib.contextmanager
    def lock(self):
        self.events.append('lock')
        yield

    def step(self, name):
        self.events.append(name)
        if self.failure == name and not self.failed:
            self.failed = True
            raise RuntimeError(name)

    def verify_baseline_and_receipt(self): self.step('baseline')
    def verify_candidate_config(self): self.step('candidate_check')
    def verify_recovery_authority(self): self.step('recovery_authority')
    def verify_prepared(self): self.step('prepared_check')
    def unprepared_untouched(self): return False
    def establish_recovery_guard(self): self.step('recovery_guard')
    def terminal_state(self): self.step('terminal'); return self.terminal
    def write_prepare(self): self.step('prepare')
    def disable_stop_and_verify_closed(self): self.step('stop'); self.closed = True
    def install_candidate_dropin(self): self.step('install'); assert self.closed
    def reload_and_verify_candidate_config(self): self.step('reload')
    def enable_candidate_durably(self): self.step('enable')
    def start_and_verify_candidate(self): self.step('start'); self.closed = False
    def write_committed(self): self.step('commit'); self.terminal = 'committed'; self.step('commit_after_publish')
    def write_closed(self): self.step('closed'); self.terminal = 'closed'
    def write_incident(self): self.step('incident')


class SameUnitTransactionTest(unittest.TestCase):
    def test_switch_orders_stop_before_candidate_and_durable_enable(self):
        ops = FakeOps()
        tx.switch(ops)
        self.assertEqual(ops.terminal, 'committed')
        for before, after in (('prepare', 'stop'), ('stop', 'install'), ('install', 'reload'),
                              ('reload', 'start'), ('start', 'commit'), ('commit', 'enable')):
            self.assertLess(ops.events.index(before), ops.events.index(after))

    def test_failure_after_prepare_never_restarts_old_and_closes(self):
        for step in ('stop', 'install', 'reload', 'start', 'commit'):
            with self.subTest(step=step):
                ops = FakeOps(step)
                with self.assertRaises(RuntimeError):
                    tx.switch(ops)
                self.assertTrue(ops.closed)
                self.assertEqual(ops.terminal, 'closed')

    def test_published_commit_is_never_followed_by_closed_terminal(self):
        ops = FakeOps('commit_after_publish')
        with self.assertRaises(tx.CommitPersistenceUncertain):
            tx.switch(ops)
        self.assertEqual(ops.terminal, 'committed')
        self.assertNotIn('closed', ops.events)
        self.assertTrue(ops.closed)

    def test_enable_failure_after_commit_closes_and_records_incident(self):
        ops = FakeOps('enable')
        with self.assertRaises(tx.CommitPersistenceUncertain): tx.switch(ops)
        self.assertEqual(ops.terminal, 'committed')
        self.assertTrue(ops.closed)
        self.assertIn('incident', ops.events)

    def test_no_boot_enable_before_candidate_commit(self):
        for failure in ('install', 'reload', 'start', 'commit'):
            with self.subTest(failure=failure):
                ops = FakeOps(failure)
                with self.assertRaises(RuntimeError): tx.switch(ops)
                self.assertNotIn('enable', ops.events)

    def test_closed_terminal_recovery_remains_closed(self):
        ops = FakeOps()
        ops.terminal = 'closed'
        tx.recover(ops)
        self.assertTrue(ops.closed)
        self.assertNotIn('start', ops.events)

    def test_committed_recovery_failure_closes_without_overwriting_terminal(self):
        ops = FakeOps('start')
        ops.terminal = 'committed'
        with self.assertRaises(RuntimeError):
            tx.recover(ops)
        self.assertTrue(ops.closed)
        self.assertEqual(ops.terminal, 'committed')
        self.assertIn('incident', ops.events)

    def test_unconfirmed_stop_never_claims_success(self):
        ops = FakeOps('install')
        def cannot_close(): raise OSError('listener still present')
        ops.disable_stop_and_verify_closed = cannot_close
        with self.assertRaises(tx.RecoveryUnconfirmed):
            tx.switch(ops)
        self.assertNotIn('closed', ops.events)

    def test_unreadable_prepare_in_authorized_recovery_closes_candidate(self):
        ops = FakeOps('prepared_check')
        with self.assertRaises(tx.RecoveryUnconfirmed):
            tx.recover(ops)
        self.assertTrue(ops.closed)
        self.assertNotIn('start', ops.events)

    def test_unauthorized_recovery_does_not_stop_unit(self):
        ops = FakeOps('recovery_authority')
        with self.assertRaises(RuntimeError):
            tx.recover(ops)
        self.assertNotIn('stop', ops.events)


if __name__ == '__main__':
    unittest.main()
