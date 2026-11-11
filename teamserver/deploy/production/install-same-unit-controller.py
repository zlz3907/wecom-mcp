#!/usr/bin/python3 -I
"""Install only reviewed MCP bootstrap controller files; never switch services."""
import contextlib
import fcntl
import hashlib
import json
import os
import secrets
import re
from pathlib import Path
import stat
import subprocess
import sys
import tarfile

ARCHIVE_SHA = '1372378cc8446074bd65a5cf5679a9d9f4dba1688a1c2c2cfa307e085fd01764'
PROBE_SHA = '0f427eef348b5e34fdb62fe476213cf9770ccda8a37443d1f82a7e93fc8279d4'
NAMES = {'same-unit-execution.pyz': ARCHIVE_SHA, 'fleet-readonly-probe': PROBE_SHA}
DIRECTORIES = {
    'usr/local/lib/wecom-mcp-open-bootstrap': 0o755,
    'var/lib/wecom-mcp-isolated/bootstrap-evidence': 0o700,
    'var/lib/wecom-mcp-isolated/bootstrap-used': 0o700,
    'var/lib/wecom-mcp-isolated/bootstrap-transactions': 0o700,
    'etc/gnas/mcp-bootstrap-plans': 0o700,
}
EXPECTED_RUNTIME = {
    'gnas-server.service': 'a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e',
    'gnas-oauth-zhycit-next.service': 'cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1',
    'wecom-mcp-zhycit-migration.service': '82fd10c16f5ccf72219818a9efc00e0b79f4e70a8726faf071059b6a6344fa03',
    'wecom-mcp-discovery-21462d2.service': '988055b38a6963afe1f6cfe3b2bdb9b691483cb599974a450efae535d356ad22',
}


def digest(data): return hashlib.sha256(data).hexdigest()


def snapshot():
    result = {}
    for unit, expected in EXPECTED_RUNTIME.items():
        output = subprocess.run(['/usr/bin/systemctl', 'show', unit, '-p', 'MainPID', '-p', 'ActiveState', '-p', 'NRestarts'],
                                check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10).stdout.decode()
        lines = output.splitlines()
        props = dict(row.split('=', 1) for row in lines if '=' in row)
        if len(lines) != 3 or set(props) != {'MainPID', 'ActiveState', 'NRestarts'}:
            raise ValueError('ambiguous service state')
        pid = int(props['MainPID'])
        if pid <= 1 or props['ActiveState'] != 'active' or props['NRestarts'] != '0':
            raise ValueError('service unavailable')
        if digest((Path('/proc') / str(pid) / 'exe').read_bytes()) != expected:
            raise ValueError('service binary changed')
        result[unit] = pid
    return result


def syncdir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try: os.fsync(fd)
    finally: os.close(fd)


def trusted_parent(path, root, owner):
    p = path
    while True:
        info = p.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            raise ValueError('untrusted install parent')
        if p == root: return
        if p == p.parent: raise ValueError('install outside trusted root')
        p = p.parent


def ensure_dir(path, root, owner, mode):
    trusted_parent(path.parent, root, owner)
    try:
        # The installer is single-threaded; ensure an interrupt cannot leave
        # a newly created directory with a restrictive inherited umask.
        old_umask = os.umask(0)
        try:
            path.mkdir(mode=mode)
        finally:
            os.umask(old_umask)
        syncdir(path.parent)
    except FileExistsError:
        pass
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or stat.S_IMODE(info.st_mode) != mode:
        raise ValueError('untrusted install directory')


def publish(path, data, expected, owner):
    if digest(data) != expected: raise ValueError('artifact input digest mismatch')
    def verify_existing():
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != owner or stat.S_IMODE(info.st_mode) != 0o555 or digest(path.read_bytes()) != expected:
            raise ValueError('existing artifact differs')
    try:
        verify_existing()
        return
    except FileNotFoundError:
        pass
    temporary = path.parent / ('.mcp-install-' + secrets.token_hex(16))
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    try:
        with os.fdopen(fd, 'wb') as target:
            target.write(data); target.flush()
            os.fchmod(target.fileno(), 0o555); os.fsync(target.fileno())
        try:
            os.link(temporary, path, follow_symlinks=False)
        except FileExistsError:
            verify_existing()
        syncdir(path.parent)
        verify_existing()
    finally:
        temporary.unlink(missing_ok=True)
        syncdir(path.parent)


def cleanup_orphans(directory, owner):
    # The production entry point holds the common MCP release lock; never
    # delete unknown names, symlinks, foreign files, or non-restrictive modes.
    for path in directory.iterdir():
        if not re.fullmatch(r'\.mcp-install-[a-f0-9]{32}', path.name):
            continue
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != owner or stat.S_IMODE(info.st_mode) not in (0o600, 0o555):
            raise ValueError('untrusted incomplete installer file')
        path.unlink()
        syncdir(directory)


@contextlib.contextmanager
def release_lock():
    path = Path('/var/lib/wecom-mcp-isolated/release.lock')
    trusted_parent(path.parent, Path('/'), 0)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        before = os.fstat(fd)
        named = path.lstat()
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != 0 or before.st_mode & 0o022 or
                (before.st_dev, before.st_ino) != (named.st_dev, named.st_ino)):
            raise ValueError('untrusted installer lock')
        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        named = path.lstat()
        if (before.st_dev, before.st_ino) != (named.st_dev, named.st_ino):
            raise ValueError('installer lock changed')
        yield
        named = path.lstat()
        if (before.st_dev, before.st_ino) != (named.st_dev, named.st_ino):
            raise ValueError('installer lock changed')
    finally:
        os.close(fd)


def install(stream, root=Path('/'), owner=0, observe=snapshot, hashes=NAMES):
    if not root.is_absolute() or root.resolve() != root:
        raise ValueError('untrusted install root')
    contents = {}
    with tarfile.open(fileobj=stream, mode='r|*') as archive:
        for member in archive:
            if member.name not in hashes or member.name in contents or not member.isfile() or not 0 < member.size <= 20 * 1024 * 1024:
                raise ValueError('unexpected install artifact')
            data = archive.extractfile(member).read(member.size + 1)
            if len(data) != member.size or digest(data) != hashes[member.name]:
                raise ValueError('artifact digest mismatch')
            contents[member.name] = data
    if set(contents) != set(hashes): raise ValueError('missing install artifact')
    before = observe()
    for relative, mode in DIRECTORIES.items():
        ensure_dir(root / relative, root, owner, mode)
    directory = root / 'usr/local/lib/wecom-mcp-open-bootstrap'
    cleanup_orphans(directory, owner)
    if any(p.name not in hashes for p in directory.iterdir()):
        raise ValueError('unexpected controller directory content')
    for name, expected in hashes.items(): publish(directory / name, contents[name], expected, owner)
    if observe() != before: raise ValueError('live service changed during install')
    return {'state': 'controller_installed', 'service_restarted': False, 'routes_changed': False}


if __name__ == '__main__':
    try:
        if os.geteuid() != 0 or len(sys.argv) != 1: raise ValueError('root and no arguments required')
        with release_lock():
            print(json.dumps(install(sys.stdin.buffer), sort_keys=True))
    except Exception as error:
        print('MCP_CONTROLLER_INSTALL_REFUSED: ' + type(error).__name__, file=sys.stderr)
        sys.exit(1)
