import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('blue', HERE / 'mcp_blue_green_executor.py')
b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b)


class BlueGreenExecutorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.addCleanup(self.tmp.cleanup)
        self.rid = '20261104T120000Z-' + 'a' * 12
        self.releases = self.root / 'releases'
        self.releases.mkdir()
        self.directory = self.releases / self.rid
        self.directory.mkdir()
        (self.directory / 'wecom-mcp-team').write_bytes(b'binary')
        (self.directory / 'discovery-policy.json').write_text('{}')
        (self.directory / 'blue.conf').write_bytes(b.blue_dropin(self.rid))
        (self.directory / 'plan.json').write_text(json.dumps(b.PLAN))
        self.manifest = {
            'schema_version': 1, 'environment': b.c.ENVIRONMENT, 'release_id': self.rid,
            'deployment_topology': 'blue-green-isolated-ready-worker', 'plan': b.PLAN,
            'fleet_state_vector': b.executor_vector(), 'fleet_state_vector_sha256': b.c.fleet_state_vector_sha256(),
            'source_clean': True, 'target': 'linux/amd64', 'ci_url': 'https://ci.example/run',
            'files': {name: b.c.sha(self.directory / name) for name in b.FILES[:-1]},
            'expected_runtime_path': '/home/product/services/mcp/wecom/releases/20261103T000000Z-aaaaaaaaaaaa/wecom-mcp-team',
            'expected_binary_sha256': 'a' * 64, 'expected_unit_fingerprint': 'b' * 64,
            'expected_runtime_config_fingerprint': 'c' * 64, 'expected_gnas_release_id': 'gnas',
            'expected_gnas_binary_sha256': 'd' * 64,
        }
        (self.directory / 'manifest.json').write_text(json.dumps(self.manifest))
        (self.directory / 'SHA256SUMS').write_text(''.join(b.c.sha(self.directory / name) + '  ' + name + '\n' for name in sorted(b.FILES)))

    def unsafe_regular(self, path, root=False):
        return path.lstat()

    def test_candidate_rejects_mutated_blue_plan_and_missing_isolation_flag(self):
        with patch.object(b.c, 'regular', side_effect=self.unsafe_regular):
            got = b.verify(self.directory, self.rid)
            self.assertEqual(got['release_id'], self.rid)
        for mutate in (
            lambda m: m['plan']['blue'].__setitem__('loopback', '127.0.0.1:7702'),
            lambda m: m['fleet_state_vector'].__setitem__(1, {'host': 'mcp.jianpinke.com', 'state': 'ready'}),
        ):
            bad = json.loads(json.dumps(self.manifest)); mutate(bad)
            (self.directory / 'manifest.json').chmod(0o600)
            (self.directory / 'manifest.json').write_text(json.dumps(bad))
            (self.directory / 'manifest.json').chmod(0o400)
            with patch.object(b.c, 'regular', side_effect=self.unsafe_regular):
                with self.assertRaises(ValueError):
                    b.verify(self.directory, self.rid)
            (self.directory / 'manifest.json').chmod(0o600)
            (self.directory / 'manifest.json').write_text(json.dumps(self.manifest))
            (self.directory / 'manifest.json').chmod(0o400)

    def test_blue_dropin_has_distinct_state_listener_flag_and_cgroup(self):
        raw = b.blue_dropin(self.rid).decode()
        self.assertIn('--listen 127.0.0.1:7703', raw)
        self.assertIn('--gnas-ready-worker-isolation', raw)
        self.assertIn(str(b.BLUE_STATE), raw)
        self.assertIn('KillMode=control-group', raw)
        self.assertNotIn('--gnas-fleet-runtime', raw)
        self.assertNotIn('--config ', raw)

    def test_atomic_switch_requires_readback_and_never_invokes_nginx(self):
        colors = [{'color': 'green'}, {'color': 'blue'}]
        with patch.object(b.c, 'regular'), patch.object(b, 'read_active', side_effect=colors), patch.object(b, 'run') as run:
            b.switch('blue', 'green')
            args = run.call_args.args
            self.assertEqual(args[0], str(b.SWITCHER))
            self.assertIn('--expected-color', args)
            self.assertNotIn('nginx', args)
        with patch.object(b.c, 'regular'), patch.object(b, 'read_active', side_effect=[{'color': 'green'}, {'color': 'green'}]), patch.object(b, 'run'):
            with self.assertRaisesRegex(ValueError, 'readback'):
                b.switch('blue', 'green')

    def test_deploy_orders_blue_before_atomic_cutover_and_rolls_back_green(self):
        manifest = dict(self.manifest, files={'wecom-mcp-team': 'f' * 64, 'blue.conf': 'e' * 64, 'plan.json': 'd' * 64})
        green = {'runtime_verified': True, 'active': 'active', 'restarts': 0,
                 'runtime_path': manifest['expected_runtime_path'], 'binary_sha256': manifest['expected_binary_sha256'],
                 'unit_fingerprint': manifest['expected_unit_fingerprint'], 'runtime_config_fingerprint': manifest['expected_runtime_config_fingerprint']}
        state = {'color': 'green'}
        calls = []
        def switch(target, expected):
            self.assertEqual(state['color'], expected); state['color'] = target; calls.append(('switch', target))
        def run(*args): calls.append(args); return ''
        with patch.object(b, 'verify', return_value=manifest), patch.object(b.c, 'RELEASES', self.releases), patch.object(b.c, 'status', return_value=green), patch.object(b.c, 'check_gnas'), patch.object(b.c, 'sha', return_value='s' * 64), patch.object(b.c, 'regular'), patch.object(b, 'trusted_directory'), patch.object(b, 'gateway_status', return_value={'ActiveState': 'active', 'MainPID': '7'}), patch.object(b, 'blue_status', side_effect=[{'ActiveState': 'inactive', 'MainPID': '0', 'KillMode': 'control-group'}, {'ActiveState': 'active', 'MainPID': '42', 'KillMode': 'control-group'}]), patch.object(b, 'read_active', side_effect=lambda: dict(state)), patch.object(b.c, 'check_approval'), patch.object(b.c, 'approval'), patch.object(b.c, 'atomic'), patch.object(b, 'check_vector') as check, patch.object(b, 'switch', side_effect=switch), patch.object(b, 'run', side_effect=run), patch.object(b, 'BLUE_STATE', self.root):
            result = b.deploy(self.rid, 'APR-20261104T120000Z-testonly')
        self.assertEqual(result['state'], 'blue_deployed')
        self.assertEqual(calls[0], ('systemctl', 'daemon-reload'))
        self.assertEqual(calls[1], ('systemctl', 'start', b.BLUE_UNIT))
        self.assertEqual(calls[2], ('switch', 'blue'))
        self.assertEqual(check.call_args_list[0].args, ('blue', False))
        self.assertEqual(check.call_args_list[1].args, ('blue', True))

    def test_failure_restores_green_before_stopping_blue(self):
        manifest = dict(self.manifest, files={'wecom-mcp-team': 'f' * 64, 'blue.conf': 'e' * 64, 'plan.json': 'd' * 64})
        green = {'runtime_verified': True, 'active': 'active', 'restarts': 0,
                 'runtime_path': manifest['expected_runtime_path'], 'binary_sha256': manifest['expected_binary_sha256'],
                 'unit_fingerprint': manifest['expected_unit_fingerprint'], 'runtime_config_fingerprint': manifest['expected_runtime_config_fingerprint']}
        state = {'color': 'green'}; calls = []
        def switch(target, expected): self.assertEqual(state['color'], expected); state['color'] = target; calls.append(('switch', target))
        def vector(color, public):
            if color == 'blue' and public:
                raise ValueError('public contract failed')
        def run(*args): calls.append(args); return ''
        with patch.object(b, 'verify', return_value=manifest), patch.object(b.c, 'RELEASES', self.releases), patch.object(b.c, 'status', return_value=green), patch.object(b.c, 'check_gnas'), patch.object(b.c, 'sha', return_value='s' * 64), patch.object(b.c, 'regular'), patch.object(b, 'trusted_directory'), patch.object(b, 'gateway_status', return_value={'ActiveState': 'active', 'MainPID': '7'}), patch.object(b, 'blue_status', side_effect=[{'ActiveState': 'inactive', 'MainPID': '0', 'KillMode': 'control-group'}, {'ActiveState': 'active', 'MainPID': '42', 'KillMode': 'control-group'}]), patch.object(b, 'read_active', side_effect=lambda: dict(state)), patch.object(b.c, 'check_approval'), patch.object(b.c, 'approval'), patch.object(b.c, 'atomic'), patch.object(b, 'check_vector', side_effect=vector), patch.object(b, 'switch', side_effect=switch), patch.object(b, 'run', side_effect=run), patch.object(b, 'BLUE_STATE', self.root):
            with self.assertRaisesRegex(ValueError, 'green entrypoint restored'):
                b.deploy(self.rid, 'APR-20261104T120000Z-testonly')
        self.assertEqual(calls[-2], ('switch', 'green'))
        self.assertEqual(calls[-1], ('systemctl', 'stop', b.BLUE_UNIT))

    def test_inactive_gateway_refuses_before_approval_or_blue_start(self):
        manifest = dict(self.manifest, files={'wecom-mcp-team': 'f' * 64, 'blue.conf': 'e' * 64, 'plan.json': 'd' * 64})
        green = {'runtime_verified': True, 'active': 'active', 'restarts': 0,
                 'runtime_path': manifest['expected_runtime_path'], 'binary_sha256': manifest['expected_binary_sha256'],
                 'unit_fingerprint': manifest['expected_unit_fingerprint'], 'runtime_config_fingerprint': manifest['expected_runtime_config_fingerprint']}
        with patch.object(b, 'verify', return_value=manifest), patch.object(b.c, 'RELEASES', self.releases), patch.object(b.c, 'status', return_value=green), patch.object(b.c, 'check_gnas'), patch.object(b, 'blue_status', return_value={'ActiveState': 'inactive', 'MainPID': '0', 'KillMode': 'control-group'}), patch.object(b, 'gateway_status', return_value={'ActiveState': 'inactive', 'MainPID': '0'}), patch.object(b.c, 'check_approval') as approval, patch.object(b, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'gateway'):
                b.deploy(self.rid, 'APR-20261104T120000Z-testonly')
            approval.assert_not_called(); run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
