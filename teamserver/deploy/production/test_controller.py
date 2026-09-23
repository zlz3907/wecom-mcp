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

    def test_service_template_preserves_static_and_adds_dynamic(self):
        raw = c.dropin('20260923T080000Z-'+'a'*12).decode()
        self.assertIn('--gnas-fleet-runtime ', raw)
        self.assertIn('--gnas-discovery-policy ', raw)
        self.assertIn('--gnas-state-root ', raw)
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
        m={'release_id':rid,'expected_runtime_path':str(previous),'expected_binary_sha256':c.sha(previous),'expected_unit_fingerprint':'b'*64,'expected_runtime_config_fingerprint':'e'*64,'expected_gnas_release_id':'gnas','expected_gnas_binary_sha256':'c'*64,'files':{'wecom-mcp-team':'d'*64}}
        original=c.regular
        with patch.object(c,'RELEASES',releases),patch.object(c,'CONTROL',control),patch.object(c,'STATE',state),patch.object(c,'DROPIN',override),patch.object(c,'verify',return_value=m),patch.object(c,'status'),patch.object(c,'baseline_matches',return_value=True),patch.object(c,'check_gnas'),patch.object(c,'healthy'),patch.object(c,'regular',side_effect=lambda p,root=False:original(p,False)),patch.object(c,'check_approval'),patch.object(c,'preflight_rollback'),patch.object(c,'approval'),patch.object(c,'restart_and_verify',side_effect=ValueError('test failure')),patch.object(c,'restore') as restore:
            with self.assertRaisesRegex(ValueError,'automatic rollback completed'):c.deploy(rid,'APR-20260923T080000Z-testonly')
            restore.assert_called_once_with(rid)
            self.assertTrue((control/'rollback'/rid/'baseline.json').exists())

    def test_observation_has_eleven_samples_and_ten_intervals(self):
        m={'files':{'wecom-mcp-team':'a'*64}}
        with patch.object(c,'verify',return_value=m),patch.object(c,'read_json',return_value={'unit_fingerprint':'u','runtime_config_fingerprint':'r'}),patch.object(c,'unit_fingerprint',return_value='u'),patch.object(c,'runtime_fingerprint',return_value='r'),patch.object(c,'healthy') as healthy,patch.object(c.time,'sleep') as sleep,patch('builtins.print'):
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

    def test_dead_candidate_can_rollback_only_exact_override(self):
        rid='20260923T080000Z-'+'a'*12
        override=self.root/'override.conf';override.write_bytes(c.dropin(rid))
        m={'files':{'wecom-mcp-team':'a'*64,'service.conf':c.sha(override)}}
        current={'runtime_path':str(c.RELEASES/rid/'wecom-mcp-team'),'binary_sha256':'a'*64,'unit_fingerprint':'u','active':'failed','runtime_verified':False,'restarts':3}
        saved={'runtime_path':'/old','binary_sha256':'b'*64}
        original=c.regular
        with patch.object(c,'DROPIN',override),patch.object(c,'verify',return_value=m),patch.object(c,'status',return_value=current),patch.object(c,'read_json',return_value=saved),patch.object(c,'regular',side_effect=lambda p,root=False:original(p,False)),patch.object(c,'approval') as approval,patch.object(c,'restore') as restore:
            self.assertEqual(c.rollback(rid,'APR-20260923T080000Z-testonly')['state'],'rolled_back')
            restore.assert_called_once_with(rid)
            override.write_bytes(b'unexpected configuration')
            with self.assertRaises(ValueError):c.rollback(rid,'APR-20260923T080000Z-otherone')
            self.assertEqual(approval.call_count,1)

    def test_unverified_process_cannot_pass_health(self):
        current=dict(active='active',restarts=0,runtime_path='/expected',binary_sha256='a'*64,runtime_verified=False)
        with patch.object(c,'status',return_value=current),patch.object(c,'probe') as probe:
            with self.assertRaises(ValueError):c.healthy('/expected','a'*64,c.HOSTS)
            probe.assert_not_called()

    def test_old_binary_preflight_is_read_only_and_failure_keeps_live_unit(self):
        rid='20260923T080000Z-'+'a'*12
        m={'expected_runtime_path':'/fixed/old/wecom-mcp-team'}
        with patch.object(c,'verify',return_value=m),patch.object(c,'status'),patch.object(c,'baseline_matches',return_value=True),patch.object(c,'run') as run:
            c.preflight_rollback(rid)
            args=run.call_args.args
            self.assertEqual(args[0],'systemd-run')
            self.assertEqual(args[-4:],('/fixed/old/wecom-mcp-team','--gnas-fleet-runtime',str(c.RUNTIME),'--check-config'))
            self.assertIn('--property=StandardOutput=null',args)
            self.assertIn('--property=StandardError=null',args)
            self.assertNotIn('restart',args)
            run.side_effect=ValueError('controlled check failed')
            with self.assertRaises(ValueError):c.preflight_rollback(rid)

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


if __name__ == '__main__':
    unittest.main()
