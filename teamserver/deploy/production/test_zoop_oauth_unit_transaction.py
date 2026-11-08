import importlib.util
from pathlib import Path
import tempfile
import unittest

path=Path(__file__).with_name('zoop_oauth_unit_transaction.py')
spec=importlib.util.spec_from_file_location('unit_transaction',path)
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class UnitTransactionTests(unittest.TestCase):
    def test_unit_replacement_checks_expected_bytes_and_only_restarts_target(self):
        with tempfile.TemporaryDirectory() as d:
            unit=Path(d)/'service';unit.write_bytes(b'old');commands=[]
            m.activate(unit,b'old',b'new',0o644,lambda *cmd:commands.append(cmd))
            self.assertEqual(unit.read_bytes(),b'new')
            self.assertEqual(commands,[('/usr/bin/systemctl','daemon-reload'),('/usr/bin/systemctl','restart','gnas-oauth-zhycit-next.service')])
            with self.assertRaisesRegex(ValueError,'drifted'):m.activate(unit,b'old',b'bad',0o644,lambda *cmd:commands.append(cmd))
            self.assertEqual(unit.read_bytes(),b'new');self.assertEqual(len(commands),2)
    def test_journal_creation_is_exclusive_and_cannot_follow_symlink(self):
        with tempfile.TemporaryDirectory() as d:
            journal=Path(d)/'journal';m.write_exclusive(journal,b'first',0o440)
            with self.assertRaises(FileExistsError):m.write_exclusive(journal,b'second',0o440)
            self.assertEqual(journal.read_bytes(),b'first')
            link=Path(d)/'link';link.symlink_to(journal)
            with self.assertRaises(FileExistsError):m.write_exclusive(link,b'bad',0o440)
            self.assertEqual(journal.read_bytes(),b'first')
