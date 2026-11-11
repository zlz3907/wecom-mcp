"""Exact target-route writes and Nginx checks for the bootstrap controller.

The caller must already hold the reviewed production locks and must perform
the live public/other-enterprise probes after reload. This module does not
authorize a switch or run on import.
"""
import hashlib
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess


def sha(data):
    return hashlib.sha256(data).hexdigest()


class TargetNginx:
    def __init__(self, path, owner, variants, full_hashes, run=None, listxattr=None):
        self.path = Path(path)
        self.owner = owner
        self.variants = tuple(variants)
        self.full_hashes = dict(full_hashes)
        self.run = run or self._run
        self.listxattr = listxattr if listxattr is not None else getattr(os, 'listxattr', None)
        if self.listxattr is None:
            raise ValueError('xattr verification is unavailable')
        if not self.path.is_absolute() or len(self.variants) != 3 or len(set(self.variants)) != 3:
            raise ValueError('invalid route contract')
        if (set(self.full_hashes) != {sha(value) for value in self.variants}
                or any(not isinstance(value, str) or not re.fullmatch(r'[0-9a-f]{64}', value) for value in self.full_hashes.values())):
            raise ValueError('incomplete full nginx fingerprints')
        if self.path.parent.resolve() != self.path.parent:
            raise ValueError('untrusted target parent')
        parent = self.path.parent.lstat()
        if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != self.owner or parent.st_mode & 0o022:
            raise ValueError('untrusted target parent')
        self.parent_identity = (parent.st_dev, parent.st_ino, parent.st_uid, parent.st_gid, stat.S_IMODE(parent.st_mode))

    @staticmethod
    def _run(*args):
        return subprocess.run(args, check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=25).stdout

    def _open_parent(self):
        fd = os.open(self.path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            info, named = os.fstat(fd), self.path.parent.lstat()
            identity = lambda value: (value.st_dev, value.st_ino, value.st_uid, value.st_gid, stat.S_IMODE(value.st_mode))
            if identity(info) != self.parent_identity or identity(named) != self.parent_identity:
                raise ValueError('target parent changed')
        except Exception:
            os.close(fd)
            raise
        return fd

    def _read_route(self, parent_fd):
        fd = os.open(self.path.name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=parent_fd)
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            named = os.stat(self.path.name, dir_fd=parent_fd, follow_symlinks=False)
            if (not stat.S_ISREG(info.st_mode) or info.st_uid != self.owner or info.st_mode & 0o022
                    or (info.st_dev, info.st_ino) != (named.st_dev, named.st_ino) or self.listxattr(stream.fileno())):
                raise ValueError('untrusted target route')
            data = stream.read(65537)
            after = os.fstat(stream.fileno())
            named_after = os.stat(self.path.name, dir_fd=parent_fd, follow_symlinks=False)
            if ((info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode, info.st_size, info.st_mtime_ns) !=
                    (after.st_dev, after.st_ino, after.st_uid, after.st_gid, after.st_mode, after.st_size, after.st_mtime_ns)
                    or (info.st_dev, info.st_ino) != (named_after.st_dev, named_after.st_ino)):
                raise ValueError('target route changed during read')
        if len(data) > 65536:
            raise ValueError('target route is too large')
        return data, info

    def read_route(self):
        parent_fd = self._open_parent()
        try:
            data, _ = self._read_route(parent_fd)
            fresh_parent = self._open_parent()
            os.close(fresh_parent)
            return data
        finally:
            os.close(parent_fd)

    def replace_route(self, expected_current, desired):
        if expected_current not in self.variants or desired not in self.variants:
            raise ValueError('unapproved target route')
        parent_fd = self._open_parent()
        temporary = None
        try:
            before, info = self._read_route(parent_fd)
            if before != expected_current:
                raise ValueError('target route changed externally')
            name = '.target-bootstrap-' + secrets.token_hex(12)
            fd = os.open(name, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600, dir_fd=parent_fd)
            temporary = name
            with os.fdopen(fd, 'wb') as stream:
                created = os.fstat(stream.fileno())
                if (created.st_uid, created.st_gid) != (info.st_uid, info.st_gid):
                    os.fchown(stream.fileno(), info.st_uid, info.st_gid)
                os.fchmod(stream.fileno(), stat.S_IMODE(info.st_mode))
                stream.write(desired)
                stream.flush()
                os.fsync(stream.fileno())
            current_data, current = self._read_route(parent_fd)
            if (current_data != expected_current or
                    (current.st_dev, current.st_ino, current.st_uid, current.st_gid, current.st_mode) !=
                    (info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode)):
                raise ValueError('target route changed before replacement')
            fresh_parent = self._open_parent()
            try:
                fresh = os.fstat(fresh_parent)
                original = os.fstat(parent_fd)
                if (fresh.st_dev, fresh.st_ino) != (original.st_dev, original.st_ino):
                    raise ValueError('target parent changed before replacement')
            finally:
                os.close(fresh_parent)
            os.replace(temporary, self.path.name, src_dir_fd=parent_fd, dst_dir_fd=parent_fd)
            temporary = None
            os.fsync(parent_fd)
            after_data, after = self._read_route(parent_fd)
            if (after_data != desired or (after.st_uid, after.st_gid, after.st_mode) !=
                    (info.st_uid, info.st_gid, info.st_mode)):
                raise ValueError('target route readback mismatch')
            final_parent = self._open_parent()
            os.close(final_parent)
        finally:
            try:
                if temporary is not None:
                    os.unlink(temporary, dir_fd=parent_fd)
            finally:
                os.close(parent_fd)

    def test_and_reload(self):
        route = self.read_route()
        expected = self.full_hashes.get(sha(route))
        if expected is None or sha(self.run('/usr/sbin/nginx', '-T')) != expected:
            raise ValueError('full nginx configuration changed')
        self.run('/usr/sbin/nginx', '-t')
        if self.read_route() != route or sha(self.run('/usr/sbin/nginx', '-T')) != expected:
            raise ValueError('nginx configuration changed before reload')
        self.run('/usr/sbin/nginx', '-s', 'reload')
        if self.read_route() != route or sha(self.run('/usr/sbin/nginx', '-T')) != expected:
            raise ValueError('nginx configuration changed after reload')
