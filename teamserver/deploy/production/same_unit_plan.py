"""Validate the exact reviewed bootstrap plan before any live operation."""
from pathlib import Path
import re

SOURCE = 'eab7c778c79fff9198b4a52fe8bd6867e7acc5fe'
SOURCE_TREE = '1d025444aecb8ecc325ce3476ab9eddb6eefede3'
RELEASE_ID = '20261109T074119Z-eab7c778c79f'
CI_URL = 'https://github.com/zlz3907/wecom-mcp/actions/runs/42580859163'
CI_EVIDENCE_SHA = 'ef08dc70235ea3b713313747ed3b33e964355d88c91e86278f46f07ffc59668d'
POLICY_SHA = '7d45dd145b1c4f604619046e305e031d42e807e5c7ee32450015d04123ccbba6'
RECOVERY_AUTHORITY_SHAS = {'target': ['cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1'],
                           'shared': ['a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e',
                                      '2c945014b8088a0728b4416eb34c29b452bf05f8a9ece8843152a7d632306c7b']}
BINARY_SHA = 'c8af1b1dd27f4dd0323eee95bd16258441dd0de7d016000f724b6e0a81ff0005'
RELEASE_LOCKS = ['/home/product/services/gnas-service-gin/release.lock',
                 '/var/lib/gnas-oauth-isolated/release.lock',
                 '/var/lib/wecom-mcp-isolated/release.lock',
                 '/var/lib/wecom-mcp-release/release.lock']
PROFILES = {
    'target': {
        'unit': 'wecom-mcp-zhycit-migration.service', 'port': 7709,
        'fragment_sha256': '923e83cd55bd52e02afd7836d9bbcaddcd0088ed8962ff9180c87b32bed0d2bf',
        'previous_binary_sha256': '82fd10c16f5ccf72219818a9efc00e0b79f4e70a8726faf071059b6a6344fa03',
        'oauth_binary_sha256': 'cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1',
        'dropin_sha256': 'b687243fbbbda2b3fa6091995ba0c974d766de85f04360fe0fa065b966597432',
        'release_root': '/var/lib/wecom-mcp-isolated/releases',
        'release_locks': RELEASE_LOCKS,
        'environment_files': ['/etc/wecom-mcp/gmzoop.env', '/etc/wecom-mcp/zoop-schema-admin.env'],
    },
    'shared': {
        'unit': 'wecom-mcp-discovery-21462d2.service', 'port': 7703,
        'fragment_sha256': '4caa2abe6bed4650faee60084b1657d4e350c3724ead3d4815c7b17a6870d098',
        'previous_binary_sha256': '988055b38a6963afe1f6cfe3b2bdb9b691483cb599974a450efae535d356ad22',
        'oauth_binary_sha256': 'a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e',
        'dropin_sha256': '5c83813d40f47842d311a909e857e9b0dd06ed2c217134a5d7bb7f152e7abc90',
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
                'fleet_snapshot_sha256', 'recovery_mode', 'authorization_mode', 'previous_pid', 'authority_pids', 'recovery_authority_sha256'}
    if set(plan) != required or plan.get('schema_version') != 1 or plan.get('environment') != 'zhycit-prod-01':
        raise ValueError('bootstrap plan shape mismatch')
    if any(plan.get(key) != value for key, value in profile.items()):
        raise ValueError('bootstrap profile baseline mismatch')
    if (plan['source_commit'] != SOURCE or plan['source_tree'] != SOURCE_TREE or plan['ci_url'] != CI_URL or
            plan['previous_policy_sha256'] != POLICY_SHA or plan['candidate_policy_sha256'] != POLICY_SHA or
            plan['candidate_binary_sha256'] != BINARY_SHA or plan['ci_evidence_sha256'] != CI_EVIDENCE_SHA or
            plan['recovery_mode'] != 'same-candidate-or-closed' or
            plan['authorization_mode'] != 'wecom-all-members-policy-disabled' or
            plan['recovery_authority_sha256'] != RECOVERY_AUTHORITY_SHAS):
        raise ValueError('candidate contract mismatch')
    if plan['release_id'] != RELEASE_ID:
        raise ValueError('invalid candidate release id')
    for key in ('dropin_sha256', 'ci_evidence_sha256', 'smoke_sha256', 'fleet_snapshot_sha256'):
        if not isinstance(plan[key], str) or not re.fullmatch(r'[0-9a-f]{64}', plan[key]):
            raise ValueError('invalid evidence digest')
    if (type(plan['previous_pid']) is not int or plan['previous_pid'] < 2 or
            not isinstance(plan['authority_pids'], dict) or set(plan['authority_pids']) != {'target', 'shared'} or
            any(type(pid) is not int or pid < 2 for pid in plan['authority_pids'].values())):
        raise ValueError('invalid expected process identities')
    files = plan['environment_file_sha256']
    if (not isinstance(files, dict) or set(files) != set(profile['environment_files']) or
            any(not isinstance(value, str) or not re.fullmatch(r'[0-9a-f]{64}', value) for value in files.values())):
        raise ValueError('invalid protected environment fingerprints')
    return Path(profile['release_root']) / plan['release_id'] / 'wecom-mcp-team'
