import os
from pathlib import Path
import tempfile
import unittest

from same_unit_journal import Records
from target_bootstrap_journal import Journal


class JournalTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name).resolve()
        self.journal = Journal(self.path, os.getuid(), listxattr=lambda _fd: [])
        self.records = Records(self.journal, 'a' * 64)

    def test_terminal_is_immutable_and_bound_to_prepare(self):
        self.records.prepare()
        self.records.terminal('committed')
        self.records.terminal('committed')
        self.assertEqual(self.records.terminal_state(), 'committed')
        with self.assertRaises(ValueError): self.records.terminal('closed')
        with self.assertRaises(ValueError): Records(self.journal, 'b' * 64).terminal_state()

    def test_missing_prepare_refuses_terminal(self):
        self.records.prepare()
        self.records.terminal('closed')
        (self.path / 'prepare.json').unlink()
        with self.assertRaises(ValueError): self.records.terminal_state()

    def test_incident_does_not_replace_committed_terminal(self):
        self.records.prepare()
        self.records.terminal('committed')
        self.records.incident()
        self.assertEqual(self.records.terminal_state(), 'committed')
        self.assertEqual(len(list(self.path.glob('incident-*.json'))), 1)


if __name__ == '__main__':
    unittest.main()
