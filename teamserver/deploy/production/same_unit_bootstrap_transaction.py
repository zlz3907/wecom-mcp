"""Fail-closed in-place MCP bootstrap transaction; no production I/O here.

The adapter must implement all operations under a shared production lock and
bind the unit, candidate, environment and receipt. In particular, disabling a
unit must durably remove its boot links, and stop must drain pending systemd
jobs, the old cgroup and the listener. No candidate may run before that point.
"""


class RecoveryUnconfirmed(RuntimeError):
    pass


class CommitPersistenceUncertain(RuntimeError):
    pass


def _close(ops):
    try:
        ops.disable_stop_and_verify_closed()
    except Exception as error:
        raise RecoveryUnconfirmed('unit and listener could not be verified closed') from error


def switch(ops):
    with ops.lock():
        ops.verify_baseline_and_receipt()
        ops.verify_candidate_config()
        if ops.terminal_state() != 'none':
            raise ValueError('existing transaction requires recovery')
        ops.write_prepare()
        attempted_commit = False
        try:
            ops.disable_stop_and_verify_closed()
            ops.install_candidate_dropin()
            ops.reload_and_verify_candidate_config()
            # Start while disabled. A reboot before durable commit cannot start
            # an unverified unit via an existing boot link.
            ops.start_and_verify_candidate()
            attempted_commit = True
            ops.write_committed()
            ops.enable_candidate_durably()
        except Exception as original:
            if attempted_commit:
                try:
                    state = ops.terminal_state()
                except Exception as error:
                    _close(ops)
                    raise RecoveryUnconfirmed('terminal status is unknown') from error
                if state == 'committed':
                    _close(ops)
                    ops.write_incident()
                    raise CommitPersistenceUncertain('committed candidate closed; recovery required') from original
                if state != 'none':
                    _close(ops)
                    raise RecoveryUnconfirmed('unexpected terminal state') from original
            _close(ops)
            ops.write_closed()
            raise


def recover(ops):
    with ops.lock():
        # An invalid receipt never grants the right to stop a unit.
        ops.verify_recovery_authority()
        try:
            ops.verify_prepared()
        except Exception as error:
            try:
                if ops.unprepared_untouched():
                    return
            except Exception:
                pass
            _close(ops)
            raise RecoveryUnconfirmed('prepared transaction is not verified') from error
        try:
            ops.establish_recovery_guard()
            terminal = ops.terminal_state()
            if terminal not in ('none', 'committed', 'closed'):
                raise ValueError('unknown terminal state')
        except Exception as error:
            _close(ops)
            raise RecoveryUnconfirmed('authority or terminal state is unknown') from error
        if terminal == 'closed':
            _close(ops)
            return
        attempted_commit = False
        try:
            ops.disable_stop_and_verify_closed()
            ops.install_candidate_dropin()
            ops.reload_and_verify_candidate_config()
            ops.start_and_verify_candidate()
            if terminal == 'none':
                attempted_commit = True
                ops.write_committed()
            ops.enable_candidate_durably()
        except Exception as original:
            if attempted_commit:
                try:
                    state = ops.terminal_state()
                except Exception as error:
                    _close(ops)
                    raise RecoveryUnconfirmed('terminal status is unknown') from error
                if state == 'committed':
                    _close(ops)
                    ops.write_incident()
                    raise CommitPersistenceUncertain('committed candidate closed; recovery required') from original
                if state != 'none':
                    _close(ops)
                    raise RecoveryUnconfirmed('unexpected terminal state') from original
            _close(ops)
            if terminal == 'none':
                ops.write_closed()
            else:
                ops.write_incident()
            raise
