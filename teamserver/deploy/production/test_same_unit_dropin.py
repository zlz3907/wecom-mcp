import hashlib
import unittest
from unittest.mock import patch

import same_unit_dropin as builder
import same_unit_plan as contract


class DropinTest(unittest.TestCase):
    def fragment(self, profile):
        old = builder.OLD_RELEASES[profile]
        port = contract.PROFILES[profile]['port']
        command = ('/usr/bin/env ' + old + 'wecom-mcp-team --gnas-discovery-policy ' + old +
                   'discovery-policy.json --listen 127.0.0.1:' + str(port))
        return ('[Service]\nExecStartPre=' + command + ' --check-config\nExecStart=' + command + '\n').encode()

    def test_exact_fragment_preserves_listener_and_replaces_release(self):
        for profile in ('target', 'shared'):
            with self.subTest(profile=profile):
                data = self.fragment(profile)
                edited = dict(contract.PROFILES[profile], fragment_sha256=hashlib.sha256(data).hexdigest())
                with patch.dict(contract.PROFILES, {profile: edited}):
                    result = builder.generate(profile, data, '20261108T194434Z-dbef937502fa')
                self.assertIn(('127.0.0.1:' + str(edited['port'])).encode(), result)
                self.assertNotIn(builder.OLD_RELEASES[profile].encode(), result)
                self.assertEqual(result.count(b'20261108T194434Z-dbef937502fa'), 4)

    def test_drift_and_invalid_release_are_rejected(self):
        data = self.fragment('target')
        with self.assertRaises(ValueError):
            builder.generate('target', data, '20261108T194434Z-dbef937502fa')
        edited = dict(contract.PROFILES['target'], fragment_sha256=hashlib.sha256(data).hexdigest())
        with patch.dict(contract.PROFILES, {'target': edited}), self.assertRaises(ValueError):
            builder.generate('target', data, '../outside-dbef937502fa')


if __name__ == '__main__':
    unittest.main()
