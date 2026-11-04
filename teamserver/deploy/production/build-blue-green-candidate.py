#!/usr/bin/env python3
"""Build a local-only blue/green candidate; it cannot deploy or switch traffic."""
import argparse
import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('blue', HERE / 'mcp_blue_green_executor.py')
b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b)
ROOT = HERE.parents[2]


def git(*args):
    return subprocess.check_output(['git', '-C', str(ROOT), *args], text=True).strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output', required=True)
    for key in ('expected-runtime-path', 'expected-binary-sha256', 'expected-unit-fingerprint', 'expected-runtime-config-fingerprint', 'expected-gnas-release-id', 'expected-gnas-binary-sha256'):
        parser.add_argument('--' + key, required=True)
    parser.add_argument('--ci-url', default='', help='same-commit successful hosted CI URL')
    args = parser.parse_args()
    output = Path(args.output)
    b.require(output.is_absolute() and not output.exists() and output.parent.is_dir() and output.parent.resolve() == output.parent, 'new absolute output with canonical parent required')
    b.require(not git('status', '--porcelain'), 'clean source required')
    for digest in (args.expected_binary_sha256, args.expected_unit_fingerprint, args.expected_runtime_config_fingerprint, args.expected_gnas_binary_sha256):
        b.require(b.c.SHA.fullmatch(digest), 'invalid baseline SHA')
    previous = Path(args.expected_runtime_path)
    b.require(previous.parent.parent == b.c.RELEASES and previous.name == 'wecom-mcp-team', 'expected green process path required')
    commit, tree = git('rev-parse', 'HEAD'), git('rev-parse', 'HEAD^{tree}')
    release_id = dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + commit[:12]
    output.mkdir(mode=0o700)
    env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH='amd64', GOAMD64='v1', GOWORK='off', GOENV='off', GOFLAGS='', GOEXPERIMENT='')
    subprocess.run(['go', 'build', '-trimpath', '-o', str(output / 'wecom-mcp-team'), './cmd/wecom-mcp-team'], cwd=ROOT / 'teamserver', env=env, check=True)
    (output / 'discovery-policy.json').write_bytes((HERE.parent / 'wecom-mcp-gnas-discovery-policy.json.example').read_bytes())
    (output / 'blue.conf').write_bytes(b.blue_dropin(release_id))
    (output / 'plan.json').write_text(json.dumps(b.PLAN, indent=2) + '\n')
    manifest = {
        'schema_version': 1, 'environment': b.c.ENVIRONMENT, 'release_id': release_id,
        'deployment_topology': 'blue-green-isolated-ready-worker', 'plan': b.PLAN,
        'fleet_state_vector': b.executor_vector(), 'fleet_state_vector_sha256': b.c.fleet_state_vector_sha256(),
        'git_commit': commit, 'git_tree': tree, 'source_clean': True, 'target': 'linux/amd64',
        'expected_runtime_path': args.expected_runtime_path, 'expected_binary_sha256': args.expected_binary_sha256,
        'expected_unit_fingerprint': args.expected_unit_fingerprint, 'expected_runtime_config_fingerprint': args.expected_runtime_config_fingerprint,
        'expected_gnas_release_id': args.expected_gnas_release_id, 'expected_gnas_binary_sha256': args.expected_gnas_binary_sha256,
        'ci_url': args.ci_url,
    }
    manifest['control_plane'] = b.control_plane(release_id)
    manifest['control_plane_sha256'] = b.control_plane_sha256(manifest['control_plane'])
    b.validate_control_plane(manifest['control_plane'], release_id)
    manifest['files'] = {name: b.c.sha(output / name) for name in b.FILES[:-1]}
    (output / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    (output / 'SHA256SUMS').write_text(''.join(b.c.sha(output / name) + '  ' + name + '\n' for name in sorted(b.FILES)))
    for path in output.iterdir():
        path.chmod(0o500 if path.name == 'wecom-mcp-team' else 0o400)
    output.chmod(0o500)
    print(json.dumps({'state': 'blue_green_candidate', 'release_id': release_id, 'production_changed': False, 'ci_pending': not bool(args.ci_url)}))


if __name__ == '__main__':
    main()
