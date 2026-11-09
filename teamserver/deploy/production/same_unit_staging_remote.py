#!/usr/bin/python3 -I
"""Stage one exact immutable MCP package; never restart or route services."""
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tarfile

RID = '20261108T194434Z-dbef937502fa'
MANIFEST_SHA = '5fe3b370d1e0595031869e0b468820aac422be6fa300219164fd73d3515d2a18'
NAMES = {'wecom-mcp-team', 'discovery-policy.json', 'target.service.conf',
         'shared.service.conf', 'ci-evidence.json', 'manifest.json', 'SHA256SUMS'}
ROOT = Path('/var/lib/wecom-mcp-isolated/releases')


def snapshot():
    expected = {'gnas-server.service': 'a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e',
                'wecom-mcp-zhycit-migration.service': '82fd10c16f5ccf72219818a9efc00e0b79f4e70a8726faf071059b6a6344fa03',
                'wecom-mcp-discovery-21462d2.service': '988055b38a6963afe1f6cfe3b2bdb9b691483cb599974a450efae535d356ad22'}
    result = {}
    for unit, digest in expected.items():
        raw = subprocess.run(['/usr/bin/systemctl', 'show', unit, '-p', 'MainPID', '-p', 'ActiveState', '-p', 'NRestarts'],
                             check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10).stdout.decode()
        props = dict(line.split('=', 1) for line in raw.splitlines() if '=' in line)
        pid = int(props.get('MainPID', '0'))
        if props.get('ActiveState') != 'active' or props.get('NRestarts') != '0' or pid <= 1:
            raise ValueError('live service is not stable')
        sha = hashlib.sha256((Path('/proc') / str(pid) / 'exe').read_bytes()).hexdigest()
        if sha != digest:
            raise ValueError('live service baseline changed')
        result[unit] = {'pid': pid, 'sha256': sha}
    return result


def trusted_ancestors(path, trusted_root, owner):
    current = path
    while True:
        info = current.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            raise ValueError('untrusted release directory')
        if current == trusted_root:
            return
        if current == current.parent:
            raise ValueError('release directory outside trusted root')
        current = current.parent


def stage(stream, root=ROOT, trusted_root=Path('/'), owner=0, observe=snapshot):
    if not root.is_absolute() or root.resolve() != root:
        raise ValueError('release root is not canonical')
    trusted_ancestors(root, trusted_root, owner)
    before = observe()
    files = {}
    with tarfile.open(fileobj=stream, mode='r|*') as archive:
        for member in archive:
            if member.name not in NAMES or member.name in files or not member.isfile() or not 0 <= member.size <= 80 * 1024 * 1024:
                raise ValueError('unexpected archive member')
            source = archive.extractfile(member)
            if source is None:
                raise ValueError('unreadable archive member')
            files[member.name] = source.read(member.size + 1)
            if len(files[member.name]) != member.size:
                raise ValueError('incomplete archive member')
    if set(files) != NAMES or hashlib.sha256(files['manifest.json']).hexdigest() != MANIFEST_SHA:
        raise ValueError('candidate manifest mismatch')
    expected = ''.join(hashlib.sha256(files[name]).hexdigest() + '  ' + name + '\n'
                       for name in sorted(NAMES - {'SHA256SUMS'})).encode()
    if files['SHA256SUMS'] != expected:
        raise ValueError('candidate checksum mismatch')
    manifest = json.loads(files['manifest.json'])
    if manifest.get('release_id') != RID or manifest.get('component') != 'wecom-mcp-same-unit-bootstrap':
        raise ValueError('candidate identity mismatch')
    payload_hashes = {name: hashlib.sha256(files[name]).hexdigest()
                      for name in NAMES - {'manifest.json', 'SHA256SUMS'}}
    if manifest.get('files') != payload_hashes:
        raise ValueError('candidate payload does not match manifest')
    if observe() != before:
        raise ValueError('live service changed before staging')
    destination = root / RID
    destination.mkdir(mode=0o700)  # O_EXCL semantics; existing releases are never overwritten.
    directory = os.open(destination, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        def put(name, content, mode):
            fd = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode, dir_fd=directory)
            with os.fdopen(fd, 'wb') as target:
                os.fchmod(target.fileno(), mode)
                target.write(content)
                target.flush()
                os.fsync(target.fileno())
        put('.incomplete', b'incomplete\n', 0o400)
        os.fsync(directory)
        for name in sorted(NAMES):
            put(name, files[name], 0o555 if name == 'wecom-mcp-team' else 0o444)
        os.unlink('.incomplete', dir_fd=directory)
        os.fsync(directory)
        os.fchmod(directory, 0o555)
        os.fsync(directory)
    finally:
        os.close(directory)
    parent = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(parent)
    finally:
        os.close(parent)
    if observe() != before:
        raise ValueError('candidate staged but live baseline changed')
    return {'state': 'staged', 'release_id': RID, 'manifest_sha256': MANIFEST_SHA,
            'services_restarted': False, 'routes_changed': False}


if __name__ == '__main__':
    try:
        if os.geteuid() != 0 or len(sys.argv) != 1:
            raise ValueError('root and no arguments required')
        print(json.dumps(stage(sys.stdin.buffer), sort_keys=True))
    except Exception as error:
        print('MCP_STAGE_REFUSED: ' + (str(error) if isinstance(error, ValueError) else type(error).__name__), file=sys.stderr)
        sys.exit(1)
