"""Systemd operations for reviewed, single-unit MCP cutovers.

The surrounding controller owns approval, locks, immutable plan verification,
process identity, probes and durable transaction records. These helpers cannot
authorize a production action by themselves.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import socket
import stat
import subprocess
import time

UNITS = {
    'wecom-mcp-zhycit-migration.service': 7709,
    'wecom-mcp-discovery-21462d2.service': 7703,
}


def run(*args, timeout=30):
    return subprocess.run(args, check=True, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, timeout=timeout).stdout.decode().strip()


def fsync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


class Unit:
    def __init__(self, name, fragment_sha, candidate_sha, runner=run,
                 systemd_dir=Path('/etc/systemd/system'), cgroup_root=Path('/sys/fs/cgroup'),
                 clock=time.monotonic, sleep=time.sleep, socket_factory=socket.socket):
        if name not in UNITS or not re.fullmatch(r'[0-9a-f]{64}', fragment_sha) or not re.fullmatch(r'[0-9a-f]{64}', candidate_sha):
            raise ValueError('invalid fixed unit contract')
        self.name, self.port = name, UNITS[name]
        self.fragment_sha, self.candidate_sha = fragment_sha, candidate_sha
        self.run, self.systemd_dir, self.cgroup_root = runner, systemd_dir, cgroup_root
        self.clock, self.sleep, self.socket_factory = clock, sleep, socket_factory
        self.wants = systemd_dir / 'multi-user.target.wants'

    def show(self):
        keys = ('ActiveState', 'SubState', 'MainPID', 'ControlGroup', 'Job',
                'FragmentPath', 'DropInPaths', 'UnitFileState', 'KillMode', 'PrivateNetwork',
                'User', 'NRestarts')
        args = ['/usr/bin/systemctl', 'show', self.name]
        for key in keys:
            args.extend(('-p', key))
        rows = self.run(*args).splitlines()
        values = {}
        for row in rows:
            key, sep, value = row.partition('=')
            if not sep or key not in keys or key in values:
                raise ValueError('ambiguous systemd properties')
            values[key] = value
        if set(values) != set(keys):
            raise ValueError('missing systemd properties')
        return values

    def verify_fragment(self, props):
        fragment = self.systemd_dir / self.name
        if (props['FragmentPath'] != str(fragment) or props['KillMode'] != 'control-group' or
                props['PrivateNetwork'] != 'no' or props['User'] != 'wecom-mcp-gmzoop'):
            raise ValueError('unit contract drift')
        info = fragment.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022 or digest(fragment) != self.fragment_sha:
            raise ValueError('unit fragment drift')

    def no_processes(self):
        # The explicit unit path is fixed; a disappearing cgroup is empty.
        group = self.cgroup_root / 'system.slice' / self.name
        if group.is_symlink():
            raise ValueError('untrusted cgroup')
        if not group.exists():
            return True
        if not group.is_dir() or not (group / 'cgroup.procs').is_file():
            raise ValueError('untrusted cgroup')
        def walk_error(error):
            raise error
        for root, dirs, files in os.walk(group, followlinks=False, onerror=walk_error):
            if any((Path(root) / name).is_symlink() for name in dirs):
                raise ValueError('unexpected cgroup link')
            if 'cgroup.procs' not in files or (Path(root) / 'cgroup.procs').is_symlink():
                raise ValueError('cgroup process list is unavailable')
            if (Path(root) / 'cgroup.procs').read_text().strip():
                return False
        return True

    def no_listener(self, proc_root=Path('/proc')):
        if (proc_root / 'self/ns/net').stat().st_ino != (proc_root / '1/ns/net').stat().st_ino:
            raise ValueError('network namespace changed')
        for table in ('tcp', 'tcp6'):
            rows = (proc_root / 'net' / table).read_text().splitlines()[1:]
            for row in rows:
                columns = row.split()
                if len(columns) < 10:
                    raise ValueError('malformed network socket table')
                if columns[3] == '0A' and columns[1].rsplit(':', 1)[-1] == format(self.port, '04X'):
                    return False
        with self.socket_factory(socket.AF_INET, socket.SOCK_STREAM) as sock:
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try:
                sock.bind(('127.0.0.1', self.port))
            except OSError:
                return False
        return True

    def listener_belongs_to_main(self, pid, proc_root=Path('/proc')):
        """Require exactly one loopback listener owned by the unit main PID."""
        process = proc_root / str(pid)
        if ((proc_root / 'self/ns/net').stat().st_ino != (proc_root / '1/ns/net').stat().st_ino or
                (process / 'ns/net').stat().st_ino != (proc_root / 'self/ns/net').stat().st_ino):
            raise ValueError('network namespace changed')
        wanted = '0100007F:' + format(self.port, '04X')
        listeners = []
        for table in ('tcp', 'tcp6'):
            rows = (process / 'net' / table).read_text().splitlines()
            if not rows:
                raise ValueError('network socket table unavailable')
            for row in rows[1:]:
                columns = row.split()
                if len(columns) < 10:
                    raise ValueError('malformed network socket table')
                local, state = columns[1], columns[3]
                if state == '0A' and local.rsplit(':', 1)[-1] == format(self.port, '04X'):
                    listeners.append((table, local, columns[9]))
        if len(listeners) != 1 or listeners[0][0] != 'tcp' or listeners[0][1] != wanted:
            return False
        sockets = set()
        for fd in (process / 'fd').iterdir():
            try:
                target = os.readlink(fd)
            except FileNotFoundError:
                continue
            match = re.fullmatch(r'socket:\[([0-9]+)\]', target)
            if match:
                sockets.add(match.group(1))
        return listeners[0][2] in sockets

    def disable_stop_and_verify_closed(self, timeout=45):
        props = self.show()
        self.verify_fragment(props)
        self.run('/usr/bin/systemctl', 'disable', self.name)
        link = self.wants / self.name
        if link.exists() or link.is_symlink():
            raise ValueError('old boot link still exists')
        fsync_directory(self.wants)
        self.run('/usr/bin/systemctl', 'stop', self.name, timeout=timeout)
        deadline = self.clock() + timeout
        while True:
            props = self.show()
            self.verify_fragment(props)
            if (props['ActiveState'] in ('inactive', 'failed') and props['MainPID'] == '0'
                    and not props['Job'] and props['UnitFileState'] == 'disabled'
                    and self.no_processes() and self.no_listener()):
                return
            if self.clock() >= deadline:
                raise TimeoutError('unit did not stop and close')
            self.sleep(0.25)

    def enable_candidate_durably(self):
        self.run('/usr/bin/systemctl', 'enable', self.name)
        link = self.wants / self.name
        if not link.is_symlink() or link.resolve() != (self.systemd_dir / self.name).resolve():
            raise ValueError('unexpected boot link')
        fsync_directory(self.wants)
        props = self.show()
        self.verify_fragment(props)
        if props['UnitFileState'] != 'enabled' or props['Job']:
            raise ValueError('unit enable is not settled')

    def install_candidate_dropin(self, data, expected_sha, owner=0):
        """Publish exactly one approved drop-in without replacing any file."""
        if (not isinstance(data, bytes) or not data or len(data) > 65536 or
                not re.fullmatch(r'[0-9a-f]{64}', expected_sha) or
                hashlib.sha256(data).hexdigest() != expected_sha):
            raise ValueError('candidate drop-in digest mismatch')
        root = self.systemd_dir.lstat()
        if not stat.S_ISDIR(root.st_mode) or root.st_uid != owner or root.st_mode & 0o022:
            raise ValueError('untrusted systemd directory')
        directory = self.systemd_dir / (self.name + '.d')
        try:
            directory.mkdir(mode=0o755)
            fsync_directory(self.systemd_dir)
        except FileExistsError:
            pass
        info = directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022:
            raise ValueError('untrusted drop-in directory')
        path = directory / 'zz-mcp-open-bootstrap.conf'
        if any(entry.name != path.name for entry in directory.iterdir()):
            raise ValueError('unexpected unit drop-in')
        if path.exists() or path.is_symlink():
            current = path.lstat()
            if (not stat.S_ISREG(current.st_mode) or current.st_uid != owner or
                    stat.S_IMODE(current.st_mode) != 0o644 or digest(path) != expected_sha):
                raise ValueError('existing drop-in differs')
            fsync_directory(directory)
            return
        temporary = directory / ('.candidate-' + secrets.token_hex(12))
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644)
        try:
            with os.fdopen(fd, 'wb') as stream:
                os.fchmod(stream.fileno(), 0o644)
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            os.link(temporary, path, follow_symlinks=False)
            fsync_directory(directory)
            if digest(path) != expected_sha:
                raise ValueError('candidate drop-in readback mismatch')
        finally:
            temporary.unlink(missing_ok=True)
            fsync_directory(directory)

    def candidate_commands(self, data, expected_sha, candidate):
        if not isinstance(data, bytes) or hashlib.sha256(data).hexdigest() != expected_sha:
            raise ValueError('candidate drop-in digest mismatch')
        try:
            lines = data.decode('utf-8').splitlines()
            commands = {}
            for field in ('ExecStartPre', 'ExecStart'):
                entries = [line[len(field) + 1:] for line in lines if line.startswith(field + '=')]
                if len(entries) != 2 or entries[0] or not entries[1]:
                    raise ValueError('ambiguous candidate command')
                commands[field] = shlex.split(entries[1])
        except UnicodeDecodeError as error:
            raise ValueError('invalid candidate drop-in') from error
        for field, args in commands.items():
            if (str(candidate) not in args or args.count(str(candidate)) != 1 or
                    '$' in ''.join(args) or '%' in ''.join(args) or
                    args.count('--gnas-discovery-policy') != 1 or
                    args[args.index('--gnas-discovery-policy') + 1:args.index('--gnas-discovery-policy') + 2] != [str(Path(candidate).parent / 'discovery-policy.json')] or
                    args.count('--gnas-state-root') != 1 or
                    args[args.index('--gnas-state-root') + 1:args.index('--gnas-state-root') + 2] != ['/home/product/services/mcp/wecom/state/discovery'] or
                    args.count('--listen') != 1 or
                    args[args.index('--listen') + 1:args.index('--listen') + 2] != ['127.0.0.1:' + str(self.port)] or
                    ('--check-config' in args) != (field == 'ExecStartPre')):
                raise ValueError('candidate command does not bind executable and listener')
        return commands

    def check_candidate_config(self, data, expected_sha, candidate, policy_sha, environment_files, owner=0):
        """Run a no-listener check with production files mounted read-only."""
        commands = self.candidate_commands(data, expected_sha, candidate)
        candidate = Path(candidate)
        directory = candidate.parent
        for path, expected, immutable in ((candidate, self.candidate_sha, True),
                                          (directory / 'discovery-policy.json', policy_sha, True),
                                          *((Path(name), value, False) for name, value in environment_files.items())):
            info = path.lstat()
            if (not stat.S_ISREG(info.st_mode) or info.st_uid != owner or info.st_mode & 0o022 or
                    (immutable and info.st_mode & 0o222) or digest(path) != expected):
                raise ValueError('candidate or protected environment drift')
        info = directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != owner or info.st_mode & 0o222:
            raise ValueError('candidate release directory is mutable')
        unit = 'mcp-bootstrap-check-' + secrets.token_hex(12) + '.service'
        args = ['/usr/bin/systemd-run', '--quiet', '--wait', '--collect', '--unit=' + unit,
                '--property=Type=oneshot', '--property=RuntimeMaxSec=35', '--property=TimeoutStartSec=35',
                '--property=Restart=no', '--property=KillMode=control-group',
                '--property=User=wecom-mcp-gmzoop', '--property=Group=wecom-mcp-gmzoop',
                '--property=ProtectSystem=strict', '--property=ProtectHome=read-only',
                '--property=NoNewPrivileges=yes', '--property=PrivateTmp=yes',
                '--property=StandardOutput=null', '--property=StandardError=null']
        args.extend('--property=EnvironmentFile=' + name for name in environment_files)
        args.extend(['/usr/bin/env', 'TEAM_MCP_CAPABILITY_CATALOG_PUSH_ENABLED=false', *commands['ExecStartPre']])
        try:
            self.run(*args, timeout=45)
        finally:
            # A client timeout does not imply systemd stopped the transient job.
            if self.run('/usr/bin/systemctl', 'show', unit, '-p', 'LoadState', '--value') != 'not-found':
                try:
                    self.run('/usr/bin/systemctl', 'stop', unit, timeout=45)
                except subprocess.CalledProcessError:
                    if self.run('/usr/bin/systemctl', 'show', unit, '-p', 'LoadState', '--value') != 'not-found':
                        raise
            deadline = self.clock() + 15
            while self.run('/usr/bin/systemctl', 'show', unit, '-p', 'LoadState', '--value') != 'not-found':
                if self.clock() >= deadline:
                    raise TimeoutError('candidate config check unit did not disappear')
                self.sleep(0.25)
        for name, expected in environment_files.items():
            if digest(Path(name)) != expected:
                raise ValueError('protected environment changed during preflight')

    def reload_and_verify_candidate_config(self, data, expected_sha, candidate):
        """Check systemd loaded the exact approved candidate before enable."""
        path = self.systemd_dir / (self.name + '.d') / 'zz-mcp-open-bootstrap.conf'
        if not path.is_file() or path.is_symlink() or digest(path) != expected_sha:
            raise ValueError('candidate drop-in changed')
        commands = self.candidate_commands(data, expected_sha, candidate)
        self.run('/usr/bin/systemctl', 'daemon-reload')
        props = self.show()
        self.verify_fragment(props)
        if (props['DropInPaths'] != str(path) or props['ActiveState'] not in ('inactive', 'failed') or
                props['MainPID'] != '0' or props['Job']):
            raise ValueError('candidate unit is not safely stopped and loaded')
        raw = self.run('/usr/bin/busctl', '--system', '--json=short', 'call', 'org.freedesktop.systemd1',
                       '/org/freedesktop/systemd1', 'org.freedesktop.systemd1.Manager', 'GetUnit', 's', self.name)
        obj = json.loads(raw)
        if obj.get('type') != 'o' or not isinstance(obj.get('data'), list) or len(obj['data']) != 1:
            raise ValueError('invalid systemd unit identity')
        unit_path = obj['data'][0]
        if not isinstance(unit_path, str) or not unit_path.startswith('/org/freedesktop/systemd1/unit/'):
            raise ValueError('invalid systemd object path')
        for field, expected in commands.items():
            raw = self.run('/usr/bin/busctl', '--system', '--json=short', 'get-property', 'org.freedesktop.systemd1',
                           unit_path, 'org.freedesktop.systemd1.Service', field)
            value = json.loads(raw)
            entries = value.get('data')
            if (value.get('type') != 'a(sasbttttuii)' or not isinstance(entries, list) or len(entries) != 1 or
                    not isinstance(entries[0], list) or len(entries[0]) != 10 or entries[0][0] != expected[0] or
                    entries[0][1] != expected or entries[0][2] is not False):
                raise ValueError('loaded candidate command mismatch')
        if digest(path) != expected_sha:
            raise ValueError('candidate drop-in changed after reload')

    def verify_running_candidate(self, candidate):
        props = self.show()
        self.verify_fragment(props)
        if (props['ActiveState'] != 'active' or props['SubState'] != 'running' or props['Job'] or
                props['NRestarts'] != '0' or props['ControlGroup'] != '/system.slice/' + self.name):
            raise ValueError('candidate unit is not running')
        if not props['MainPID'].isdigit() or int(props['MainPID']) < 2:
            raise ValueError('candidate pid is invalid')
        proc = Path('/proc') / props['MainPID'] / 'exe'
        if proc.resolve() != candidate or digest(proc) != self.candidate_sha:
            raise ValueError('running candidate identity mismatch')
        if not self.listener_belongs_to_main(props['MainPID']):
            raise ValueError('candidate listener ownership mismatch')
        if self.show() != props or proc.resolve() != candidate:
            raise ValueError('candidate changed during verification')
