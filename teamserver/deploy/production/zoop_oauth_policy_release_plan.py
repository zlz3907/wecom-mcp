"""Read-only plan for upgrading an already adaptive isolated OAuth unit."""
import hashlib
import json
import re


def _sha(data):
    return hashlib.sha256(data).hexdigest()


def build(units, manifest_bytes, binary_sha, original_unit, old_release, old_sha,
          shared_release, shared_sha, grant_sha):
    manifest = json.loads(manifest_bytes)
    if not (manifest.get('schema_version') == 2 and manifest.get('environment') == 'zhycit-prod-01'
            and manifest.get('artifact') == 'gnas-service-gin' and manifest.get('source_clean') is True
            and manifest.get('target') == {'goos': 'linux', 'goarch': 'amd64', 'cgo_enabled': False}
            and manifest.get('binary') == {'name': 'GNASServer', 'sha256': binary_sha}
            and manifest.get('previous_release_id') == old_release
            and manifest.get('previous_binary_sha256') == old_sha
            and re.fullmatch(r'[0-9a-f]{40}', manifest.get('git_commit', ''))
            and re.fullmatch(r'[0-9a-f]{40}', manifest.get('git_tree', ''))
            and units.RID.fullmatch(shared_release) and re.fullmatch(r'[0-9a-f]{64}', shared_sha)):
        raise ValueError('manifest does not bind the requested isolated predecessor')
    rid = manifest['release_id']
    candidate = units.upgrade_adaptive(original_unit, old_release, old_sha, grant_sha, rid, binary_sha, grant_sha)
    rollback = units.upgrade_adaptive(original_unit, old_release, old_sha, grant_sha, old_release, old_sha, grant_sha)
    candidate_probe = units.upgrade_adaptive(original_unit, old_release, old_sha, grant_sha, rid, binary_sha, grant_sha, probe=True)
    rollback_probe = units.upgrade_adaptive(original_unit, old_release, old_sha, grant_sha, old_release, old_sha, grant_sha, probe=True)
    if rollback != original_unit:
        raise ValueError('rollback does not reproduce the current isolated unit')
    return {
        'environment': 'zhycit-prod-01', 'target': 'zhycit.com',
        'candidate_release': rid, 'candidate_sha256': binary_sha,
        'candidate_manifest_sha256': _sha(manifest_bytes), 'source_commit': manifest['git_commit'],
        'source_tree': manifest['git_tree'], 'old_release': old_release, 'old_sha256': old_sha,
        'shared_release': shared_release, 'shared_sha256': shared_sha, 'grant_sha256': grant_sha,
        'old_unit_sha256': _sha(original_unit), 'candidate_unit_sha256': _sha(candidate),
        'rollback_unit_sha256': _sha(rollback), 'candidate_probe_unit_sha256': _sha(candidate_probe),
        'rollback_probe_unit_sha256': _sha(rollback_probe),
    }
