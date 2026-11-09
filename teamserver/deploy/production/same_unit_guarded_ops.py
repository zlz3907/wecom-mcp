"""Bind same-unit transaction operations to continuously checked authority.

A caller must supply the already verified immutable plan, receipt and package,
plus journal, systemd unit and real network probes. This module does not install
itself or create approvals. Safety checks run between every mutating operation.
"""
import contextlib
import time

import same_unit_authority as authority
import same_unit_locks as locks
import same_unit_plan as contract


class GuardedOps:
    def __init__(self, plan, unit, records, verify_receipt, consume_receipt,
                 verify_recovery_receipt, verify_baseline, candidate_check,
                 candidate_data, public_probe, clock=time.monotonic, sleep=time.sleep,
                 recovery_check=lambda: None, read_candidate=None, verify_untouched=None,
                 lock_acquire=locks.acquire, authority_check=None):
        self.plan, self.candidate = plan, contract.validate(plan)
        if (unit.name != plan['unit'] or unit.port != plan['port'] or
                unit.fragment_sha != plan['fragment_sha256'] or unit.candidate_sha != plan['candidate_binary_sha256']):
            raise ValueError('systemd unit does not match approved plan')
        self.unit, self.records = unit, records
        self._verify_receipt, self._consume_receipt = verify_receipt, consume_receipt
        self._verify_recovery_receipt, self._verify_baseline = verify_recovery_receipt, verify_baseline
        self._candidate_check, self._public_probe = candidate_check, public_probe
        self.data, self.clock, self.sleep = candidate_data, clock, sleep
        self._read_candidate = read_candidate or (lambda: self.data)
        self._recovery_check = recovery_check
        self._verify_untouched = verify_untouched or verify_baseline
        self._recovering = False
        self.lock_acquire = lock_acquire
        self.authority_check = authority_check or (lambda name: authority.verify(name, allow_successor=self._recovering))
        self._check_lock, self._authority_snapshot = None, None

    @contextlib.contextmanager
    def lock(self):
        with self.lock_acquire(self.plan['release_locks'], bootstrap_legacy=True) as check:
            self._check_lock = check
            try:
                yield
            finally:
                self._check_lock = None

    def _gate(self):
        if self._check_lock is None or self._authority_snapshot is None:
            raise ValueError('release guard not established')
        self._check_lock()
        if {name: self.authority_check(name) for name in ('target', 'shared')} != self._authority_snapshot:
            raise ValueError('authorization authority changed')
        self._check_lock()

    def _establish(self, pin_original_pid=True):
        if self._check_lock is None:
            raise ValueError('release locks not held')
        self._check_lock()
        self._authority_snapshot = {name: self.authority_check(name) for name in ('target', 'shared')}
        if pin_original_pid and any(self._authority_snapshot[name]['pid'] != self.plan['authority_pids'][name] for name in ('target', 'shared')):
            raise ValueError('authorization authority process changed')
        self._gate()

    def verify_baseline_and_receipt(self):
        self._establish()
        self._verify_receipt()
        self._verify_baseline()
        self._gate()

    def verify_candidate_config(self):
        self._gate()
        self._candidate_check()
        self._gate()

    def verify_recovery_authority(self):
        if self._check_lock is None:
            raise ValueError('release locks not held')
        self._check_lock()
        self._verify_recovery_receipt()

    def verify_prepared(self):
        self.records.verify_prepared()

    def unprepared_untouched(self):
        # The receipt may have been consumed before prepare was durable. The
        # original service must still match fully before doing nothing.
        if self.records.journal.read('prepare.json') is not None or self.records.journal.read('terminal.json') is not None:
            return False
        self._verify_untouched()
        return True

    def establish_recovery_guard(self):
        self._recovering = True
        self._establish(pin_original_pid=False)
        self._recovery_check()
        self._gate()

    def terminal_state(self):
        self._gate()
        return self.records.terminal_state()

    def write_prepare(self):
        self._gate()
        self._consume_receipt()
        self.records.prepare()
        self._gate()

    def disable_stop_and_verify_closed(self):
        # Remain able to close after a guard detects drift: refusing to close a
        # partially enabled unit could leave an unverified candidate serving.
        self.unit.disable_stop_and_verify_closed()

    def install_candidate_dropin(self):
        self._gate()
        self.data = self._read_candidate()
        self._gate()
        self.unit.install_candidate_dropin(self.data, self.plan['dropin_sha256'])
        self._gate()

    def reload_and_verify_candidate_config(self):
        self._gate()
        self.unit.reload_and_verify_candidate_config(self.data, self.plan['dropin_sha256'], self.candidate)
        self._gate()

    def enable_candidate_durably(self):
        self._gate()
        self.unit.enable_candidate_durably()
        self._gate()

    def start_and_verify_candidate(self):
        self._gate()
        self.unit.run('/usr/bin/systemctl', 'start', self.unit.name, timeout=45)
        deadline = self.clock() + 30
        while True:
            self._gate()
            try:
                self.unit.verify_running_candidate(self.candidate)
                self._public_probe()
                break
            except (ValueError, OSError, TimeoutError):
                if self.clock() >= deadline:
                    raise
                self.sleep(0.5)
        self._gate()

    def write_committed(self):
        self._gate()
        self.records.terminal('committed')
        self._gate()

    def write_closed(self):
        self.records.terminal('closed')

    def write_incident(self):
        self.records.incident()
