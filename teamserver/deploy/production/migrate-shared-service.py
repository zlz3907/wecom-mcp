#!/usr/bin/python3 -I
"""Fixed one-process gmzoop -> sharedzoop migration under the release lock."""
import contextlib
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import socket
import re
import sys

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('controller', HERE / 'mcp_release_controller.py')
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
SOURCE = 'wecom-mcp@gmzoop.service'
SOURCE_DROPIN = Path('/etc/systemd/system') / (SOURCE + '.d/zz-managed-release.conf')
TARGET_FILE = Path('/etc/systemd/system') / c.UNIT
CONTROLLER = Path('/usr/local/sbin/wecom-mcp-release-controller')


@contextlib.contextmanager
def source_scope():
    unit, dropin = c.UNIT, c.DROPIN
    c.UNIT, c.DROPIN = SOURCE, SOURCE_DROPIN
    try:
        yield
    finally:
        c.UNIT, c.DROPIN = unit, dropin


def prop(unit, name):
    return c.run('systemctl', 'show', unit, '-p', name, '--value')


def absent(path):
    c.require(not path.exists() and not path.is_symlink(), 'target path already exists')


def target_fingerprint():
    pairs = [[str(TARGET_FILE), c.sha(HERE / c.UNIT)]]
    return hashlib.sha256(json.dumps(pairs, separators=(',', ':')).encode()).hexdigest()


def source_status():
    with source_scope():
        return c.status()


def inactive(unit):
    c.require(prop(unit, 'MainPID') == '0' and prop(unit, 'ActiveState') in ('inactive', 'failed'), 'unit must be stopped')


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 7702))


POLICY_PROPERTIES = ('User', 'Group', 'EnvironmentFiles', 'Environment', 'Restart', 'RestartUSec',
    'TimeoutStopUSec', 'OOMPolicy', 'NoNewPrivileges', 'PrivateTmp', 'ProtectHome',
    'ProtectSystem', 'ProtectKernelTunables', 'ProtectKernelModules', 'ProtectControlGroups',
    'RestrictSUIDSGID', 'LockPersonality', 'MemoryDenyWriteExecute', 'MemoryHigh', 'MemoryMax',
    'TasksMax', 'LimitNOFILE', 'CPUQuotaPerSecUSec')


def policy(unit):
    return {key: prop(unit, key) for key in POLICY_PROPERTIES}


def policy_hash(unit):
    return hashlib.sha256(json.dumps(policy(unit), sort_keys=True).encode()).hexdigest()


def listener(pid):
    raw = c.run('ss', '-H', '-ltnp', 'sport = :7702')
    lines = raw.splitlines()
    c.require(len(lines) == 1 and lines[0].split()[3] == '127.0.0.1:7702'
              and re.findall(r'pid=(\d+)', raw) == [str(pid)], 'listener ownership mismatch')


def fields(rid):
    directory = c.RELEASES / rid
    m = c.verify(directory, rid)
    current = source_status()
    c.require(c.baseline_matches(current, m), 'source baseline drift')
    c.require(prop(SOURCE, 'UnitFileState') == 'enabled', 'source enablement drift')
    inactive(c.UNIT)
    c.require(prop(c.UNIT, 'UnitFileState') == 'disabled', 'target enablement drift')
    absent(TARGET_FILE)
    absent(c.DROPIN.parent)
    c.require(m['expected_unmanaged_unit_fingerprint'] == target_fingerprint(), 'target unit fingerprint mismatch')
    for path in (Path(__file__).resolve(), HERE / 'mcp_release_controller.py', HERE / c.UNIT, CONTROLLER):
        c.regular(path, True)
    c.require(m.get('ci_url', '').startswith('https://'), 'same-commit successful CI required')
    expected = c.deploy_fields(m, directory)
    expected.update(source_unit=SOURCE, target_unit=c.UNIT,
                    expected_main_pid=current['main_pid'],
                    source_policy_sha256=policy_hash(SOURCE),
                    target_unit_sha256=c.sha(HERE / c.UNIT),
                    migrator_sha256=c.sha(Path(__file__).resolve()),
                    controller_sha256=c.sha(HERE / 'mcp_release_controller.py'),
                    previous_controller_sha256=c.sha(CONTROLLER))
    listener(current['main_pid'])
    return m, expected


def preflight(rid):
    # No listening socket and no initialization. The same user/environment and
    # immutable binary validate both modes before the existing process is stopped.
    for recovery in (False, True):
        args = ['systemd-run', '--quiet', '--wait', '--collect',
                '--unit=wecom-mcp-migration-check-' + rid + ('-static' if recovery else '-hybrid'),
                '--property=Type=oneshot', '--property=TimeoutStartSec=35',
                '--property=RuntimeMaxSec=35', '--property=Restart=no',
                '--property=User=wecom-mcp-gmzoop', '--property=Group=wecom-mcp-gmzoop',
                '--property=EnvironmentFile=/etc/wecom-mcp/gmzoop.env',
                '--property=ProtectSystem=strict', '--property=ProtectHome=read-only',
                '--property=NoNewPrivileges=yes', '--property=PrivateTmp=yes',
                '--property=ReadWritePaths=' + str(c.BASE / 'instances/gmzoop/data'),
                '--property=ReadWritePaths=' + str(c.STATE),
                '--property=StandardOutput=null', '--property=StandardError=null',
                str(c.RELEASES / rid / 'wecom-mcp-team'), '--gnas-fleet-runtime', str(c.RUNTIME),
                '--gnas-discovery-policy', str(c.RELEASES / rid / 'discovery-policy.json'),
                '--gnas-state-root', str(c.STATE), '--listen', '127.0.0.1:7702']
        if recovery:
            args += ['--gnas-static-only', 'https://' + c.HOSTS[0]]
        c.run(*(args + ['--check-config']))


def journal(record, phase, **extra):
    c.atomic(record / 'phase.json', json.dumps(dict(phase=phase, **extra)).encode(), 0o400)


def effective(m):
    c.require(policy(c.UNIT) == policy(SOURCE), 'source security or resource policy changed')
    source_paths = set(prop(SOURCE, 'ReadWritePaths').split())
    c.require(set(prop(c.UNIT, 'ReadWritePaths').split()) == source_paths | {str(c.STATE)}, 'writable paths changed')
    c.require(c.unit_fingerprint(True) == m['expected_unmanaged_unit_fingerprint'], 'target effective unit drift')
    for name, expected in (('User', 'wecom-mcp-gmzoop'), ('Group', 'wecom-mcp-gmzoop'),
                           ('FragmentPath', str(TARGET_FILE)), ('NoNewPrivileges', 'yes'),
                           ('ProtectSystem', 'strict'), ('ProtectHome', 'read-only')):
        c.require(prop(c.UNIT, name) == expected, 'target effective properties differ')
    c.require('/etc/wecom-mcp/gmzoop.env' in prop(c.UNIT, 'EnvironmentFiles'), 'environment reference changed')


def install_controller(expected):
    current = c.sha(CONTROLLER)
    c.require(current in (expected['previous_controller_sha256'], expected['controller_sha256']), 'concurrent controller change')
    if current != expected['controller_sha256']:
        c.atomic(CONTROLLER, (HERE / 'mcp_release_controller.py').read_bytes(), 0o755)
    c.require(c.sha(CONTROLLER) == expected['controller_sha256'], 'controller install failed')


def finish(m, expected, record, recovery=False):
    rid = m['release_id']
    c.require(c.sha(CONTROLLER) in (expected['previous_controller_sha256'], expected['controller_sha256']), 'concurrent controller change; compensation refused')
    c.require(policy_hash(SOURCE) == expected['source_policy_sha256'], 'source policy drift')
    inactive(SOURCE)
    c.require(prop(SOURCE, 'UnitFileState') == 'disabled', 'source still enabled')
    effective(m)
    c.require(c.runtime_fingerprint() == m['expected_runtime_config_fingerprint'], 'runtime configuration drift')
    c.require(c.sha(c.DROPIN) in (m['files']['service.conf'], m['files']['recovery.conf']), 'target override changed')
    if recovery:
        c.atomic(c.DROPIN, (c.RELEASES / rid / 'recovery.conf').read_bytes())
    journal(record, 'starting_static' if recovery else 'starting_hybrid')
    c.restart_and_verify(str(c.RELEASES / rid / 'wecom-mcp-team'), m['files']['wecom-mcp-team'],
                         c.HOSTS[:1] if recovery else c.HOSTS,
                         expected_config_fingerprint=m['expected_runtime_config_fingerprint'])
    if recovery:
        c.recovery_health(m)
    listener(c.status()['main_pid'])
    journal(record, 'target_verified', recovery=recovery)
    install_controller(expected)
    c.run('systemctl', 'enable', c.UNIT)
    c.require(prop(c.UNIT, 'UnitFileState') == 'enabled', 'target not enabled')
    inactive(SOURCE)
    c.require(prop(SOURCE, 'UnitFileState') == 'disabled', 'source re-enabled')
    c.recovery_matches(m)
    state = dict(unit_fingerprint=c.unit_fingerprint(), runtime_config_fingerprint=c.runtime_fingerprint())
    c.atomic(c.CONTROL / 'rollback' / rid / ('recovered.json' if recovery else 'deployed.json'), json.dumps(state).encode(), 0o400)
    journal(record, 'recovered_static' if recovery else 'migrated_hybrid')
    return {'state': 'recovered_static' if recovery else 'migrated_hybrid', 'unit': c.UNIT,
            'release_id': rid, 'observation_complete': False}


def apply(rid, aid):
    m, expected = fields(rid)
    c.check_approval(aid, 'migrate-shared-service', expected)
    c.check_gnas(m)
    c.require(c.STATE.is_dir() and c.STATE.resolve() == c.STATE, 'state root missing')
    preflight(rid)
    c.require(fields(rid)[1] == expected, 'baseline changed during preflight')
    record = c.CONTROL / 'unit-migrations' / rid
    record.mkdir(parents=True, mode=0o700)
    c.atomic(record / 'expected.json', json.dumps(expected).encode(), 0o400)
    c.atomic(record / 'controller.before', CONTROLLER.read_bytes(), 0o400)
    c.atomic(record / 'source-status.json', json.dumps(source_status()).encode(), 0o400)
    # Journal first; the one-time receipt is the authority to continue after a crash.
    journal(record, 'prepared', approval_id=aid)
    c.approval(aid, 'migrate-shared-service', expected)
    c.atomic(record / 'consumed-approval.json', json.dumps(c.read_json(c.APPROVALS / (aid + '.json'), True)).encode(), 0o400)
    try:
        c.require(fields(rid)[1] == expected, 'baseline drift immediately before migration')
        c.DROPIN.parent.mkdir(mode=0o755)
        c.atomic(TARGET_FILE, (HERE / c.UNIT).read_bytes())
        c.atomic(c.DROPIN, (c.RELEASES / rid / 'service.conf').read_bytes())
        c.run('systemctl', 'daemon-reload')
        effective(m)
        c.require(c.baseline_matches(source_status(), m), 'source changed during target preparation')
        c.require(c.sha(CONTROLLER) == expected['previous_controller_sha256'], 'controller changed before stop')
        journal(record, 'stopping_source')
        # The source is disabled before stopping; the target never overlaps it.
        c.run('systemctl', 'disable', SOURCE)
        c.run('systemctl', 'stop', SOURCE)
        inactive(SOURCE)
        c.require(not Path('/proc/%d' % expected['expected_main_pid']).exists(), 'source process still exists')
        free_port()
        journal(record, 'source_stopped')
    except Exception:
        # If stop completed, preserve the new-unit setup for explicit recovery.
        # If the original process is still healthy, undo preparation only.
        if prop(SOURCE, 'ActiveState') == 'active':
            abort_prepared(rid)
        raise
    (c.CONTROL / 'rollback' / rid).mkdir(mode=0o700)
    try:
        return finish(m, expected, record)
    except Exception:
        # Never restart the incompatible historical binary. Recovery stays on
        # the exact same immutable new binary and target unit.
        finish(m, expected, record, recovery=True)
        raise ValueError('migration hybrid failed; same-version static recovery active')


def abort_prepared(rid):
    record = c.CONTROL / 'unit-migrations' / rid
    expected = c.read_json(record / 'expected.json', True)
    receipt_path = record / 'consumed-approval.json'
    if receipt_path.exists():
        receipt = c.read_json(receipt_path, True)
    else:
        aid = c.read_json(record / 'phase.json', True)['approval_id']
        receipt = c.read_json(c.APPROVALS / (aid + '.json'), True)
    c.require(receipt['action'] == 'migrate-shared-service' and receipt['environment'] == c.ENVIRONMENT
              and all(receipt.get(k) == v for k, v in expected.items()), 'consumed migration authority mismatch')
    c.regular(c.CONTROL / 'used' / receipt['approval_id'], True)
    current = source_status()
    c.require(c.baseline_matches(current, expected) and current['main_pid'] == expected['expected_main_pid'],
              'abort only allowed while original process is still healthy')
    listener(current['main_pid'])
    c.require(c.sha(CONTROLLER) == expected['previous_controller_sha256'], 'controller changed; abort refused')
    inactive(c.UNIT)
    c.require(prop(c.UNIT, 'UnitFileState') == 'disabled', 'target enablement drift')
    # Validate every remaining file before removing any owned preparation.
    m = c.verify(c.RELEASES / rid, rid)
    if TARGET_FILE.exists():
        c.regular(TARGET_FILE, True)
        c.require(c.sha(TARGET_FILE) == expected['target_unit_sha256'], 'target file drift')
    if c.DROPIN.parent.exists():
        c.require(set(c.DROPIN.parent.iterdir()) <= {c.DROPIN}, 'unexpected target drop-in')
        if c.DROPIN.exists():
            c.regular(c.DROPIN, True)
            c.require(c.sha(c.DROPIN) == m['files']['service.conf'], 'target override drift')
    c.require(prop(SOURCE, 'UnitFileState') in ('disabled', 'enabled'), 'source enablement drift')
    if c.DROPIN.exists():
        c.DROPIN.unlink()
    if c.DROPIN.parent.exists():
        c.DROPIN.parent.rmdir()
    if TARGET_FILE.exists():
        TARGET_FILE.unlink()
    c.run('systemctl', 'daemon-reload')
    c.run('systemctl', 'enable', SOURCE)
    c.require(source_status() == current and prop(SOURCE, 'UnitFileState') == 'enabled', 'preparation restoration failed')
    journal(record, 'aborted_before_stop')
    return {'state': 'aborted_before_stop', 'source_restarted': False}


def recover(rid, aid=None):
    record = c.CONTROL / 'unit-migrations' / rid
    expected = c.read_json(record / 'expected.json', True)
    m = c.verify(c.RELEASES / rid, rid)
    c.require(c.deploy_fields(m, c.RELEASES / rid).items() <= expected.items(), 'recovery candidate mismatch')
    for key, path in (('migrator_sha256', Path(__file__).resolve()), ('controller_sha256', HERE / 'mcp_release_controller.py'), ('target_unit_sha256', TARGET_FILE)):
        c.require(c.sha(path) == expected[key], 'recovery implementation drift')
    inactive(SOURCE)
    c.require(prop(SOURCE, 'UnitFileState') == 'disabled', 'source still enabled')
    effective(m)
    c.require(c.sha(CONTROLLER) in (expected['previous_controller_sha256'], expected['controller_sha256']), 'controller drift')
    current = c.status()
    c.require(current['runtime_path'] == str(c.RELEASES / rid / 'wecom-mcp-team') and current['binary_sha256'] == m['files']['wecom-mcp-team'], 'target runtime drift')
    expected = dict(expected, recovery_source_unit_fingerprint=c.unit_fingerprint())
    if aid is None:
        return {'required_approval_fields': expected, 'production_changed': False}
    c.check_approval(aid, 'recover-shared-migration', expected)
    c.approval(aid, 'recover-shared-migration', expected)
    (c.CONTROL / 'rollback' / rid).mkdir(mode=0o700, exist_ok=True)
    return finish(m, expected, record, recovery=True)


def main():
    c.require(os.geteuid() == 0, 'root required')
    os.environ['PATH'] = '/usr/sbin:/usr/bin:/sbin:/bin'
    c.require(len(sys.argv) in (3, 4), 'check/stage/apply/recover RELEASE [ISSUED_APPROVAL] required')
    action, rid = sys.argv[1:3]
    c.require(c.RID.fullmatch(rid), 'invalid release ID')
    c.require(c.CONTROL.resolve() == c.CONTROL and c.CONTROL.stat().st_uid == 0 and not c.CONTROL.stat().st_mode & 0o022, 'invalid control root')
    c.regular(c.CONTROL / 'release.lock', True)
    with (c.CONTROL / 'release.lock').open('r') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if action == 'check' and len(sys.argv) == 3:
            result = {'required_approval_fields': fields(rid)[1], 'production_changed': False}
        elif action == 'recover-check' and len(sys.argv) == 3:
            result = recover(rid)
        elif action == 'abort-prestop' and len(sys.argv) == 3:
            result = abort_prepared(rid)
        elif action == 'preflight' and len(sys.argv) == 3:
            before = fields(rid)
            preflight(rid)
            c.require(fields(rid) == before, 'preflight baseline drift')
            result = {'state': 'both_modes_preflight_passed', 'service_restarted': False}
        elif action == 'stage' and len(sys.argv) == 3:
            with source_scope():
                result = c.stage(rid)
        elif action in ('apply', 'recover') and len(sys.argv) == 4:
            result = (apply if action == 'apply' else recover)(rid, sys.argv[3])
        else:
            raise ValueError('unsupported action')
        print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('MCP_MIGRATION_REFUSED: ' + str(error) if isinstance(error, ValueError) else 'MCP_MIGRATION_REFUSED: controlled operation failed', file=sys.stderr)
        sys.exit(1)
