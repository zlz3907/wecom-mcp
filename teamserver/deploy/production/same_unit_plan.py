"""Validate the exact reviewed bootstrap plan before any live operation."""
from pathlib import Path
import re

SOURCE = 'dbef937502fa2c4f005f97afbe1dea7db7bc9dc6'
SOURCE_TREE = '8755b036994079ff6ba994a5d4231f5330b47588'
CI_URL = 'https://github.com/zlz3907/wecom-mcp/actions/runs/42516298366'
CI_EVIDENCE_SHA = '734636620bbff22845443e4a16be0c6e524e72e7297adaa7476d3227dcad881b'
POLICY_SHA = '7d45dd145b1c4f604619046e305e031d42e807e5c7ee32450015d04123ccbba6'
BINARY_SHA = '709f6f940f3155bf6174e7a978f08cc7a422f7bedfa4744b825aea71e7b76a04'
RELEASE_LOCKS = ['/var/lib/gnas-release-controller/mcp-policy.lock',
                 '/var/lib/gnas-oauth-isolated/release.lock',
                 '/var/lib/wecom-mcp-isolated/release.lock',
                 '/var/lib/wecom-mcp-release/release.lock']
PROFILES = {
    'target': {
        'unit': 'wecom-mcp-zhycit-migration.service', 'port': 7709,
        'fragment_sha256': '923e83cd55bd52e02afd7836d9bbcaddcd0088ed8962ff9180c87b32bed0d2bf',
        'previous_binary_sha256': '82fd10c16f5ccf72219818a9efc00e0b79f4e70a8726faf071059b6a6344fa03',
        'oauth_binary_sha256': 'cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1',
        'dropin_sha256': '897fd309f5cf131d3558186d30b10f12d9e1706cfef47fcc7c2f707033aa5cad',
        'release_root': '/var/lib/wecom-mcp-isolated/releases',
        'release_locks': RELEASE_LOCKS,
        'environment_files': ['/etc/wecom-mcp/gmzoop.env', '/etc/wecom-mcp/zoop-schema-admin.env'],
    },
    'shared': {
        'unit': 'wecom-mcp-discovery-21462d2.service', 'port': 7703,
        'fragment_sha256': '4caa2abe6bed4650faee60084b1657d4e350c3724ead3d4815c7b17a6870d098',
        'previous_binary_sha256': '988055b38a6963afe1f6cfe3b2bdb9b691483cb599974a450efae535d356ad22',
        'oauth_binary_sha256': 'a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e',
        'dropin_sha256': '22dc82ab945890b96c21a50c65ebe45d4e61191b9ff1d8a286ef3f38325292ad',
        'release_root': '/var/lib/wecom-mcp-isolated/releases',
        'release_locks': RELEASE_LOCKS,
        'environment_files': ['/etc/wecom-mcp/gmzoop.env'],
    },
}


def validate(plan):
    if not isinstance(plan, dict) or plan.get('profile') not in PROFILES:
        raise ValueError('unknown bootstrap profile')
    profile = PROFILES[plan['profile']]
    required = {'schema_version', 'environment', 'profile', 'unit', 'port', 'fragment_sha256',
                'previous_binary_sha256', 'oauth_binary_sha256', 'previous_policy_sha256',
                'release_id', 'source_commit', 'source_tree', 'ci_url', 'candidate_binary_sha256',
                'candidate_policy_sha256', 'dropin_sha256', 'ci_evidence_sha256', 'smoke_sha256',
                'release_root', 'release_locks', 'environment_files', 'environment_file_sha256',
                'fleet_snapshot_sha256', 'recovery_mode'}
    if set(plan) != required or plan.get('schema_version') != 1 or plan.get('environment') != 'zhycit-prod-01':
        raise ValueError('bootstrap plan shape mismatch')
    if any(plan.get(key) != value for key, value in profile.items()):
        raise ValueError('bootstrap profile baseline mismatch')
    if (plan['source_commit'] != SOURCE or plan['source_tree'] != SOURCE_TREE or plan['ci_url'] != CI_URL or
            plan['previous_policy_sha256'] != POLICY_SHA or plan['candidate_policy_sha256'] != POLICY_SHA or
            plan['candidate_binary_sha256'] != BINARY_SHA or plan['ci_evidence_sha256'] != CI_EVIDENCE_SHA or
            plan['recovery_mode'] != 'same-candidate-or-closed'):
        raise ValueError('candidate contract mismatch')
    if plan['release_id'] != '20261108T194434Z-dbef937502fa':
        raise ValueError('invalid candidate release id')
    for key in ('dropin_sha256', 'ci_evidence_sha256', 'smoke_sha256', 'fleet_snapshot_sha256'):
        if not isinstance(plan[key], str) or not re.fullmatch(r'[0-9a-f]{64}', plan[key]):
            raise ValueError('invalid evidence digest')
    files = plan['environment_file_sha256']
    if (not isinstance(files, dict) or set(files) != set(profile['environment_files']) or
            any(not isinstance(value, str) or not re.fullmatch(r'[0-9a-f]{64}', value) for value in files.values())):
        raise ValueError('invalid protected environment fingerprints')
    return Path(profile['release_root']) / plan['release_id'] / 'wecom-mcp-team'
