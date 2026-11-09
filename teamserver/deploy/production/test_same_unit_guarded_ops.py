import contextlib
import unittest

import same_unit_bootstrap_transaction as transaction
from same_unit_guarded_ops import GuardedOps
from test_same_unit_plan import fixture


class Records:
    def __init__(self): self.state = 'none'; self.prepared = False; self.journal = self
    def read(self, name):
        if name == 'prepare.json': return b'prepared' if self.prepared else None
        if name == 'terminal.json': return None if self.state == 'none' else self.state.encode()
    def verify_prepared(self):
        if not self.prepared: raise ValueError('not prepared')
    def prepare(self): self.prepared = True
    def terminal_state(self): return self.state
    def terminal(self, state): self.state = state
    def incident(self): pass


class Unit:
    name = 'wecom-mcp-zhycit-migration.service'
    def __init__(self): self.closed = False; self.started = False; self.events = []
    def disable_stop_and_verify_closed(self): self.closed = True; self.events.append('closed')
    def install_candidate_dropin(self, *_): self.events.append('install')
    def reload_and_verify_candidate_config(self, *_): self.events.append('reload')
    def enable_candidate_durably(self): self.events.append('enable')
    def run(self, *_, **__): self.started = True; self.closed = False; self.events.append('start')
    def verify_running_candidate(self, *_): pass


class GuardedOpsTest(unittest.TestCase):
    def make(self, drift_at=None):
        plan = fixture('target'); unit = Unit(); records = Records()
        unit.port, unit.fragment_sha, unit.candidate_sha = plan['port'], plan['fragment_sha256'], plan['candidate_binary_sha256']
        checks = {'value': 0, 'drift': False}
        @contextlib.contextmanager
        def acquire(paths, bootstrap_legacy=False):
            self.assertEqual(paths, plan['release_locks']); self.assertTrue(bootstrap_legacy)
            def check():
                checks['value'] += 1
                if checks['drift']: raise ValueError('lock drift')
            yield check
        def authority(name): return {'unit': name, 'pid': plan['authority_pids'][name]}
        def probe():
            if drift_at == 'after_start': checks['drift'] = True
        op = GuardedOps(plan, unit, records, lambda: None, lambda: None, lambda: None,
                        lambda: None, lambda: None, b'candidate', probe,
                        lock_acquire=acquire, authority_check=authority)
        if drift_at == 'after_stop':
            close = unit.disable_stop_and_verify_closed
            def stop(): close(); checks['drift'] = True
            unit.disable_stop_and_verify_closed = stop
        if drift_at == 'before_start':
            reload = unit.reload_and_verify_candidate_config
            def changed(*args): reload(*args); checks['drift'] = True
            unit.reload_and_verify_candidate_config = changed
        return op, unit, records

    def test_success_uses_all_guards_and_commits(self):
        op, unit, records = self.make()
        transaction.switch(op)
        self.assertEqual(records.state, 'committed')
        self.assertEqual(unit.events, ['closed', 'install', 'reload', 'start', 'enable'])

    def test_wrong_systemd_unit_is_rejected_before_operations(self):
        plan = fixture('target'); wrong = Unit(); wrong.name = fixture('shared')['unit']
        wrong.port, wrong.fragment_sha, wrong.candidate_sha = plan['port'], plan['fragment_sha256'], plan['candidate_binary_sha256']
        with self.assertRaisesRegex(ValueError, 'does not match'):
            GuardedOps(plan, wrong, Records(), lambda: None, lambda: None, lambda: None,
                       lambda: None, lambda: None, b'candidate', lambda: None)
        self.assertEqual(wrong.events, [])

    def test_consumed_receipt_without_prepare_keeps_verified_old_unit_running(self):
        op, unit, records = self.make()
        transaction.recover(op)
        self.assertEqual(unit.events, [])
        self.assertEqual(records.state, 'none')

    def test_recovery_allows_new_stable_authority_pids_after_restart(self):
        op, unit, records = self.make()
        records.prepared = True
        op.authority_check = lambda name: {'unit': name, 'pid': 1234 if name == 'target' else 5678}
        transaction.recover(op)
        self.assertEqual(records.state, 'committed')
        self.assertTrue(unit.started)

    def test_authority_change_during_recovery_closes(self):
        op, unit, records = self.make()
        records.prepared = True
        seen = {'calls': 0}
        def changed(name):
            seen['calls'] += 1
            return {'unit': name, 'pid': op.plan['authority_pids'][name] if seen['calls'] < 10 else 2}
        op.authority_check = changed
        with self.assertRaises((ValueError, transaction.RecoveryUnconfirmed)): transaction.recover(op)
        self.assertTrue(unit.closed)
        self.assertFalse(unit.started)

    def test_drift_after_stop_before_start_or_after_start_closes(self):
        for point in ('after_stop', 'before_start', 'after_start'):
            with self.subTest(point=point):
                op, unit, records = self.make(point)
                with self.assertRaises(ValueError): transaction.switch(op)
                self.assertTrue(unit.closed)
                self.assertEqual(records.state, 'closed')
                if point != 'after_start': self.assertFalse(unit.started)


if __name__ == '__main__':
    unittest.main()
