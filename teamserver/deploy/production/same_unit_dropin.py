"""Generate a same-port replacement for a reviewed live unit fragment."""
import hashlib
import re
import shlex

import same_unit_plan as contract

OLD_RELEASES = {
    'target': '/var/lib/wecom-mcp-isolated/releases/20261107T-preview-actor-8bec94b/',
    'shared': '/home/product/services/mcp/wecom/releases/20261105T001200-21462d2dfb22/',
}


def generate(profile_name, fragment, release_id):
    if profile_name not in contract.PROFILES or not isinstance(fragment, bytes):
        raise ValueError('unknown unit profile')
    profile = contract.PROFILES[profile_name]
    if hashlib.sha256(fragment).hexdigest() != profile['fragment_sha256']:
        raise ValueError('unit fragment drift')
    if not isinstance(release_id, str) or not re.fullmatch(r'[0-9]{8}T[0-9]{6}Z-' + contract.SOURCE[:12], release_id):
        raise ValueError('invalid release id')
    old = OLD_RELEASES[profile_name]
    new = profile['release_root'] + '/' + release_id + '/'
    lines = fragment.decode('utf-8').splitlines()
    commands = {}
    for field in ('ExecStartPre', 'ExecStart'):
        entries = [line[len(field) + 1:] for line in lines if line.startswith(field + '=')]
        if len(entries) != 1 or entries[0].count(old) != 2:
            raise ValueError('unexpected original unit command')
        args = shlex.split(entries[0])
        if (args.count(old + 'wecom-mcp-team') != 1 or args.count(old + 'discovery-policy.json') != 1 or
                args.count('--listen') != 1 or
                args[args.index('--listen') + 1:args.index('--listen') + 2] != ['127.0.0.1:' + str(profile['port'])] or
                ('--check-config' in args) != (field == 'ExecStartPre')):
            raise ValueError('unexpected original command arguments')
        commands[field] = entries[0].replace(old, new)
    return ('[Service]\nExecStartPre=\nExecStart=\nExecStartPre=' + commands['ExecStartPre'] +
            '\nExecStart=' + commands['ExecStart'] + '\n').encode()
