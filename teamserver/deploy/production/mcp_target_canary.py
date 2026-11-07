#!/usr/bin/python3 -I
"""Start one isolated MCP instance; never switch traffic or restart shared services."""
import datetime as dt
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import shutil
import socket
import stat
import subprocess
import sys
import time

ROOT = Path('/var/lib/wecom-mcp-isolated')
APPROVALS = Path('/etc/gnas/release-approvals')
UNIT = 'wecom-mcp-zhycit-isolated.service'
UNIT_PATH = Path('/etc/systemd/system') / UNIT
SHARED = 'wecom-mcp-discovery-21462d2.service'
GNAS_CANARY = 'gnas-oauth-zhycit-isolated.service'
RID = '20261106T203247Z-932ca8583c69'
GNAS_SHA = 'd5b8cb5c98e74f45e7cc54b645ae9d686fa7003e9391bcaa85317101f3d531df'
OLD_GNAS_RID = '20261106T040805Z-1acaa4269334'
OLD_GNAS_SHA = 'f80fa1116cebd1b55523782259f626450deef7e2c23818c4bec92840b0d173a0'
OLD_MCP_SHA = '988055b38a6963afe1f6cfe3b2bdb9b691483cb599974a450efae535d356ad22'
BINARY_SHA = '48fd78e9e344554ad35f8c65107a62407b0691d2dabb42bdf1d81b60e8b3bfef'
POLICY_SHA = '7d45dd145b1c4f604619046e305e031d42e807e5c7ee32450015d04123ccbba6'
RELEASE = '20261106T203247Z-c81ba765e322'
RELEASE_DIR = ROOT / 'releases' / RELEASE
STATE = '/home/product/services/mcp/wecom/state/discovery'
HOSTS = ('zhycit.com', 'mcp.wesiyu.com', 'mcp.rtyouth.com', 'mcp.jianpinke.com')


def require(condition, message):
    if not condition: raise ValueError(message)


def sha(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''): h.update(block)
    return h.hexdigest()


def trusted(path, directory=False):
    st = path.lstat()
    require(path.resolve() == path and st.st_uid == 0 and not st.st_mode & 0o022, 'untrusted path')
    require(stat.S_ISDIR(st.st_mode) if directory else stat.S_ISREG(st.st_mode), 'wrong path type')


def run(*args, timeout=20):
    return subprocess.run(args, check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=timeout).stdout


def properties(unit):
    text = run('/usr/bin/systemctl', 'show', unit, '-p', 'ActiveState', '-p', 'MainPID', '-p', 'NRestarts').decode()
    return dict(row.split('=', 1) for row in text.splitlines() if '=' in row)


def probe(port, host, path):
    conn = http.client.HTTPConnection('127.0.0.1', port, timeout=4)
    try:
        conn.request('GET', path, headers={'Host': host})
        r = conn.getresponse(); body = r.read(65537)
        require(len(body) <= 65536, 'probe body too large')
        return r.status, body
    finally: conn.close()


def snapshot():
    status = run('/usr/local/sbin/gnas-release-controller', 'status').decode()
    gnas = dict(row.split('=', 1) for row in status.splitlines() if '=' in row)
    require(gnas.get('service_state') == 'active' and gnas.get('release_id') == OLD_GNAS_RID
            and gnas.get('runtime_sha256') == OLD_GNAS_SHA and gnas.get('restarts') == '0', 'shared GNAS changed')
    shared = properties(SHARED); pid = int(shared.get('MainPID', '0'))
    require(shared.get('ActiveState') == 'active' and shared.get('NRestarts') == '0' and pid > 1
            and sha(Path('/proc') / str(pid) / 'exe') == OLD_MCP_SHA, 'shared MCP changed')
    vector = tuple(probe(7703, host, '/healthz')[0] for host in HOSTS)
    require(vector == (200, 200, 200, 503), 'shared tenant health changed')
    return (gnas.get('main_pid'), pid, vector)


def unit_content():
    binary = str(RELEASE_DIR / 'wecom-mcp-team')
    policy = str(RELEASE_DIR / 'discovery-policy.json')
    # EnvironmentFile values override systemd Environment entries, so env sets
    # the isolated GNAS endpoint after the existing protected file is loaded.
    args = ('/usr/bin/env GNAS_BASE_URL=http://127.0.0.1:7503 '
            '"TEAM_MCP_ADVERTISED_SCOPES=zoop.read zoop.write" '
            'TEAM_MCP_ADVERTISED_SCOPES_RESOURCE=https://zhycit.com/mcp ' + binary +
            ' --gnas-discovery-policy ' + policy + ' --gnas-state-root ' + STATE +
            ' --listen 127.0.0.1:7705 --gnas-ready-worker-isolation --gnas-ready-worker-timeout 5s')
    return (('[Unit]\nDescription=Isolated target WeCom MCP canary\nAfter=network-online.target ' + GNAS_CANARY + '\n'
            '[Service]\nType=simple\nUser=wecom-mcp-gmzoop\nGroup=wecom-mcp-gmzoop\n'
            'EnvironmentFile=/etc/wecom-mcp/gmzoop.env\nExecStartPre=' + args + ' --check-config\n'
            'ExecStart=' + args + ' --gnas-fleet-refresh 30s\nRestart=no\nTimeoutStopSec=30s\nOOMPolicy=stop\n'
            'NoNewPrivileges=true\nPrivateTmp=true\nProtectHome=read-only\nProtectSystem=strict\n'
            'ProtectKernelTunables=true\nProtectKernelModules=true\nProtectControlGroups=true\n'
            'RestrictSUIDSGID=true\nLockPersonality=true\nMemoryDenyWriteExecute=true\n'
            'ReadWritePaths=' + STATE + '\nKillMode=control-group\nMemoryHigh=96M\nMemoryMax=128M\n'
            'TasksMax=96\nLimitNOFILE=4096\nCPUQuota=20%\n').encode())


def check_receipt(approval):
    require(re.fullmatch(r'APR-[0-9]{8}T[0-9]{6}Z-[a-zA-Z0-9._-]{6,64}', approval), 'invalid receipt ID')
    path = APPROVALS / (approval + '.json'); trusted(path)
    require(stat.S_IMODE(path.stat().st_mode) == 0o400, 'receipt mode is unsafe')
    data = json.loads(path.read_text())
    expected = {'schema_version': 1, 'approval_id': approval, 'environment': 'zhycit-prod-01',
        'action': 'start-isolated-mcp', 'release_id': RELEASE, 'source_commit': 'c81ba765e32292875b863a058a0ac34ef1ceaefe',
        'binary_sha256': BINARY_SHA, 'policy_sha256': POLICY_SHA, 'gnas_canary_release_id': RID,
        'gnas_canary_sha256': GNAS_SHA, 'shared_gnas_release_id': OLD_GNAS_RID, 'shared_gnas_sha256': OLD_GNAS_SHA,
        'shared_mcp_sha256': OLD_MCP_SHA, 'listen': '127.0.0.1:7705', 'host': 'zhycit.com',
        'unit_sha256': hashlib.sha256(unit_content()).hexdigest(), 'controller_sha256': sha(Path(__file__).resolve())}
    require(set(data) == set(expected) | {'approved_at', 'expires_at', 'approved_by'}
            and all(data.get(k) == v for k, v in expected.items()), 'receipt does not bind action')
    now = dt.datetime.now(dt.timezone.utc)
    approved = dt.datetime.fromisoformat(data['approved_at']); expires = dt.datetime.fromisoformat(data['expires_at'])
    require(approved.tzinfo is not None and expires.tzinfo is not None and approved <= now < expires
            and expires - approved <= dt.timedelta(hours=4) and bool(data['approved_by']), 'receipt expired')
    return data


def main(approval):
    require(os.geteuid() == 0 and Path(__file__).resolve() == Path('/usr/local/sbin/mcp-target-canary.py'), 'invalid controller')
    for p in (Path('/usr'), Path('/usr/local'), Path('/usr/local/sbin'), Path('/etc'), Path('/etc/gnas'), APPROVALS,
              Path('/etc/systemd'), Path('/etc/systemd/system'), Path('/var/lib'), ROOT, ROOT/'releases', ROOT/'used'):
        trusted(p, True)
    trusted(Path(__file__).resolve()); trusted(RELEASE_DIR, True)
    trusted(Path('/usr/local/sbin/gnas-release-controller')); trusted(ROOT/'release.lock')
    for name, digest in (('wecom-mcp-team', BINARY_SHA), ('discovery-policy.json', POLICY_SHA)):
        trusted(RELEASE_DIR/name); require(sha(RELEASE_DIR/name) == digest, 'candidate digest mismatch')
    with (ROOT/'release.lock').open('r+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        before = snapshot()
        gnas = properties(GNAS_CANARY); pid = int(gnas.get('MainPID', '0'))
        require(gnas.get('ActiveState') == 'active' and gnas.get('NRestarts') == '0' and pid > 1
                and sha(Path('/proc')/str(pid)/'exe') == GNAS_SHA, 'isolated GNAS is not healthy')
        require(probe(7503, 'zhycit.com', '/.well-known/oauth-authorization-server/gnas/oauth')[0] == 200,
                'isolated OAuth metadata unavailable')
        require(not UNIT_PATH.exists() and not UNIT_PATH.is_symlink(), 'isolated unit exists')
        require(run('/usr/bin/systemctl', 'show', UNIT, '-p', 'LoadState', '--value').decode().strip() == 'not-found', 'isolated unit loaded')
        nginx = run('/usr/sbin/nginx', '-T', timeout=20)
        require(not re.search(rb'(?<![0-9])7705(?![0-9])', nginx), 'isolated endpoint is routed')
        mem = re.search(r'^MemAvailable:\s+([0-9]+)\s+kB$', Path('/proc/meminfo').read_text(), re.MULTILINE)
        require(mem and int(mem.group(1)) >= 384 * 1024, 'insufficient available memory')
        sock = socket.socket()
        try: sock.bind(('127.0.0.1', 7705))
        finally: sock.close()
        check_receipt(approval)
        used = ROOT/'used'/approval
        fd = os.open(used, os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o440)
        with os.fdopen(fd, 'w') as f: f.write(dt.datetime.now(dt.timezone.utc).isoformat()+'\n'); f.flush(); os.fsync(f.fileno())
        fd = os.open(UNIT_PATH, os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW, 0o644)
        with os.fdopen(fd, 'wb') as f: f.write(unit_content()); f.flush(); os.fsync(f.fileno())
        try:
            run('/usr/bin/systemctl', 'daemon-reload', timeout=30)
            run('/usr/bin/systemctl', 'start', UNIT, timeout=50)
            deadline = time.monotonic()+40
            while True:
                try:
                    health = tuple(probe(7705, h, '/healthz')[0] for h in HOSTS)
                    if health == (200, 200, 200, 503): break
                except OSError: pass
                require(time.monotonic() < deadline, 'isolated MCP health did not converge')
                time.sleep(1)
            state = properties(UNIT); pid = int(state.get('MainPID', '0'))
            require(state.get('ActiveState') == 'active' and state.get('NRestarts') == '0' and pid > 1
                    and sha(Path('/proc')/str(pid)/'exe') == BINARY_SHA, 'isolated MCP binary mismatch')
            code, body = probe(7705, 'zhycit.com', '/.well-known/oauth-protected-resource/mcp')
            meta = json.loads(body)
            require(code == 200 and meta.get('resource') == 'https://zhycit.com/mcp'
                    and {'zoop.read', 'zoop.write'}.issubset(set(meta.get('scopes_supported', []))), 'target metadata mismatch')
            require(snapshot() == before, 'shared service changed during canary start')
        except Exception:
            # Never stop a canary that someone has already exposed through Nginx.
            try:
                if not re.search(rb'(?<![0-9])7705(?![0-9])', run('/usr/sbin/nginx','-T',timeout=20)):
                    run('/usr/bin/systemctl','stop',UNIT,timeout=35)
            except Exception: pass
            raise
        print(json.dumps({'state':'isolated_mcp_ready','release_id':RELEASE,'binary_sha256':BINARY_SHA,
                          'public_route_changed':False,'shared_restarted':False,'approval_id':approval},sort_keys=True))


if __name__ == '__main__':
    try:
        require(len(sys.argv) == 2, 'exact receipt required'); main(sys.argv[1])
    except Exception as error:
        print('isolated MCP canary refused or failed: ' + (str(error) if isinstance(error, ValueError) else type(error).__name__), file=sys.stderr)
        sys.exit(1)
