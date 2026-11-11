#!/usr/bin/env python3
"""Build an immutable 7703 discovery candidate; never contacts production."""
import argparse
import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('discovery_controller', HERE / 'mcp_discovery_release_controller.py')
if spec is None or spec.loader is None:
    raise RuntimeError('discovery controller is unavailable')
d = importlib.util.module_from_spec(spec)
spec.loader.exec_module(d)
ROOT = HERE.parents[2]


def git(*args):
    return subprocess.check_output(['git', '-C', str(ROOT), *args], text=True).strip()


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--output', required=True)
    p.add_argument('--expected-runtime-path', required=True)
    p.add_argument('--expected-binary-sha256', required=True)
    p.add_argument('--expected-unit-fingerprint', required=True)
    p.add_argument('--expected-runtime-config-fingerprint', required=True)
    p.add_argument('--expected-current-unit-fragment-sha256', required=True)
    p.add_argument('--expected-gnas-release-id', required=True)
    p.add_argument('--expected-gnas-binary-sha256', required=True)
    p.add_argument('--expected-current-archive-sha256', required=True)
    p.add_argument('--expected-current-manifest-sha256', required=True)
    p.add_argument('--expected-current-catalogue-sha256', required=True)
    p.add_argument('--legacy-smoke-sha256', required=True)
    p.add_argument('--ci-url', default='', help='same-commit hosted CI URL; omission is local-only')
    a = p.parse_args()
    out = Path(a.output)
    d.require(out.is_absolute() and not out.exists() and out.parent.is_dir() and out.parent.resolve() == out.parent,
              'new absolute output with existing canonical parent required')
    d.require(not git('status', '--porcelain'), 'clean source required')
    commit, tree = git('rev-parse', 'HEAD'), git('rev-parse', 'HEAD^{tree}')
    rid = dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + commit[:12]
    for value in (a.expected_binary_sha256, a.expected_unit_fingerprint,
                  a.expected_runtime_config_fingerprint, a.expected_current_unit_fragment_sha256,
                  a.expected_gnas_binary_sha256, a.expected_current_archive_sha256,
                  a.expected_current_manifest_sha256, a.expected_current_catalogue_sha256,
                  a.legacy_smoke_sha256):
        d.require(d.SHA.fullmatch(value), 'invalid baseline SHA')
    previous = Path(a.expected_runtime_path)
    d.require(previous.parent.parent == d.RELEASES and previous.name == 'wecom-mcp-team',
              'expected actual process path required')
    out.mkdir(mode=0o700)
    env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH='amd64', GOAMD64='v1',
               GOWORK='off', GOENV='off', GOFLAGS='', GOEXPERIMENT='')
    subprocess.run(['go', 'build', '-trimpath', '-o', str(out / 'wecom-mcp-team'), './cmd/wecom-mcp-team'],
                   cwd=ROOT / 'teamserver', env=env, check=True)
    (out / 'discovery-policy.json').write_bytes((HERE.parent / 'wecom-mcp-gnas-discovery-policy.json.example').read_bytes())
    (out / 'service.conf').write_bytes(d.dropin(rid))
    (out / 'recovery.conf').write_bytes(d.recovery_dropin(rid))
    manifest = {
        'schema_version': 2,
        'recovery_mode': 'database-only',
        'recovery_hosts': list(d.HOSTS),
        'profile': d.PROFILE,
        'environment': d.ENVIRONMENT,
        'service_unit': d.UNIT,
        'loopback': d.LOOPBACK,
        'resources': dict(d.RESOURCES),
        'observed_fleet_snapshot': d.observed_fleet_snapshot(),
        'public_hosts': list(d.HOSTS),
        'readiness_file': str(d.READINESS_FILE),
        'release_id': rid,
        'git_commit': commit,
        'git_tree': tree,
        'source_clean': True,
        'target': 'linux/amd64',
        'files': {name: d.sha(out / name) for name in d.FILES[:-1]},
        'expected_runtime_path': a.expected_runtime_path,
        'expected_binary_sha256': a.expected_binary_sha256,
        'expected_unit_fingerprint': a.expected_unit_fingerprint,
        'expected_unmanaged_unit_fingerprint': a.expected_unit_fingerprint,
        'expected_runtime_config_fingerprint': a.expected_runtime_config_fingerprint,
        'expected_current_unit_fragment_sha256': a.expected_current_unit_fragment_sha256,
        'expected_gnas_release_id': a.expected_gnas_release_id,
        'expected_gnas_binary_sha256': a.expected_gnas_binary_sha256,
        'expected_current_release_id': d.CURRENT_RELEASE_ID,
        'expected_current_binary_sha256': d.CURRENT_BINARY_SHA256,
        'expected_current_policy_sha256': d.CURRENT_POLICY_SHA256,
        'expected_previous_release_id': d.PREVIOUS_RELEASE_ID,
        'expected_previous_binary_sha256': d.PREVIOUS_BINARY_SHA256,
        'expected_current_archive_sha256': a.expected_current_archive_sha256,
        'expected_current_manifest_sha256': a.expected_current_manifest_sha256,
        'expected_current_catalogue_sha256': a.expected_current_catalogue_sha256,
        'legacy_smoke_sha256': a.legacy_smoke_sha256,
        'fleet_state_vector': d.fleet_state_vector(),
        'fleet_state_vector_sha256': d.fleet_state_vector_sha256(),
        'ci_url': a.ci_url,
        'created_at': dt.datetime.now(dt.timezone.utc).isoformat(),
    }
    (out / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    (out / 'SHA256SUMS').write_text(''.join(d.sha(out / name) + '  ' + name + '\n' for name in sorted(d.FILES)))
    for file in out.iterdir():
        file.chmod(0o500 if file.name == 'wecom-mcp-team' else 0o400)
    out.chmod(0o500)
    # A developer build is not root-owned on macOS. Production admission will
    # re-read the archive into a root-owned immutable directory and execute
    # the same verifier there; local validation keeps every structural/SHA
    # check while only relaxing ownership for this not-yet-admitted artifact.
    original_regular = d.c.regular
    d.c.regular = lambda path, root=False: original_regular(path, False)
    try:
        d.verify(out, rid)
    finally:
        d.c.regular = original_regular
    print(json.dumps({
        'state': 'candidate', 'profile': d.PROFILE, 'release_id': rid,
        'binary_sha256': manifest['files']['wecom-mcp-team'],
        'manifest_sha256': d.sha(out / 'manifest.json'),
        'production_changed': False, 'ci_pending': not bool(a.ci_url),
    }, sort_keys=True))


if __name__ == '__main__':
    main()
