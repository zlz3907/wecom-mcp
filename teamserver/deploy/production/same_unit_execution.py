#!/usr/bin/python3 -I
"""Root-only entry point for an exactly approved same-unit MCP transaction.

Requires an independently installed, root-owned controller, immutable plan,
receipt, evidence, candidate and per-approval journal. No install side effects.
"""
import os as _os
import stat as _stat
import sys as _sys
from pathlib import Path as _Path


def _verify_controller_archive():
    path = _Path('/usr/local/lib/wecom-mcp-open-bootstrap/same-unit-execution.pyz')
    if _Path(_sys.argv[0]) != path or path.is_symlink():
        raise ValueError('untrusted controller archive path')
    info = path.lstat()
    if not _stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError('untrusted controller archive')
    parent = path.parent
    while True:
        item = parent.lstat()
        if not _stat.S_ISDIR(item.st_mode) or item.st_uid != 0 or item.st_mode & 0o022:
            raise ValueError('untrusted controller directory')
        if parent == _Path('/'):
            break
        parent = parent.parent


if __name__ == '__main__':
    try:
        _verify_controller_archive()
    except Exception:
        print('MCP_OPEN_BOOTSTRAP_REFUSED: untrusted controller archive', file=_sys.stderr)
        _sys.exit(1)

import hashlib
import os
from pathlib import Path
import re
import sys

import same_unit_bootstrap_transaction as transaction
import same_unit_guarded_ops as guarded
import same_unit_journal as records
import same_unit_live_gates as gates
import same_unit_plan as contract
import same_unit_receipt as receipt
from same_unit_systemd import Unit
from target_bootstrap_journal import Journal

APPROVALS = Path('/etc/gnas/release-approvals')
PLANS = Path('/etc/gnas/mcp-bootstrap-plans')
EVIDENCE = Path('/var/lib/wecom-mcp-isolated/bootstrap-evidence')
USED = Path('/var/lib/wecom-mcp-isolated/bootstrap-used')
JOURNALS = Path('/var/lib/wecom-mcp-isolated/bootstrap-transactions')


def execute(action, approval_id):
    if os.geteuid() != 0 or action not in ('switch', 'recover') or not re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[A-Za-z0-9._-]{6,64}', approval_id):
        raise ValueError('root, fixed action and valid approval ID required')
    plan_path, receipt_path = PLANS / (approval_id + '.json'), APPROVALS / (approval_id + '.json')
    plan_bytes = receipt._read_regular(plan_path, Path('/'), 0)
    plan = receipt._json(plan_bytes)
    plan_sha = hashlib.sha256(plan_bytes).hexdigest()
    candidate = contract.validate(plan)
    def evidence_check():
        payloads = {}
        for field in ('fleet_snapshot_sha256', 'smoke_sha256'):
            value = plan[field]
            content = receipt._read_regular(EVIDENCE / (value + '.json'), Path('/'), 0)
            if hashlib.sha256(content).hexdigest() != value:
                raise ValueError('release evidence changed')
            payloads[field] = receipt._json(content)
        fleet = payloads['fleet_snapshot_sha256']
        expected = {'mode': 'read-only-gnas-discovery', 'bindings': [
            {'host': 'mcp.jianpinke.com', 'personnel_schema': 'unavailable', 'state': 'tenant_unavailable'},
            {'host': 'mcp.rtyouth.com', 'personnel_schema': 'compatible', 'state': 'ready'},
            {'host': 'mcp.wesiyu.com', 'personnel_schema': 'compatible', 'state': 'ready'},
            {'host': 'zhycit.com', 'personnel_schema': 'compatible', 'state': 'ready'}]}
        if (fleet.get('schema_version') != 1 or fleet.get('authority') != 'gnas-service-discovery' or
                fleet.get('probe_sha256') != '0f427eef348b5e34fdb62fe476213cf9770ccda8a37443d1f82a7e93fc8279d4' or
                fleet.get('result') != expected):
            raise ValueError('fleet evidence does not establish required bindings')
        smoke = payloads['smoke_sha256']
        if (smoke.get('schema_version') != 1 or smoke.get('release_id') != contract.RELEASE_ID or
                smoke.get('candidate_binary_sha256') != contract.BINARY_SHA or
                smoke.get('config_preflight') != {'target': 'passed', 'shared': 'passed'} or
                smoke.get('public_resource_issuer') != 'passed'):
            raise ValueError('candidate preflight evidence mismatch')
        return expected
    receipt._trusted_directory(USED, Path('/'), 0)
    receipt._trusted_directory(JOURNALS, Path('/'), 0)
    journal = Journal(JOURNALS / approval_id, 0)
    ledger = records.Records(journal, plan_sha)
    unit = Unit(plan['unit'], plan['fragment_sha256'], plan['candidate_binary_sha256'])

    def check_approval():
        checked, sha = receipt.validate(receipt_path, plan_path, approval_id, 'deploy-mcp-open-bootstrap')
        if checked != plan or sha != plan_sha:
            raise ValueError('approval plan changed')

    def consume():
        receipt.consume(USED, approval_id, plan_sha)

    def check_recovery():
        checked, sha = receipt.verify_consumed(receipt_path, plan_path, USED, approval_id)
        if checked != plan or sha != plan_sha:
            raise ValueError('recovery plan changed')

    def untouched_baseline():
        gates.baseline(plan, unit)

    def old_baseline():
        result = gates.baseline(plan, unit)
        if result['pid'] != plan['previous_pid']:
            raise ValueError('old MCP process changed')

    def config_check():
        expected_fleet = evidence_check()
        hosts = ('zhycit.com',) if plan['profile'] == 'target' else ('mcp.wesiyu.com', 'mcp.rtyouth.com')
        gates.public_probe(hosts)
        gates.fleet_probe(expected_fleet, hosts)
        data = gates.candidate(plan)
        unit.check_candidate_config(data, plan['dropin_sha256'], candidate,
                                    plan['candidate_policy_sha256'], plan['environment_file_sha256'])
        old_baseline()

    def recovery_check():
        evidence_check()
        data = gates.candidate(plan)
        unit.check_candidate_config(data, plan['dropin_sha256'], candidate,
                                    plan['candidate_policy_sha256'], plan['environment_file_sha256'])

    def runtime_probe():
        gates.health_probe(plan)

    ops = guarded.GuardedOps(plan, unit, ledger, check_approval, consume, check_recovery,
                             old_baseline, config_check, None, runtime_probe,
                             recovery_check=recovery_check, read_candidate=lambda: gates.candidate(plan),
                             verify_untouched=untouched_baseline)
    (transaction.switch if action == 'switch' else transaction.recover)(ops)
    return {'action': action, 'profile': plan['profile'], 'release_id': plan['release_id'],
            'plan_sha256': plan_sha, 'state': ledger.terminal_state()}


def main():
    if len(sys.argv) != 3:
        raise ValueError('usage: same-unit-execution.py switch|recover APPROVAL_ID')
    import json
    print(json.dumps(execute(sys.argv[1], sys.argv[2]), sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Inputs and upstream HTTP bodies may contain sensitive data.
        print('MCP_OPEN_BOOTSTRAP_REFUSED: ' + type(error).__name__, file=sys.stderr)
        sys.exit(1)
