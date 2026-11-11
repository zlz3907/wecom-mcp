"""Immutable, root-owned transaction records for the target bootstrap."""
import os
from pathlib import Path
import re
import secrets
import stat

NAME = re.compile(r'(?:prepare\.json|terminal\.json|old-route\.conf|incident-[a-f0-9]{24}\.json)\Z')


class Journal:
    def __init__(self, directory, owner, listxattr=None):
        self.directory, self.owner = Path(directory), owner
        self.listxattr = listxattr if listxattr is not None else getattr(os, 'listxattr', None)
        if not self.directory.is_absolute() or self.directory.resolve() != self.directory or self.listxattr is None:
            raise ValueError('untrusted journal directory')
        info = self.directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o077:
            raise ValueError('untrusted journal directory')
        self.identity = (info.st_dev, info.st_ino, info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode))

    def _open(self):
        fd = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            identity = lambda info: (info.st_dev, info.st_ino, info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode))
            if identity(os.fstat(fd)) != self.identity or identity(self.directory.lstat()) != self.identity:
                raise ValueError('journal directory changed')
        except Exception:
            os.close(fd)
            raise
        return fd

    def _read(self, directory, name):
        try:
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=directory)
        except FileNotFoundError:
            return None
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            named = os.stat(name, dir_fd=directory, follow_symlinks=False)
            if (not stat.S_ISREG(info.st_mode) or info.st_uid != self.owner or stat.S_IMODE(info.st_mode) != 0o400
                    or (info.st_dev, info.st_ino) != (named.st_dev, named.st_ino) or self.listxattr(stream.fileno())):
                raise ValueError('untrusted journal record')
            content = stream.read(65537)
            after = os.fstat(stream.fileno())
            named_after = os.stat(name, dir_fd=directory, follow_symlinks=False)
            if (len(content) > 65536 or (info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns) !=
                    (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns)
                    or (info.st_dev, info.st_ino) != (named_after.st_dev, named_after.st_ino)):
                raise ValueError('journal record changed')
            return content

    def read(self, name):
        if not NAME.fullmatch(name):
            raise ValueError('invalid journal record name')
        directory = self._open()
        try:
            value = self._read(directory, name)
            fresh = self._open()
            os.close(fresh)
            return value
        finally:
            os.close(directory)

    def write_once(self, name, content):
        if not NAME.fullmatch(name) or not isinstance(content, bytes) or not 1 <= len(content) <= 65536:
            raise ValueError('invalid journal record')
        directory = self._open()
        temporary = None
        try:
            existing = self._read(directory, name)
            if existing is not None:
                if existing != content:
                    raise ValueError('journal record conflicts')
                os.fsync(directory)
                return
            candidate = '.target-journal-' + secrets.token_hex(12)
            fd = os.open(candidate, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o400, dir_fd=directory)
            temporary = candidate
            with os.fdopen(fd, 'wb') as stream:
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
            try:
                os.link(candidate, name, src_dir_fd=directory, dst_dir_fd=directory, follow_symlinks=False)
            except FileExistsError:
                if self._read(directory, name) != content:
                    raise ValueError('journal record conflicts')
            os.fsync(directory)
            if self._read(directory, name) != content:
                raise ValueError('journal record readback mismatch')
            fresh = self._open()
            os.close(fresh)
        finally:
            try:
                if temporary is not None:
                    os.unlink(temporary, dir_fd=directory)
                    os.fsync(directory)
            finally:
                os.close(directory)
