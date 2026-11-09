"""Fail-closed transaction logic for a target-only bootstrap switch.

The production adapter is intentionally not provided by this module. Every
operation, including durable journal writes and release locks, must be supplied
by a reviewed controller; importing this file cannot change production.
"""
import target_bootstrap_recovery as routes


class RecoveryUnconfirmed(RuntimeError):
    pass


class CommitPersistenceUncertain(RuntimeError):
    pass


def marker_state(ops, intent):
    try:
        committed = ops.committed_state(intent)
        closed = ops.closed_state(intent)
    except Exception as error:
        raise RecoveryUnconfirmed('terminal marker status is unknown') from error
    if committed not in ('exact', 'absent') or closed not in ('exact', 'absent') or (committed == 'exact' and closed == 'exact'):
        raise RecoveryUnconfirmed('terminal markers conflict or are unknown')
    return 'committed' if committed == 'exact' else ('closed' if closed == 'exact' else 'none')


def close_target(ops, intent, closed, terminal):
    ops.disable_old_and_confirm_stopped()
    ops.fence_old_starts(intent)
    current = ops.read_route()
    routes.recovery_action(intent, current, False)
    if current != closed:
        ops.replace_route(closed)
    # A crash may have occurred after replacing bytes but before reload.
    ops.test_and_reload()
    ops.verify_old_stopped()
    ops.verify_closed_public()
    if terminal == 'none':
        ops.write_closed(intent)
    elif terminal == 'committed':
        # Preserve the immutable terminal and append a separate incident.
        ops.write_incident(intent)


def switch(ops):
    """Cut over after preflight, never automatically restarting the old unit."""
    with ops.lock():
        old = ops.read_route()
        new, closed = routes.route_variants(old)
        ops.verify_baseline(old)
        ops.verify_old_boot_guard()
        if not ops.candidate_ready():
            raise ValueError('candidate is not ready')
        intent = routes.make_intent(old)
        # A prepared journal is not the start fence. It allows interrupted
        # pre-commit stops to be diagnosed without restarting the old unit.
        ops.write_prepare(intent)
        commit_attempted = False
        try:
            # First disable/stop the old unit, then acquire the boot guard's
            # exclusive lock and publish the durable start fence while held.
            ops.disable_old_and_confirm_stopped()
            ops.fence_old_starts(intent)
            if ops.read_route() != old:
                raise ValueError('route changed before cutover')
            ops.replace_route(new)
            ops.test_and_reload()
            ops.verify_candidate_public()
            commit_attempted = True
            ops.write_committed(intent)
        except Exception as original:
            if commit_attempted:
                state = marker_state(ops, intent)
                if state == 'committed':
                    # The terminal marker is visible. Do not create a
                    # contradictory closed marker or silently change routing.
                    raise CommitPersistenceUncertain('commit marker published but persistence was not confirmed') from original
                if state != 'none':
                    raise RecoveryUnconfirmed('commit marker is conflicting or unknown') from original
            try:
                close_target(ops, intent, closed, 'none')
            except Exception as recovery:
                raise RecoveryUnconfirmed('switch failed and recovery is not verified') from recovery
            raise original


def recover(ops, intent, old):
    """Resume durable intent; unknown route drift is reported unconfirmed."""
    new, closed = routes.route_variants(old)
    if intent != routes.make_intent(old):
        raise ValueError('journal does not bind original route')
    with ops.lock():
        ops.verify_old_boot_guard()
        ops.disable_old_and_confirm_stopped()
        ops.fence_old_starts(intent)
        terminal = marker_state(ops, intent)
        commit_attempted = False
        try:
            current = ops.read_route()
            # An immutable closed terminal is not reopened by recovery.
            ready = bool(ops.candidate_ready()) if terminal != 'closed' else False
            action = routes.recovery_action(intent, current, ready)
            if action == 'route_candidate':
                ops.replace_route(new)
            elif action == 'route_closed':
                ops.replace_route(closed)
            # Always reload, including when the file already has desired bytes.
            ops.test_and_reload()
            if ready:
                ops.verify_candidate_public()
                if terminal == 'none':
                    commit_attempted = True
                    ops.write_committed(intent)
            else:
                ops.verify_old_stopped()
                ops.verify_closed_public()
                if terminal == 'none':
                    ops.write_closed(intent)
                elif terminal == 'committed':
                    ops.write_incident(intent)
        except Exception as original:
            state = marker_state(ops, intent)
            if commit_attempted and terminal == 'none' and state == 'committed':
                raise CommitPersistenceUncertain('commit marker published but persistence was not confirmed') from original
            try:
                close_target(ops, intent, closed, state)
            except Exception as error:
                raise RecoveryUnconfirmed('recovery could not verify a closed target') from error
            raise original
