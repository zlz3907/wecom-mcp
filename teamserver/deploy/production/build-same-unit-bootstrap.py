#!/usr/bin/env python3
"""Package the already verified shared MCP binary for both original units."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import stat

import same_unit_plan as contract
from same_unit_systemd import Unit


def read_exact(path, expected, limit):
    path = Path(path)
    if not path.is_absolute() or path.resolve() != path:
        raise ValueError('input must be canonical')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_mode & 0o022:
            raise ValueError('input is not a protected regular file')
        data = stream.read(limit + 1)
    if len(data) > limit or hashlib.sha256(data).hexdigest() != expected:
        raise ValueError('input digest mismatch')
    return data


def write_new(path, data, mode):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, 'wb') as stream:
        os.fchmod(stream.fileno(), mode)
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def main():
    parser = argparse.ArgumentParser()
    for name in ('binary', 'policy', 'target_dropin', 'shared_dropin', 'ci_evidence', 'output'):
        parser.add_argument('--' + name.replace('_', '-'), required=True)
    args = parser.parse_args()
    out = Path(args.output)
    parent = out.parent.lstat()
    if (not out.is_absolute() or out.parent.resolve() != out.parent or out.exists() or out.is_symlink() or
            not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.getuid() or parent.st_mode & 0o022):
        raise ValueError('output parent or target is unsafe')
    contents = {
        'wecom-mcp-team': read_exact(args.binary, contract.BINARY_SHA, 80 * 1024 * 1024),
        'discovery-policy.json': read_exact(args.policy, contract.POLICY_SHA, 65536),
        'target.service.conf': read_exact(args.target_dropin, contract.PROFILES['target']['dropin_sha256'], 65536),
        'shared.service.conf': read_exact(args.shared_dropin, contract.PROFILES['shared']['dropin_sha256'], 65536),
        'ci-evidence.json': read_exact(args.ci_evidence, contract.CI_EVIDENCE_SHA, 65536),
    }
    release_id = contract.RELEASE_ID
    for profile in ('target', 'shared'):
        item = contract.PROFILES[profile]
        candidate = Path(item['release_root']) / release_id / 'wecom-mcp-team'
        Unit(item['unit'], item['fragment_sha256'], contract.BINARY_SHA).candidate_commands(
            contents[profile + '.service.conf'], item['dropin_sha256'], candidate)
    manifest = {'schema_version': 1, 'component': 'wecom-mcp-same-unit-bootstrap',
                'environment': 'zhycit-prod-01', 'release_id': release_id,
                'source_commit': contract.SOURCE, 'source_tree': contract.SOURCE_TREE,
                'ci_url': contract.CI_URL, 'recovery_mode': 'same-candidate-or-closed',
                'files': {name: hashlib.sha256(data).hexdigest() for name, data in contents.items()}}
    contents['manifest.json'] = (json.dumps(manifest, sort_keys=True, indent=2) + '\n').encode()
    sums = ''.join(hashlib.sha256(contents[name]).hexdigest() + '  ' + name + '\n' for name in sorted(contents))
    contents['SHA256SUMS'] = sums.encode()
    out.mkdir(mode=0o700)
    for name, data in contents.items():
        write_new(out / name, data, 0o500 if name == 'wecom-mcp-team' else 0o400)
    out.chmod(0o500)
    fd = os.open(out, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)
    fd = os.open(out.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)
    print(json.dumps({'state': 'local-candidate', 'release_id': release_id,
                      'manifest_sha256': hashlib.sha256(contents['manifest.json']).hexdigest(),
                      'production_changed': False}, sort_keys=True))


if __name__ == '__main__':
    main()
