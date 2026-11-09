import fcntl
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import target_bootstrap_fence as fence
import target_bootstrap_old_start_guard as guard


class TargetBootstrapFenceTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.directory = self.root / 'controlled'
        self.directory.mkdir(mode=0o700)
        (self.directory / guard.LOCK).touch(mode=0o444)

    def test_publish_is_idempotent_and_blocks_old_start(self):
        self.assertTrue(guard.may_start(self.directory, self.root, os.getuid()))
        fence.publish(self.directory, self.root, os.getuid(), b'approved')
        self.assertFalse(guard.may_start(self.directory, self.root, os.getuid()))
        fence.publish(self.directory, self.root, os.getuid(), b'approved')
        with self.assertRaises(ValueError):
            fence.publish(self.directory, self.root, os.getuid(), b'different')

    def test_old_service_lock_prevents_publishing(self):
        fd = guard.acquire_start_lock(self.directory, self.root, os.getuid())
        self.assertIsNotNone(fd)
        try:
            with self.assertRaises(BlockingIOError):
                fence.publish(self.directory, self.root, os.getuid(), b'approved')
            self.assertFalse((self.directory / guard.FENCE).exists())
        finally:
            os.close(fd)

    def test_wrong_owner_or_unsafe_lock_refuses(self):
        with self.assertRaises(ValueError):
            fence.publish(self.directory, self.root, os.getuid() + 1, b'approved')
        (self.directory / guard.LOCK).chmod(0o666)
        with self.assertRaises(ValueError):
            fence.publish(self.directory, self.root, os.getuid(), b'approved')

    def test_published_fence_survives_directory_sync_failure(self):
        real_fsync = os.fsync
        count = 0

        def fail_once(fd):
            nonlocal count
            count += 1
            if count == 2:
                raise OSError('injected directory sync failure')
            return real_fsync(fd)

        with mock.patch.object(fence.os, 'fsync', side_effect=fail_once):
            with self.assertRaises(OSError):
                fence.publish(self.directory, self.root, os.getuid(), b'approved')
        self.assertFalse(guard.may_start(self.directory, self.root, os.getuid()))
        fence.publish(self.directory, self.root, os.getuid(), b'approved')

    def test_cleanup_unlink_failure_never_leaks_exclusive_lock(self):
        with mock.patch.object(fence.os, 'unlink', side_effect=OSError('injected unlink error')):
            with self.assertRaises(OSError):
                fence.publish(self.directory, self.root, os.getuid(), b'approved')
        with (self.directory / guard.LOCK).open('rb') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_cleanup_directory_sync_failure_never_leaks_exclusive_lock(self):
        real_fsync = os.fsync
        count = 0

        def fail_cleanup(fd):
            nonlocal count
            count += 1
            if count == 3:
                raise OSError('injected cleanup sync error')
            return real_fsync(fd)

        with mock.patch.object(fence.os, 'fsync', side_effect=fail_cleanup):
            with self.assertRaises(OSError):
                fence.publish(self.directory, self.root, os.getuid(), b'approved')
        with (self.directory / guard.LOCK).open('rb') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)


if __name__ == '__main__':
    unittest.main()
