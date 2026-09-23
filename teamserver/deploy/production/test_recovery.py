import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('controller', Path(__file__).with_name('mcp_release_controller.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)
spec = importlib.util.spec_from_file_location('upgrader', Path(__file__).with_name('upgrade-controller.py'))
u = importlib.util.module_from_spec(spec)
spec.loader.exec_module(u)


class RecoveryTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()
        self.rid = '20260923T080000Z-' + 'a'*12
        self.directory = self.root / 'releases' / self.rid
        self.directory.mkdir(parents=True)
        self.control = self.root / 'control'
        (self.control / 'rollback' / self.rid).mkdir(parents=True)
        self.override = self.root / 'override.conf'
        for name, value in [('RELEASES', self.directory.parent), ('CONTROL', self.control), ('DROPIN', self.override)]:
            p = patch.object(c, name, value)
            p.start(); self.addCleanup(p.stop)
        original = c.regular
        p = patch.object(c, 'regular', side_effect=lambda p, root=False: original(p, False))
        p.start(); self.addCleanup(p.stop)
        (self.directory / 'wecom-mcp-team').write_bytes(b'new-binary')
        (self.directory / 'service.conf').write_bytes(c.dropin(self.rid))
        (self.directory / 'recovery.conf').write_bytes(c.recovery_dropin(self.rid))
        (self.directory / 'discovery-policy.json').write_text('{"version":1,"api_whitelist":{"read":["get_records"]}}')
        self.m = dict(schema_version=2, environment=c.ENVIRONMENT, release_id=self.rid,
                      git_commit='a'*40, git_tree='b'*40, source_clean=True, target='linux/amd64',
                      expected_runtime_path=str(c.RELEASES/'old'/'wecom-mcp-team'),
                      expected_binary_sha256='c'*64, expected_unit_fingerprint='d'*64,
                      expected_runtime_config_fingerprint='e'*64, expected_unmanaged_unit_fingerprint='f'*64,
                      expected_gnas_release_id='20260923T070000Z-'+'b'*12, expected_gnas_binary_sha256='1'*64,
                      recovery_mode='same-version-static', recovery_hosts=list(c.HOSTS[:1]))
        self.seal()

    def seal(self):
        self.m['files'] = {name: c.sha(self.directory/name) for name in c.FILES[:-1]}
        (self.directory/'manifest.json').write_text(json.dumps(self.m))
        (self.directory/'SHA256SUMS').write_text(''.join(c.sha(self.directory/name)+'  '+name+'\n' for name in sorted(c.FILES)))

    def test_recovery_artifact_cannot_select_old_binary_or_looser_mode(self):
        c.verify(self.directory, self.rid)
        for data in (b'[Service]\nExecStart=/old/binary\n', c.dropin(self.rid), c.recovery_dropin(self.rid).replace(b'--gnas-static-only', b'--fleet')):
            (self.directory/'recovery.conf').write_bytes(data)
            self.seal()  # Even a self-consistent re-hash cannot authorize arbitrary args.
            with self.assertRaisesRegex(ValueError, 'recovery arguments'):
                c.verify(self.directory, self.rid)

    def test_legacy_candidate_and_ci_pending_cannot_promote(self):
        self.m['schema_version']=1
        self.seal()
        with self.assertRaisesRegex(ValueError,'manifest identity'):c.verify(self.directory,self.rid)
        self.m['schema_version']=2
        self.seal()
        with patch.object(c,'INCOMING',self.directory.parent),patch.object(c,'status') as status:
            with self.assertRaisesRegex(ValueError,'CI evidence'):c.stage(self.rid)
            status.assert_not_called()

    def test_restore_uses_same_binary_and_rejects_discovery_host(self):
        with patch.object(c, 'recovery_matches'), patch.object(c, 'restart_and_verify') as restart, patch.object(c, 'healthy') as healthy, patch.object(c, 'probe') as probe, patch.object(c, 'unit_fingerprint', return_value='unit'):
            c.restore(self.rid)
            self.assertEqual(self.override.read_bytes(), c.recovery_dropin(self.rid))
            self.assertEqual(restart.call_args.args, (str(self.directory/'wecom-mcp-team'), self.m['files']['wecom-mcp-team'], c.HOSTS[:1]))
            self.assertEqual(healthy.call_args.args[2], c.HOSTS[:1])
            self.assertTrue(all(call.args[0] == c.HOSTS[1] and call.args[2] == 421 for call in probe.call_args_list))
            self.assertTrue((self.control/'rollback'/self.rid/'recovered.json').exists())

    def test_failed_recovery_never_records_success(self):
        with patch.object(c, 'recovery_matches'), patch.object(c, 'restart_and_verify', side_effect=ValueError('restart failed')):
            with self.assertRaisesRegex(ValueError, 'restart failed'):
                c.restore(self.rid)
            self.assertFalse((self.control/'rollback'/self.rid/'recovered.json').exists())

    def test_drift_refuses_recovery_before_override_write(self):
        with patch.object(c, 'runtime_fingerprint', return_value='changed'), patch.object(c, 'restart_and_verify') as restart:
            with self.assertRaisesRegex(ValueError, 'configuration drift'):
                c.restore(self.rid)
            self.assertFalse(self.override.exists())
            restart.assert_not_called()

    def test_dead_same_version_can_recover_but_unknown_override_cannot(self):
        self.override.write_bytes(c.dropin(self.rid))
        current = dict(runtime_path=str(self.directory/'wecom-mcp-team'), binary_sha256=self.m['files']['wecom-mcp-team'], unit_fingerprint='u', active='failed', runtime_verified=False)
        with patch.object(c,'status',return_value=current), patch.object(c,'check_approval') as check, patch.object(c,'approval') as approval, patch.object(c,'preflight_recovery'), patch.object(c,'restore') as restore:
            self.assertEqual(c.rollback(self.rid,'approval')['state'],'recovered_static')
            fields=check.call_args.args[2]
            self.assertEqual(fields['recovery_sha256'],self.m['files']['recovery.conf'])
            self.assertEqual(fields['binary_sha256'],self.m['files']['wecom-mcp-team'])
            restore.assert_called_once_with(self.rid)
            self.override.write_bytes(b'unknown')
            with self.assertRaises(ValueError):c.rollback(self.rid,'another')
            self.assertEqual(approval.call_count,1)


class UpgradeTests(unittest.TestCase):
    def setUp(self):
        tmp=tempfile.TemporaryDirectory();self.addCleanup(tmp.cleanup)
        self.root=Path(tmp.name).resolve()
        self.source=self.root/'new.py';self.source.write_bytes(b'new')
        self.target=self.root/'installed';self.target.write_bytes(b'old')
        self.control=self.root/'control';self.control.mkdir()
        self.facts=dict(runtime_verified=True,active='active',restarts=0,main_pid=123,runtime_path='/actual',binary_sha256='a'*64,unit_fingerprint='b'*64,runtime_config_fingerprint='c'*64)
        for obj, key, value in [(u,'SOURCE',self.source),(u,'TARGET',self.target),(u.c,'CONTROL',self.control)]:
            p=patch.object(obj,key,value);p.start();self.addCleanup(p.stop)
        for obj,key in [(u.c,'regular'),(u,'trusted_directory'),(u.c,'check_approval'),(u.c,'approval')]:
            p=patch.object(obj,key);p.start();self.addCleanup(p.stop)
        p=patch.object(u.c,'status',return_value=self.facts);self.status=p.start();self.addCleanup(p.stop)

    def test_upgrade_preserves_service_and_retains_exact_prior_controller(self):
        expected,_=u.fields()
        self.assertEqual(expected['previous_controller_sha256'],u.c.sha(self.target))
        self.assertEqual(expected['expected_main_pid'],123)
        with patch.object(u.c,'run') as run:
            self.assertEqual(u.upgrade('approval')['state'],'controller_upgraded')
            run.assert_not_called()
        self.assertEqual(self.target.read_bytes(),b'new')
        self.assertEqual((self.control/'controller-upgrades'/'approval'/'controller.before').read_bytes(),b'old')

    def test_rejected_approval_leaves_installed_controller(self):
        u.c.check_approval.side_effect=ValueError('binding mismatch')
        with self.assertRaises(ValueError):u.upgrade('approval')
        self.assertEqual(self.target.read_bytes(),b'old')
        u.c.approval.assert_not_called()

    def test_process_restart_during_upgrade_restores_controller(self):
        self.status.side_effect=[self.facts,self.facts,self.facts,dict(self.facts,main_pid=124)]
        with self.assertRaisesRegex(ValueError,'service drift'):u.upgrade('approval')
        self.assertEqual(self.target.read_bytes(),b'old')
        self.assertFalse((self.control/'controller-upgrades'/'approval'/'completed.json').exists())

    def test_drift_after_consumption_does_not_overwrite_concurrent_controller(self):
        original=u.fields
        count=0
        def fields():
            nonlocal count
            count+=1
            if count==3:self.target.write_bytes(b'concurrent-controller')
            return original()
        with patch.object(u,'fields',side_effect=fields):
            with self.assertRaisesRegex(ValueError,'before replacement'):u.upgrade('approval')
        self.assertEqual(self.target.read_bytes(),b'concurrent-controller')
        self.assertEqual((self.control/'controller-upgrades'/'approval'/'controller.before').read_bytes(),b'old')

    def test_old_controller_drift_before_consumption_is_rejected(self):
        original=u.fields
        count=0
        def fields():
            nonlocal count
            count+=1
            if count==2:self.target.write_bytes(b'changed')
            return original()
        with patch.object(u,'fields',side_effect=fields):
            with self.assertRaisesRegex(ValueError,'before upgrade'):u.upgrade('approval')
        u.c.approval.assert_not_called()
        self.assertEqual(self.target.read_bytes(),b'changed')


if __name__=='__main__':unittest.main()
