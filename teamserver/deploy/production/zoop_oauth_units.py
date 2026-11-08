"""Construct exact isolated OAuth unit bytes without changing other options."""
import re

RID = re.compile(r'^[0-9]{8}T[0-9]{6}Z-[a-zA-Z0-9._-]+$')
SHA = re.compile(r'^[0-9a-f]{64}$')
PREFIX = b'ExecStart=/usr/bin/python3 -I /usr/local/sbin/zoop-next-env-exec.py '


def rewrite(current, old_args, new_args, probe=False):
    if len(old_args) != 5 or len(new_args) != 5:
        raise ValueError('five exact arguments required')
    for args in (old_args, new_args):
        if not (RID.fullmatch(args[0]) and SHA.fullmatch(args[1]) and RID.fullmatch(args[2])
                and SHA.fullmatch(args[3]) and SHA.fullmatch(args[4])):
            raise ValueError('invalid release arguments')
    old = PREFIX + ' '.join(old_args).encode() + b'\n'
    if current.count(old) != 1 or current.count(b'ExecStart=') != 1:
        raise ValueError('unit does not match expected startup')
    start = PREFIX + ' '.join(new_args).encode() + b'\n'
    if probe:
        start = start.replace(b'zoop-next-env-exec.py ', b'zoop-oauth-probe-env-exec.py ', 1)
        start = start[:-1] + b' 7505\n'
        # The probe is temporary and must not survive a boot.
        if current.count(b'[Install]\nWantedBy=multi-user.target\n') != 1:
            raise ValueError('unexpected install section')
        if current.count(b'Restart=on-failure\n') != 1:
            raise ValueError('unexpected restart policy')
        current = current.replace(b'Restart=on-failure\n', b'Restart=no\n')
        current = current.replace(b'[Install]\nWantedBy=multi-user.target\n', b'')
    return current.replace(old, start, 1)
