"""Acquire exact root-protected release locks in their fixed order."""
import contextlib
import fcntl
import os
from pathlib import Path
import stat


def _parents(path, root, owner):
    if not path.is_absolute() or not root.is_absolute():
        raise ValueError('relative release lock')
    directory = path.parent
    while True:
        info = directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            raise ValueError('untrusted release lock directory')
        if directory == root:
            return
        if directory == directory.parent:
            raise ValueError('release lock outside trusted root')
        directory = directory.parent


@contextlib.contextmanager
def acquire(paths, root=Path('/'), owner=0):
    if not paths or len(set(paths)) != len(paths):
        raise ValueError('invalid release lock list')
    held = []
    try:
        for item in paths:
            path = Path(item)
            _parents(path, root, owner)
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
            held.append(fd)
            info, named = os.fstat(fd), path.lstat()
            if (not stat.S_ISREG(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022 or
                    (info.st_dev, info.st_ino) != (named.st_dev, named.st_ino)):
                raise ValueError('untrusted release lock')
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            current = path.lstat()
            if (info.st_dev, info.st_ino) != (current.st_dev, current.st_ino):
                raise ValueError('release lock changed')
            _parents(path, root, owner)
        yield
    finally:
        for fd in reversed(held):
            os.close(fd)
