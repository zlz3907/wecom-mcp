"""Fail closed if the pinned GNAS authorization authority has drifted.

This phase only supports two exact pre-policy binaries in all-members mode.
The shared old binary does not yet grant all tools; completing fleet rollout
also requires its separately controlled GNAS upgrade.
"""
import json
from pathlib import Path
import re
import subprocess

from same_unit_systemd import digest

AUTHORITIES = {
    'target': ('gnas-oauth-zhycit-next.service', 'cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1'),
    'shared': ('gnas-server.service', 'a655935e3ab5cef0fb2aff9e2af770c0457c136cb77d3e0bee0f9de5a579553e'),
}
ENV_KEYS = (b'GNAS_OAUTH21_EMPLOYEE_AUTHORIZATION_MODE', b'GNAS_OAUTH21_EMPLOYEE_SCOPES_JSON',
            b'GNAS_OAUTH21_EMPLOYEE_TOOLS_JSON')


def evaluate_environ(raw):
    if not isinstance(raw, bytes) or len(raw) > 1024 * 1024:
        raise ValueError('authority environment unavailable')
    values = {}
    for row in raw.split(b'\0'):
        key, separator, value = row.partition(b'=')
        if separator and key in ENV_KEYS:
            if key in values:
                raise ValueError('ambiguous authority environment')
            values[key] = value
    if set(values) != set(ENV_KEYS) or values[ENV_KEYS[0]] != b'wecom_all_members':
        raise ValueError('authority is not in all-members mode')
    try:
        scopes, tools = (json.loads(values[key]) for key in ENV_KEYS[1:])
    except (ValueError, UnicodeDecodeError) as error:
        raise ValueError('invalid authority grants') from error
    if (not isinstance(scopes, list) or len(scopes) != 2 or set(scopes) != {'zoop.read', 'zoop.write'} or
            not isinstance(tools, list) or not tools or any(not isinstance(t, str) or not t for t in tools)):
        raise ValueError('authority grants changed')


def verify(profile, proc=Path('/proc'), runner=subprocess.run):
    if profile not in AUTHORITIES:
        raise ValueError('unknown authority')
    unit, expected_sha = AUTHORITIES[profile]
    def props():
        result = runner(['/usr/bin/systemctl', 'show', unit, '-p', 'ActiveState', '-p', 'MainPID', '-p', 'NRestarts'],
                        check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10)
        rows = result.stdout.decode().splitlines()
        data = dict(row.split('=', 1) for row in rows if '=' in row)
        if len(rows) != 3 or len(data) != 3 or set(data) != {'ActiveState', 'MainPID', 'NRestarts'}:
            raise ValueError('ambiguous authority service state')
        if (data['ActiveState'] != 'active' or data['NRestarts'] != '0' or
                not re.fullmatch(r'[0-9]+', data['MainPID']) or int(data['MainPID']) < 2):
            raise ValueError('authority unavailable')
        return data
    before = props()
    process = proc / before['MainPID']
    if digest(process / 'exe') != expected_sha:
        raise ValueError('authority binary changed')
    evaluate_environ((process / 'environ').read_bytes())
    if props() != before or digest(process / 'exe') != expected_sha:
        raise ValueError('authority changed during inspection')
    return {'unit': unit, 'pid': int(before['MainPID']), 'binary_sha256': expected_sha,
            'all_members': True, 'policy_contract': 'absent-in-pinned-binary'}
