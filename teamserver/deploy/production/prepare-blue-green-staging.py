#!/usr/bin/python3 -I
"""Root-only, no-traffic preparation for the isolated blue/gateway artifacts.

``--check`` is read-only.  ``--apply-inert`` installs only static root-owned
files after a separate exact approval; it deliberately does not daemon-reload,
start a unit, write an active receipt, edit Nginx, or touch a Binding/DB.
"""
import datetime as dt
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
    c.check_approval(approval_id, 'install-blue-green-inert', expected)
    for _, target, _ in TARGETS:
        trusted_directory(target.parent)
        require(not target.exists(), 'staging target already exists')
    for path in (b.BLUE_STATE, b.ACTIVE.parent):
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        trusted_directory(path)
    uid, gid = pwd.getpwnam('wecom-mcp-gmzoop').pw_uid, grp.getgrnam('wecom-mcp-gmzoop').gr_gid
    os.chown(b.BLUE_STATE, uid, gid); os.chmod(b.BLUE_STATE, 0o700)
    c.approval(approval_id, 'install-blue-green-inert', expected)
    for source, target, mode in TARGETS:
        c.atomic(target, (HERE / source).read_bytes(), mode)
        require(regular(target) == value['artifacts'][str(target)], 'installed artifact digest mismatch')
    # Intentionally no systemctl daemon-reload/start, no gateway receipt and no
    # network route modification.  A later, separate sample approval is needed.
    return {'state': 'inert_artifacts_installed', 'traffic': 'disabled',
            'binding_write_permitted': False, 'service_restarted': False}


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
