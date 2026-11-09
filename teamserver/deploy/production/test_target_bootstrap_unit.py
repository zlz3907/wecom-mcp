import hashlib
import unittest

import target_bootstrap_unit as u


class TargetBootstrapUnitTest(unittest.TestCase):
    def setUp(self):
        args = '/usr/bin/env GNAS_BASE_URL=http://127.0.0.1:7504 ' + u.OLD_RELEASE + 'wecom-mcp-team --gnas-discovery-policy ' + u.OLD_RELEASE + 'discovery-policy.json --listen 127.0.0.1:7709'
        self.unit = ('[Service]\nUser=wecom-mcp-gmzoop\nKillMode=control-group\nExecStartPre=' + args + ' --check-config\nExecStart=' + args + '\n').encode()
        self.sha = hashlib.sha256(self.unit).hexdigest()

    def test_dropin_guards_both_check_and_process(self):
        dropin = u.guarded_dropin(self.unit, self.sha).decode()
        self.assertIn('ExecStartPre=/usr/bin/python3 -I ' + u.GUARD + ' --check', dropin)
        self.assertIn('ExecStart=/usr/bin/python3 -I ' + u.GUARD + ' --exec /usr/bin/env ', dropin)
        self.assertEqual(dropin.count('--listen 127.0.0.1:7709'), 2)

    def test_candidate_changes_only_release_and_port(self):
        release = '20261108T194434Z-dbef937502fa'
        candidate = u.candidate_unit(self.unit, self.sha, release)
        self.assertEqual(candidate.count(b'--listen 127.0.0.1:7710'), 2)
        self.assertEqual(candidate.count(release.encode()), 4)
        self.assertNotIn(u.OLD_RELEASE.encode(), candidate)

    def test_changed_or_ambiguous_units_rejected(self):
        for unit, digest in ((self.unit, '0' * 64), (self.unit + b'ExecStart=unexpected\n', hashlib.sha256(self.unit + b'ExecStart=unexpected\n').hexdigest())):
            with self.subTest(unit=unit), self.assertRaises(ValueError):
                u.guarded_dropin(unit, digest)
        for release in ('../../other', '20261108T194434Z-INVALID00000'):
            with self.subTest(release=release), self.assertRaises(ValueError):
                u.candidate_unit(self.unit, self.sha, release)


if __name__ == '__main__':
    unittest.main()
