#!/usr/bin/python3 -I
"""Root-only, fixed-scope MCP promotion. Never handles credentials or business data."""
import datetime as dt
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import time
import urllib.request
import urllib.error

ENVIRONMENT = 'zhycit-prod-01/wecom-mcp-gmzoop'
UNIT = 'wecom-mcp@gmzoop.service'
BASE = Path('/home/product/services/mcp/wecom')
RELEASES = BASE / 'releases'
INCOMING = Path('/var/lib/wecom-mcp-release/incoming')
CONTROL = Path('/var/lib/wecom-mcp-release')
APPROVALS = Path('/etc/wecom-mcp/release-approvals')
DROPIN = Path('/etc/systemd/system/wecom-mcp@gmzoop.service.d/zz-managed-release.conf')
RUNTIME = BASE / 'instances/gmzoop/config/fleet-runtime-20260916.json'
STATE = BASE / 'state/discovery'
HOSTS = ('mcp.wesiyu.com', 'mcp.jianpinke.com')
FILES = ('wecom-mcp-team', 'discovery-policy.json', 'service.conf', 'manifest.json')
RID = re.compile(r'^\d{8}T\d{6}Z-[a-f0-9]{12}$')
SHA = re.compile(r'^[a-f0-9]{64}$')
APR = re.compile(r'^APR-\d{8}T\d{6}Z-[A-Za-z0-9._-]{6,64}$')


def require(ok, message):
    if not ok:
        raise ValueError(message)


def sha(path):
    with path.open('rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest() if hasattr(hashlib, 'file_digest') else stream_hash(f)


def stream_hash(f):
    h = hashlib.sha256()
    for block in iter(lambda: f.read(1 << 20), b''):
        h.update(block)
    return h.hexdigest()


def regular(path, root=False):
    s = path.lstat()
    require(stat.S_ISREG(s.st_mode) and path.resolve() == path, 'regular canonical file required')
    if root:
        require(s.st_uid == 0 and not s.st_mode & 0o022, 'root ownership and non-writable trust boundary required')
    return s


def read_json(path, root=False):
    require(regular(path, root).st_size <= 65536, 'JSON too large')
    with path.open() as f:
        return json.load(f)


def run(*args):
    result = subprocess.run(args, capture_output=True, text=True, timeout=45)
    require(result.returncode == 0, 'controlled command failed: ' + args[0])
    return result.stdout.strip()


def unit_fingerprint():
    paths = [run('systemctl', 'show', UNIT, '-p', 'FragmentPath', '--value')]
    paths += run('systemctl', 'show', UNIT, '-p', 'DropInPaths', '--value').split()
    pairs = []
    for name in sorted(paths):
        p = Path(name)
        regular(p, True)
        pairs.append([name, sha(p)])
    return hashlib.sha256(json.dumps(pairs, separators=(',', ':')).encode()).hexdigest()


def runtime_fingerprint():
    # These files contain references/capability policy, never environment secrets.
    manifest = read_json(RUNTIME)
    require(manifest.get('version') == 1 and isinstance(manifest.get('bindings'), list) and 0 < len(manifest['bindings']) <= 64, 'invalid protected runtime mapping')
    pairs = [[str(RUNTIME), sha(RUNTIME)]]
    seen = set()
    for binding in manifest['bindings']:
        path = Path(binding.get('instance_config_path', ''))
        require(path.is_absolute() and path.suffix == '.json' and str(path).startswith(str(BASE / 'instances') + '/') and path not in seen, 'invalid protected instance reference')
        data = read_json(path)
        require(data.get('version') == 1 and data.get('tenant_route') and data.get('registry_document_id'), 'invalid protected static instance')
        seen.add(path)
        pairs.append([str(path), sha(path)])
    return hashlib.sha256(json.dumps(sorted(pairs), separators=(',', ':')).encode()).hexdigest()


def status():
    pid = int(run('systemctl', 'show', UNIT, '-p', 'MainPID', '--value'))
    active = run('systemctl', 'show', UNIT, '-p', 'ActiveState', '--value')
    verified = pid > 1 and Path('/proc/%d/exe' % pid).exists()
    if verified:
        executable = Path('/proc/%d/exe' % pid).resolve()
    else:
        configured = run('systemctl', 'show', UNIT, '-p', 'ExecStart', '--value')
        paths = re.findall(r'path=([^ ;]+)', configured)
        require(len(paths) == 1, 'configured runtime is ambiguous')
        executable = Path(paths[0])
    regular(executable, True)
    require(executable.parent.parent == RELEASES and executable.name == 'wecom-mcp-team', 'unexpected runtime path')
    return {'environment': ENVIRONMENT, 'runtime_path': str(executable), 'binary_sha256': sha(executable),
            'unit_fingerprint': unit_fingerprint(), 'runtime_config_fingerprint': runtime_fingerprint(), 'active': active, 'runtime_verified': verified,
            'restarts': int(run('systemctl', 'show', UNIT, '-p', 'NRestarts', '--value'))}


def dropin(release_id):
    require(RID.fullmatch(release_id), 'invalid release ID')
    args = '%s --gnas-fleet-runtime %s --gnas-discovery-policy %s --gnas-state-root %s --listen 127.0.0.1:7702' % (
        RELEASES / release_id / 'wecom-mcp-team', RUNTIME, RELEASES / release_id / 'discovery-policy.json', STATE)
    return ('[Service]\nExecStartPre=\nExecStart=\nExecStartPre=' + args + ' --check-config\nExecStart=' + args +
            ' --gnas-fleet-refresh 30s\nReadWritePaths=' + str(STATE) + '\n').encode()


def verify(directory, release_id):
    require(RID.fullmatch(release_id), 'invalid release ID')
    require(directory.is_dir() and directory.resolve() == directory and not directory.is_symlink(), 'invalid candidate directory')
    require({p.name for p in directory.iterdir()} == set(FILES) | {'SHA256SUMS'}, 'unexpected candidate contents')
    regular(directory / 'SHA256SUMS', True)
    require((directory / 'SHA256SUMS').read_text() == ''.join(sha(directory / name) + '  ' + name + '\n' for name in sorted(FILES)), 'candidate checksum mismatch')
    m = read_json(directory / 'manifest.json', True)
    require(m.get('schema_version') == 1 and m.get('environment') == ENVIRONMENT and m.get('release_id') == release_id, 'manifest identity mismatch')
    require(re.fullmatch(r'[a-f0-9]{40}', m.get('git_commit', '')) and re.fullmatch(r'[a-f0-9]{40}', m.get('git_tree', '')), 'invalid source identity')
    require(m.get('source_clean') is True and m.get('target') == 'linux/amd64', 'invalid build provenance')
    require(set(m.get('files', {})) == set(FILES[:-1]), 'unexpected artifact list')
    for name, digest in m['files'].items():
        regular(directory / name, True)
        require(SHA.fullmatch(digest) and sha(directory / name) == digest, 'artifact digest mismatch')
    require((directory / 'service.conf').read_bytes() == dropin(release_id), 'unexpected service arguments')
    policy = read_json(directory / 'discovery-policy.json', True)
    require(set(policy) == {'version', 'api_whitelist'} and policy['version'] == 1, 'unexpected discovery policy')
    require(m.get('expected_runtime_path', '').startswith(str(RELEASES) + '/') and SHA.fullmatch(m.get('expected_binary_sha256', '')) and SHA.fullmatch(m.get('expected_unit_fingerprint', '')) and SHA.fullmatch(m.get('expected_runtime_config_fingerprint', '')), 'rollback baseline missing')
    return m


def baseline_matches(current, expected):
    return current.get('runtime_verified') is True and current['active'] == 'active' and current['restarts'] == 0 and all(current[key] == expected['expected_' + key] for key in ('runtime_path', 'binary_sha256', 'unit_fingerprint', 'runtime_config_fingerprint'))


def atomic(path, data, mode=0o644):
    temporary = path.with_name(path.name + '.new')
    with temporary.open('xb') as f:
        os.chmod(temporary, mode)
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
    os.replace(temporary, path)
    fd = os.open(str(path.parent), os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def check_approval(approval_id, action, expected):
    require(APR.fullmatch(approval_id), 'invalid approval ID')
    receipt = read_json(APPROVALS / (approval_id + '.json'), True)
    require(not (APPROVALS / (approval_id + '.json')).stat().st_mode & 0o222, 'approval must be read-only')
    require(receipt.get('approval_id') == approval_id and receipt.get('action') == action and receipt.get('environment') == ENVIRONMENT, 'approval scope mismatch')
    require(receipt.get('approved_by') and receipt.get('schema_version') == 1, 'approval authority missing')
    now = dt.datetime.now(dt.timezone.utc)
    start = dt.datetime.fromisoformat(receipt['approved_at'].replace('Z', '+00:00'))
    end = dt.datetime.fromisoformat(receipt['expires_at'].replace('Z', '+00:00'))
    require(start <= now < end and dt.timedelta(0) < end - start <= dt.timedelta(hours=4), 'approval expired or invalid')
    require(set(receipt) == set(expected) | {'schema_version', 'environment', 'approval_id', 'action', 'approved_by', 'approved_at', 'expires_at'}, 'unexpected approval fields')
    require(all(receipt.get(k) == v for k, v in expected.items()), 'approval binding mismatch')
    return receipt


def approval(approval_id, action, expected):
    receipt = check_approval(approval_id, action, expected)
    marker = CONTROL / 'used' / approval_id
    with marker.open('xb') as f:
        os.chmod(marker, 0o400)
        f.write(b'consumed\n')
        f.flush()
        os.fsync(f.fileno())
    fd = os.open(str(marker.parent), os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)
    return receipt


def probe(host, path, expected, public=False):
    if public:
        request = urllib.request.Request('https://' + host + path, method='GET')
        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *args, **kwargs):
                return None
        try:
            with urllib.request.build_opener(NoRedirect).open(request, timeout=5) as response:
                code = response.status
        except urllib.error.HTTPError as error:
            code = error.code
    else:
        conn = http.client.HTTPConnection('127.0.0.1', 7702, timeout=5)
        try:
            conn.request('GET', path, headers={'Host': host})
            response = conn.getresponse()
            code = response.status
        finally:
            conn.close()
    require(code == expected, 'health/auth contract failed')


def healthy(path, digest, hosts, legacy=False):
    current = status()
    require(current.get('runtime_verified') is True and current['active'] == 'active' and current['restarts'] == 0 and current['runtime_path'] == path and current['binary_sha256'] == digest, 'runtime mismatch or restart')
    for host in hosts:
        for endpoint, code in (('/healthz', 200), ('/readyz', 200), ('/mcp', 401), ('/.well-known/oauth-protected-resource' if legacy else '/.well-known/oauth-protected-resource/mcp', 200)):
            probe(host, endpoint, code)
        for endpoint, code in (('/healthz', 200), ('/readyz', 200), ('/mcp', 401)):
            probe(host, endpoint, code, True)


def restart_and_verify(path, digest, hosts, legacy=False, expected_config_fingerprint=None):
    run('systemctl', 'daemon-reload')
    fingerprint = unit_fingerprint()
    config_fingerprint = expected_config_fingerprint or runtime_fingerprint()
    require(runtime_fingerprint() == config_fingerprint, 'approved runtime configuration drift before restart')
    run('systemctl', 'restart', UNIT)
    deadline = time.monotonic() + 45
    while True:
        current = status()
        require(current['runtime_path'] == path and current['binary_sha256'] == digest and current['restarts'] == 0 and current['unit_fingerprint'] == fingerprint and current['runtime_config_fingerprint'] == config_fingerprint, 'unexpected runtime/configuration or restart during startup')
        try:
            healthy(path, digest, hosts, legacy)
            return
        except (OSError, ValueError, urllib.error.URLError):
            if time.monotonic() >= deadline:
                raise ValueError('startup readiness deadline exceeded')
            time.sleep(1)


def check_gnas(m):
    raw = run('sudo', '/usr/local/sbin/gnas-release-controller', 'status')
    values = dict(line.split('=', 1) for line in raw.splitlines() if '=' in line)
    require(values.get('release_id') == m['expected_gnas_release_id'] and values.get('runtime_sha256') == m['expected_gnas_binary_sha256'] and values.get('service_state') == 'active' and values.get('restarts') == '0', 'GNAS dependency not at approved release')
    # The independently approved GNAS configuration workflow issues this
    # non-secret attestation after enabling refresh and verifying real contracts.
    # Bind it to the actual process so a restart invalidates the evidence.
    ready = read_json(Path('/etc/wecom-mcp/gnas-discovery-readiness.json'), True)
    require(ready.get('release_id') == values['release_id'] and ready.get('binary_sha256') == values['runtime_sha256'] and str(ready.get('main_pid')) == values['main_pid'] and ready.get('refresh_enabled') is True and ready.get('contracts_verified') is True, 'GNAS enablement/contract attestation mismatch')
    verified_at = dt.datetime.fromisoformat(ready['verified_at'].replace('Z', '+00:00'))
    age = dt.datetime.now(dt.timezone.utc) - verified_at
    require(dt.timedelta(0) <= age <= dt.timedelta(hours=4), 'GNAS readiness evidence expired')
    for host in HOSTS:
        probe(host, '/.well-known/oauth-authorization-server/gnas/oauth', 200, True)


def stage(release_id):
    source, dest = INCOMING / release_id, RELEASES / release_id
    m = verify(source, release_id)
    require(baseline_matches(status(), m), 'live baseline drift')
    require(not dest.exists(), 'immutable release already exists')
    dest.mkdir(mode=0o750)
    for name in FILES + ('SHA256SUMS',):
        shutil.copyfile(source / name, dest / name)
        os.chmod(dest / name, 0o555 if name == 'wecom-mcp-team' else 0o444)
    os.chmod(dest, 0o555)
    verify(dest, release_id)
    return {'state': 'staged', 'release_id': release_id, 'service_restarted': False}


def preflight_rollback(release_id):
    m = verify(RELEASES / release_id, release_id)
    require(baseline_matches(status(), m), 'live baseline drift before rollback preflight')
    # systemd loads the existing protected environment. Neither Python nor the
    # caller reads it. The old binary exits before Listen or any business write.
    run('systemd-run', '--quiet', '--wait', '--collect',
        '--unit=wecom-mcp-rollback-check-' + release_id,
        '--property=Type=oneshot', '--property=RuntimeMaxSec=35',
        '--property=TimeoutStartSec=35', '--property=Restart=no',
        '--property=User=wecom-mcp-gmzoop', '--property=Group=wecom-mcp-gmzoop',
        '--property=EnvironmentFile=/etc/wecom-mcp/gmzoop.env',
        '--property=ProtectSystem=strict', '--property=ProtectHome=read-only',
        '--property=NoNewPrivileges=yes', '--property=PrivateTmp=yes',
        '--property=ReadWritePaths=' + str(BASE / 'instances/gmzoop/data'),
        '--property=StandardOutput=null', '--property=StandardError=null',
        m['expected_runtime_path'], '--gnas-fleet-runtime', str(RUNTIME), '--check-config')
    require(baseline_matches(status(), m), 'live baseline drift during rollback preflight')
    return {'state': 'rollback_preflight_passed', 'release_id': release_id, 'service_restarted': False}


def deploy(release_id, approval_id):
    directory = RELEASES / release_id
    m = verify(directory, release_id)
    require(baseline_matches(status(), m), 'live baseline drift')
    check_gnas(m)
    previous = Path(m['expected_runtime_path'])
    regular(previous, True)
    require(sha(previous) == m['expected_binary_sha256'], 'rollback binary changed')
    # Prove existing host's baseline before touching the effective unit.
    healthy(str(previous), m['expected_binary_sha256'], HOSTS[:1], True)
    require(STATE.is_dir() and STATE.resolve() == STATE, 'state root not provisioned')
    expected = {k: m[k] for k in ('release_id', 'expected_runtime_path', 'expected_binary_sha256', 'expected_unit_fingerprint', 'expected_runtime_config_fingerprint', 'expected_gnas_release_id', 'expected_gnas_binary_sha256')}
    expected.update(binary_sha256=m['files']['wecom-mcp-team'], manifest_sha256=sha(directory / 'manifest.json'))
    check_approval(approval_id, 'deploy', expected)
    preflight_rollback(release_id)
    require(baseline_matches(status(), m), 'live baseline drift before approval consumption')
    approval(approval_id, 'deploy', expected)
    # Snapshot only the managed override; old protected env/drop-ins stay in place.
    rollback = CONTROL / 'rollback' / release_id
    rollback.mkdir(mode=0o700)
    old = DROPIN.read_bytes() if DROPIN.exists() else None
    if old is not None:
        regular(DROPIN, True)
        (rollback / 'service.conf').write_bytes(old)
        os.chmod(rollback / 'service.conf', 0o400)
    record = {'runtime_path': str(previous), 'binary_sha256': m['expected_binary_sha256'], 'unit_fingerprint': m['expected_unit_fingerprint'], 'managed_override_present': old is not None}
    atomic(rollback / 'baseline.json', json.dumps(record).encode(), 0o400)
    require(baseline_matches(status(), m), 'live baseline drift before switch')
    try:
        atomic(DROPIN, (directory / 'service.conf').read_bytes())
        restart_and_verify(str(directory / 'wecom-mcp-team'), m['files']['wecom-mcp-team'], HOSTS, expected_config_fingerprint=m['expected_runtime_config_fingerprint'])
    except Exception:
        restore(release_id)
        raise ValueError('deploy failed; approved automatic rollback completed')
    atomic(rollback / 'deployed.json', json.dumps({'unit_fingerprint': unit_fingerprint(), 'runtime_config_fingerprint': runtime_fingerprint()}).encode(), 0o400)
    return {'state': 'deployed', 'release_id': release_id, 'observation_complete': False}


def restore(release_id):
    directory = CONTROL / 'rollback' / release_id
    baseline = read_json(directory / 'baseline.json', True)
    target = Path(baseline['runtime_path'])
    regular(target, True)
    require(sha(target) == baseline['binary_sha256'], 'rollback binary mismatch')
    if baseline['managed_override_present']:
        regular(directory / 'service.conf', True)
        atomic(DROPIN, (directory / 'service.conf').read_bytes())
    elif DROPIN.exists():
        DROPIN.unlink()
    run('systemctl', 'daemon-reload')
    require(unit_fingerprint() == baseline['unit_fingerprint'], 'rollback unit fingerprint mismatch')
    restart_and_verify(str(target), baseline['binary_sha256'], HOSTS[:1], True)


def rollback(release_id, approval_id):
    m = verify(RELEASES / release_id, release_id)
    regular(DROPIN, True)
    require(sha(DROPIN) == m['files']['service.conf'], 'managed override is not the rollback source')
    current = status()
    require(current['runtime_path'] == str(RELEASES / release_id / 'wecom-mcp-team') and current['binary_sha256'] == m['files']['wecom-mcp-team'], 'rollback source drift')
    saved = read_json(CONTROL / 'rollback' / release_id / 'baseline.json', True)
    expected = {'release_id': release_id, 'binary_sha256': current['binary_sha256'], 'expected_unit_fingerprint': current['unit_fingerprint'], 'rollback_runtime_path': saved['runtime_path'], 'rollback_binary_sha256': saved['binary_sha256']}
    approval(approval_id, 'rollback', expected)
    restore(release_id)
    return {'state': 'rolled_back', 'release_id': release_id}


def observe(release_id):
    m = verify(RELEASES / release_id, release_id)
    recorded = read_json(CONTROL / 'rollback' / release_id / 'deployed.json', True)
    for index in range(11):
        require(unit_fingerprint() == recorded['unit_fingerprint'] and runtime_fingerprint() == recorded['runtime_config_fingerprint'], 'configuration drift during observation')
        healthy(str(RELEASES / release_id / 'wecom-mcp-team'), m['files']['wecom-mcp-team'], HOSTS)
        print(json.dumps({'state': 'observing', 'sample': index + 1, 'total': 11}), flush=True)
        if index < 10:
            time.sleep(30)
    return {'state': 'observed', 'release_id': release_id, 'seconds': 300, 'owner_accepted': False}


def main():
    require(os.geteuid() == 0, 'root required; no deployer approval writes')
    os.environ['PATH'] = '/usr/sbin:/usr/bin:/sbin:/bin'
    require(len(sys.argv) >= 2, 'action required')
    action, args = sys.argv[1], sys.argv[2:]
    require(CONTROL.is_dir() and CONTROL.resolve() == CONTROL and CONTROL.stat().st_uid == 0 and not CONTROL.stat().st_mode & 0o022, 'controller not installed')
    with (CONTROL / 'release.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if action == 'status' and not args:
            result = status()
        elif action in ('verify', 'stage', 'observe', 'preflight-rollback') and len(args) == 1:
            require(RID.fullmatch(args[0]), 'invalid release ID')
            handlers = {'verify': lambda rid: verify(RELEASES / rid, rid), 'stage': stage, 'observe': observe, 'preflight-rollback': preflight_rollback}
            result = handlers[action](args[0])
        elif action in ('deploy', 'rollback') and len(args) == 2:
            require(RID.fullmatch(args[0]), 'invalid release ID')
            result = deploy(*args) if action == 'deploy' else rollback(*args)
        else:
            raise ValueError('unsupported action or arguments')
        if action != 'status':
            with (CONTROL / 'events.jsonl').open('a') as events:
                events.write(json.dumps({'at': dt.datetime.now(dt.timezone.utc).isoformat(), 'action': action, 'release_id': args[0], 'result': result.get('state', 'verified')}) + '\n')
                events.flush()
                os.fsync(events.fileno())
        print(json.dumps(result, sort_keys=True))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # Errors never include upstream bodies, environment or subprocess output.
        print('MCP_RELEASE_REFUSED: ' + str(error) if isinstance(error, ValueError) else 'MCP_RELEASE_REFUSED: controlled operation failed', file=sys.stderr)
        sys.exit(1)
