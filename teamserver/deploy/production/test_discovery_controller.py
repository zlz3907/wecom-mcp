import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('discovery_controller_test_target', HERE / 'mcp_discovery_release_controller.py')
TARGET = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(TARGET)


class DiscoveryProfileTests(unittest.TestCase):
    def test_direct_mutations_are_disabled_until_fail_closed_executor_exists(self):
        for action in (TARGET.c.deploy, TARGET.c.rollback):
            with self.subTest(action=action), patch.object(TARGET.c, 'status') as status:
                with self.assertRaisesRegex(ValueError, 'fail-closed executor'):
                    action('20261106T010203Z-' + 'a' * 12, 'unused')
                status.assert_not_called()

    def test_main_dispatch_rejects_deploy_and_rollback(self):
        with tempfile.TemporaryDirectory() as directory:
            control = Path(directory).resolve()
            actual_require = TARGET.c.require
            def require_for_fixture(ok, message):
                if message != 'controller not installed':
                    actual_require(ok, message)
            for action in ('deploy', 'rollback'):
                with self.subTest(action=action), patch.object(TARGET.c, 'CONTROL', control), \
                        patch.object(TARGET.c.os, 'geteuid', return_value=0), \
                        patch.object(TARGET.c, 'require', side_effect=require_for_fixture), \
                        patch.object(TARGET.c.sys, 'argv', ['controller', action, '20261106T010203Z-' + 'a' * 12, 'unused']), \
                        patch.object(TARGET.c, 'status') as status:
                    with self.assertRaisesRegex(ValueError, 'fail-closed executor'):
                        TARGET.c.main()
                    status.assert_not_called()

    def test_profile_is_fixed_to_live_7703_service(self):
        self.assertEqual(TARGET.PROFILE, 'gnas-discovery-21462d2-v1')
        self.assertEqual(TARGET.UNIT, 'wecom-mcp-discovery-21462d2.service')
        self.assertEqual(TARGET.LOOPBACK, '127.0.0.1:7703')
        self.assertEqual(TARGET.RESOURCES['zhycit.com'], 'https://zhycit.com/mcp')
        self.assertEqual(TARGET.HOSTS, ('zhycit.com',))
        self.assertEqual(len(TARGET.OBSERVED_FLEET_SNAPSHOT), 4)
        self.assertNotIn('7702', str(TARGET.DROPIN))

    def test_dropin_preserves_discovery_flags_and_7703(self):
        raw = TARGET.dropin('20261106T010203Z-' + 'a' * 12).decode()
        self.assertIn('127.0.0.1:7703', raw)
        self.assertIn('--gnas-ready-worker-isolation', raw)
        self.assertIn('--gnas-ready-worker-timeout 5s', raw)
        self.assertIn('--gnas-fleet-refresh 30s', raw)
        self.assertIn('Environment="TEAM_MCP_ADVERTISED_SCOPES=zoop.read zoop.write"', raw)
        self.assertIn('Environment=TEAM_MCP_ADVERTISED_SCOPES_RESOURCE=https://zhycit.com/mcp', raw)
        self.assertNotIn('127.0.0.1:7702', raw)
        self.assertIn(str(TARGET.STATE), raw)

    def test_health_checks_every_authoritative_binding(self):
        with patch.object(TARGET.c, 'require_runtime'), patch.object(TARGET, '_ready') as ready, patch.object(TARGET, '_unready') as unready:
            TARGET._healthy('/runtime', 'a' * 64, TARGET.HOSTS)
        self.assertEqual({call.args[0] for call in ready.call_args_list}, {'zhycit.com', 'mcp.wesiyu.com', 'mcp.rtyouth.com'})
        self.assertEqual([call.args[0] for call in unready.call_args_list], ['mcp.jianpinke.com'])

    def test_gnas_readiness_must_include_exact_authoritative_fleet(self):
        with patch.object(TARGET, '_base_check_gnas'), patch.object(TARGET.c, 'read_json', return_value={'authoritative_fleet_vector': TARGET.fleet_state_vector()}):
            TARGET._check_gnas({})
        with patch.object(TARGET, '_base_check_gnas'), patch.object(TARGET.c, 'read_json', return_value={'authoritative_fleet_vector': TARGET.fleet_state_vector()[:1]}):
            with self.assertRaisesRegex(ValueError, 'authoritative fleet'):
                TARGET._check_gnas({})

    def test_local_and_public_readiness_contracts_are_distinct(self):
        with patch.object(TARGET.c, 'probe') as probe:
            TARGET._ready('mcp.jyiai.com', loopback=TARGET.LOOPBACK, public_resource_metadata=True)
        calls = [(call.args, call.kwargs) for call in probe.call_args_list]
        self.assertIn((('mcp.jyiai.com', '/healthz', 200), {'loopback': TARGET.LOOPBACK}), calls)
        self.assertIn((('mcp.jyiai.com', '/.well-known/oauth-protected-resource', 200), {'loopback': TARGET.LOOPBACK}), calls)
        self.assertIn((('mcp.jyiai.com', '/mcp', 401, True), {}), calls)
        self.assertIn((('mcp.jyiai.com', '/.well-known/oauth-protected-resource/mcp', 200, True), {}), calls)

    def test_unready_checks_only_fixed_public_route(self):
        with patch.object(TARGET.c, 'probe') as probe:
            TARGET._unready('mcp.jyiai.com', loopback=TARGET.LOOPBACK)
        self.assertEqual(probe.call_count, 4)
        self.assertTrue(all(call.kwargs.get('public', False) or call.kwargs.get('loopback') == TARGET.LOOPBACK
                            for call in probe.call_args_list))
        self.assertTrue(all(call.kwargs.get('public', False) is False
                            for call in probe.call_args_list))

    def test_zoop_unmapped_route_is_fail_closed_in_observation_contract(self):
        with patch.object(TARGET.c, 'probe') as probe:
            TARGET._unmapped('mcp.jyiai.com', loopback=TARGET.LOOPBACK)
        self.assertEqual(probe.call_count, 8)
        self.assertTrue(all(call.args[0] == 'mcp.jyiai.com' for call in probe.call_args_list))
        self.assertTrue(all(call.args[2] == 421 for call in probe.call_args_list[:4]))
        self.assertTrue(all(call.args[2] == 404 and call.kwargs.get('public') is True
                            for call in probe.call_args_list[4:]))

    def test_deploy_receipt_binds_profile_lineage_and_resource(self):
        directory = Path('/tmp/discovery-test-candidate')
        base = {
            'release_id': '20261106T010203Z-' + 'b' * 12,
            'expected_runtime_path': str(TARGET.RELEASES / 'current' / 'wecom-mcp-team'),
            'expected_binary_sha256': 'a' * 64,
            'expected_unit_fingerprint': 'b' * 64,
            'expected_runtime_config_fingerprint': 'c' * 64,
            'expected_gnas_release_id': 'gnas-current',
            'expected_gnas_binary_sha256': 'd' * 64,
            'expected_unmanaged_unit_fingerprint': 'e' * 64,
            'recovery_mode': 'database-only',
            'recovery_hosts': list(TARGET.HOSTS),
            'files': {'wecom-mcp-team': 'a' * 64, 'discovery-policy.json': 'b' * 64,
                      'service.conf': 'c' * 64, 'recovery.conf': 'd' * 64},
            'expected_current_release_id': TARGET.CURRENT_RELEASE_ID,
            'expected_current_binary_sha256': TARGET.CURRENT_BINARY_SHA256,
            'expected_previous_release_id': TARGET.PREVIOUS_RELEASE_ID,
            'expected_previous_binary_sha256': TARGET.PREVIOUS_BINARY_SHA256,
            'expected_current_unit_fragment_sha256': 'f' * 64,
            'expected_current_archive_sha256': '1' * 64,
            'expected_current_manifest_sha256': '2' * 64,
            'expected_current_catalogue_sha256': '3' * 64,
            'legacy_smoke_sha256': '4' * 64,
        }
        with patch.object(TARGET, '_base_deploy_fields', return_value={}):
            expected = TARGET.deploy_fields(base, directory)
        self.assertEqual(expected['profile'], TARGET.PROFILE)
        self.assertEqual(expected['service_unit'], TARGET.UNIT)
        self.assertEqual(expected['loopback'], TARGET.LOOPBACK)
        self.assertEqual(expected['resources'], TARGET.RESOURCES)
        self.assertEqual(expected['expected_previous_release_id'], TARGET.PREVIOUS_RELEASE_ID)

    def test_manifest_lineage_constants_are_not_7702_baseline(self):
        self.assertEqual(TARGET.CURRENT_RELEASE_ID, '20261105T001200-21462d2dfb22')
        self.assertEqual(TARGET.PREVIOUS_RELEASE_ID, '20261105T000000-396f2500540e')
        self.assertEqual(len(TARGET.CURRENT_BINARY_SHA256), 64)
        self.assertEqual(len(TARGET.PREVIOUS_BINARY_SHA256), 64)


if __name__ == '__main__':
    unittest.main()
