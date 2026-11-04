#!/usr/bin/python3 -I
"""Dedicated blue/green executor; never managed by the shared-green controller.

This program is intentionally inert until installed separately by a root-owned
workflow.  It requires a pre-provisioned loopback gateway/switch and refuses
to alter Nginx, GNAS, tenant Bindings, or the shared green unit.
"""
import datetime as dt
import fcntl
import hashlib
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
import urllib.error
import urllib.request

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('controller', HERE / 'mcp_release_controller.py')
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)

BLUE_UNIT = 'wecom-mcp@sharedzoop-blue.service'
BLUE_DROPIN = Path('/etc/systemd/system') / (BLUE_UNIT + '.d/zz-managed-blue-green.conf')
BLUE_UNIT_FILE = Path('/etc/systemd/system') / BLUE_UNIT
BLUE_STATE = c.BASE / 'state/discovery-blue'
BLUE_LOOPBACK = '127.0.0.1:7703'
GATEWAY_UNIT = 'wecom-mcp-loopback-gateway.service'
GATEWAY_UNIT_FILE = Path('/etc/systemd/system') / GATEWAY_UNIT
GATEWAY = Path('/usr/local/sbin/wecom-mcp-loopback-gateway')
EXECUTOR = Path('/usr/local/sbin/mcp-blue-green-executor')
SWITCHER = Path('/usr/local/sbin/wecom-mcp-loopback-switch')
ACTIVE = c.CONTROL / 'entrypoint/active.json'
FILES = ('wecom-mcp-team', 'discovery-policy.json', 'blue.conf', 'plan.json', 'manifest.json')
RID = c.RID
GATEWAY_COLOR_HEADER = 'X-WeCom-MCP-Loopback-Color'

PLAN = {
    'version': 1,
    'green': {'unit': c.UNIT, 'loopback': '127.0.0.1:7702', 'state_root': str(c.STATE)},
    'blue': {'unit': BLUE_UNIT, 'loopback': BLUE_LOOPBACK, 'state_root': str(BLUE_STATE), 'ready_worker_isolation': True, 'kill_mode': 'control-group'},
    'entrypoint': {
        'gateway_unit': GATEWAY_UNIT,
        # 7701 is occupied by an existing production service.  7704 is only
        # a candidate reservation and must still be independently read back.
        'gateway_loopback': '127.0.0.1:7704',
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


def sha_bytes(value):
    return hashlib.sha256(value).hexdigest()


def source_sha(name):
    return sha_bytes((HERE / name).read_bytes())


def active_parent_policy():
    # A directory has no content SHA.  Bind the exact path and its required
    # ownership/mode policy to a digest, then verify that policy at runtime.
    value = {'path': str(ACTIVE.parent), 'uid': 0, 'disallow_mode': 0o022}
    return dict(value, policy_sha256=sha_bytes(canonical(value).encode()))


def control_plane(release_id):
    """Immutable code/unit fingerprints that the receipt and runtime share."""
    return {
        'schema_version': 1,
        'executor': {'path': str(EXECUTOR), 'sha256': source_sha('mcp_blue_green_executor.py')},
        'switcher': {'path': str(SWITCHER), 'sha256': source_sha('wecom-mcp-loopback-switch.py')},
        'gateway': {
            'unit': GATEWAY_UNIT, 'unit_path': str(GATEWAY_UNIT_FILE),
            'unit_sha256': source_sha('wecom-mcp-loopback-gateway.service'),
            'path': str(GATEWAY), 'sha256': source_sha('wecom-mcp-loopback-gateway.py'),
            'loopback': PLAN['entrypoint']['gateway_loopback'],
        },
        'blue': {
            'unit': BLUE_UNIT, 'unit_path': str(BLUE_UNIT_FILE),
            'unit_sha256': source_sha('wecom-mcp@sharedzoop-blue.service'),
            'dropin_path': str(BLUE_DROPIN), 'dropin_sha256': sha_bytes(blue_dropin(release_id)),
        },
        'active_receipt_parent': active_parent_policy(),
    }


def control_plane_sha256(value):
    return sha_bytes(canonical(value).encode())


def validate_control_plane(value, release_id):
    """Validate immutable paths/shape without trusting the local checkout.

    The deployed executor is copied to `/usr/local/sbin`, so it must validate
    hashes against the installed objects, not try to re-open source filenames
    from a developer checkout.  The builder still emits the exact source
    hashes through ``control_plane`` below.
    """
    require(isinstance(value, dict) and value.get('schema_version') == 1
            and set(value) == {'schema_version', 'executor', 'switcher', 'gateway', 'blue', 'active_receipt_parent'},
            'invalid control-plane shape')
    for key, path in (('executor', EXECUTOR), ('switcher', SWITCHER)):
        item = value.get(key)
        require(isinstance(item, dict) and item.get('path') == str(path)
                and c.SHA.fullmatch(item.get('sha256', '')) and set(item) == {'path', 'sha256'},
                'invalid ' + key + ' control-plane binding')
    gateway = value.get('gateway')
    require(isinstance(gateway, dict) and gateway.get('unit') == GATEWAY_UNIT
            and gateway.get('unit_path') == str(GATEWAY_UNIT_FILE)
            and gateway.get('path') == str(GATEWAY)
            and gateway.get('loopback') == PLAN['entrypoint']['gateway_loopback']
            and c.SHA.fullmatch(gateway.get('sha256', ''))
            and c.SHA.fullmatch(gateway.get('unit_sha256', ''))
            and set(gateway) == {'unit', 'unit_path', 'unit_sha256', 'path', 'sha256', 'loopback'},
            'invalid gateway control-plane binding')
    blue = value.get('blue')
    require(isinstance(blue, dict) and blue.get('unit') == BLUE_UNIT
            and blue.get('unit_path') == str(BLUE_UNIT_FILE)
            and blue.get('dropin_path') == str(BLUE_DROPIN)
            and blue.get('dropin_sha256') == sha_bytes(blue_dropin(release_id))
            and c.SHA.fullmatch(blue.get('unit_sha256', ''))
            and set(blue) == {'unit', 'unit_path', 'unit_sha256', 'dropin_path', 'dropin_sha256'},
            'invalid blue control-plane binding')
    require(value.get('active_receipt_parent') == active_parent_policy(),
            'active receipt parent policy mismatch')


def trusted_active_parent(policy):
    expected = active_parent_policy()
    require(policy == expected, 'active receipt parent policy mismatch')
    path = Path(policy['path'])
    value = path.lstat()
    require(path.is_dir() and path.resolve() == path and value.st_uid == policy['uid']
            and not value.st_mode & policy['disallow_mode'], 'untrusted active receipt parent')


def trusted_component(path, digest, label):
    c.regular(path, True)
    require(c.sha(path) == digest, label + ' fingerprint mismatch')


def unit_components(unit, fragment, fragment_sha256, dropins):
    current_fragment = run('systemctl', 'show', unit, '-p', 'FragmentPath', '--value')
    current_dropins = run('systemctl', 'show', unit, '-p', 'DropInPaths', '--value').split()
    require(current_fragment == str(fragment), 'unexpected ' + unit + ' fragment')
    require(current_dropins == [str(path) for path, _ in dropins], 'unexpected ' + unit + ' drop-ins')
    trusted_component(fragment, fragment_sha256, unit + ' fragment')
    for path, digest in dropins:
        trusted_component(path, digest, unit + ' drop-in')


def control_plane_runtime(manifest, phase):
    """Check installed root-owned code and effective systemd topology.

    ``phase`` is deliberately small: before install blue has no drop-in; after
    daemon-reload it must have exactly the immutable release drop-in.  This
    makes an unreviewed systemd override fail closed rather than being silently
    folded into a candidate.
    """
    require(phase in ('prestart', 'started'), 'invalid control-plane phase')
    value = manifest['control_plane']
    validate_control_plane(value, manifest['release_id'])
    require(manifest.get('control_plane_sha256') == control_plane_sha256(value),
            'control-plane manifest mismatch')
    trusted_component(EXECUTOR, value['executor']['sha256'], 'executor')
    trusted_component(SWITCHER, value['switcher']['sha256'], 'switcher')
    trusted_component(GATEWAY, value['gateway']['sha256'], 'gateway')
    unit_components(GATEWAY_UNIT, GATEWAY_UNIT_FILE, value['gateway']['unit_sha256'], [])
    dropins = [] if phase == 'prestart' else [(BLUE_DROPIN, value['blue']['dropin_sha256'])]
    unit_components(BLUE_UNIT, BLUE_UNIT_FILE, value['blue']['unit_sha256'], dropins)
    trusted_active_parent(value['active_receipt_parent'])
    gateway = gateway_status()
    require(gateway.get('ActiveState') == 'active' and int(gateway.get('MainPID', '0')) > 1,
            'loopback gateway is not active')
    return gateway


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
    validate_control_plane(manifest.get('control_plane'), release_id)
    require(manifest.get('control_plane_sha256') == control_plane_sha256(manifest['control_plane']),
            'control-plane manifest mismatch')
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
    trusted_active_parent(active_parent_policy())
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
                gateway_public_color(color, row['host'], 503)
                c.unready(row['host'], loopback=loopback)
            else:
                unavailable_local(row['host'], loopback)
            continue
        require(row['state'] == 'ready', 'unknown fleet state')
        if public:
            gateway_public_color(color, row['host'], 200)
        for path, code in (('/healthz', 200), ('/readyz', 200), ('/mcp', 401), ('/.well-known/oauth-protected-resource/mcp', 200)):
            c.probe(row['host'], path, code, public=public, loopback=loopback)


def gateway_public_color(color, host, expected_code):
    require(color in ('green', 'blue') and host in c.HOSTS and expected_code in (200, 503),
            'invalid gateway color probe')
    # The gateway adds this fixed non-sensitive response header only after it
    # has opened the root-owned receipt for this request.  Without it, status
    # codes alone cannot distinguish a still-direct public route from a real
    # atomic receipt cutover.
    request = urllib.request.Request('https://' + host + '/healthz', method='GET')
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None
    try:
        with urllib.request.build_opener(NoRedirect).open(request, timeout=5) as response:
            require(response.status == expected_code and response.getheader(GATEWAY_COLOR_HEADER) == color,
                    'public gateway receipt color mismatch')
    except urllib.error.HTTPError as error:
        require(error.code == expected_code and error.headers.get(GATEWAY_COLOR_HEADER) == color,
                'public gateway receipt color mismatch')


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
        'control_plane_sha256': manifest['control_plane_sha256'],
    }


def current_dropin():
    if not BLUE_DROPIN.exists():
        return None
    c.regular(BLUE_DROPIN, True)
    return BLUE_DROPIN.read_bytes()


def remove_dropin(expected_digest):
    if not BLUE_DROPIN.exists():
        return
    c.regular(BLUE_DROPIN, True)
    require(c.sha(BLUE_DROPIN) == expected_digest, 'blue drop-in changed concurrently')
    BLUE_DROPIN.unlink()
    descriptor = os.open(str(BLUE_DROPIN.parent), os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def restore_dropin(previous, expected_digest):
    """Restore the exact pre-transaction drop-in after blue has stopped."""
    if previous is None:
        remove_dropin(expected_digest)
    else:
        require(BLUE_DROPIN.exists(), 'blue drop-in disappeared during cleanup')
        c.regular(BLUE_DROPIN, True)
        require(c.sha(BLUE_DROPIN) == expected_digest, 'blue drop-in changed concurrently')
        c.atomic(BLUE_DROPIN, previous)
    run('systemctl', 'daemon-reload')


def verify_green_continuity(manifest, green):
    """Do not stop blue until receipt, green process and public vector agree."""
    require(read_active()['color'] == 'green', 'entrypoint is not green during cleanup')
    control_plane_runtime(manifest, 'started')
    current = c.status()
    require(c.baseline_matches(current, manifest) and current == green,
            'green baseline changed during cleanup')
    check_vector('green', True)


def cleanup_failed_deploy(manifest, green, previous_dropin, start_attempted):
    """Return traffic to independently verified green before touching blue.

    If that verification fails, blue intentionally stays running: receipt
    readback alone is not proof that the external gateway consumed it.
    """
    # A failed atomic write/daemon-reload happens before this executor asks
    # systemd to start blue.  Restore that configuration immediately; there is
    # no blue process to stop and consequently no justification to touch the
    # currently serving green route.
    if not start_attempted:
        try:
            require(read_active()['color'] == 'green', 'entrypoint changed before blue start')
            restore_dropin(previous_dropin, manifest['control_plane']['blue']['dropin_sha256'])
            return
        except Exception as error:
            raise ValueError('blue-green deploy failed before blue start; drop-in cleanup incomplete') from error
    try:
        color = read_active()['color']
        if color == 'blue':
            switch('green', 'blue')
        require(read_active()['color'] == 'green', 'entrypoint did not restore green')
        verify_green_continuity(manifest, green)
    except Exception as error:
        raise ValueError('blue-green deploy failed; green continuity not verified and blue left active') from error
    try:
        if start_attempted:
            run('systemctl', 'stop', BLUE_UNIT)
            require(blue_status().get('ActiveState') in ('inactive', 'failed'), 'blue unit did not stop')
        restore_dropin(previous_dropin, manifest['control_plane']['blue']['dropin_sha256'])
    except Exception as error:
        raise ValueError('blue-green deploy failed; green verified but blue cleanup incomplete') from error


def deploy(release_id, approval_id):
    directory = c.RELEASES / release_id
    manifest = verify(directory, release_id)
    green = c.status()
    require(c.baseline_matches(green, manifest), 'green baseline drift')
    c.check_gnas(manifest)
    initial_blue = blue_status()
    require(initial_blue.get('ActiveState') in ('inactive', 'failed') and initial_blue.get('MainPID') in ('0', ''), 'blue unit already active')
    control_plane_runtime(manifest, 'prestart')
    require(read_active()['color'] == 'green', 'entrypoint is not green')
    trusted_directory(BLUE_DROPIN.parent)
    require(not BLUE_DROPIN.exists() and not BLUE_DROPIN.is_symlink(),
            'blue managed drop-in already present')
    c.check_approval(approval_id, 'deploy-blue-green', expected_fields(manifest, directory))
    require(BLUE_STATE.is_dir() and BLUE_STATE.resolve() == BLUE_STATE, 'blue state root not provisioned')
    previous_dropin = current_dropin()
    c.approval(approval_id, 'deploy-blue-green', expected_fields(manifest, directory))
    start_attempted = False
    try:
        c.atomic(BLUE_DROPIN, (directory / 'blue.conf').read_bytes())
        run('systemctl', 'daemon-reload')
        control_plane_runtime(manifest, 'started')
        start_attempted = True
        run('systemctl', 'start', BLUE_UNIT)
        started_blue = blue_status()
        require(started_blue.get('ActiveState') == 'active' and int(started_blue.get('MainPID', '0')) > 1, 'blue unit did not start')
        check_vector('blue', False)
        require(c.status() == green, 'green changed before cutover')
        switch('blue', 'green')
        check_vector('blue', True)
        require(c.status() == green, 'green changed during cutover')
    except Exception as error:
        try:
            cleanup_failed_deploy(manifest, green, previous_dropin, start_attempted)
        except Exception as cleanup_error:
            raise cleanup_error from error
        raise ValueError('blue-green deploy failed; green continuity restored and blue cleanup completed') from error
    return {'state': 'blue_deployed', 'release_id': release_id, 'green_restarted': False}


def rollback(release_id, approval_id):
    directory = c.RELEASES / release_id
    manifest = verify(directory, release_id)
    green = c.status()
    require(c.baseline_matches(green, manifest), 'green baseline drift')
    control_plane_runtime(manifest, 'started')
    require(read_active()['color'] == 'blue', 'entrypoint is not blue')
    c.check_approval(approval_id, 'rollback-blue-green', expected_fields(manifest, directory))
    c.approval(approval_id, 'rollback-blue-green', expected_fields(manifest, directory))
    switch('green', 'blue')
    verify_green_continuity(manifest, green)
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
