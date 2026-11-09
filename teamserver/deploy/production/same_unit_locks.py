"""Release locks, including the pinned legacy GNAS lock for open bootstrap.

The legacy path is writable by the local product account.  Inode rechecks detect
ordinary concurrent replacement; they do not protect against a privileged local
actor.  It must never be used to enable managed policies.
"""
import contextlib
import fcntl
import os
from pathlib import Path
import stat

LEGACY = Path('/home/product/services/gnas-service-gin/release.lock')
LEGACY_DIRS = {
    Path('/home/product'): (1000, 1000, 0o750),
    Path('/home/product/services'): (1000, 1000, 0o755),
    Path('/home/product/services/gnas-service-gin'): (1000, 1000, 0o775),
}


def _identity(info):
    return (info.st_dev, info.st_ino, info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode), stat.S_IFMT(info.st_mode))


def _parents(path, root, owner, legacy=False):
    if not path.is_absolute() or not root.is_absolute() or os.path.normpath(path) != str(path):
        raise ValueError('relative release lock')
    result = {}
    directory = path.parent
    while True:
        info = directory.lstat()
        exception = LEGACY_DIRS.get(directory) if legacy else None
        if not stat.S_ISDIR(info.st_mode):
            raise ValueError('untrusted release lock directory')
        if exception is not None:
            if (info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode)) != exception:
                raise ValueError('legacy lock directory changed')
        elif info.st_uid != owner or info.st_mode & 0o022:
            raise ValueError('untrusted release lock directory')
        result[directory] = _identity(info)
        if directory == root:
            return result
        if directory == directory.parent:
            raise ValueError('release lock outside trusted root')
        directory = directory.parent


@contextlib.contextmanager
def acquire(paths, root=Path('/'), owner=0, bootstrap_legacy=False):
    if not paths or len(set(paths)) != len(paths):
        raise ValueError('invalid release lock list')
    if bootstrap_legacy and (Path(paths[0]) != LEGACY or sum(Path(p) == LEGACY for p in paths) != 1):
        raise ValueError('invalid legacy bootstrap lock order')
    held = []
    try:
        for item in paths:
            path = Path(item)
            legacy = bootstrap_legacy and path == LEGACY
            parents = _parents(path, root, owner, legacy)
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
            held.append((fd, path, parents, legacy))
            info, named = os.fstat(fd), path.lstat()
            if (not stat.S_ISREG(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022 or
                    _identity(info) != _identity(named)):
                raise ValueError('untrusted release lock')
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            if _identity(info) != _identity(path.lstat()) or _parents(path, root, owner, legacy) != parents:
                raise ValueError('release lock changed')
        expected = [(fd, path, parents, legacy, _identity(os.fstat(fd))) for fd, path, parents, legacy in held]
        def verify():
            for fd, path, parents, legacy, identity in expected:
                if (_identity(os.fstat(fd)) != identity or _identity(path.lstat()) != identity or
                        _parents(path, root, owner, legacy) != parents):
                    raise ValueError('release lock changed while held')
        verify()
        yield verify
        verify()
    finally:
        for fd, _, _, _ in reversed(held):
            os.close(fd)
