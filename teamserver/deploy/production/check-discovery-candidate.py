#!/usr/bin/env python3
"""Offline check-only gate for a 7703 discovery candidate.

It validates only local immutable artifacts and never reads production state,
starts a service, changes Nginx, or consumes a receipt.
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import tarfile

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('discovery_controller', HERE / 'mcp_discovery_release_controller.py')
if spec is None or spec.loader is None:
    raise RuntimeError('discovery controller is unavailable')
d = importlib.util.module_from_spec(spec)
spec.loader.exec_module(d)


def sha(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def verify_candidate_archive(candidate, archive):
    # Match the flat archive format produced by stage-candidate.py. Never
    # extract untrusted member names to the filesystem.
    names = set(d.FILES) | {'SHA256SUMS'}
    with tarfile.open(archive, 'r:*') as tar:
        members = tar.getmembers()
        d.require(len(members) == len(names) and {m.name for m in members} == names and
                  all(m.isfile() and 0 <= m.size <= 64 * 1024 * 1024 for m in members),
                  'unexpected candidate archive content')
        for member in members:
            stream = tar.extractfile(member)
            d.require(stream is not None and hashlib.sha256(stream.read()).hexdigest() == sha(candidate / member.name),
                      'candidate archive differs from verified directory')


def verify_evidence(checked, archive, manifest, catalogue, smoke):
    """Bind the supplied evidence bytes to the candidate's frozen baseline."""
    for path, field in ((archive, 'expected_current_archive_sha256'),
                        (manifest, 'expected_current_manifest_sha256'),
                        (catalogue, 'expected_current_catalogue_sha256'),
                        (smoke, 'legacy_smoke_sha256')):
        d.require(path.is_file() and path.resolve() == path and not path.is_symlink(),
                  'evidence path must be a canonical regular file')
        d.require(sha(path) == checked[field], 'evidence digest mismatch: ' + field)
    info = json.loads(manifest.read_bytes())
    release = checked['expected_current_release_id']
    d.require(info.get('release_id') == release and
              info.get('binary_sha256') == checked['expected_current_binary_sha256'] and
              info.get('policy_sha256') == checked['expected_current_policy_sha256'],
              'fallback manifest baseline mismatch')
    expected = {release + '/' + name for name in
                ('wecom-mcp-team', 'discovery-policy.json', 'fallback-manifest.json', 'SHA256SUMS')}
    with tarfile.open(archive, 'r:gz') as tar:
        entries = tar.getmembers()
        files = [item for item in entries if item.isfile()]
        d.require(all(item.isfile() or (item.isdir() and item.name.rstrip('/') == release) for item in entries)
                  and len(files) == len(expected) and {item.name for item in files} == expected,
                  'unexpected fallback archive content')
        data = {}
        for item in files:
            d.require(0 <= item.size <= 64 * 1024 * 1024, 'fallback archive entry too large')
            stream = tar.extractfile(item)
            d.require(stream is not None, 'fallback archive entry unreadable')
            data[item.name.rsplit('/', 1)[1]] = stream.read()
        d.require(data['fallback-manifest.json'] == manifest.read_bytes() and
                  data['SHA256SUMS'] == catalogue.read_bytes(), 'fallback archive metadata mismatch')
        d.require(hashlib.sha256(data['wecom-mcp-team']).hexdigest() == info.get('binary_sha256') and
                  hashlib.sha256(data['discovery-policy.json']).hexdigest() == info.get('policy_sha256'),
                  'fallback archive payload mismatch')
        names = ('discovery-policy.json', 'fallback-manifest.json', 'wecom-mcp-team')
        expected_sums = ''.join(hashlib.sha256(data[name]).hexdigest() + '  ' + name + '\n'
                                for name in names).encode()
        d.require(data['SHA256SUMS'] == expected_sums, 'fallback catalogue invalid')


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--candidate', required=True)
    p.add_argument('--archive', required=True)
    p.add_argument('--fallback-archive', required=True)
    p.add_argument('--fallback-manifest', required=True)
    p.add_argument('--fallback-catalogue', required=True)
    p.add_argument('--legacy-smoke', required=True)
    a = p.parse_args()
    candidate = Path(a.candidate)
    archive = Path(a.archive)
    fallback = Path(a.fallback_archive)
    d.require(candidate.is_dir() and candidate.resolve() == candidate, 'candidate directory must be canonical')
    d.require(archive.is_file() and archive.resolve() == archive, 'candidate archive must be canonical')
    d.require(fallback.is_file() and fallback.resolve() == fallback, 'fallback archive must be canonical')
    manifest = json.loads((candidate / 'manifest.json').read_text())
    original_regular = d.c.regular
    d.c.regular = lambda path, root=False: original_regular(path, False)
    try:
        checked = d.verify(candidate, manifest['release_id'])
    finally:
        d.c.regular = original_regular
    verify_candidate_archive(candidate, archive)
    verify_evidence(checked, fallback, Path(a.fallback_manifest), Path(a.fallback_catalogue), Path(a.legacy_smoke))
    service = (candidate / 'service.conf').read_bytes()
    recovery = (candidate / 'recovery.conf').read_bytes()
    d.require(b'127.0.0.1:7703' in service and b'127.0.0.1:7703' in recovery, 'candidate is not 7703-bound')
    d.require(b'127.0.0.1:7702' not in service and b'127.0.0.1:7702' not in recovery, 'legacy 7702 target leaked into candidate')
    print(json.dumps({
        'state': 'check-only-passed',
        'profile': checked['profile'],
        'release_id': checked['release_id'],
        'candidate_archive_sha256': sha(archive),
        'fallback_archive_sha256': sha(fallback),
        'production_changed': False,
        'receipt_consumed': False,
    }, sort_keys=True))


if __name__ == '__main__':
    main()
