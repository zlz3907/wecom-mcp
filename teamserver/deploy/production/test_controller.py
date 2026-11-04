import datetime as dt
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('controller', Path(__file__).with_name('mcp_release_controller.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name).resolve()
        self.addCleanup(self.tmp.cleanup)

    def test_baseline_uses_process_not_current_symlink(self):
        expected = {'expected_runtime_path': '/releases/actual/bin', 'expected_binary_sha256': 'a'*64, 'expected_unit_fingerprint': 'b'*64, 'expected_runtime_config_fingerprint':'e'*64}
        current = {k.removeprefix('expected_'): v for k, v in expected.items()}
        current.update(active='active', restarts=0, runtime_verified=True)
        self.assertTrue(c.baseline_matches(current, expected))
        for key, value in [('runtime_verified',False),('runtime_path','/releases/stale/bin'),('binary_sha256','c'*64),('unit_fingerprint','d'*64),('runtime_config_fingerprint','f'*64),('restarts',1),('active','failed')]:
            self.assertFalse(c.baseline_matches(dict(current, **{key:value}), expected))

    def test_service_template_uses_database_discovery_only(self):
        raw = c.dropin('20260923T080000Z-'+'a'*12).decode()
        self.assertIn('--gnas-discovery-policy ', raw)
        self.assertIn('--gnas-state-root ', raw)
        self.assertNotIn('--gnas-fleet-runtime ', raw)
        self.assertNotIn('Environment=', raw)
        self.assertNotIn('nginx', raw)
        with self.assertRaises(ValueError):
            c.dropin('../escape')

    def test_receipt_exact_expiring_and_single_use(self):
        approvals=self.root/'approvals';approvals.mkdir()
        control=self.root/'control';(control/'used').mkdir(parents=True)
        aid='APR-20260923T080000Z-testonly';now=dt.datetime.now(dt.timezone.utc)
        expected={'release_id':'20260923T080000Z-'+'a'*12, 'binary_sha256':'a'*64}
        base=dict(schema_version=1, environment=c.ENVIRONMENT, approval_id=aid, action='deploy', approved_by='test-admin', approved_at=(now-dt.timedelta(minutes=1)).isoformat(), expires_at=(now+dt.timedelta(hours=1)).isoformat(), **expected)
        path=approvals/(aid+'.json')
        def write(data):
            if path.exists():path.chmod(0o600)
            path.write_text(json.dumps(data));path.chmod(0o400)
        original=c.regular
        with patch.object(c,'APPROVALS',approvals),patch.object(c,'CONTROL',control),patch.object(c,'regular',side_effect=lambda p,root=False:original(p,False)):
            for changes in ({'binary_sha256':'b'*64},{'environment':'other'},{'action':'rollback'},{'expires_at':(now-dt.timedelta(seconds=1)).isoformat()},{'expires_at':(now+dt.timedelta(hours=5)).isoformat()},{'extra':'not-allowed'}):
                write(dict(base,**changes))
                with self.assertRaises(ValueError):c.approval(aid,'deploy',expected)
                self.assertFalse((control/'used'/aid).exists())
            write(base);c.approval(aid,'deploy',expected)
            with self.assertRaises(FileExistsError):c.approval(aid,'deploy',expected)

    def test_missing_receipt_never_switches(self):
        with patch.object(c,'APPROVALS',self.root),patch.object(c,'run') as run:
            with self.assertRaises(FileNotFoundError):c.approval('APR-20260923T080000Z-testonly','deploy',{})
            run.assert_not_called()

    def test_automatic_rollback_on_restart_or_contract_failure(self):
        rid='20260923T080000Z-'+'a'*12
        releases=self.root/'releases';directory=releases/rid;directory.mkdir(parents=True)
        (directory/'service.conf').write_text('[Service]\n')
        (directory/'manifest.json').write_text('{}')
        previous=releases/'previous'/'wecom-mcp-team';previous.parent.mkdir();previous.write_bytes(b'old')
        control=self.root/'control';(control/'rollback').mkdir(parents=True)
        state=self.root/'state';state.mkdir()
        override=self.root/'service.conf'
        m={'ci_url':'https://ci.example/run','release_id':rid,'expected_runtime_path':str(previous),'expected_binary_sha256':c.sha(previous),'expected_unit_fingerprint':'b'*64,'expected_runtime_config_fingerprint':'e'*64,'expected_unmanaged_unit_fingerprint':'f'*64,'recovery_mode':'database-only','recovery_hosts':list(c.HOSTS),'expected_gnas_release_id':'gnas','expected_gnas_binary_sha256':'c'*64,'files':{'wecom-mcp-team':'d'*64,'recovery.conf':'f'*64}}
        original=c.regular
        with patch.object(c,'RELEASES',releases),patch.object(c,'CONTROL',control),patch.object(c,'STATE',state),patch.object(c,'DROPIN',override),patch.object(c,'verify',return_value=m),patch.object(c,'status'),patch.object(c,'baseline_matches',return_value=True),patch.object(c,'check_gnas'),patch.object(c,'healthy'),patch.object(c,'regular',side_effect=lambda p,root=False:original(p,False)),patch.object(c,'check_approval'),patch.object(c,'preflight_recovery'),patch.object(c,'recovery_matches'),patch.object(c,'approval'),patch.object(c,'restart_and_verify',side_effect=ValueError('test failure')),patch.object(c,'restore') as restore:
            with self.assertRaisesRegex(ValueError,'same-version database-only recovery completed'):c.deploy(rid,'APR-20260923T080000Z-testonly')
            restore.assert_called_once_with(rid)
            self.assertTrue((control/'rollback'/rid/'baseline.json').exists())

    def test_observation_has_eleven_samples_and_ten_intervals(self):
        m={'files':{'wecom-mcp-team':'a'*64,'service.conf':'s'}}
        with patch.object(c,'verify',return_value=m),patch.object(c,'read_json',return_value={'unit_fingerprint':'u','runtime_config_fingerprint':'r'}),patch.object(c,'unit_fingerprint',return_value='u'),patch.object(c,'runtime_fingerprint',return_value='r'),patch.object(c,'recovery_matches'),patch.object(c,'sha',return_value='s'),patch.object(c,'healthy') as healthy,patch.object(c.time,'sleep') as sleep,patch('builtins.print'):
            result=c.observe('20260923T080000Z-'+'a'*12)
            self.assertEqual(healthy.call_count,11);self.assertEqual(sleep.call_count,10)
            sleep.assert_called_with(30);self.assertFalse(result['owner_accepted'])

    def test_delayed_start_and_startup_deadline(self):
        current={'runtime_path':'/expected','binary_sha256':'a'*64,'restarts':0,'unit_fingerprint':'u','runtime_config_fingerprint':'r'}
        with patch.object(c,'run'),patch.object(c,'status',return_value=current),patch.object(c,'unit_fingerprint',return_value='u'),patch.object(c,'runtime_fingerprint',return_value='r'),patch.object(c,'healthy',side_effect=[OSError('not listening'),None]) as health,patch.object(c.time,'sleep'):
            c.restart_and_verify('/expected','a'*64,c.HOSTS)
            self.assertEqual(health.call_count,2)
        with patch.object(c,'run'),patch.object(c,'status',return_value=current),patch.object(c,'unit_fingerprint',return_value='u'),patch.object(c,'runtime_fingerprint',return_value='r'),patch.object(c,'healthy',side_effect=OSError('not ready')),patch.object(c.time,'monotonic',side_effect=[0,46]):
            with self.assertRaisesRegex(ValueError,'deadline'):c.restart_and_verify('/expected','a'*64,c.HOSTS)

    def test_unverified_process_cannot_pass_health(self):
        current=dict(active='active',restarts=0,runtime_path='/expected',binary_sha256='a'*64,runtime_verified=False)
        with patch.object(c,'status',return_value=current),patch.object(c,'probe') as probe:
            with self.assertRaises(ValueError):c.healthy('/expected','a'*64,c.HOSTS)
            probe.assert_not_called()

    def test_new_binary_preflight_is_read_only_and_failure_keeps_live_unit(self):
        rid='20260923T080000Z-'+'a'*12
        with patch.object(c,'verify',return_value={}),patch.object(c,'recovery_matches'),patch.object(c,'run') as run:
            c.preflight_recovery(rid)
            args=run.call_args.args
            self.assertEqual(args[0],'systemd-run')
            self.assertIn(str(c.RELEASES/rid/'wecom-mcp-team'),args)
            self.assertNotIn('--gnas-static-only',args)
            self.assertNotIn('--gnas-fleet-runtime',args)
            self.assertNotIn('--fleet',args)
            self.assertEqual(args[-1],'--check-config')
            self.assertIn('--property=StandardOutput=null',args)
            self.assertIn('--property=StandardError=null',args)
            self.assertNotIn('restart',args)
            run.side_effect=ValueError('controlled check failed')
            with self.assertRaises(ValueError):c.preflight_recovery(rid)

    def test_approved_configuration_drift_refuses_restart(self):
        with patch.object(c,'run') as run,patch.object(c,'unit_fingerprint',return_value='u'),patch.object(c,'runtime_fingerprint',return_value='changed'):
            with self.assertRaisesRegex(ValueError,'before restart'):
                c.restart_and_verify('/expected','a'*64,c.HOSTS,expected_config_fingerprint='approved')
            run.assert_called_once_with('systemctl','daemon-reload')

    def test_optimized_staging_still_rejects_bad_input_before_ssh(self):
        import subprocess
        import sys
        directory=self.root/'candidate';directory.mkdir()
        rid="bad';touch /tmp/must-not-run;#"
        for name in c.FILES:
            (directory/name).write_text(json.dumps({'release_id':rid}) if name=='manifest.json' else 'test')
        (directory/'SHA256SUMS').write_text(''.join(c.sha(directory/name)+'  '+name+'\n' for name in sorted(c.FILES)))
        scripts=self.root/'bin';scripts.mkdir();marker=self.root/'ssh-called'
        ssh=scripts/'ssh';ssh.write_text('#!/bin/sh\ntouch "'+str(marker)+'"\nexit 1\n');ssh.chmod(0o700)
        env=dict(os.environ,PATH=str(scripts),PYTHONOPTIMIZE='1')
        result=subprocess.run([sys.executable,'-O',str(Path(__file__).with_name('stage-candidate.py')),'--candidate',str(directory)],capture_output=True,text=True,env=env)
        self.assertNotEqual(result.returncode,0);self.assertIn('invalid release ID',result.stderr);self.assertFalse(marker.exists())

    def test_ready_worker_isolation_manifest_freezes_state_and_blue_green_plan(self):
        manifest = {'schema_version': 3, **c.isolation_manifest_fields()}
        self.assertTrue(c.verify_isolation_manifest(manifest))
        self.assertEqual(manifest['fleet_state_vector'], [
            {'host': 'mcp.wesiyu.com', 'state': 'ready'},
            {'host': 'mcp.jianpinke.com', 'state': 'tenant_unavailable'},
            {'host': 'mcp.rtyouth.com', 'state': 'ready'},
        ])
        for mutate in (
            lambda m: m['fleet_state_vector'].__setitem__(1, {'host': 'mcp.jianpinke.com', 'state': 'ready'}),
            lambda m: m.__setitem__('fleet_state_vector_sha256', '0' * 64),
            lambda m: m['blue_green_plan']['blue'].__setitem__('loopback', '127.0.0.1:7702'),
            lambda m: m.__setitem__('deployment_topology', 'shared-restart'),
        ):
            bad = json.loads(json.dumps(manifest))
            mutate(bad)
            with self.assertRaises(ValueError):
                c.verify_isolation_manifest(bad)
        with self.assertRaises(ValueError):
            c.verify_isolation_manifest({'schema_version': 2, 'ready_worker_isolation': True})

    def test_isolation_receipt_fields_bind_frozen_vector(self):
        required = ('release_id', 'expected_runtime_path', 'expected_binary_sha256', 'expected_unit_fingerprint', 'expected_runtime_config_fingerprint', 'expected_gnas_release_id', 'expected_gnas_binary_sha256', 'expected_unmanaged_unit_fingerprint', 'recovery_mode', 'recovery_hosts')
        manifest = {
            'release_id': '20260923T080000Z-' + 'a' * 12,
            'files': {'wecom-mcp-team': 'a' * 64, 'recovery.conf': 'b' * 64},
            **{key: ('database-only' if key == 'recovery_mode' else list(c.HOSTS) if key == 'recovery_hosts' else 'a' * 64 if key.endswith('fingerprint') or key.endswith('sha256') else '/release/path' if key == 'expected_runtime_path' else 'gnas') for key in required if key != 'release_id'},
            **c.isolation_manifest_fields(),
        }
        (self.root / 'manifest.json').write_text('{}')
        fields = c.deploy_fields(manifest, self.root)
        self.assertTrue(fields['ready_worker_isolation'])
        self.assertEqual(fields['fleet_state_vector'], c.fleet_state_vector())
        self.assertEqual(fields['fleet_state_vector_sha256'], c.fleet_state_vector_sha256())
        self.assertEqual(fields['blue_green_plan'], c.BLUE_GREEN_PLAN)

    def test_isolation_receipt_rejects_state_vector_drift(self):
        approvals = self.root / 'approvals'; approvals.mkdir()
        aid = 'APR-20260923T080000Z-testonly'
        now = dt.datetime.now(dt.timezone.utc)
        expected = {'release_id': '20260923T080000Z-' + 'a' * 12, **c.isolation_manifest_fields()}
        receipt = json.loads(json.dumps(dict(schema_version=1, environment=c.ENVIRONMENT, approval_id=aid, action='blue-green-plan', approved_by='test-admin', approved_at=(now-dt.timedelta(minutes=1)).isoformat(), expires_at=(now+dt.timedelta(hours=1)).isoformat(), **expected)))
        path = approvals / (aid + '.json')
        path.write_text(json.dumps(receipt)); path.chmod(0o400)
        original = c.regular
        with patch.object(c, 'APPROVALS', approvals), patch.object(c, 'regular', side_effect=lambda p,root=False:original(p,False)):
            self.assertEqual(c.check_approval(aid, 'blue-green-plan', expected)['fleet_state_vector'], c.fleet_state_vector())
            receipt['fleet_state_vector'][1]['state'] = 'ready'
            path.chmod(0o600); path.write_text(json.dumps(receipt)); path.chmod(0o400)
            with self.assertRaises(ValueError):
                c.check_approval(aid, 'blue-green-plan', expected)

    def test_shared_green_deploy_refuses_isolation_plan_before_runtime_calls(self):
        rid = '20260923T080000Z-' + 'a' * 12
        with patch.object(c, 'verify', return_value={'_ready_worker_isolation': True}), patch.object(c, 'status') as status:
            with self.assertRaisesRegex(ValueError, 'shared green restart is prohibited'):
                c.deploy(rid, 'APR-20260923T080000Z-testonly')
            status.assert_not_called()

    def test_shared_green_preflight_refuses_isolation_plan_before_systemd(self):
        rid = '20260923T080000Z-' + 'a' * 12
        with patch.object(c, 'verify', return_value={'_ready_worker_isolation': True}), patch.object(c, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'shared green preflight is prohibited'):
                c.preflight_recovery(rid)
            run.assert_not_called()

    def test_frozen_state_vector_health_is_exact(self):
        with patch.object(c, 'require_runtime') as runtime, patch.object(c, 'ready') as ready, patch.object(c, 'unready') as unready:
            c.healthy_fleet_state_vector('/expected', 'a' * 64, c.fleet_state_vector())
            runtime.assert_called_once_with('/expected', 'a' * 64)
            self.assertEqual([call.args[0] for call in ready.call_args_list], ['mcp.wesiyu.com', 'mcp.rtyouth.com'])
            self.assertEqual([call.kwargs for call in ready.call_args_list], [
                {'loopback': '127.0.0.1:7702', 'public_resource_metadata': True},
                {'loopback': '127.0.0.1:7702', 'public_resource_metadata': True},
            ])
            unready.assert_called_once_with('mcp.jianpinke.com', loopback='127.0.0.1:7702')
        altered = c.fleet_state_vector()
        altered[0]['state'] = 'tenant_unavailable'
        with self.assertRaises(ValueError):
            c.healthy_fleet_state_vector('/expected', 'a' * 64, altered)

    def test_blue_state_vector_uses_blue_loopback_and_public_metadata(self):
        with patch.object(c, 'require_runtime'), patch.object(c, 'ready') as ready, patch.object(c, 'unready') as unready:
            c.healthy_fleet_state_vector('/blue', 'b' * 64, c.fleet_state_vector(), c.BLUE_GREEN_PLAN, 'blue')
            self.assertTrue(all(call.kwargs['loopback'] == '127.0.0.1:7703' and call.kwargs['public_resource_metadata'] is True for call in ready.call_args_list))
            unready.assert_called_once_with('mcp.jianpinke.com', loopback='127.0.0.1:7703')
        invalid = json.loads(json.dumps(c.BLUE_GREEN_PLAN))
        invalid['blue']['loopback'] = '127.0.0.1:7704'
        with self.assertRaises(ValueError):
            c.blue_green_probe_target(invalid, 'blue')

    def test_ready_blue_probe_checks_public_resource_metadata(self):
        with patch.object(c, 'probe') as probe:
            c.ready('mcp.wesiyu.com', loopback='127.0.0.1:7703', public_resource_metadata=True)
            self.assertIn((('mcp.wesiyu.com', '/.well-known/oauth-protected-resource/mcp', 200), {'loopback': '127.0.0.1:7703'}), [(call.args, call.kwargs) for call in probe.call_args_list])
            self.assertIn((('mcp.wesiyu.com', '/.well-known/oauth-protected-resource/mcp', 200, True), {}), [(call.args, call.kwargs) for call in probe.call_args_list])


if __name__ == '__main__':
    unittest.main()


class StartupExecTests(unittest.TestCase):
    def test_known_launcher_waits_then_verifies_actual_binary(self):
        current=dict(runtime_path='/expected',binary_sha256='a'*64,restarts=0,unit_fingerprint='u',runtime_config_fingerprint='r')
        with patch.object(c,'run'),patch.object(c,'unit_fingerprint',return_value='u'),patch.object(c,'runtime_fingerprint',return_value='r'),patch.object(c,'status',side_effect=[ValueError('unexpected runtime path'),current]),patch.object(c,'exec_pending',return_value=True),patch.object(c,'healthy') as healthy,patch.object(c.time,'monotonic',return_value=0),patch.object(c.time,'sleep') as sleep:
            c.restart_and_verify('/expected','a'*64,c.HOSTS)
            sleep.assert_called_once_with(0.1);healthy.assert_called_once()

    def test_foreign_or_overdue_startup_does_not_retry(self):
        for pending, times in ((False,[0,0]),(True,[0,3])):
            with self.subTest(pending=pending,times=times),patch.object(c,'run'),patch.object(c,'unit_fingerprint',return_value='u'),patch.object(c,'runtime_fingerprint',return_value='r'),patch.object(c,'status',side_effect=ValueError('unexpected runtime path')),patch.object(c,'exec_pending',return_value=pending),patch.object(c,'healthy') as healthy,patch.object(c.time,'monotonic',side_effect=times),patch.object(c.time,'sleep') as sleep:
                with self.assertRaisesRegex(ValueError,'unexpected runtime path'):c.restart_and_verify('/expected','a'*64,c.HOSTS)
                healthy.assert_not_called();sleep.assert_not_called()

    def test_pending_requires_exact_config_and_known_manager_image(self):
        props={'ExecStart':'{ path=/expected ; argv[]=/expected ; }','NRestarts':'0','ActiveState':'active','MainPID':'123'}
        def run(*args):return props[args[-2]]
        with patch.object(c,'run',side_effect=run):
            for actual, wanted in (('/usr/lib/systemd/systemd',True),('/usr/lib/systemd/systemd-executor',True),('/expected',True),('/foreign',False)):
                with patch.object(c.os,'readlink',side_effect=lambda p: '/usr/lib/systemd/systemd' if p=='/proc/1/exe' else actual):
                    self.assertEqual(c.exec_pending('/expected'),wanted)
            props['ExecStart']='{ path=/foreign ; }'
            with self.assertRaisesRegex(ValueError,'configured runtime drift'):c.exec_pending('/expected')
