#!/usr/bin/env python3
"""Build a local-only Zhonghe bootstrap artifact; never contacts production.

This is not an installer or an approval. The release controller and a safe
compatible recovery path must still be implemented and independently reviewed.
"""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

EXPECTED_COMMIT = 'dbef937502fa2c4f005f97afbe1dea7db7bc9dc6'
EXPECTED_POLICY = '7d45dd145b1c4f604619046e305e031d42e807e5c7ee32450015d04123ccbba6'
PREVIOUS_MCP = '82fd10c16f5ccf72219818a9efc00e0b79f4e70a8726faf071059b6a6344fa03'
PREVIOUS_OAUTH = 'cc1a536c23715162b9c622a91fba1da0a4b36d3ac529d8982cc86b0347210ce1'
CI_URL = 'https://github.com/zlz3907/wecom-mcp/actions/runs/42516298366'


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def git(source, *args):
    return subprocess.check_output(['git', '-C', str(source), *args], text=True).strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    source, output = Path(args.source), Path(args.output)
    if not source.is_absolute() or source.resolve() != source or not source.is_dir():
        raise ValueError('source must be a canonical directory')
    if not output.is_absolute() or output.exists() or output.parent.resolve() != output.parent:
        raise ValueError('output must be new under a canonical directory')
    if git(source, 'status', '--porcelain', '--untracked-files=all'):
        raise ValueError('source must be clean')
    commit, tree = git(source, 'rev-parse', 'HEAD'), git(source, 'rev-parse', 'HEAD^{tree}')
    if commit != EXPECTED_COMMIT or not re.fullmatch(r'[0-9a-f]{40}', tree):
        raise ValueError('unexpected source commit')
    policy = source / 'teamserver/deploy/wecom-mcp-gnas-discovery-policy.json.example'
    if not policy.is_file() or policy.is_symlink() or digest(policy) != EXPECTED_POLICY:
        raise ValueError('unexpected discovery policy')
    output.mkdir(mode=0o700)
    env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH='amd64', GOAMD64='v1',
               GOWORK='off', GOENV='off', GOFLAGS='', GOEXPERIMENT='', GOPROXY='off')
    # Building from the tracked Git tree prevents ignored local files from
    # entering the source or a Go embed, while leaving the worktree untouched.
    with tempfile.TemporaryDirectory(prefix='mcp-bootstrap-source-') as temporary:
        archive = subprocess.Popen(['git', '-C', str(source), 'archive', '--format=tar', commit], stdout=subprocess.PIPE)
        try:
            subprocess.run(['tar', '-xf', '-', '-C', temporary], stdin=archive.stdout, check=True)
        finally:
            archive.stdout.close()
        if archive.wait() != 0:
            raise ValueError('cannot export source commit')
        subprocess.run(['go', 'build', '-trimpath', '-o', str(output / 'wecom-mcp-team'), './cmd/wecom-mcp-team'],
                       cwd=Path(temporary) / 'teamserver', env=env, check=True)
    (output / 'discovery-policy.json').write_bytes(policy.read_bytes())
    if git(source, 'status', '--porcelain', '--untracked-files=all') or git(source, 'rev-parse', 'HEAD') != commit:
        raise ValueError('source changed during build')
    release_id = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + commit[:12]
    manifest = {'schema_version': 1, 'stage': 'local_only', 'component': 'wecom-mcp-target-bootstrap',
                'environment': 'zhycit-prod-01', 'release_id': release_id, 'git_commit': commit,
                'git_tree': tree, 'target': 'linux/amd64', 'ci_url': CI_URL,
                'ci_verification': 'external_required',
                'previous_mcp_sha256': PREVIOUS_MCP, 'previous_oauth_sha256': PREVIOUS_OAUTH,
                'files': {name: digest(output / name) for name in ('wecom-mcp-team', 'discovery-policy.json')}}
    (output / 'manifest.json').write_text(json.dumps(manifest, sort_keys=True, indent=2) + '\n')
    (output / 'SHA256SUMS').write_text(''.join(digest(output / name) + '  ' + name + '\n'
                                               for name in ('discovery-policy.json', 'manifest.json', 'wecom-mcp-team')))
    for file in output.iterdir():
        file.chmod(0o500 if file.name == 'wecom-mcp-team' else 0o400)
    output.chmod(0o500)
    print(json.dumps({'result': 'local_artifact', 'release_id': release_id,
                      'binary_sha256': manifest['files']['wecom-mcp-team'],
                      'production_changed': False}, sort_keys=True))


if __name__ == '__main__':
    main()
