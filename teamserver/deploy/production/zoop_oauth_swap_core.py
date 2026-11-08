"""Offline state machine for a target-only OAuth release.

The production adapter must enforce trusted paths, exact receipts, locks, and
process/route checks. This module has no production side effects on its own.
"""
from dataclasses import dataclass


@dataclass(frozen=True)
class Plan:
    candidate_release: str
    candidate_sha256: str
    candidate_manifest_sha256: str
    old_release: str
    old_sha256: str
    shared_release: str
    shared_sha256: str
    old_unit_sha256: str
    candidate_unit_sha256: str
    rollback_unit_sha256: str
    grant_sha256: str


class SwapError(RuntimeError):
    pass


def require(condition, message):
    if not condition:
        raise SwapError(message)


def switch(plan, ops):
    """Swap the isolated authority, restoring the rebased old unit on failure.

    Canary startup is separately approved and completed before this function;
    both verification callbacks below are read-only evidence checks. Receipt
    validation and a preflight of the *old* binary under the current shared
    authority are required. The old
    on-disk unit is never used for rollback because its pinned shared baseline
    may have expired.
    """
    with ops.release_lock():
        before = ops.snapshot()
        ops.verify_plan(plan, before)
        ops.verify_receipt(plan, before)
        ops.verify_old_recovery_candidate(plan, before)
        ops.verify_new_canary(plan, before)
        ops.consume_receipt()
        # This evidence and the exact rollback bytes must survive a crash.
        ops.record_prepared(plan, before)
        touched = False
        try:
            ops.assert_unchanged(plan, before)
            # Set before the operation: a failed systemd command can have
            # changed the unit or stopped the process before raising.
            touched = True
            ops.install_and_restart_candidate(plan, before)
            ops.verify_candidate(plan, before)
            ops.verify_public_and_other_tenants(plan, before)
            ops.record_success(plan, before)
        except Exception as initial:
            if not touched:
                raise
            try:
                ops.ensure_restart_job_settled()
                ops.install_and_restart_rollback(plan, before)
                ops.verify_rollback(plan, before)
                ops.verify_public_and_other_tenants(plan, before)
                ops.record_rollback(plan, before, type(initial).__name__)
            except Exception as rollback:
                raise SwapError('candidate failed and automatic rollback was not verified') from rollback
            raise SwapError('candidate failed; automatic rollback verified') from initial
    return 'isolated_oauth_switched'


def recover_incomplete(plan, ops):
    """Finish the bounded rollback from a durably prepared, interrupted swap.

    The adapter must validate the original consumed approval and journal,
    reject completed/changed releases, and establish that the saved rollback
    unit and shared baseline remain valid before making any change.
    """
    with ops.release_lock():
        # Recovery must remain available even if the candidate has exited.
        before = ops.snapshot_for_recovery()
        ops.verify_recovery_journal(plan, before)
        ops.verify_recovery_preconditions(plan, before)
        ops.install_and_restart_rollback(plan, before)
        ops.verify_rollback(plan, before)
        ops.verify_public_and_other_tenants(plan, before)
        ops.record_rollback(plan, before, 'interrupted')
    return 'interrupted_swap_rolled_back'
