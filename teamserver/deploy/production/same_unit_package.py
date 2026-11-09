"""Verify an immutable same-unit bootstrap package before upload or use."""
import hashlib
import json
import os
from pathlib import Path
import stat

import same_unit_plan as plan
from same_unit_systemd import Unit

FILES = ('wecom-mcp-team', 'discovery-policy.json', 'target.service.conf',
         'shared.service.conf', 'ci-evidence.json', 'manifest.json', 'SHA256SUMS')


def verify(directory, owner):
    directory = Path(directory)
    info = directory.lstat()
    if (not directory.is_absolute() or directory.resolve() != directory or not stat.S_ISDIR(info.st_mode) or
            info.st_uid != owner or stat.S_IMODE(info.st_mode) != 0o500 or
            {path.name for path in directory.iterdir()} != set(FILES)):
        raise ValueError('untrusted candidate directory')
    values = {}
    for name in FILES:
        path = directory / name
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, 'rb') as stream:
            entry = os.fstat(stream.fileno())
            expected_mode = 0o500 if name == 'wecom-mcp-team' else 0o400
            if not stat.S_ISREG(entry.st_mode) or entry.st_uid != owner or stat.S_IMODE(entry.st_mode) != expected_mode:
                raise ValueError('untrusted candidate file')
            data = stream.read(80 * 1024 * 1024 + 1)
            if len(data) > 80 * 1024 * 1024:
                raise ValueError('candidate file too large')
            values[name] = data
    expected_sums = ''.join(hashlib.sha256(values[name]).hexdigest() + '  ' + name + '\n'
                            for name in sorted(set(FILES) - {'SHA256SUMS'})).encode()
    if values['SHA256SUMS'] != expected_sums:
        raise ValueError('candidate checksum mismatch')
    manifest = json.loads(values['manifest.json'])
    expected = {'schema_version': 1, 'component': 'wecom-mcp-same-unit-bootstrap',
                'environment': 'zhycit-prod-01', 'release_id': '20261108T194434Z-dbef937502fa',
                'source_commit': plan.SOURCE, 'source_tree': plan.SOURCE_TREE,
                'ci_url': plan.CI_URL, 'recovery_mode': 'same-candidate-or-closed',
                'files': {name: hashlib.sha256(values[name]).hexdigest()
                          for name in FILES if name not in ('manifest.json', 'SHA256SUMS')}}
    if manifest != expected or expected['files'] != {
            'wecom-mcp-team': plan.BINARY_SHA, 'discovery-policy.json': plan.POLICY_SHA,
            'target.service.conf': plan.PROFILES['target']['dropin_sha256'],
            'shared.service.conf': plan.PROFILES['shared']['dropin_sha256'],
            'ci-evidence.json': plan.CI_EVIDENCE_SHA}:
        raise ValueError('candidate manifest mismatch')
    for profile in ('target', 'shared'):
        binding = plan.PROFILES[profile]
        candidate = Path(binding['release_root']) / expected['release_id'] / 'wecom-mcp-team'
        Unit(binding['unit'], binding['fragment_sha256'], plan.BINARY_SHA).candidate_commands(
            values[profile + '.service.conf'], binding['dropin_sha256'], candidate)
    return hashlib.sha256(values['manifest.json']).hexdigest()
