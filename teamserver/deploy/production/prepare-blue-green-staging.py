#!/usr/bin/python3 -I
"""Root-only, no-traffic preparation for the isolated blue/gateway artifacts.

``--check`` is read-only.  ``--apply-inert`` installs only static root-owned
files after a separate exact approval; it deliberately does not daemon-reload,
start a unit, write an active receipt, edit Nginx, or touch a Binding/DB.
"""
import datetime as dt
import contextlib
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import pwd
import grp
import sys

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('blue', HERE / 'mcp_blue_green_executor.py')
b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b)
c = b.c

TARGETS = (
    ('mcp_blue_green_executor.py', Path('/usr/local/sbin/mcp-blue-green-executor'), 0o755),
    ('wecom-mcp-loopback-switch.py', b.SWITCHER, 0o755),
    ('wecom-mcp-loopback-gateway.py', b.GATEWAY, 0o755),
    ('wecom-mcp@sharedzoop-blue.service', b.BLUE_UNIT_FILE, 0o644),
    ('wecom-mcp-loopback-gateway.service', b.GATEWAY_UNIT_FILE, 0o644),
)
ENVIRONMENT_FILE = Path('/etc/wecom-mcp/gmzoop.env')
MIN_AVAILABLE_BYTES = 256 * 1024 * 1024
SAMPLE_MEMORY_MAX_BYTES = 128 * 1024 * 1024
APPLY_ACTION = 'install-blue-green-inert'
JOURNAL_SCHEMA_VERSION = 1


def require(ok, message):
    if not ok:
        raise ValueError(message)


def mem_available_bytes():
    values = {}
    for line in Path('/proc/meminfo').read_text().splitlines():
        key, value = line.split(':', 1)
        values[key] = int(value.strip().split()[0]) * 1024
    return values['MemAvailable']


def regular(path):
    c.regular(path, True)
    return c.sha(path)


def artifact_fields():
    return {str(target): regular(HERE / source) for source, target, _ in TARGETS}


def trusted_directory(path):
    value = path.lstat()
    require(path.is_dir() and path.resolve() == path and value.st_uid == 0
            and not value.st_mode & 0o022, 'unsafe staging directory')


def journal_path(approval_id):
    require(c.APR.fullmatch(approval_id), 'invalid approval ID')
    return c.CONTROL / ('blue-green-staging.' + approval_id + '.json')


@contextlib.contextmanager
def staging_lock():
    """Serialize inert installation with every other staging process.

    The lock lives in the controller's root-owned control directory rather
    than in either color's state root.  This is important while both colors
    are being prepared: a per-color lock would still allow two installers to
    consume the same approval and race on the shared static artifacts.
    """
    trusted_directory(c.CONTROL)
    # Reuse the controller/executor deployment lock.  A staging-only lock
    # would still permit a concurrent green release or blue promotion.
    lock_path = c.CONTROL / 'release.lock'
    with lock_path.open('a') as lock:
        os.chmod(lock_path, 0o600)
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise ValueError('another blue-green staging apply is running') from error
        try:
            yield
        finally:
            fcntl.flock(lock.fileno(), fcntl.LOCK_UN)


def write_journal(path, value):
    c.atomic(path, json.dumps(value, sort_keys=True, separators=(',', ':')).encode(), 0o400)


def read_journal(path):
    if not path.exists():
        return None
    return c.read_json(path, True)


def remove_journal(path):
    temporary = path.with_name(path.name + '.new')
    if temporary.exists() or temporary.is_symlink():
        temporary.unlink()
    if path.exists() or path.is_symlink():
        path.unlink()


def service_identity():
    return pwd.getpwnam('wecom-mcp-gmzoop').pw_uid, grp.getgrnam('wecom-mcp-gmzoop').gr_gid


def missing_directories(path):
    """Return the canonical missing directory chain without creating it."""
    require(path.is_absolute() and path == path.resolve(), 'staging directory must be canonical')
    missing = []
    current = path
    while not current.exists():
        require(not current.is_symlink(), 'staging directory must not be a symlink')
        missing.append(current)
        parent = current.parent
        require(parent != current, 'staging directory has no trusted parent')
        current = parent
    trusted_directory(current)
    return list(reversed(missing))


def state_directory_ok(path, uid, gid):
    value = path.lstat()
    require(path.is_dir() and path.resolve() == path and value.st_uid == uid and value.st_gid == gid
            and value.st_mode & 0o777 == 0o700, 'blue state root ownership or mode mismatch')


def target_preflight(value):
    targets = []
    for source, target, mode in TARGETS:
        trusted_directory(target.parent)
        require(not target.exists() and not target.is_symlink(), 'staging target already exists')
        temporary = target.with_name(target.name + '.new')
        require(not temporary.exists() and not temporary.is_symlink(), 'staging temporary target already exists')
        targets.append({'source': source, 'target': str(target), 'mode': mode,
                        'sha256': value['artifacts'][str(target)]})
    state_dirs = []
    seen = set()
    for path in (b.BLUE_STATE, b.ACTIVE.parent):
        for missing in missing_directories(path):
            if str(missing) not in seen:
                state_dirs.append(missing)
                seen.add(str(missing))
    return targets, state_dirs


def installed_matches(journal, expected):
    require(journal.get('schema_version') == JOURNAL_SCHEMA_VERSION
            and journal.get('state') == 'installed'
            and journal.get('approval_id')
            and journal.get('expected') == expected, 'completed staging journal binding mismatch')
    for target, digest in expected['artifacts'].items():
        require(regular(Path(target)) == digest, 'installed artifact digest mismatch')
    uid, gid = service_identity()
    state_directory_ok(b.BLUE_STATE, uid, gid)
    trusted_directory(b.ACTIVE.parent)
    return True


def rollback_install(journal, recover_planned=False):
    """Remove only objects planned by this approval, failing closed on drift."""
    errors = []
    expected_by_target = {item['target']: item['sha256'] for item in journal.get('targets', [])}
    for target in reversed(list(expected_by_target)):
        path = Path(target)
        if not (path.exists() or path.is_symlink()):
            continue
        try:
            require(not path.is_symlink() and regular(path) == expected_by_target[target],
                    'rollback target drift: ' + target)
            path.unlink()
        except Exception as error:
            errors.append(str(error))
    for target in expected_by_target:
        temporary = Path(target).with_name(Path(target).name + '.new')
        if temporary.exists() or temporary.is_symlink():
            try:
                temporary.unlink()
            except Exception as error:
                errors.append(str(error))
    directories = list(journal.get('created_dirs', []))
    if recover_planned:
        directories = list(dict.fromkeys(directories + journal.get('planned_dirs', [])))
    for directory in reversed([Path(path) for path in directories]):
        if not (directory.exists() or directory.is_symlink()):
            continue
        try:
            require(not directory.is_symlink() and directory.is_dir(), 'rollback directory drift: ' + str(directory))
            directory.rmdir()
        except Exception as error:
            errors.append(str(error))
    if errors:
        raise ValueError('inert staging rollback incomplete: ' + '; '.join(errors))


def apply_result(state):
    return {'state': state, 'traffic': 'disabled',
            'binding_write_permitted': False, 'service_restarted': False}


def inactive(unit):
    active = c.run('systemctl', 'show', unit, '-p', 'ActiveState', '--value')
    pid = c.run('systemctl', 'show', unit, '-p', 'MainPID', '--value')
    require(active in ('inactive', 'failed') and pid in ('0', ''), 'unit must be inactive for inert staging')
    return {'active_state': active, 'main_pid': pid}


def fields():
    green = c.status()
    require(c.baseline_matches(green, {'expected_runtime_path': green['runtime_path'],
            'expected_binary_sha256': green['binary_sha256'],
            'expected_unit_fingerprint': green['unit_fingerprint'],
            'expected_runtime_config_fingerprint': green['runtime_config_fingerprint']}),
            'green baseline is not stable')
    c.regular(ENVIRONMENT_FILE, True)  # path/ownership only; never read contents
    require(mem_available_bytes() >= MIN_AVAILABLE_BYTES, 'insufficient MemAvailable for isolated sample')
    require(not b.BLUE_DROPIN.exists() and not b.BLUE_DROPIN.is_symlink(), 'blue drop-in already exists')
    return {
        'environment': c.ENVIRONMENT,
        'artifacts': artifact_fields(),
        'green_runtime_path': green['runtime_path'], 'green_binary_sha256': green['binary_sha256'],
        'green_unit_fingerprint': green['unit_fingerprint'],
        'environment_file_path': str(ENVIRONMENT_FILE),
        'environment_file_sha256': None,
        'blue_state_root': str(b.BLUE_STATE), 'receipt_parent': str(b.ACTIVE.parent),
        'min_available_bytes': MIN_AVAILABLE_BYTES, 'sample_memory_max_bytes': SAMPLE_MEMORY_MAX_BYTES,
        'blue': inactive(b.BLUE_UNIT), 'gateway': inactive(b.GATEWAY_UNIT),
        'traffic': 'disabled', 'binding_write_permitted': False,
    }


def apply_inert(approval_id, value):
    expected = dict(value, installer_sha256=regular(Path(__file__).resolve()))
    journal = journal_path(approval_id)
    with staging_lock():
        previous = read_journal(journal)
        if previous is not None:
            if previous.get('state') == 'installed':
                installed_matches(previous, expected)
                return apply_result('inert_artifacts_already_installed')
            if previous.get('state') == 'consumed':
                # A killed process can leave the one-time receipt consumed.  A
                # retry first removes only the journal-listed partial objects,
                # then refuses to spend a different approval implicitly.
                try:
                    rollback_install(previous, recover_planned=True)
                    remove_journal(journal)
                except Exception as error:
                    raise ValueError('interrupted inert staging requires manual rollback: ' + str(error)) from error
                raise ValueError('previous inert staging apply was interrupted; issue a new approval')
            raise ValueError('unknown inert staging journal state')

        # All checks above are read-only.  In particular, no target, state
        # root, receipt parent, chown, or artifact write occurs before this
        # exact approval is atomically consumed under the cross-process lock.
        c.check_approval(approval_id, APPLY_ACTION, expected)
        targets, planned_dirs = target_preflight(value)
        journal_value = {'schema_version': JOURNAL_SCHEMA_VERSION, 'state': 'consumed',
                         'approval_id': approval_id, 'action': APPLY_ACTION,
                         'expected': expected, 'targets': targets,
                         'planned_dirs': [str(path) for path in planned_dirs],
                         'created_dirs': []}
        c.approval(approval_id, APPLY_ACTION, expected)
        try:
            write_journal(journal, journal_value)
            for path in planned_dirs:
                require(not path.exists() and not path.is_symlink(), 'staging directory appeared during apply')
                path.mkdir(mode=0o700)
                os.chmod(path, 0o700)
                journal_value['created_dirs'].append(str(path))
                write_journal(journal, journal_value)
            uid, gid = service_identity()
            os.chown(b.BLUE_STATE, uid, gid)
            os.chmod(b.BLUE_STATE, 0o700)
            write_journal(journal, journal_value)
            for source, target, mode in TARGETS:
                c.atomic(target, (HERE / source).read_bytes(), mode)
                require(regular(target) == value['artifacts'][str(target)], 'installed artifact digest mismatch')
                write_journal(journal, journal_value)
            journal_value['state'] = 'installed'
            write_journal(journal, journal_value)
        except Exception as error:
            try:
                rollback_install(journal_value)
                remove_journal(journal)
            except Exception as rollback_error:
                raise ValueError('inert staging failed and rollback was incomplete: %s; %s' % (error, rollback_error)) from error
            raise
    # Intentionally no systemctl daemon-reload/start, no gateway receipt and no
    # network route modification.  A later, separate sample approval is needed.
    return apply_result('inert_artifacts_installed')


def main():
    require(os.geteuid() == 0, 'root required')
    os.environ['PATH'] = '/usr/sbin:/usr/bin:/sbin:/bin'
    require(len(sys.argv) in (2, 3) and sys.argv[1] in ('--check', '--apply-inert'),
            'usage: --check | --apply-inert approval-id')
    value = fields()
    if sys.argv[1] == '--check':
        print(json.dumps({'state': 'inert_staging_checked', **value}, sort_keys=True))
        return
    require(len(sys.argv) == 3, 'exact approval required for inert install')
    print(json.dumps(apply_inert(sys.argv[2], value), sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('BLUE_GREEN_STAGING_REFUSED: ' + str(error) if isinstance(error, ValueError)
              else 'BLUE_GREEN_STAGING_REFUSED: controlled operation failed', file=sys.stderr)
        sys.exit(1)
