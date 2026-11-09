#!/usr/bin/python3 -I
"""Refuse old target starts once the bootstrap transaction is fenced.

This standalone guard is not installed by importing it. Its root-owned unit
drop-in and fence directory require a separately recorded production change.
"""
import errno
import fcntl
import os
from pathlib import Path
import stat
import sys

DIRECTORY = Path('/var/lib/wecom-mcp-isolated')
FENCE = 'target-open-bootstrap-intent.json'
LOCK = 'target-open-bootstrap-old-start.lock'


def acquire_start_lock(directory, trusted_root, owner):
    if not directory.is_absolute() or not trusted_root.is_absolute():
        return None
    directory, trusted_root = Path(os.path.normpath(directory)), Path(os.path.normpath(trusted_root))
    current = directory
    while True:
        try:
            info = os.lstat(current)
        except OSError:
            return None
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            return None
        if current == trusted_root:
            break
        if current == current.parent:
            return None
        current = current.parent
    try:
        fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    except OSError:
        return None
    lock_fd = None
    try:
        try:
            lock_fd = os.open(LOCK, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=fd)
            lock_info = os.fstat(lock_fd)
            named_lock = os.stat(LOCK, dir_fd=fd, follow_symlinks=False)
            if (not stat.S_ISREG(lock_info.st_mode) or lock_info.st_uid != owner or lock_info.st_mode & 0o022 or not lock_info.st_mode & 0o004
                    or lock_info.st_dev != named_lock.st_dev or lock_info.st_ino != named_lock.st_ino):
                return None
            # The controller stops the old unit, takes the exclusive lock,
            # publishes the fence and only then releases. A concurrent start
            # waits here and observes the durable fence before executing.
            fcntl.flock(lock_fd, fcntl.LOCK_SH)
            current_lock = os.stat(LOCK, dir_fd=fd, follow_symlinks=False)
            if lock_info.st_dev != current_lock.st_dev or lock_info.st_ino != current_lock.st_ino:
                return None
        except OSError:
            return None
        # Any fence entry, including malformed content or a symlink, denies.
        try:
            os.stat(FENCE, dir_fd=fd, follow_symlinks=False)
            return None
        except OSError as error:
            if error.errno != errno.ENOENT:
                return None
        before = os.fstat(fd)
        after = os.lstat(directory)
        if before.st_dev != after.st_dev or before.st_ino != after.st_ino:
            return None
        os.set_inheritable(lock_fd, True)
        accepted, lock_fd = lock_fd, None
        return accepted
    finally:
        if lock_fd is not None:
            os.close(lock_fd)
        os.close(fd)


def may_start(directory, trusted_root, owner):
    lock_fd = acquire_start_lock(directory, trusted_root, owner)
    if lock_fd is None:
        return False
    os.close(lock_fd)
    return True


def main():
    if len(sys.argv) < 3 or sys.argv[1] != '--exec':
        return 1
    lock_fd = acquire_start_lock(DIRECTORY, Path('/'), 0)
    if lock_fd is None:
        return 1
    os.execv(sys.argv[2], sys.argv[2:])


if __name__ == '__main__':
    sys.exit(main())
