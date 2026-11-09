import unittest

import same_unit_plan as contract


def fixture(profile):
    return {'schema_version': 1, 'environment': 'zhycit-prod-01', 'profile': profile,
            **contract.PROFILES[profile], 'previous_policy_sha256': contract.POLICY_SHA,
            'release_id': contract.RELEASE_ID, 'source_commit': contract.SOURCE,
            'source_tree': contract.SOURCE_TREE, 'ci_url': contract.CI_URL,
            'candidate_binary_sha256': contract.BINARY_SHA, 'candidate_policy_sha256': contract.POLICY_SHA,
            'ci_evidence_sha256': contract.CI_EVIDENCE_SHA, 'smoke_sha256': 'c' * 64,
            'fleet_snapshot_sha256': 'd' * 64, 'recovery_mode': 'same-candidate-or-closed',
            'authorization_mode': 'wecom-all-members-policy-disabled',
            'environment_file_sha256': {p: 'e' * 64 for p in contract.PROFILES[profile]['environment_files']}}


class PlanTest(unittest.TestCase):
    def test_known_profiles_bind_candidate_to_their_own_release_roots(self):
        for profile in ('target', 'shared'):
            plan = fixture(profile)
            self.assertEqual(str(contract.validate(plan)),
                             plan['release_root'] + '/' + plan['release_id'] + '/wecom-mcp-team')

    def test_changed_runtime_or_source_never_matches_profile(self):
        for field, value in (('unit', 'other.service'), ('port', 7702),
                             ('source_commit', '0' * 40), ('candidate_binary_sha256', '0' * 64),
                             ('release_id', '../../bad'), ('recovery_mode', 'old-version'),
                             ('authorization_mode', 'managed-policy'),
                             ('oauth_binary_sha256', '0' * 64), ('fleet_snapshot_sha256', 'missing')):
            with self.subTest(field=field):
                plan = fixture('target')
                plan[field] = value
                with self.assertRaises(ValueError): contract.validate(plan)

    def test_extra_fields_are_rejected(self):
        plan = fixture('shared')
        plan['unexpected'] = True
        with self.assertRaises(ValueError): contract.validate(plan)


if __name__ == '__main__':
    unittest.main()
