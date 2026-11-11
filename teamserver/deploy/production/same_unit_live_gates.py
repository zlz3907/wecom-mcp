"""Read-only production gates for the reviewed MCP open-bootstrap package."""
import hashlib
import http.client
import json
import os
from pathlib import Path
import stat
import subprocess

import same_unit_plan as contract
from same_unit_systemd import digest

MANIFEST_SHA = '736e98c2ccf23c35c54d887d0066e8a1de57af3b0487bd34ebe12f498636f68f'
EXPECTED_FILES = {'wecom-mcp-team', 'discovery-policy.json', 'target.service.conf',
                  'shared.service.conf', 'ci-evidence.json', 'manifest.json', 'SHA256SUMS'}
PUBLIC = {'zhycit.com': (401, 200), 'mcp.wesiyu.com': (401, 200),
          'mcp.rtyouth.com': (401, 200), 'mcp.jianpinke.com': (503, 503)}


def candidate(plan, owner=0):
    binary = contract.validate(plan)
    directory = binary.parent
    p = directory
    while True:
        info = p.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            raise ValueError('untrusted candidate directory')
        if p == Path('/'):
            break
        p = p.parent
    if stat.S_IMODE(directory.stat().st_mode) != 0o555 or {p.name for p in directory.iterdir()} != EXPECTED_FILES:
        raise ValueError('candidate directory changed')
    data = {}
    for name in EXPECTED_FILES:
        path = directory / name
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
        with os.fdopen(fd, 'rb') as stream:
            info, named = os.fstat(stream.fileno()), path.lstat()
            mode = 0o555 if name == 'wecom-mcp-team' else 0o444
            if (not stat.S_ISREG(info.st_mode) or info.st_uid != owner or stat.S_IMODE(info.st_mode) != mode or
                    (info.st_dev, info.st_ino) != (named.st_dev, named.st_ino)):
                raise ValueError('candidate file changed')
            content = stream.read(80 * 1024 * 1024 + 1)
            if len(content) > 80 * 1024 * 1024:
                raise ValueError('candidate file too large')
            data[name] = content
    sha = lambda name: hashlib.sha256(data[name]).hexdigest()
    expected = {'wecom-mcp-team': contract.BINARY_SHA, 'discovery-policy.json': contract.POLICY_SHA,
                'target.service.conf': contract.PROFILES['target']['dropin_sha256'],
                'shared.service.conf': contract.PROFILES['shared']['dropin_sha256'],
                'ci-evidence.json': contract.CI_EVIDENCE_SHA}
    if sha('manifest.json') != MANIFEST_SHA or any(sha(name) != value for name, value in expected.items()):
        raise ValueError('candidate digest mismatch')
    sums = ''.join(sha(name) + '  ' + name + '\n' for name in sorted(EXPECTED_FILES - {'SHA256SUMS'})).encode()
    manifest = json.loads(data['manifest.json'])
    if data['SHA256SUMS'] != sums or manifest.get('files') != expected or manifest.get('release_id') != contract.RELEASE_ID:
        raise ValueError('candidate manifest mismatch')
    return data[plan['profile'] + '.service.conf']


def baseline(plan, unit):
    props = unit.show()
    unit.verify_fragment(props)
    if (props['ActiveState'] != 'active' or props['SubState'] != 'running' or props['UnitFileState'] != 'enabled' or
            props['DropInPaths'] or props['Job'] or props['NRestarts'] != '0' or
            not props['MainPID'].isdigit() or int(props['MainPID']) < 2):
        raise ValueError('old MCP unit unavailable or changed')
    proc = Path('/proc') / props['MainPID'] / 'exe'
    if digest(proc) != plan['previous_binary_sha256'] or not unit.listener_belongs_to_main(props['MainPID']):
        raise ValueError('old MCP process or listener changed')
    for name, expected in plan['environment_file_sha256'].items():
        path = Path(name); info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022 or digest(path) != expected:
            raise ValueError('protected MCP environment changed')
    if unit.show() != props or digest(proc) != plan['previous_binary_sha256']:
        raise ValueError('old MCP process changed during inspection')
    return {'pid': int(props['MainPID']), 'binary_sha256': plan['previous_binary_sha256']}


def public_probe(hosts=None):
    results = {}
    hosts = tuple(PUBLIC) if hosts is None else tuple(hosts)
    if not hosts or len(set(hosts)) != len(hosts) or any(host not in PUBLIC for host in hosts):
        raise ValueError('invalid public probe hosts')
    for host in hosts:
        expected = PUBLIC[host]
        observed = []
        for path in ('/mcp', '/.well-known/oauth-protected-resource/mcp',
                     '/.well-known/oauth-authorization-server/gnas/oauth'):
            if host == 'mcp.jianpinke.com' and path.endswith('/gnas/oauth'):
                continue
            connection = http.client.HTTPSConnection(host, timeout=8)
            try:
                connection.request('GET', path, headers={'Accept': 'application/json'})
                response = connection.getresponse()
                content = response.read(65537)
                if len(content) > 65536:
                    raise ValueError('public response too large: ' + host)
                observed.append(response.status)
                if host != 'mcp.jianpinke.com' and path != '/mcp':
                    body = json.loads(content)
                    issuer = 'https://' + host + '/gnas/oauth'
                    if path.endswith('/mcp'):
                        if body.get('resource') != 'https://' + host + '/mcp' or body.get('authorization_servers') != [issuer]:
                            raise ValueError('public resource metadata drift: ' + host)
                    elif body.get('issuer') != issuer:
                        raise ValueError('public authorization issuer drift: ' + host)
            finally:
                connection.close()
        if tuple(observed[:2]) != expected or (host != 'mcp.jianpinke.com' and observed[2] != 200):
            raise ValueError('public endpoint drift: ' + host)
        results[host] = observed
    return results


def health_probe(plan):
    host = 'zhycit.com' if plan['profile'] == 'target' else 'mcp.wesiyu.com'
    connection = http.client.HTTPConnection('127.0.0.1', plan['port'], timeout=8)
    try:
        connection.request('GET', '/healthz', headers={'Host': host})
        response = connection.getresponse()
        response.read(65537)
        if response.status != 200:
            raise ValueError('candidate health check failed')
    finally:
        connection.close()
    # The isolated target is independently routed. A shared unit serves more
    # than one tenant, so an individual tenant outage must not close it for all.
    if plan['profile'] == 'target':
        public_probe(('zhycit.com',))


def compare_fleet_result(raw, expected, required_hosts=None):
    if not isinstance(raw, bytes) or len(raw) > 65536:
        raise ValueError('live tenant fleet changed')
    actual = json.loads(raw)
    if required_hosts is None:
        if actual != expected:
            raise ValueError('live tenant fleet changed')
        return
    def select(value):
        rows = value.get('bindings') if isinstance(value, dict) else None
        if not isinstance(rows, list) or value.get('mode') != 'read-only-gnas-discovery':
            raise ValueError('live tenant fleet changed')
        selected = [row for row in rows if isinstance(row, dict) and row.get('host') in required_hosts]
        if len(selected) != len(required_hosts) or {r.get('host') for r in selected} != set(required_hosts):
            raise ValueError('live tenant fleet changed')
        return sorted(selected, key=lambda row: row['host'])
    if not required_hosts or select(actual) != select(expected):
        raise ValueError('live tenant fleet changed')


def fleet_probe(expected_result, required_hosts=None, probe=Path('/usr/local/lib/wecom-mcp-open-bootstrap/fleet-readonly-probe'),
                expected_sha='0f427eef348b5e34fdb62fe476213cf9770ccda8a37443d1f82a7e93fc8279d4',
                owner=0, trusted_root=Path('/'), runner=subprocess.run):
    """Repeat the pinned, read-only GNAS discovery probe under the MCP UID."""
    import secrets
    probe = Path(probe)
    info = probe.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != owner or stat.S_IMODE(info.st_mode) != 0o555 or
            digest(probe) != expected_sha):
        raise ValueError('fleet discovery probe changed')
    directory = probe.parent
    while True:
        info = directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            raise ValueError('untrusted fleet probe directory')
        if directory == trusted_root:
            break
        if directory == directory.parent:
            raise ValueError('fleet probe outside trusted root')
        directory = directory.parent
    unit = 'mcp-fleet-guard-' + secrets.token_hex(12) + '.service'
    args = ['/usr/bin/systemd-run', '--quiet', '--wait', '--pipe', '--collect', '--unit=' + unit,
            '--property=Type=oneshot', '--property=RuntimeMaxSec=65', '--property=TimeoutStartSec=65',
            '--property=Restart=no', '--property=KillMode=control-group',
            '--property=User=wecom-mcp-gmzoop', '--property=Group=wecom-mcp-gmzoop',
            '--property=ProtectSystem=strict', '--property=ProtectHome=read-only',
            '--property=NoNewPrivileges=yes', '--property=PrivateTmp=yes',
            '--property=EnvironmentFile=/etc/wecom-mcp/gmzoop.env', str(probe)]
    try:
        result = runner(args, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, check=True, timeout=75)
        compare_fleet_result(result.stdout, expected_result, required_hosts)
    finally:
        # A systemd-run client timeout does not stop the oneshot by itself.
        runner(['/usr/bin/systemctl', 'stop', unit], stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL, timeout=45)
        import time
        deadline = time.monotonic() + 15
        while True:
            state = runner(['/usr/bin/systemctl', 'show', unit, '-p', 'LoadState', '--value'],
                           check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10)
            if state.stdout.decode().strip() == 'not-found':
                break
            if time.monotonic() >= deadline:
                raise ValueError('fleet probe unit did not disappear')
            time.sleep(0.25)
