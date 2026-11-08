"""Read-only binding of an isolated OAuth release to its exact recovery units."""
import hashlib
import json
import re


def digest(data): return hashlib.sha256(data).hexdigest()


def build(units, manifest_bytes, binary_sha, original_unit, old_args, shared_release, shared_sha):
    manifest = json.loads(manifest_bytes)
    if not (manifest.get('schema_version') == 2 and manifest.get('environment') == 'zhycit-prod-01'
            and manifest.get('artifact') == 'gnas-service-gin' and manifest.get('source_clean') is True
            and manifest.get('target') == {'goos':'linux','goarch':'amd64','cgo_enabled':False}
            and manifest.get('binary') == {'name':'GNASServer','sha256':binary_sha}
            and manifest.get('previous_release_id') == shared_release
            and manifest.get('previous_binary_sha256') == shared_sha
            and re.fullmatch(r'[0-9a-f]{40}',manifest.get('git_commit',''))
            and re.fullmatch(r'[0-9a-f]{40}',manifest.get('git_tree',''))):
        raise ValueError('manifest does not bind the requested release and shared baseline')
    new_args = [manifest['release_id'],binary_sha,shared_release,shared_sha,old_args[4]]
    recovery_args = [old_args[0],old_args[1],shared_release,shared_sha,old_args[4]]
    candidate = units.rewrite(original_unit,old_args,new_args)
    rollback = units.rewrite(original_unit,old_args,recovery_args)
    candidate_probe = units.rewrite(original_unit,old_args,new_args,probe=True)
    rollback_probe = units.rewrite(original_unit,old_args,recovery_args,probe=True)
    return {
        'environment':'zhycit-prod-01','target':'zhycit.com',
        'candidate_release':manifest['release_id'],'candidate_sha256':binary_sha,
        'candidate_manifest_sha256':digest(manifest_bytes),'source_commit':manifest['git_commit'],
        'source_tree':manifest['git_tree'],'old_release':old_args[0],'old_sha256':old_args[1],
        'shared_release':shared_release,'shared_sha256':shared_sha,'grant_sha256':old_args[4],
        'old_unit_sha256':digest(original_unit),'candidate_unit_sha256':digest(candidate),
        'rollback_unit_sha256':digest(rollback),'candidate_probe_unit_sha256':digest(candidate_probe),
        'rollback_probe_unit_sha256':digest(rollback_probe),
    }
