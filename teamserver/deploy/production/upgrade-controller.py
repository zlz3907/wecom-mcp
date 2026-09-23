#!/usr/bin/python3 -I
"""Separately approved controller upgrade. Never changes or restarts the service."""
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sys

sys.dont_write_bytecode = True
SOURCE = Path(__file__).resolve().with_name('mcp_release_controller.py')
SELF = Path(__file__).resolve()
TARGET = Path('/usr/local/sbin/wecom-mcp-release-controller')
spec = importlib.util.spec_from_file_location('controller', SOURCE)
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


def trusted_directory(path):
    c.require(path.is_dir() and path.resolve() == path and path.stat().st_uid == 0 and not path.stat().st_mode & 0o022, 'unsafe control directory')


def fields():
    for path in (SOURCE, SELF, TARGET):
        c.regular(path, True)
    facts = c.status()
    c.require(facts.get('runtime_verified') is True and facts['active'] == 'active' and facts['restarts'] == 0, 'healthy verified baseline required')
    expected = {'controller_sha256': c.sha(SOURCE), 'upgrader_sha256': c.sha(SELF), 'previous_controller_sha256': c.sha(TARGET), 'expected_main_pid': facts['main_pid']}
    expected.update({'expected_' + key: facts[key] for key in ('runtime_path', 'binary_sha256', 'unit_fingerprint', 'runtime_config_fingerprint')})
    return expected, facts


def upgrade(approval_id):
    expected, facts = fields()
    c.check_approval(approval_id, 'upgrade-controller', expected)
    c.require(c.sha(SOURCE) != c.sha(TARGET), 'controller already at candidate')
    backups = c.CONTROL / 'controller-upgrades'
    backups.mkdir(mode=0o700, exist_ok=True)
    trusted_directory(backups)
    backup = backups / approval_id
    c.require(not backup.exists(), 'upgrade backup already exists')
    old = TARGET.read_bytes()
    new = SOURCE.read_bytes()
    c.require(fields() == (expected, facts), 'baseline changed before upgrade')
    c.approval(approval_id, 'upgrade-controller', expected)
    backup.mkdir(mode=0o700)
    c.atomic(backup / 'controller.before', old, 0o400)
    c.atomic(backup / 'approval-binding.json', json.dumps(expected).encode(), 0o400)
    c.require(fields() == (expected, facts), 'baseline changed before replacement')
    try:
        c.atomic(TARGET, new, 0o755)
        c.require(c.sha(TARGET) == expected['controller_sha256'] and c.status() == facts, 'post-upgrade identity or service drift')
        c.atomic(backup / 'completed.json', json.dumps({'controller_sha256': c.sha(TARGET), 'service_restarted': False}).encode(), 0o400)
    except Exception:
        # Never overwrite an unrelated concurrent administrator change.
        c.require(c.sha(TARGET) == expected['controller_sha256'], 'controller changed concurrently; retained backup requires reviewed recovery')
        c.atomic(TARGET, old, 0o755)
        c.require(c.sha(TARGET) == expected['previous_controller_sha256'], 'controller restoration failed')
        raise
    return {'state': 'controller_upgraded', 'controller_sha256': c.sha(TARGET), 'service_restarted': False}


def main():
    c.require(os.geteuid() == 0, 'root admin required')
    os.environ['PATH'] = '/usr/sbin:/usr/bin:/sbin:/bin'
    c.require(sys.argv[1:] == ['--check'] or len(sys.argv) == 3 and sys.argv[1] == '--apply', 'usage: --check | --apply approval-id')
    for directory in (c.CONTROL, c.CONTROL / 'used', c.APPROVALS):
        trusted_directory(directory)
    lock_path = c.CONTROL / 'release.lock'
    c.regular(lock_path, True)
    # --check remains read-only and contends on exactly the deployment lock.
    with lock_path.open('r') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if sys.argv[1] == '--check':
            expected, _ = fields()
            result = {'environment': c.ENVIRONMENT, 'action': 'upgrade-controller', **expected, 'service_restarted': False}
        else:
            result = upgrade(sys.argv[2])
        print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('UPGRADE_REFUSED: ' + str(error) if isinstance(error, ValueError) else 'UPGRADE_REFUSED: controlled operation failed', file=sys.stderr)
        sys.exit(1)
