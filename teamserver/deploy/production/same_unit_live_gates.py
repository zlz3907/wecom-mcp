"""Read-only production gates for the reviewed MCP open-bootstrap package."""
import hashlib
import http.client
import json
import os
from pathlib import Path
import stat

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


def public_probe():
    results = {}
    for host, expected in PUBLIC.items():
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
    public_probe()
