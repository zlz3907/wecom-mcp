#!/usr/bin/python3 -I
"""Fail-closed route target check for an armed MCP policy compatibility floor.

Call only while holding the same production release locks as the route change.
This prototype is not integrated or approved for production. Policy activation,
effective nginx configuration and the running tenant/auth configuration still
need an atomic, independently verified contract before this can be used.
An absent floor fails closed in both bootstrap and managed-policy phases.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess

FLOOR = Path('/etc/wecom-mcp/mcp-policy-floor-zhycit.json')
PORT = re.compile(rb'proxy_pass\s+http://127\.0\.0\.1:([0-9]{1,5});')
UNIT = re.compile(r'wecom-mcp-[a-z0-9-]+\.service\Z')
SHA = re.compile(r'[0-9a-f]{64}\Z')


def read_floor():
    fd = os.open(FLOOR, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if info.st_uid != 0 or not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o400:
            raise ValueError('untrusted MCP policy compatibility floor')
        raw = stream.read(16385)
    if len(raw) > 16384:
        raise ValueError('oversize MCP policy compatibility floor')
    for directory in (FLOOR.parent, FLOOR.parent.parent):
        info = directory.lstat()
        if directory.resolve() != directory or info.st_uid != 0 or not stat.S_ISDIR(info.st_mode) or info.st_mode & 0o022:
            raise ValueError('untrusted MCP policy compatibility directory')
    try:
        return json.loads(raw)
    except (ValueError, UnicodeDecodeError) as exc:
        raise ValueError('invalid MCP policy compatibility floor') from exc


def _owned_listeners(pid, port):
    listeners = set()
    route_reachable = False
    # Include wildcard and IPv6 listeners on this port, not just an exact
    # IPv4 address: SO_REUSEPORT or dual-stack binding must not hide a second
    # process receiving the route's traffic.
    for name in ('tcp', 'tcp6'):
        for line in Path('/proc/%d/net/%s' % (pid, name)).read_text().splitlines()[1:]:
            fields = line.split()
            if len(fields) >= 10 and fields[1].endswith(':%04X' % port) and fields[3] == '0A':
                listeners.add(fields[9])
                if name == 'tcp' and fields[1] in ('0100007F:%04X' % port, '00000000:%04X' % port):
                    route_reachable = True
    owned = set()
    for entry in Path('/proc/%d/fd' % pid).iterdir():
        try:
            link = os.readlink(entry)
        except FileNotFoundError:
            continue
        match = re.fullmatch(r'socket:\[([0-9]+)\]', link)
        if match:
            owned.add(match.group(1))
    if not route_reachable or not listeners or not listeners.issubset(owned):
        raise ValueError('MCP target does not own every loopback listener')
    return listeners


def _start_time(pid):
    raw = Path('/proc/%d/stat' % pid).read_text()
    return raw[raw.rfind(')') + 2:].split()[19]


def _namespace(pid):
    info = os.stat('/proc/%d/ns/net' % pid)
    return info.st_dev, info.st_ino


def _execution_digest(pid):
    digest = hashlib.sha256()
    for name in ('cmdline', 'environ'):
        data = Path('/proc/%d/%s' % (pid, name)).read_bytes()
        if not data or len(data) > 1048576:
            raise ValueError('MCP process execution configuration unavailable')
        digest.update(len(data).to_bytes(8, 'big'))
        digest.update(data)
    return digest.hexdigest()


def _nginx_processes():
    result = subprocess.run(['/usr/bin/systemctl', 'show', 'nginx.service', '--property=ControlGroup', '--property=ActiveState', '--property=MainPID', '--property=Delegate'],
                            check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10)
    properties = dict(line.split('=', 1) for line in result.stdout.decode('ascii').splitlines() if '=' in line)
    group = properties.get('ControlGroup', '')
    if not group.startswith('/') or '..' in group or properties.get('ActiveState') != 'active' or properties.get('Delegate') != 'no':
        raise ValueError('nginx cgroup unavailable')
    base = Path('/sys/fs/cgroup')
    directory = base / group.removeprefix('/')
    if not directory.resolve().is_relative_to(base):
        raise ValueError('invalid nginx cgroup')
    if any(entry.is_dir() for entry in directory.iterdir()):
        raise ValueError('nested nginx cgroups are not supported')
    pids = frozenset(int(value) for value in (directory / 'cgroup.procs').read_text().split())
    main_pid = int(properties.get('MainPID', '0'))
    if not pids or main_pid not in pids or any(pid <= 1 for pid in pids):
        raise ValueError('nginx processes unavailable')
    return pids


def _assert_nginx_namespace(pid):
    namespace = _namespace(pid)
    nginx_pids = _nginx_processes()
    if any(_namespace(nginx_pid) != namespace for nginx_pid in nginx_pids):
        raise ValueError('nginx and MCP network namespaces differ')
    return namespace, frozenset((nginx_pid, _start_time(nginx_pid)) for nginx_pid in nginx_pids)


def _assert_no_other_socket_holders(pid, listeners):
    for process in Path('/proc').iterdir():
        if not process.name.isdigit() or int(process.name) == pid:
            continue
        try:
            entries = list((process / 'fd').iterdir())
        except (FileNotFoundError, ProcessLookupError):
            continue
        for entry in entries:
            try:
                link = os.readlink(entry)
            except (FileNotFoundError, ProcessLookupError):
                continue
            match = re.fullmatch(r'socket:\[([0-9]+)\]', link)
            if match and match.group(1) in listeners:
                raise ValueError('MCP listener is shared with another process')


def live_binary_sha(unit, port):
    def state():
        result = subprocess.run(['/usr/bin/systemctl', 'show', unit, '--property=ActiveState', '--property=MainPID'],
                                check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10)
        return dict(line.split('=', 1) for line in result.stdout.decode('ascii').splitlines() if '=' in line)
    before = state()
    pid = int(before.get('MainPID', '0'))
    if before.get('ActiveState') != 'active' or pid <= 1:
        raise ValueError('MCP compatibility target is not active')
    start = _start_time(pid)
    listeners = _owned_listeners(pid, port)
    execution = _execution_digest(pid)
    nginx_pids = _assert_nginx_namespace(pid)
    _assert_no_other_socket_holders(pid, listeners)
    digest = hashlib.sha256()
    with open('/proc/%d/exe' % pid, 'rb') as binary:
        binary_identity = os.fstat(binary.fileno())
        for block in iter(lambda: binary.read(1024 * 1024), b''):
            digest.update(block)
    current_binary = os.stat('/proc/%d/exe' % pid)
    if ((binary_identity.st_dev, binary_identity.st_ino) != (current_binary.st_dev, current_binary.st_ino)
            or state() != before or _start_time(pid) != start or _owned_listeners(pid, port) != listeners
            or _assert_nginx_namespace(pid) != nginx_pids or _execution_digest(pid) != execution):
        raise ValueError('MCP compatibility target changed')
    _assert_no_other_socket_holders(pid, listeners)
    return digest.hexdigest(), execution


def validate_route(route, floor, resolve=live_binary_sha):
    if not isinstance(floor, dict) or set(floor) != {'schema_version', 'host', 'phase', 'targets'} or floor['schema_version'] != 1 or floor['host'] != 'zhycit.com' or floor['phase'] not in ('bootstrap', 'policy') or not isinstance(floor['targets'], list) or not 1 <= len(floor['targets']) <= 8:
        raise ValueError('invalid MCP policy compatibility floor')
    targets = {}
    for target in floor['targets']:
        if not isinstance(target, dict) or set(target) != {'port', 'unit', 'sha256', 'route_sha256', 'runtime_sha256', 'nginx_config_sha256'}:
            raise ValueError('invalid MCP policy compatibility target')
        port, unit, digest, route_digest, runtime_digest, nginx_digest = target['port'], target['unit'], target['sha256'], target['route_sha256'], target['runtime_sha256'], target['nginx_config_sha256']
        if type(port) is not int or not 1 <= port <= 65535 or port in targets or not isinstance(unit, str) or not UNIT.fullmatch(unit) or any(not isinstance(value, str) or not SHA.fullmatch(value) for value in (digest, route_digest, runtime_digest, nginx_digest)):
            raise ValueError('invalid MCP policy compatibility target')
        targets[port] = (unit, digest, route_digest, runtime_digest, nginx_digest)
    ports = PORT.findall(route)
    if len(ports) != 2 or ports[0] != ports[1]:
        raise ValueError('unrecognized MCP route')
    target = targets.get(int(ports[0]))
    if target is None or hashlib.sha256(route).hexdigest() != target[2] or resolve(target[0], int(ports[0])) != (target[1], target[3]):
        raise ValueError('MCP route would bypass the policy compatibility floor')
    return target[4]


def guard_route(route):
    floor = read_floor()
    return validate_route(route, floor)


def guard_staged_nginx(expected_digest):
    if not isinstance(expected_digest, str) or not SHA.fullmatch(expected_digest):
        raise ValueError('invalid expected nginx configuration digest')
    result = subprocess.run(['/usr/sbin/nginx', '-T'], check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=20)
    if hashlib.sha256(result.stdout).hexdigest() != expected_digest:
        raise ValueError('nginx configuration does not match the reviewed policy floor')
