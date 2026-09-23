#!/usr/bin/python3 -I
"""Separate root-admin infrastructure action; never restarts a service."""
import json
import os
from pathlib import Path
import pwd
import grp
import sys
import importlib.util

SOURCE = Path(__file__).resolve().with_name('mcp_release_controller.py')
spec = importlib.util.spec_from_file_location('mcp_release_controller', SOURCE)
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


def main():
    c.require(os.geteuid() == 0, 'root admin required')
    c.require(len(sys.argv) in (2, 3) and sys.argv[1] in ('--check', '--apply'), 'usage: --check | --apply approval-id')
    c.regular(SOURCE, True)
    c.regular(Path(__file__).resolve(), True)
    facts = c.status()
    c.require(facts.get('runtime_verified') is True and facts['active'] == 'active' and facts['restarts'] == 0, 'unverified or unhealthy baseline')
    expected = {'controller_sha256': c.sha(SOURCE), 'installer_sha256': c.sha(Path(__file__).resolve()),
                'expected_runtime_path': facts['runtime_path'], 'expected_binary_sha256': facts['binary_sha256'], 'expected_unit_fingerprint': facts['unit_fingerprint'], 'expected_runtime_config_fingerprint': facts['runtime_config_fingerprint']}
    if sys.argv[1] == '--check':
        print(json.dumps({'environment': c.ENVIRONMENT, 'action': 'install-controller', **expected, 'service_restarted': False}, sort_keys=True))
        return
    c.require(len(sys.argv) == 3, 'exact root-owned approval receipt required')
    # The administrator provisions the approval directory and receipt separately.
    c.require(c.APPROVALS.is_dir() and c.APPROVALS.resolve() == c.APPROVALS and c.APPROVALS.stat().st_uid == 0 and not c.APPROVALS.stat().st_mode & 0o022, 'trusted approval directory missing')
    target = Path('/usr/local/sbin/wecom-mcp-release-controller')
    c.require(not target.exists(), 'controller already installed; upgrades require a separate reviewed flow')
    c.check_approval(sys.argv[2], 'install-controller', expected)
    for path in (c.CONTROL, c.CONTROL / 'used'):
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        c.require(path.resolve() == path and path.stat().st_uid == 0 and not path.stat().st_mode & 0o022, 'unsafe control directory')
    c.approval(sys.argv[2], 'install-controller', expected)
    for path in (c.INCOMING, c.CONTROL / 'rollback'):
        path.mkdir(mode=0o700, exist_ok=True)
        c.require(path.resolve() == path and path.stat().st_uid == 0 and not path.stat().st_mode & 0o022, 'unsafe control directory')
    c.STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    c.require(c.STATE.resolve() == c.STATE and c.STATE.is_dir(), 'invalid discovery state root')
    uid, gid = pwd.getpwnam('wecom-mcp-gmzoop').pw_uid, grp.getgrnam('wecom-mcp-gmzoop').gr_gid
    os.chown(c.STATE, uid, gid)
    os.chmod(c.STATE, 0o700)
    c.atomic(target, SOURCE.read_bytes(), 0o755)
    c.require(c.status() == facts, 'service changed during infrastructure installation')
    print(json.dumps({'state': 'controller_installed', 'controller_sha256': c.sha(target), 'service_restarted': False}))


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('INSTALL_REFUSED: ' + str(error) if isinstance(error, ValueError) else 'INSTALL_REFUSED: controlled operation failed', file=sys.stderr)
        sys.exit(1)
