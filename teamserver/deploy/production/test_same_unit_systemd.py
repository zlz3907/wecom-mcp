import errno
import hashlib
import json
import shlex
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import same_unit_systemd as systemd


class UnitTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.systemd = self.root / 'systemd'
        self.systemd.mkdir()
        self.wants = self.systemd / 'multi-user.target.wants'
        self.wants.mkdir()
        self.groups = self.root / 'cgroups'
        self.groups.mkdir()
        self.name = 'wecom-mcp-zhycit-migration.service'
        self.fragment = self.systemd / self.name
        self.fragment.write_text('[Service]\n')
        self.link = self.wants / self.name
        self.link.symlink_to(self.fragment)
        self.events = []
        self.props = {'ActiveState': 'active', 'SubState': 'running', 'MainPID': '42',
            'ControlGroup': '/system.slice/' + self.name, 'Job': '', 'FragmentPath': str(self.fragment),
            'DropInPaths': '', 'UnitFileState': 'enabled', 'KillMode': 'control-group', 'PrivateNetwork': 'no',
            'User': 'wecom-mcp-gmzoop', 'NRestarts': '0'}

        def runner(*args, **kwargs):
            self.events.append(args[1])
            if args[1] == 'show':
                return '\n'.join(k + '=' + v for k, v in self.props.items())
            if args[1] == 'disable':
                self.link.unlink(missing_ok=True)
                self.props['UnitFileState'] = 'disabled'
            if args[1] == 'stop':
                self.props.update(ActiveState='inactive', SubState='dead', MainPID='0', ControlGroup='')
            if args[1] == 'enable':
                self.link.symlink_to(self.fragment)
                self.props['UnitFileState'] = 'enabled'
            return ''

        self.unit = systemd.Unit(self.name, '1' * 64, '2' * 64, runner=runner,
                                 systemd_dir=self.systemd, cgroup_root=self.groups)

    def test_disable_stop_waits_for_cgroup_and_listener(self):
        with patch.object(self.unit, 'verify_fragment'), patch.object(self.unit, 'no_listener', return_value=True):
            self.unit.disable_stop_and_verify_closed()
        self.assertLess(self.events.index('disable'), self.events.index('stop'))
        self.assertFalse(self.link.exists())

    def test_stop_refuses_live_cgroup(self):
        group = self.groups / 'system.slice' / self.name
        group.mkdir(parents=True)
        (group / 'cgroup.procs').write_text('123\n')
        with patch.object(self.unit, 'verify_fragment'), patch.object(self.unit, 'no_listener', return_value=True), \
                patch.object(self.unit, 'clock', side_effect=[0, 100]):
            with self.assertRaises(TimeoutError):
                self.unit.disable_stop_and_verify_closed(timeout=1)

    def test_stop_refuses_listener_or_pending_job(self):
        for change in ('listener', 'job'):
            with self.subTest(change=change):
                self.setUp()
                original_show = self.unit.show
                def show():
                    props = original_show()
                    if change == 'job' and props['ActiveState'] == 'inactive': props['Job'] = 'restart'
                    return props
                with patch.object(self.unit, 'verify_fragment'), patch.object(self.unit, 'show', side_effect=show), \
                        patch.object(self.unit, 'no_listener', return_value=change != 'listener'), \
                        patch.object(self.unit, 'clock', side_effect=[0, 100]):
                    with self.assertRaises(TimeoutError):
                        self.unit.disable_stop_and_verify_closed(timeout=1)

    def test_enable_requires_exact_unit_link(self):
        self.link.unlink()
        self.props['UnitFileState'] = 'disabled'
        with patch.object(self.unit, 'verify_fragment'):
            self.unit.enable_candidate_durably()
        self.assertEqual(self.link.resolve(), self.fragment)

    def test_no_listener_checks_real_bind_failure(self):
        class BusySocket:
            def __enter__(self): return self
            def __exit__(self, *args): pass
            def setsockopt(self, *_args): pass
            def bind(self, _address): raise OSError(errno.EADDRINUSE, 'busy')
        self.unit.socket_factory = lambda *_: BusySocket()
        proc = self.root / 'proc'
        for part in ('self/ns', '1/ns', 'net'): (proc / part).mkdir(parents=True)
        (proc / 'self/ns/net').touch(); os.link(proc / 'self/ns/net', proc / '1/ns/net')
        for table in ('tcp', 'tcp6'): (proc / 'net' / table).write_text('header\n')
        self.assertFalse(self.unit.no_listener(proc))

    def test_no_listener_ignores_time_wait_but_rejects_ipv6_listener(self):
        class Socket:
            def __enter__(self): return self
            def __exit__(self,*args): pass
            def setsockopt(self,*args): self.reuse=True
            def bind(self,_):
                if not getattr(self,'reuse',False): raise OSError(errno.EADDRINUSE,'time wait')
        self.unit.socket_factory = lambda *_: Socket()
        proc=self.root/'proc'
        for part in ('self/ns','1/ns','net'): (proc/part).mkdir(parents=True)
        (proc/'self/ns/net').touch();os.link(proc/'self/ns/net',proc/'1/ns/net')
        row='0: 00000000:1E1D 00000000:0000 {} 0 0 0 0 0 0\n'
        (proc/'net/tcp').write_text('header\n'+row.format('06'))
        (proc/'net/tcp6').write_text('header\n')
        self.assertTrue(self.unit.no_listener(proc))
        (proc/'net/tcp6').write_text('header\n'+row.format('0A'))
        self.assertFalse(self.unit.no_listener(proc))

    def test_properties_must_be_complete_and_unambiguous(self):
        self.unit.run = lambda *_: 'ActiveState=active\nActiveState=inactive'
        with self.assertRaises(ValueError):
            self.unit.show()

    def test_dropin_is_immutable_and_idempotent(self):
        data = b'[Service]\nExecStart=\nExecStart=/candidate\n'
        expected = hashlib.sha256(data).hexdigest()
        self.unit.install_candidate_dropin(data, expected, owner=os.getuid())
        self.unit.install_candidate_dropin(data, expected, owner=os.getuid())
        path = self.systemd / (self.name + '.d') / 'zz-mcp-open-bootstrap.conf'
        self.assertEqual(path.read_bytes(), data)
        other = b'[Service]\nExecStart=/other\n'
        with self.assertRaisesRegex(ValueError, 'existing drop-in differs'):
            self.unit.install_candidate_dropin(other, hashlib.sha256(other).hexdigest(), owner=os.getuid())

    def test_dropin_refuses_unknown_or_symlink_files(self):
        directory = self.systemd / (self.name + '.d')
        directory.mkdir()
        (directory / 'other.conf').write_text('other')
        data = b'[Service]\n'
        with self.assertRaisesRegex(ValueError, 'unexpected unit drop-in'):
            self.unit.install_candidate_dropin(data, hashlib.sha256(data).hexdigest(), owner=os.getuid())

    def test_reload_binds_loaded_commands_before_enable(self):
        candidate = '/safe/release/wecom-mcp-team'
        start = "/usr/bin/env 'SCOPES=zoop.read zoop.write' " + candidate + ' --gnas-discovery-policy /safe/release/discovery-policy.json --gnas-state-root /home/product/services/mcp/wecom/state/discovery --listen 127.0.0.1:7709'
        pre = start + ' --check-config'
        data = ('[Service]\nExecStartPre=\nExecStart=\nExecStartPre=' + pre + '\nExecStart=' + start + '\n').encode()
        checksum = hashlib.sha256(data).hexdigest()
        self.unit.install_candidate_dropin(data, checksum, owner=os.getuid())
        self.props.update(ActiveState='inactive', MainPID='0', UnitFileState='disabled',
                          DropInPaths=str(self.systemd / (self.name + '.d') / 'zz-mcp-open-bootstrap.conf'))
        previous = self.unit.run
        def runner(*args, **kwargs):
            if args[1] == 'daemon-reload': return ''
            if args[0] == '/usr/bin/busctl' and 'GetUnit' in args:
                return json.dumps({'type':'o','data':['/org/freedesktop/systemd1/unit/mock']})
            if args[0] == '/usr/bin/busctl' and 'get-property' in args:
                command = pre if 'ExecStartPre' in args else start
                argv = shlex.split(command)
                return json.dumps({'type':'a(sasbttttuii)','data':[[argv[0],argv,False,0,0,0,0,0,0,0]]})
            return previous(*args, **kwargs)
        self.unit.run = runner
        with patch.object(self.unit, 'verify_fragment'):
            self.unit.reload_and_verify_candidate_config(data, checksum, Path(candidate))
            self.props['DropInPaths'] = ''
            with self.assertRaisesRegex(ValueError, 'not safely stopped'):
                self.unit.reload_and_verify_candidate_config(data, checksum, Path(candidate))

    def test_reload_rejects_unapproved_listener(self):
        candidate = '/safe/release/wecom-mcp-team'
        command = candidate + ' --gnas-discovery-policy /safe/release/discovery-policy.json --gnas-state-root /home/product/services/mcp/wecom/state/discovery --listen 127.0.0.1:1234'
        data = ('[Service]\nExecStartPre=\nExecStart=\nExecStartPre=' + command +
                ' --check-config\nExecStart=' + command + '\n').encode()
        checksum = hashlib.sha256(data).hexdigest()
        self.unit.install_candidate_dropin(data, checksum, owner=os.getuid())
        with self.assertRaisesRegex(ValueError, 'does not bind'):
            self.unit.reload_and_verify_candidate_config(data, checksum, Path(candidate))

    def test_candidate_listener_must_be_unique_loopback_and_owned(self):
        proc = self.root / 'proc'
        for pid in ('self', '1', '42'):
            (proc / pid / 'ns').mkdir(parents=True)
        netns = proc / 'self/ns/net'
        netns.write_bytes(b'namespace')
        os.link(netns, proc / '1/ns/net')
        os.link(netns, proc / '42/ns/net')
        (proc / '42/net').mkdir()
        (proc / '42/fd').mkdir()
        tcp = proc / '42/net/tcp'
        tcp6 = proc / '42/net/tcp6'
        row = '0: 0100007F:1E1D 00000000:0000 0A 0:0 0:0 0 1000 0 4567\n'
        tcp.write_text('header\n' + row)
        tcp6.write_text('header\n')
        (proc / '42/fd/3').symlink_to('socket:[4567]')
        self.assertTrue(self.unit.listener_belongs_to_main('42', proc_root=proc))
        tcp.write_text('header\n' + row.replace('0100007F', '00000000'))
        self.assertFalse(self.unit.listener_belongs_to_main('42', proc_root=proc))
        tcp.write_text('header\n' + row + row)
        self.assertFalse(self.unit.listener_belongs_to_main('42', proc_root=proc))
        tcp.write_text('header\n' + row)
        (proc / '42/fd/3').unlink()
        self.assertFalse(self.unit.listener_belongs_to_main('42', proc_root=proc))

    def test_preflight_uses_read_only_systemd_unit_and_disables_reporter(self):
        release = self.root / 'release'
        release.mkdir()
        self.addCleanup(lambda: release.chmod(0o700) if release.exists() else None)
        binary = release / 'wecom-mcp-team'
        binary.write_bytes(b'binary')
        policy = release / 'discovery-policy.json'
        policy.write_bytes(b'policy')
        binary.chmod(0o500)
        policy.chmod(0o400)
        release.chmod(0o500)
        environment = self.root / 'env'
        environment.write_bytes(b'protected')
        environment.chmod(0o600)
        self.unit.candidate_sha = hashlib.sha256(binary.read_bytes()).hexdigest()
        cmd = str(binary) + ' --gnas-discovery-policy ' + str(policy) + ' --gnas-state-root /home/product/services/mcp/wecom/state/discovery --listen 127.0.0.1:7709'
        data = ('[Service]\nExecStartPre=\nExecStart=\nExecStartPre=' + cmd +
                ' --check-config\nExecStart=' + cmd + '\n').encode()
        calls = []
        def runner(*args, **kwargs):
            calls.append(args)
            if args[0] == '/usr/bin/systemctl' and args[1] == 'show': return 'not-found'
            return ''
        self.unit.run = runner
        self.unit.check_candidate_config(data, hashlib.sha256(data).hexdigest(), binary,
                                         hashlib.sha256(policy.read_bytes()).hexdigest(),
                                         {str(environment): hashlib.sha256(environment.read_bytes()).hexdigest()}, owner=os.getuid())
        command = calls[0]
        self.assertIn('--property=ProtectSystem=strict', command)
        self.assertIn('--property=KillMode=control-group', command)
        self.assertIn('TEAM_MCP_CAPABILITY_CATALOG_PUSH_ENABLED=false', command)
        self.assertIn('--check-config', command)

    def test_preflight_does_not_run_if_config_hash_is_wrong(self):
        release = self.root / 'release'
        release.mkdir()
        binary = release / 'wecom-mcp-team'
        binary.write_bytes(b'binary')
        policy = release / 'discovery-policy.json'
        policy.write_bytes(b'policy')
        binary.chmod(0o500)
        policy.chmod(0o400)
        release.chmod(0o500)
        self.addCleanup(lambda: release.chmod(0o700) if release.exists() else None)
        self.unit.candidate_sha = '0' * 64
        cmd = str(binary) + ' --gnas-discovery-policy ' + str(policy) + ' --gnas-state-root /home/product/services/mcp/wecom/state/discovery --listen 127.0.0.1:7709'
        data = ('[Service]\nExecStartPre=\nExecStart=\nExecStartPre=' + cmd +
                ' --check-config\nExecStart=' + cmd + '\n').encode()
        with self.assertRaisesRegex(ValueError, 'candidate or protected environment drift'):
            self.unit.check_candidate_config(data, hashlib.sha256(data).hexdigest(), binary, '1' * 64, {}, owner=os.getuid())
        self.assertEqual(self.events, [])

    def test_preflight_timeout_stops_transient_unit(self):
        release = self.root / 'release'
        release.mkdir()
        self.addCleanup(lambda: release.chmod(0o700) if release.exists() else None)
        binary, policy = release / 'wecom-mcp-team', release / 'discovery-policy.json'
        binary.write_bytes(b'binary'); policy.write_bytes(b'policy')
        binary.chmod(0o500); policy.chmod(0o400); release.chmod(0o500)
        self.unit.candidate_sha = hashlib.sha256(binary.read_bytes()).hexdigest()
        cmd = str(binary) + ' --gnas-discovery-policy ' + str(policy) + ' --gnas-state-root /home/product/services/mcp/wecom/state/discovery --listen 127.0.0.1:7709'
        data = ('[Service]\nExecStartPre=\nExecStart=\nExecStartPre=' + cmd +
                ' --check-config\nExecStart=' + cmd + '\n').encode()
        calls = []
        def runner(*args, **kwargs):
            calls.append(args)
            if args[0] == '/usr/bin/systemd-run': raise subprocess.TimeoutExpired(args, 45)
            if args[1] == 'show': return 'not-found' if any(call[1] == 'stop' for call in calls if len(call) > 1) else 'loaded'
            return ''
        self.unit.run = runner
        with self.assertRaises(subprocess.TimeoutExpired):
            self.unit.check_candidate_config(data, hashlib.sha256(data).hexdigest(), binary,
                                             hashlib.sha256(policy.read_bytes()).hexdigest(), {}, owner=os.getuid())
        self.assertTrue(any(call[1] == 'stop' for call in calls if len(call) > 1))


if __name__ == '__main__':
    unittest.main()
