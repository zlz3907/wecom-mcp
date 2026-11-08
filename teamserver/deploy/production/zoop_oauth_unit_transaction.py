"""Primitives for recoverable unit changes; no command-line entry point.

The caller is responsible for fixed root-owned paths, locks, approved digests,
trust checks and health validation. Never use directly as a release controller.
"""
import os
from pathlib import Path
import tempfile


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0))
    try: os.fsync(fd)
    finally: os.close(fd)


def write_exclusive(path, data, mode):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, 'wb') as target:
        target.write(data)
        target.flush()
        os.fsync(target.fileno())
    sync_directory(path.parent)


def replace_expected(path, expected, replacement, mode):
    if path.read_bytes() != expected:
        raise ValueError('unit drifted before replacement')
    fd, temp_name = tempfile.mkstemp(prefix='.zoop-unit-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as target:
            target.write(replacement)
            target.flush()
            os.fchmod(target.fileno(), mode)
            os.fsync(target.fileno())
        if path.read_bytes() != expected:
            raise ValueError('unit drifted during replacement')
        os.replace(temp_name, path)
        sync_directory(path.parent)
    finally:
        if os.path.exists(temp_name): os.unlink(temp_name)


def activate(path, expected, replacement, mode, run):
    replace_expected(path, expected, replacement, mode)
    run('/usr/bin/systemctl', 'daemon-reload')
    run('/usr/bin/systemctl', 'restart', 'gnas-oauth-zhycit-next.service')
