"""Durably publish the target old-start fence under its exclusive lock."""
import fcntl
import os
from pathlib import Path
import secrets
import stat

import target_bootstrap_old_start_guard as guard


def trusted_directory(directory, trusted_root, owner):
    if not directory.is_absolute() or not trusted_root.is_absolute():
        return False
    current = directory
    while True:
        try:
            info = os.lstat(current)
        except OSError:
            return False
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            return False
        if current == trusted_root:
            return True
        if current == current.parent:
            return False
        current = current.parent


def publish(directory, trusted_root, owner, expected):
    """Write exact fence bytes once. Caller must first stop/disable old unit.

    The exclusive inode is the same root-owned lock used by old-start guards.
    Any uncertain published result remains fenced and must be inspected or
    retried with exactly the same bytes; existing different content is refused.
    """
    if (not isinstance(expected, bytes) or not 1 <= len(expected) <= 4096
            or not trusted_directory(directory, trusted_root, owner)):
        raise ValueError('invalid fence input or directory')
    dir_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    lock_fd = None
    temporary = None
    try:
        opened = os.fstat(dir_fd)
        named = os.lstat(directory)
        if (opened.st_dev, opened.st_ino) != (named.st_dev, named.st_ino):
            raise ValueError('fence directory changed')
        lock_fd = os.open(guard.LOCK, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=dir_fd)
        info = os.fstat(lock_fd)
        named_lock = os.stat(guard.LOCK, dir_fd=dir_fd, follow_symlinks=False)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022
                or not info.st_mode & 0o004 or (info.st_dev, info.st_ino) != (named_lock.st_dev, named_lock.st_ino)):
            raise ValueError('untrusted start lock')
        fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        current = os.stat(guard.LOCK, dir_fd=dir_fd, follow_symlinks=False)
        if (info.st_dev, info.st_ino) != (current.st_dev, current.st_ino) or not trusted_directory(directory, trusted_root, owner):
            raise ValueError('start lock changed')

        def verify_existing():
            fence_fd = os.open(guard.FENCE, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=dir_fd)
            try:
                fence = os.fstat(fence_fd)
                if (not stat.S_ISREG(fence.st_mode) or fence.st_uid != owner or stat.S_IMODE(fence.st_mode) != 0o400
                        or os.read(fence_fd, 4097) != expected):
                    raise ValueError('existing fence differs')
            finally:
                os.close(fence_fd)

        try:
            verify_existing()
        except FileNotFoundError:
            name = '.target-open-fence-' + secrets.token_hex(12)
            fd = os.open(name, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o400, dir_fd=dir_fd)
            temporary = name
            try:
                with os.fdopen(fd, 'wb') as stream:
                    stream.write(expected)
                    stream.flush()
                    os.fsync(stream.fileno())
                os.link(temporary, guard.FENCE, src_dir_fd=dir_fd, dst_dir_fd=dir_fd, follow_symlinks=False)
                os.fsync(dir_fd)
            finally:
                os.unlink(temporary, dir_fd=dir_fd)
                temporary = None
                os.fsync(dir_fd)
            verify_existing()
        os.fsync(dir_fd)
    finally:
        try:
            if temporary is not None:
                try:
                    os.unlink(temporary, dir_fd=dir_fd)
                    os.fsync(dir_fd)
                except FileNotFoundError:
                    pass
        finally:
            try:
                if lock_fd is not None:
                    os.close(lock_fd)
            finally:
                os.close(dir_fd)
