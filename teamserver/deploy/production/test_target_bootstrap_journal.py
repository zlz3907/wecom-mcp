import os
from pathlib import Path
import tempfile
import threading
import unittest
from unittest import mock

import target_bootstrap_journal as j


class TargetBootstrapJournalTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve() / 'journal'
        self.root.mkdir(mode=0o700)
        self.journal = j.Journal(self.root, os.getuid(), lambda _fd: [])

    def test_once_and_conflicting_terminal(self):
        self.assertIsNone(self.journal.read('terminal.json'))
        self.journal.write_once('terminal.json', b'committed')
        self.journal.write_once('terminal.json', b'committed')
        with self.assertRaises(ValueError):
            self.journal.write_once('terminal.json', b'closed')
        self.assertEqual(self.journal.read('terminal.json'), b'committed')

    def test_concurrent_identical_writes_are_idempotent(self):
        errors = []
        def write():
            try:
                self.journal.write_once('prepare.json', b'exact')
            except Exception as error:
                errors.append(error)
        threads = [threading.Thread(target=write) for _ in range(2)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        self.assertEqual(errors, [])
        self.assertEqual(self.journal.read('prepare.json'), b'exact')

    def test_bad_names_permissions_and_symlinks_are_rejected(self):
        with self.assertRaises(ValueError):
            self.journal.write_once('../outside', b'data')
        (self.root / 'terminal.json').symlink_to('/nonexistent')
        with self.assertRaises(OSError):
            self.journal.read('terminal.json')

    def test_after_publish_sync_failure_can_be_retried(self):
        real_fsync = os.fsync
        count = 0
        def injected(fd):
            nonlocal count
            count += 1
            if count == 2:
                raise OSError('directory sync failure')
            return real_fsync(fd)
        with mock.patch.object(j.os, 'fsync', side_effect=injected):
            with self.assertRaises(OSError):
                self.journal.write_once('terminal.json', b'committed')
        self.journal.write_once('terminal.json', b'committed')
        self.assertEqual(self.journal.read('terminal.json'), b'committed')
