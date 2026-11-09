"""Fail-closed transaction logic for a target-only bootstrap switch.

The production adapter is intentionally not provided by this module. Every
operation, including durable journal writes and release locks, must be supplied
by a reviewed controller; importing this file cannot change production.

The adapter must durably disable the old unit, wait for its entire cgroup and
listener to stop, then publish the fence under the exact guard lock inode. The
candidate shares the existing idempotency state; before old shutdown its
preflight must not issue real writes or schema mutations.
"""
import target_bootstrap_recovery as routes


class RecoveryUnconfirmed(RuntimeError):
    pass


class CommitPersistenceUncertain(RuntimeError):
    pass


def isolate_unconfirmed(ops, message, original):
    try:
        ops.stop_candidate_and_verify_no_listener()
    except Exception as error:
        raise RecoveryUnconfirmed('route and candidate isolation are not verified') from error
    raise RecoveryUnconfirmed(message) from original


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
        ops.replace_route(current, closed)
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
        if marker_state(ops, intent) != 'none':
            raise RecoveryUnconfirmed('existing terminal requires recovery rather than a new switch')
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
            ops.replace_route(old, new)
            ops.test_and_reload()
            ops.verify_candidate_public()
            commit_attempted = True
            ops.write_committed(intent)
        except Exception as original:
            if commit_attempted:
                try:
                    state = marker_state(ops, intent)
                except RecoveryUnconfirmed as marker_error:
                    isolate_unconfirmed(ops, 'terminal marker status is unknown', marker_error)
                if state == 'committed':
                    # The terminal marker is visible. Do not create a
                    # contradictory closed marker or silently change routing.
                    raise CommitPersistenceUncertain('commit marker published but persistence was not confirmed') from original
                if state != 'none':
                    isolate_unconfirmed(ops, 'unexpected terminal after commit attempt', original)
            try:
                close_target(ops, intent, closed, 'none')
            except Exception as recovery:
                # Disk bytes do not prove live Nginx reloaded.
                isolate_unconfirmed(ops, 'switch failed and recovery is not verified', recovery)
            raise original


def recover(ops, intent, old):
    """Resume durable intent; unknown route drift is reported unconfirmed."""
    new, closed = routes.route_variants(old)
    if intent != routes.make_intent(old):
        raise ValueError('journal does not bind original route')
    with ops.lock():
        try:
            ops.verify_old_boot_guard()
            ops.disable_old_and_confirm_stopped()
            ops.fence_old_starts(intent)
        except Exception as error:
            isolate_unconfirmed(ops, 'old service stop or start fence is not verified', error)
        try:
            terminal = marker_state(ops, intent)
        except RecoveryUnconfirmed as error:
            isolate_unconfirmed(ops, 'terminal marker status is unknown', error)
        commit_attempted = False
        try:
            current = ops.read_route()
            # An immutable closed terminal is not reopened by recovery.
            ready = bool(ops.candidate_ready()) if terminal != 'closed' else False
            action = routes.recovery_action(intent, current, ready)
            if action == 'route_candidate':
                ops.replace_route(current, new)
            elif action == 'route_closed':
                ops.replace_route(current, closed)
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
            try:
                state = marker_state(ops, intent)
            except RecoveryUnconfirmed as error:
                isolate_unconfirmed(ops, 'terminal marker status is unknown', error)
            if commit_attempted and terminal == 'none' and state == 'committed':
                raise CommitPersistenceUncertain('commit marker published but persistence was not confirmed') from original
            try:
                close_target(ops, intent, closed, state)
            except Exception as error:
                isolate_unconfirmed(ops, 'recovery could not verify a closed target', error)
            raise original
