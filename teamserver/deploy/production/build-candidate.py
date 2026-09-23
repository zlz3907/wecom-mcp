#!/usr/bin/env python3
"""Build an immutable local MCP candidate; no upload or production access."""
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
spec = importlib.util.spec_from_file_location('controller', HERE / 'mcp_release_controller.py')
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
ROOT = HERE.parents[2]


def git(*args):
    return subprocess.check_output(['git', '-C', str(ROOT), *args], text=True).strip()


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--output', required=True)
    for key in ('expected-runtime-path','expected-binary-sha256','expected-unit-fingerprint','expected-runtime-config-fingerprint','expected-unmanaged-unit-fingerprint','expected-gnas-release-id','expected-gnas-binary-sha256'):
        p.add_argument('--'+key, required=True)
    p.add_argument('--ci-url', default='', help='same-commit successful hosted CI; omission creates a local-only candidate')
    a = p.parse_args()
    out = Path(a.output)
    c.require(out.is_absolute() and not out.exists() and out.parent.is_dir() and out.parent.resolve() == out.parent, 'new absolute output with existing canonical parent required')
    c.require(not git('status','--porcelain'), 'clean source required')
    commit, tree = git('rev-parse','HEAD'), git('rev-parse','HEAD^{tree}')
    rid = dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ-') + commit[:12]
    for digest in (a.expected_binary_sha256, a.expected_unit_fingerprint, a.expected_runtime_config_fingerprint, a.expected_gnas_binary_sha256, a.expected_unmanaged_unit_fingerprint):
        c.require(c.SHA.fullmatch(digest), 'invalid baseline SHA')
    previous = Path(a.expected_runtime_path)
    c.require(previous.parent.parent == c.RELEASES and previous.name == 'wecom-mcp-team', 'expected actual process path required')
    out.mkdir(mode=0o700)
    env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH='amd64', GOAMD64='v1', GOWORK='off', GOENV='off', GOFLAGS='', GOEXPERIMENT='')
    subprocess.run(['go','build','-trimpath','-o',str(out/'wecom-mcp-team'),'./cmd/wecom-mcp-team'], cwd=ROOT/'teamserver', env=env, check=True)
    (out/'discovery-policy.json').write_bytes((HERE.parent/'wecom-mcp-gnas-discovery-policy.json.example').read_bytes())
    (out/'service.conf').write_bytes(c.dropin(rid))
    (out/'recovery.conf').write_bytes(c.recovery_dropin(rid))
    manifest={'schema_version':2, 'recovery_mode':'same-version-static', 'recovery_hosts':list(c.HOSTS[:1]), 'expected_unmanaged_unit_fingerprint':a.expected_unmanaged_unit_fingerprint, 'environment':c.ENVIRONMENT, 'release_id':rid, 'git_commit':commit, 'git_tree':tree, 'source_clean':True, 'target':'linux/amd64', 'files':{name:c.sha(out/name) for name in c.FILES[:-1]}, 'expected_runtime_path':a.expected_runtime_path, 'expected_binary_sha256':a.expected_binary_sha256, 'expected_unit_fingerprint':a.expected_unit_fingerprint, 'expected_runtime_config_fingerprint':a.expected_runtime_config_fingerprint, 'expected_gnas_release_id':a.expected_gnas_release_id, 'expected_gnas_binary_sha256':a.expected_gnas_binary_sha256, 'ci_url':a.ci_url, 'created_at':dt.datetime.now(dt.timezone.utc).isoformat()}
    (out/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
    (out/'SHA256SUMS').write_text(''.join(c.sha(out/name)+'  '+name+'\n' for name in sorted(c.FILES)))
    for file in out.iterdir():file.chmod(0o500 if file.name=='wecom-mcp-team' else 0o400)
    out.chmod(0o500)
    print(json.dumps({'state':'candidate','release_id':rid,'binary_sha256':manifest['files']['wecom-mcp-team'],'manifest_sha256':c.sha(out/'manifest.json'),'production_changed':False,'ci_pending':not bool(a.ci_url)}))


if __name__=='__main__':
    main()
