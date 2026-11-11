"""Deterministically derive guarded old and isolated candidate unit bytes."""
import hashlib

OLD_RELEASE = '/var/lib/wecom-mcp-isolated/releases/20261107T-preview-actor-8bec94b/'
GUARD = '/usr/local/sbin/target-bootstrap-old-start-guard.py'


def original_commands(unit, expected_sha):
    if not isinstance(unit, bytes) or hashlib.sha256(unit).hexdigest() != expected_sha:
        raise ValueError('old unit fingerprint changed')
    try:
        lines = unit.decode('utf-8').splitlines()
    except UnicodeDecodeError as error:
        raise ValueError('old unit is not UTF-8') from error
    pre = [line[len('ExecStartPre='):] for line in lines if line.startswith('ExecStartPre=')]
    start = [line[len('ExecStart='):] for line in lines if line.startswith('ExecStart=')]
    if len(pre) != 1 or len(start) != 1 or not pre[0] or not start[0]:
        raise ValueError('old unit start commands are not unique')
    if any(OLD_RELEASE not in command or '--listen 127.0.0.1:7709' not in command for command in (pre[0], start[0])):
        raise ValueError('old unit target changed')
    if '--check-config' not in pre[0] or '--check-config' in start[0]:
        raise ValueError('old unit check command changed')
    if 'User=wecom-mcp-gmzoop' not in lines or 'KillMode=control-group' not in lines:
        raise ValueError('old unit identity or cgroup behavior changed')
    return pre[0], start[0]


def guarded_dropin(unit, expected_sha):
    pre, start = original_commands(unit, expected_sha)
    return ('[Service]\nExecStartPre=\n'
            'ExecStartPre=/usr/bin/python3 -I ' + GUARD + ' --check\n'
            'ExecStartPre=' + pre + '\nExecStart=\n'
            'ExecStart=/usr/bin/python3 -I ' + GUARD + ' --exec ' + start + '\n').encode()


def candidate_unit(unit, expected_sha, release_id):
    original_commands(unit, expected_sha)
    if not isinstance(release_id, str) or len(release_id) != 29 or release_id[8] != 'T' or release_id[15] != 'Z' or release_id[16] != '-':
        raise ValueError('invalid release id')
    if not release_id[:8].isdigit() or not release_id[9:15].isdigit() or any(c not in '0123456789abcdef' for c in release_id[17:]):
        raise ValueError('invalid release id')
    new_release = '/var/lib/wecom-mcp-isolated/releases/' + release_id + '/'
    if unit.count(OLD_RELEASE.encode()) != 4 or unit.count(b'--listen 127.0.0.1:7709') != 2:
        raise ValueError('old unit substitutions are not exact')
    return unit.replace(OLD_RELEASE.encode(), new_release.encode()).replace(b'--listen 127.0.0.1:7709', b'--listen 127.0.0.1:7710')
