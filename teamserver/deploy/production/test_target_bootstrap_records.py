import os
from pathlib import Path
import tempfile
import unittest

from target_bootstrap_journal import Journal
from target_bootstrap_records import Records
import target_bootstrap_recovery as routes


class RecordsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.journal = Journal(Path(self.temp.name).resolve(), os.getuid(), listxattr=lambda _fd: [])
        self.records = Records(self.journal)
        self.old = b'location /mcp { ' + routes.OLD_PORT + b' }\nlocation /auth { ' + routes.OLD_PORT + b' }\n'
        self.intent = routes.make_intent(self.old)

    def test_prepare_and_terminal_are_durable_and_idempotent(self):
        self.records.prepare(self.old, self.intent)
        self.records.prepare(self.old, self.intent)
        self.assertEqual(self.records.read_prepared(), (self.old, self.intent))
        self.assertEqual(self.records.terminal_state(self.intent), 'none')
        self.records.write_terminal(self.intent, 'committed')
        self.records.write_terminal(self.intent, 'committed')
        self.assertEqual(self.records.terminal_state(self.intent), 'committed')
        with self.assertRaises(ValueError):
            self.records.write_terminal(self.intent, 'closed')

    def test_partial_prepare_and_unrelated_terminal_fail_closed(self):
        self.journal.write_once('old-route.conf', self.old)
        with self.assertRaises(ValueError):
            self.records.read_prepared()
        self.records.prepare(self.old, self.intent)
        self.journal.write_once('terminal.json', b'wrong')
        with self.assertRaises(ValueError):
            self.records.terminal_state(self.intent)

    def test_terminal_requires_matching_prepare(self):
        with self.assertRaises(ValueError):
            self.records.write_terminal(self.intent, 'closed')
        self.records.prepare(self.old, self.intent)
        other = dict(self.intent, old='0' * 64)
        with self.assertRaises(ValueError):
            self.records.write_terminal(other, 'closed')

    def test_terminal_with_missing_prepare_is_not_trusted(self):
        self.records.prepare(self.old, self.intent)
        self.records.write_terminal(self.intent, 'committed')
        (Path(self.temp.name) / 'prepare.json').unlink()
        with self.assertRaises(ValueError):
            self.records.terminal_state(self.intent)


if __name__ == '__main__':
    unittest.main()
