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


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--candidate', required=True)
    p.add_argument('--archive', required=True)
    p.add_argument('--fallback-archive', required=True)
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
