#!/usr/bin/python3 -I
"""Dedicated blue/green executor; never managed by the shared-green controller.

This program is intentionally inert until installed separately by a root-owned
workflow.  It requires a pre-provisioned loopback gateway/switch and refuses
to alter Nginx, GNAS, tenant Bindings, or the shared green unit.
"""
import datetime as dt
import fcntl
import http.client
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import sys
import time

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('controller', HERE / 'mcp_release_controller.py')
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)

BLUE_UNIT = 'wecom-mcp@sharedzoop-blue.service'
BLUE_DROPIN = Path('/etc/systemd/system') / (BLUE_UNIT + '.d/zz-managed-blue-green.conf')
BLUE_STATE = c.BASE / 'state/discovery-blue'
BLUE_LOOPBACK = '127.0.0.1:7703'
GATEWAY_UNIT = 'wecom-mcp-loopback-gateway.service'
SWITCHER = Path('/usr/local/sbin/wecom-mcp-loopback-switch')
ACTIVE = c.CONTROL / 'entrypoint/active.json'
FILES = ('wecom-mcp-team', 'discovery-policy.json', 'blue.conf', 'plan.json', 'manifest.json')
RID = c.RID

PLAN = {
    'version': 1,
    'green': {'unit': c.UNIT, 'loopback': '127.0.0.1:7702', 'state_root': str(c.STATE)},
    'blue': {'unit': BLUE_UNIT, 'loopback': BLUE_LOOPBACK, 'state_root': str(BLUE_STATE), 'ready_worker_isolation': True, 'kill_mode': 'control-group'},
    'entrypoint': {
        'gateway_unit': GATEWAY_UNIT,
        'gateway_loopback': '127.0.0.1:7701',
        'switcher': str(SWITCHER),
        'active_receipt': str(ACTIVE),
        'kind': 'preprovisioned-atomic-loopback-switch',
    },
    'shared_green_action': 'never-restart',
}


def require(ok, message):
    if not ok:
        raise ValueError(message)


def run(*args):
    return c.run(*args)


def trusted_directory(path):
    value = path.stat()
    require(path.is_dir() and path.resolve() == path and value.st_uid == 0 and not value.st_mode & 0o022, 'untrusted control directory')


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'))


def executor_vector():
    return c.fleet_state_vector()


def blue_dropin(release_id):
    require(RID.fullmatch(release_id), 'invalid release ID')
    base = c.RELEASES / release_id
    args = '%s --gnas-discovery-policy %s --gnas-state-root %s --listen %s --gnas-ready-worker-isolation --gnas-ready-worker-timeout 5s' % (
        base / 'wecom-mcp-team', base / 'discovery-policy.json', BLUE_STATE, BLUE_LOOPBACK)
    return ('[Service]\nExecStartPre=\nExecStart=\nExecStartPre=' + args + ' --check-config\nExecStart=' + args +
            ' --gnas-fleet-refresh 30s\nReadWritePaths=' + str(BLUE_STATE) + '\nKillMode=control-group\nTimeoutStopSec=30s\n').encode()


def verify(directory, release_id):
    require(RID.fullmatch(release_id), 'invalid release ID')
    require(directory.is_dir() and directory.resolve() == directory and not directory.is_symlink(), 'invalid candidate directory')
    require({path.name for path in directory.iterdir()} == set(FILES) | {'SHA256SUMS'}, 'unexpected candidate contents')
    c.regular(directory / 'SHA256SUMS', True)
    require((directory / 'SHA256SUMS').read_text() == ''.join(c.sha(directory / name) + '  ' + name + '\n' for name in sorted(FILES)), 'candidate checksum mismatch')
    manifest = c.read_json(directory / 'manifest.json', True)
    require(manifest.get('schema_version') == 1 and manifest.get('environment') == c.ENVIRONMENT and manifest.get('release_id') == release_id, 'manifest identity mismatch')
    require(manifest.get('deployment_topology') == 'blue-green-isolated-ready-worker' and manifest.get('plan') == PLAN, 'blue-green plan mismatch')
    require(manifest.get('fleet_state_vector') == executor_vector() and manifest.get('fleet_state_vector_sha256') == c.fleet_state_vector_sha256(), 'fleet state vector mismatch')
    require(manifest.get('source_clean') is True and manifest.get('target') == 'linux/amd64' and manifest.get('ci_url', '').startswith('https://'), 'build provenance missing')
    require(set(manifest.get('files', {})) == set(FILES[:-1]), 'artifact list mismatch')
    for name, digest in manifest['files'].items():
        c.regular(directory / name, True)
        require(c.SHA.fullmatch(digest) and c.sha(directory / name) == digest, 'artifact digest mismatch')
    require((directory / 'blue.conf').read_bytes() == blue_dropin(release_id), 'blue unit arguments mismatch')
    require(c.read_json(directory / 'plan.json', True) == PLAN, 'blue-green plan artifact mismatch')
    return manifest


def blue_status():
    values = {
        'ActiveState': run('systemctl', 'show', BLUE_UNIT, '-p', 'ActiveState', '--value'),
        'MainPID': run('systemctl', 'show', BLUE_UNIT, '-p', 'MainPID', '--value'),
        'KillMode': run('systemctl', 'show', BLUE_UNIT, '-p', 'KillMode', '--value'),
    }
    require(values.get('KillMode') == 'control-group', 'blue unit must terminate the worker cgroup')
    return values


def gateway_status():
    return {
        'ActiveState': run('systemctl', 'show', GATEWAY_UNIT, '-p', 'ActiveState', '--value'),
        'MainPID': run('systemctl', 'show', GATEWAY_UNIT, '-p', 'MainPID', '--value'),
    }


def read_active():
    receipt = c.read_json(ACTIVE, True)
    require(set(receipt) == {'version', 'color', 'loopback'} and receipt['version'] == 1 and receipt['color'] in ('green', 'blue'), 'invalid entrypoint receipt')
    require(receipt['loopback'] == PLAN[receipt['color']]['loopback'], 'entrypoint receipt/backend mismatch')
    return receipt


def switch(target, expected):
    require(target in ('green', 'blue') and expected in ('green', 'blue') and target != expected, 'invalid entrypoint switch')
    c.regular(SWITCHER, True)
    require(read_active()['color'] == expected, 'entrypoint changed concurrently')
    run(str(SWITCHER), '--active-file', str(ACTIVE), '--expected-color', expected, '--to', target)
    receipt = read_active()
    require(receipt['color'] == target, 'atomic entrypoint readback failed')


def check_vector(color, public):
    loopback = PLAN[color]['loopback']
    for row in executor_vector():
        if row['state'] == 'tenant_unavailable':
            if public:
                c.unready(row['host'], loopback=loopback)
            else:
                unavailable_local(row['host'], loopback)
            continue
        require(row['state'] == 'ready', 'unknown fleet state')
        for path, code in (('/healthz', 200), ('/readyz', 200), ('/mcp', 401), ('/.well-known/oauth-protected-resource/mcp', 200)):
            c.probe(row['host'], path, code, public=public, loopback=loopback)


def unavailable_local(host, loopback):
    local_host, separator, local_port = loopback.rpartition(':')
    require(separator == ':' and local_host == '127.0.0.1' and local_port.isdigit(), 'invalid blue loopback')
    for path in ('/healthz', '/readyz', '/mcp', '/.well-known/oauth-protected-resource/mcp'):
        conn = http.client.HTTPConnection(local_host, int(local_port), timeout=5)
        try:
            conn.request('GET', path, headers={'Host': host})
            response = conn.getresponse()
            require(response.status == 503 and response.getheader('Cache-Control') == 'no-store' and response.getheader('WWW-Authenticate') is None and response.read(128) == b'MCP instance unavailable\n', 'unready tenant isolation contract failed')
        finally:
            conn.close()


def expected_fields(manifest, directory):
    return {
        'release_id': manifest['release_id'], 'binary_sha256': manifest['files']['wecom-mcp-team'],
        'manifest_sha256': c.sha(directory / 'manifest.json'), 'blue_conf_sha256': manifest['files']['blue.conf'],
        'plan_sha256': manifest['files']['plan.json'], 'fleet_state_vector': manifest['fleet_state_vector'],
        'fleet_state_vector_sha256': manifest['fleet_state_vector_sha256'], 'blue_unit': BLUE_UNIT,
        'blue_loopback': BLUE_LOOPBACK, 'green_unit': c.UNIT, 'green_action': 'never-restart',
    }


def deploy(release_id, approval_id):
    directory = c.RELEASES / release_id
    manifest = verify(directory, release_id)
    green = c.status()
    require(c.baseline_matches(green, manifest), 'green baseline drift')
    c.check_gnas(manifest)
    initial_blue = blue_status()
    require(initial_blue.get('ActiveState') in ('inactive', 'failed') and initial_blue.get('MainPID') in ('0', ''), 'blue unit already active')
    gateway = gateway_status()
    require(gateway.get('ActiveState') == 'active' and int(gateway.get('MainPID', '0')) > 1, 'loopback gateway is not active')
    require(read_active()['color'] == 'green', 'entrypoint is not green')
    c.regular(SWITCHER, True)
    trusted_directory(BLUE_DROPIN.parent)
    c.check_approval(approval_id, 'deploy-blue-green', expected_fields(manifest, directory))
    require(BLUE_STATE.is_dir() and BLUE_STATE.resolve() == BLUE_STATE, 'blue state root not provisioned')
    c.approval(approval_id, 'deploy-blue-green', expected_fields(manifest, directory))
    c.atomic(BLUE_DROPIN, (directory / 'blue.conf').read_bytes())
    run('systemctl', 'daemon-reload')
    run('systemctl', 'start', BLUE_UNIT)
    try:
        started_blue = blue_status()
        require(started_blue.get('ActiveState') == 'active' and int(started_blue.get('MainPID', '0')) > 1, 'blue unit did not start')
        check_vector('blue', False)
        require(c.status() == green, 'green changed before cutover')
        switch('blue', 'green')
        check_vector('blue', True)
        require(c.status() == green, 'green changed during cutover')
    except Exception:
        if read_active()['color'] == 'blue':
            switch('green', 'blue')
        run('systemctl', 'stop', BLUE_UNIT)
        raise ValueError('blue-green deploy failed; green entrypoint restored')
    return {'state': 'blue_deployed', 'release_id': release_id, 'green_restarted': False}


def rollback(release_id, approval_id):
    directory = c.RELEASES / release_id
    manifest = verify(directory, release_id)
    require(c.baseline_matches(c.status(), manifest), 'green baseline drift')
    gateway = gateway_status()
    require(gateway.get('ActiveState') == 'active' and int(gateway.get('MainPID', '0')) > 1, 'loopback gateway is not active')
    require(read_active()['color'] == 'blue', 'entrypoint is not blue')
    c.check_approval(approval_id, 'rollback-blue-green', expected_fields(manifest, directory))
    c.approval(approval_id, 'rollback-blue-green', expected_fields(manifest, directory))
    switch('green', 'blue')
    check_vector('green', True)
    run('systemctl', 'stop', BLUE_UNIT)
    require(blue_status().get('ActiveState') in ('inactive', 'failed'), 'blue unit did not stop')
    return {'state': 'green_restored', 'release_id': release_id, 'green_restarted': False}


def main():
    require(os.geteuid() == 0, 'root required')
    os.environ['PATH'] = '/usr/sbin:/usr/bin:/sbin:/bin'
    require(len(sys.argv) in (3, 4), 'usage: verify RELEASE | deploy RELEASE APPROVAL | rollback RELEASE APPROVAL')
    action, release_id = sys.argv[1:3]
    require(RID.fullmatch(release_id), 'invalid release ID')
    require(c.CONTROL.is_dir() and c.CONTROL.resolve() == c.CONTROL, 'controller not installed')
    with (c.CONTROL / 'release.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if action == 'verify' and len(sys.argv) == 3:
            result = verify(c.RELEASES / release_id, release_id)
        elif action == 'deploy' and len(sys.argv) == 4:
            result = deploy(release_id, sys.argv[3])
        elif action == 'rollback' and len(sys.argv) == 4:
            result = rollback(release_id, sys.argv[3])
        else:
            raise ValueError('unsupported action')
        print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('BLUE_GREEN_REFUSED: ' + str(error) if isinstance(error, ValueError) else 'BLUE_GREEN_REFUSED: controlled operation failed', file=sys.stderr)
        sys.exit(1)
