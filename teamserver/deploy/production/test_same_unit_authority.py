import unittest

import same_unit_authority as authority


def env(mode=b'wecom_all_members', scopes=b'["zoop.read","zoop.write"]', tools=b'["wecom_record_query"]'):
    return b'\0'.join(k + b'=' + v for k, v in zip(authority.ENV_KEYS, (mode, scopes, tools)))


class AuthorityTest(unittest.TestCase):
    def test_all_member_mode_requires_both_legacy_scopes(self):
        authority.evaluate_environ(env())
        for invalid in (env(mode=b'per_user_facts'), env(scopes=b'["zoop.read"]'), env(tools=b'[]'),
                        env() + b'\0' + authority.ENV_KEYS[0] + b'=wecom_all_members'):
            with self.subTest(invalid=invalid), self.assertRaises(ValueError):
                authority.evaluate_environ(invalid)


if __name__ == '__main__':
    unittest.main()
