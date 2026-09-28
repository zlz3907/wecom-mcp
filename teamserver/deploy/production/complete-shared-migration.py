#!/usr/bin/python3 -I
"""Complete a journalled, already-switched migration using exact new approval."""
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sys

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('migration', HERE / 'migrate-shared-service.py')
u = importlib.util.module_from_spec(spec)
spec.loader.exec_module(u)
c = u.c


def fields(rid):
    m = c.verify(c.RELEASES / rid, rid)
    record = c.CONTROL / 'unit-migrations' / rid
    original = c.read_json(record / 'expected.json', True)
    receipt = c.read_json(record / 'consumed-approval.json', True)
    c.require(receipt['action'] == 'migrate-shared-service' and receipt['environment'] == c.ENVIRONMENT
              and all(receipt.get(k) == v for k,v in original.items()), 'original migration authority mismatch')
    c.regular(c.CONTROL / 'used' / receipt['approval_id'], True)
    c.require(c.sha(HERE / 'migrate-shared-service.py') == original['migrator_sha256'], 'original migrator changed')
    c.require(c.deploy_fields(m, c.RELEASES / rid).items() <= original.items(), 'original candidate changed')
    u.inactive(u.SOURCE)
    c.require(u.prop(u.SOURCE, 'UnitFileState') == 'disabled', 'source enabled')
    u.effective(m)
    c.require(u.policy_hash(u.SOURCE) == original['source_policy_sha256'], 'source policy changed')
    now = c.status()
    c.require(now['runtime_verified'] and now['active'] == 'active' and now['restarts'] == 0
              and now['runtime_path'] == str(c.RELEASES / rid / 'wecom-mcp-team')
              and now['binary_sha256'] == m['files']['wecom-mcp-team'], 'target process changed')
    u.listener(now['main_pid'])
    c.recovery_matches(m)
    override = c.sha(c.DROPIN)
    c.require(override in (m['files']['service.conf'],m['files']['recovery.conf']), 'target mode changed')
    controller_sha = c.sha(HERE / 'mcp_release_controller.py')
    actual_controller = c.sha(u.CONTROLLER)
    c.require(actual_controller in (original['previous_controller_sha256'], original['controller_sha256'], controller_sha), 'foreign controller')
    provenance = c.read_json(HERE / 'completion-provenance.json', True)
    c.require(set(provenance) == {'git_commit','ci_url','files'} and c.re.fullmatch(r'[a-f0-9]{40}', provenance['git_commit'])
              and provenance['ci_url'].startswith('https://github.com/zlz3907/wecom-mcp/actions/runs/'), 'completion CI evidence missing')
    names = {'complete-shared-migration.py','migrate-shared-service.py','mcp_release_controller.py','wecom-mcp@sharedzoop.service'}
    c.require(set(provenance['files']) == names, 'unexpected completion artifacts')
    for name, digest in provenance['files'].items():
        c.regular(HERE / name, True)
        c.require(c.sha(HERE / name) == digest, 'completion artifact changed')
    expected = dict(original, previous_controller_sha256=actual_controller, controller_sha256=controller_sha,
                    completion_sha256=c.sha(Path(__file__).resolve()), provenance_sha256=c.sha(HERE / 'completion-provenance.json'),
                    original_journal_sha256=c.sha(record / 'expected.json'), original_approval_id=receipt['approval_id'],
                    current_main_pid=now['main_pid'], current_unit_fingerprint=now['unit_fingerprint'],
                    current_override_sha256=override)
    return m, expected


def apply(rid, aid):
    m, expected = fields(rid)
    c.check_approval(aid, 'complete-shared-migration', expected)
    c.check_gnas(m)
    u.preflight(rid)
    c.require(fields(rid)[1] == expected, 'completion baseline changed')
    c.approval(aid, 'complete-shared-migration', expected)
    record = c.CONTROL / 'unit-migrations' / rid
    c.atomic(record / ('completion-' + aid + '.json'), json.dumps(expected).encode(), 0o400)
    c.require(fields(rid)[1] == expected, 'completion baseline changed before mode switch')
    # The original baseline and consumed receipt remain untouched.
    c.atomic(c.DROPIN, (c.RELEASES / rid / 'service.conf').read_bytes())
    try:
        return u.finish(m, expected, record)
    except Exception:
        u.finish(m, expected, record, recovery=True)
        raise ValueError('completion failed; same-version static recovery active')


def main():
    c.require(os.geteuid() == 0, 'root required')
    os.environ['PATH'] = '/usr/sbin:/usr/bin:/sbin:/bin'
    c.require(len(sys.argv) in (3,4), 'check/apply RELEASE [APPROVAL] required')
    action, rid = sys.argv[1:3]
    c.require(c.RID.fullmatch(rid), 'invalid release ID')
    c.regular(c.CONTROL / 'release.lock', True)
    with (c.CONTROL / 'release.lock').open('r') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if action == 'check' and len(sys.argv) == 3:
            result = {'required_approval_fields':fields(rid)[1], 'service_restarted':False}
        elif action == 'apply' and len(sys.argv) == 4:
            result = apply(rid,sys.argv[3])
        else:
            raise ValueError('unsupported action')
        print(json.dumps(result,sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('MCP_COMPLETION_REFUSED: '+str(error) if isinstance(error,ValueError) else 'MCP_COMPLETION_REFUSED: controlled operation failed',file=sys.stderr)
        sys.exit(1)
