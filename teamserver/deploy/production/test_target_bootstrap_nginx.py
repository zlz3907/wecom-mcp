import os
from pathlib import Path
import tempfile
import unittest

import target_bootstrap_nginx as n


class TargetBootstrapNginxTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        parent = Path(self.tmp.name).resolve() / 'controlled'
        parent.mkdir(mode=0o700)
        self.path = parent / 'route.conf'
        self.old, self.new, self.closed = b'old route', b'new route', b'closed route'
        self.path.write_bytes(self.old)
        self.calls = []
        self.expected = {n.sha(value): n.sha(b'full:' + value) for value in (self.old, self.new, self.closed)}
        self.nginx = n.TargetNginx(self.path, os.getuid(), (self.old, self.new, self.closed), self.expected, self.run_fake, lambda _fd: [])

    def run_fake(self, *args):
        self.calls.append(args)
        return b'full:' + self.path.read_bytes() if args[-1] == '-T' else b''

    def test_replace_and_reload_exact_candidate(self):
        self.nginx.replace_route(self.old, self.new)
        self.nginx.test_and_reload()
        self.assertEqual(self.path.read_bytes(), self.new)
        self.assertIn(('/usr/sbin/nginx', '-s', 'reload'), self.calls)
        self.assertEqual(self.calls[-1][-1], '-T')

    def test_external_route_drift_is_not_overwritten(self):
        self.path.write_bytes(b'other change')
        with self.assertRaises(ValueError):
            self.nginx.replace_route(self.old, self.new)
        self.assertEqual(self.path.read_bytes(), b'other change')

    def test_full_config_drift_refuses_reload(self):
        self.nginx.run = lambda *_args: b'unexpected full configuration'
        with self.assertRaises(ValueError):
            self.nginx.test_and_reload()
        self.assertEqual(self.calls, [])

    def test_untrusted_permissions_and_unapproved_route_refused(self):
        with self.assertRaises(ValueError):
            self.nginx.replace_route(self.old, b'unknown')
        self.path.chmod(0o666)
        with self.assertRaises(ValueError):
            self.nginx.read_route()

    def test_expected_current_prevents_reopening_a_closed_route(self):
        self.path.write_bytes(self.closed)
        with self.assertRaises(ValueError):
            self.nginx.replace_route(self.old, self.new)
        self.assertEqual(self.path.read_bytes(), self.closed)

    def test_metadata_drift_is_not_overwritten(self):
        original = self.nginx._read_route
        calls = 0
        def changed(parent):
            nonlocal calls
            value = original(parent)
            calls += 1
            if calls == 1:
                self.path.chmod(0o600)
            return value
        self.nginx._read_route = changed
        with self.assertRaises(ValueError):
            self.nginx.replace_route(self.old, self.new)
        self.assertEqual(self.path.read_bytes(), self.old)
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)

    def test_replaced_parent_inode_is_refused(self):
        parent = self.path.parent
        parent.rename(parent.with_name('moved'))
        parent.mkdir(mode=0o700)
        self.path.write_bytes(self.old)
        with self.assertRaises(ValueError):
            self.nginx.replace_route(self.old, self.new)
        self.assertEqual(self.path.read_bytes(), self.old)

    def test_extended_attributes_are_not_silently_lost(self):
        self.nginx.listxattr = lambda _fd: ['user.fixture']
        with self.assertRaises(ValueError):
            self.nginx.replace_route(self.old, self.new)

    def test_post_reload_full_config_drift_is_reported(self):
        count = 0
        def drift(*args):
            nonlocal count
            if args[-1] == '-T':
                count += 1
                return b'other configuration' if count == 3 else b'full:' + self.path.read_bytes()
            return b''
        self.nginx.run = drift
        with self.assertRaisesRegex(ValueError, 'after reload'):
            self.nginx.test_and_reload()


if __name__ == '__main__':
    unittest.main()
