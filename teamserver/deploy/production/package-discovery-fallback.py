#!/usr/bin/env python3
"""Package a read-only copy of the live 7703 release as rollback evidence."""
import argparse
import hashlib
import json
from pathlib import Path
import tarfile

import mcp_discovery_release_controller as d


def sha(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--input', required=True)
    p.add_argument('--output', required=True)
    a = p.parse_args()
    source = Path(a.input)
    output = Path(a.output)
    d.require(source.is_dir() and source.resolve() == source, 'input directory must be canonical')
    d.require(output.is_absolute() and not output.exists() and output.parent.is_dir()
              and output.parent.resolve() == output.parent, 'new absolute output required')
    files = {'wecom-mcp-team', 'discovery-policy.json'}
    d.require({p.name for p in source.iterdir()} == files, 'fallback input contains unexpected files')
    for name in files:
        d.require(source.joinpath(name).is_file() and not source.joinpath(name).is_symlink(), 'fallback file invalid')
    d.require(sha(source / 'wecom-mcp-team') == d.CURRENT_BINARY_SHA256, 'fallback binary is not the live process')
    output.mkdir(mode=0o700)
    release = output / d.CURRENT_RELEASE_ID
    release.mkdir(mode=0o700)
    for name in files:
        data = source.joinpath(name).read_bytes()
        target = release / name
        target.write_bytes(data)
        target.chmod(0o500 if name == 'wecom-mcp-team' else 0o400)
    manifest = {
        'schema_version': 1,
        'kind': 'live-runtime-fallback-evidence',
        'profile': d.PROFILE,
        'release_id': d.CURRENT_RELEASE_ID,
        'service_unit': d.UNIT,
        'loopback': d.LOOPBACK,
        'resources': d.RESOURCES,
        'observed_fleet_snapshot': d.observed_fleet_snapshot(),
        'fleet_state_vector': d.fleet_state_vector(),
        'fleet_state_vector_sha256': d.fleet_state_vector_sha256(),
        'binary_sha256': sha(release / 'wecom-mcp-team'),
        'policy_sha256': sha(release / 'discovery-policy.json'),
        'source': 'read-only production runtime copy',
        'production_changed': False,
    }
    manifest_path = release / 'fallback-manifest.json'
    manifest_path.write_text(json.dumps(manifest, indent=2) + '\n')
    manifest_path.chmod(0o400)
    sums = release / 'SHA256SUMS'
    sums.write_text(''.join(sha(release / name) + '  ' + name + '\n'
                           for name in ('discovery-policy.json', 'fallback-manifest.json', 'wecom-mcp-team')))
    sums.chmod(0o400)
    release.chmod(0o500)
    archive = output / (d.CURRENT_RELEASE_ID + '-fallback.tar.gz')
    with tarfile.open(archive, 'w:gz') as tar:
        tar.add(release, arcname=release.name, recursive=True)
    archive.chmod(0o400)
    print(json.dumps({
        'state': 'fallback-evidence',
        'archive': str(archive),
        'archive_sha256': sha(archive),
        'manifest_sha256': sha(manifest_path),
        'catalogue_sha256': sha(sums),
        'production_changed': False,
    }, sort_keys=True))


if __name__ == '__main__':
    main()
