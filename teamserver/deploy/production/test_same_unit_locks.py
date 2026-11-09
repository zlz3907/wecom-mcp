import fcntl
import os
from pathlib import Path
import tempfile
import unittest

from same_unit_locks import acquire


class LockTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.first, self.second = self.root / 'first', self.root / 'second'
        self.first.touch(mode=0o444)
        self.second.touch(mode=0o444)

    def test_release_locks_are_exclusive_and_released(self):
        with acquire([str(self.first), str(self.second)], root=self.root, owner=os.getuid()):
            with self.first.open('rb') as other, self.assertRaises(BlockingIOError):
                fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with self.first.open('rb') as other:
            fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_partial_failure_releases_prior_locks(self):
        self.second.chmod(0o666)
        with self.assertRaises(ValueError):
            with acquire([str(self.first), str(self.second)], root=self.root, owner=os.getuid()): pass
        with acquire([str(self.first)], root=self.root, owner=os.getuid()): pass

    def test_symlink_and_writable_parent_are_refused(self):
        link = self.root / 'link'
        link.symlink_to(self.first)
        with self.assertRaises(OSError):
            with acquire([str(link)], root=self.root, owner=os.getuid()): pass
        self.root.chmod(0o777)
        try:
            with self.assertRaises(ValueError):
                with acquire([str(self.first)], root=self.root, owner=os.getuid()): pass
        finally:
            self.root.chmod(0o700)


if __name__ == '__main__':
    unittest.main()
