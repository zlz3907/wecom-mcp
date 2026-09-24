import contextlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('migration', Path(__file__).with_name('migrate-shared-service.py'))
u = importlib.util.module_from_spec(spec)
spec.loader.exec_module(u)
c = u.c


class MigrationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.rid = '20260924T010000Z-' + 'a' * 12
        self.aid = 'APR-20260924T010000Z-testonly'
        self.releases = self.root / 'releases'
        self.directory = self.releases / self.rid
        self.directory.mkdir(parents=True)
        self.control = self.root / 'control'
        (self.control / 'rollback').mkdir(parents=True)
        (self.control / 'used').mkdir()
        self.state = self.root / 'state'; self.state.mkdir()
        self.here = self.root / 'tools'; self.here.mkdir()
        self.controller = self.root / 'controller'; self.controller.write_bytes(b'old controller')
        self.target = self.root / c.UNIT
        self.dropin = self.root / (c.UNIT + '.d') / 'zz-managed-release.conf'
        self.current = dict(runtime_verified=True, active='active', restarts=0, main_pid=789,
                            runtime_path='/releases/old/wecom-mcp-team', binary_sha256='b'*64,
                            unit_fingerprint='c'*64, runtime_config_fingerprint='d'*64)
        for name, data in [('service.conf', b'hybrid'), ('recovery.conf', b'static'), ('wecom-mcp-team', b'new binary')]:
            (self.directory / name).write_bytes(data)
        (self.here / c.UNIT).write_bytes(b'unit')
        (self.here / 'mcp_release_controller.py').write_bytes(b'new controller')
        self.manifest = dict(release_id=self.rid, expected_runtime_config_fingerprint='d'*64,
            files={n:c.sha(self.directory/n) for n in ('service.conf','recovery.conf','wecom-mcp-team')})
        self.expected = {'expected_'+k: self.current[k] for k in ('runtime_path','binary_sha256','unit_fingerprint','runtime_config_fingerprint')}
        self.expected.update(expected_main_pid=789, target_unit_sha256=c.sha(self.here/c.UNIT),
            controller_sha256=c.sha(self.here/'mcp_release_controller.py'), previous_controller_sha256=c.sha(self.controller),
            source_policy_sha256='policy')
        self.manifest.update({k:v for k,v in self.expected.items() if k.startswith('expected_')})
        self.receipt = dict(self.expected, action='migrate-shared-service', environment=c.ENVIRONMENT, approval_id=self.aid)
        self.approvals = self.root / 'approvals'; self.approvals.mkdir()
        (self.approvals/(self.aid+'.json')).write_text(json.dumps(self.receipt))
        self.source_active = True
        self.source_enabled = True
        self.target_active = False
        self.calls = []
        self.stack = contextlib.ExitStack(); self.addCleanup(self.stack.close)
        for obj, name, value in [(c,'RELEASES',self.releases),(c,'CONTROL',self.control),(c,'STATE',self.state),
            (c,'DROPIN',self.dropin),(c,'APPROVALS',self.approvals),(u,'HERE',self.here),
            (u,'TARGET_FILE',self.target),(u,'CONTROLLER',self.controller)]:
            self.stack.enter_context(patch.object(obj,name,value))
        original_regular = c.regular
        self.stack.enter_context(patch.object(c,'regular',side_effect=lambda p,root=False:original_regular(p,False)))
        self.stack.enter_context(patch.object(u,'fields',return_value=(self.manifest,self.expected)))
        self.stack.enter_context(patch.object(u,'source_status',side_effect=lambda:dict(self.current,active='active' if self.source_active else 'inactive')))
        self.stack.enter_context(patch.object(u,'prop',side_effect=self.prop))
        self.stack.enter_context(patch.object(c,'run',side_effect=self.command))
        self.stack.enter_context(patch.object(c,'verify',return_value=self.manifest))
        for name in ('check_approval','check_gnas'):
            self.stack.enter_context(patch.object(c,name))
        self.stack.enter_context(patch.object(c,'approval',side_effect=self.consume))
        for name in ('preflight','effective','free_port','listener'):
            self.stack.enter_context(patch.object(u,name))

    def prop(self, unit, name):
        if name == 'ActiveState': return 'active' if (self.source_active if unit == u.SOURCE else self.target_active) else 'inactive'
        if name == 'MainPID': return '789' if unit == u.SOURCE and self.source_active else '0'
        if name == 'UnitFileState': return 'enabled' if unit == u.SOURCE and self.source_enabled else 'disabled'
        raise AssertionError(name)

    def command(self, *args):
        self.calls.append(args)
        if args == ('systemctl','disable',u.SOURCE): self.source_enabled=False
        if args == ('systemctl','enable',u.SOURCE): self.source_enabled=True
        if args == ('systemctl','stop',u.SOURCE): self.source_active=False
        return ''

    def consume(self, *args):
        (self.control/'used'/self.aid).write_bytes(b'consumed\n')
        return self.receipt

    def test_switch_never_starts_target_before_source_stops(self):
        def finish(*args, **kwargs):
            self.assertFalse(self.source_active)
            self.assertFalse(self.source_enabled)
            self.assertEqual(self.dropin.read_bytes(), b'hybrid')
            return {'state':'migrated_hybrid'}
        with patch.object(u,'finish',side_effect=finish):
            self.assertEqual(u.apply(self.rid,self.aid)['state'],'migrated_hybrid')
        self.assertLess(self.calls.index(('systemctl','disable',u.SOURCE)),self.calls.index(('systemctl','stop',u.SOURCE)))

    def test_missing_readiness_leaves_both_units_untouched(self):
        with patch.object(c,'check_gnas',side_effect=FileNotFoundError):
            with self.assertRaises(FileNotFoundError):u.apply(self.rid,self.aid)
        self.assertEqual(self.calls,[])
        self.assertFalse(self.target.exists())
        self.assertFalse((self.control/'used'/self.aid).exists())

    def test_partial_install_and_failed_stop_restore_only_preparation(self):
        original = c.atomic
        def interrupted(path, data, mode=0o644):
            if path == self.dropin: raise OSError('fault injection')
            return original(path,data,mode)
        with patch.object(c,'atomic',side_effect=interrupted):
            with self.assertRaises(OSError):u.apply(self.rid,self.aid)
        self.assertTrue(self.source_active and self.source_enabled)
        self.assertFalse(self.target.exists());self.assertFalse(self.dropin.parent.exists())
        self.assertNotIn(('systemctl','stop',u.SOURCE),self.calls)

    def test_failed_stop_reenables_running_source_without_restart(self):
        original_run=self.command
        def fail(*args):
            if args == ('systemctl','stop',u.SOURCE): raise ValueError('failed stop')
            return original_run(*args)
        with patch.object(c,'run',side_effect=fail):
            with self.assertRaisesRegex(ValueError,'failed stop'):u.apply(self.rid,self.aid)
        self.assertTrue(self.source_active and self.source_enabled)
        self.assertFalse(self.target.exists())
        self.assertFalse(any('restart' in args or 'start' in args for args in self.calls))

    def test_hybrid_failure_recovers_same_candidate_only(self):
        with patch.object(u,'finish',side_effect=[ValueError('startup failure'),{}]) as finish:
            with self.assertRaisesRegex(ValueError,'static recovery active'):u.apply(self.rid,self.aid)
        self.assertEqual(finish.call_count,2)
        self.assertEqual(finish.call_args.kwargs,{'recovery':True})
        self.assertEqual(finish.call_args.args[0],self.manifest)
        self.assertFalse(any(args == ('systemctl','start',u.SOURCE) for args in self.calls))

    def test_concurrent_controller_change_refuses_compensation_before_write(self):
        self.controller.write_bytes(b'unrelated controller')
        with patch.object(c,'atomic') as atomic:
            with self.assertRaisesRegex(ValueError,'compensation refused'):
                u.finish(self.manifest,self.expected,self.control,recovery=True)
            atomic.assert_not_called()
        self.assertEqual(self.calls,[])

    def test_consumed_receipt_crash_before_install_can_abort_without_restarting(self):
        record=self.control/'unit-migrations'/self.rid;record.mkdir(parents=True)
        (record/'expected.json').write_text(json.dumps(self.expected))
        (record/'phase.json').write_text(json.dumps({'phase':'prepared','approval_id':self.aid}))
        self.consume()
        self.assertEqual(u.abort_prepared(self.rid)['state'],'aborted_before_stop')
        self.assertTrue(self.source_active and self.source_enabled)
        self.assertFalse(any('restart' in args or 'start' in args for args in self.calls))


class IsolationGateTests(unittest.TestCase):
    def test_unready_exact_response_only(self):
        from unittest.mock import MagicMock
        for status, body, cache, auth, accepted in [(503,b'MCP instance unavailable\n','no-store',None,True),
            (503,b'MCP fleet unavailable\n','no-store',None,False),(503,b'MCP instance unavailable\n',None,None,False),
            (503,b'MCP instance unavailable\n','no-store','Bearer',False),(200,b'MCP instance unavailable\n','no-store',None,False)]:
            conn=MagicMock();response=conn.getresponse.return_value;response.status=status;response.read.return_value=body
            response.getheader.side_effect=lambda key: cache if key=='Cache-Control' else auth
            with patch.object(c.http.client,'HTTPConnection',return_value=conn),patch.object(c.http.client,'HTTPSConnection',return_value=conn):
                if accepted:c.unready(c.HOSTS[1])
                else:
                    with self.assertRaises(ValueError):c.unready(c.HOSTS[1])

    def test_static_failure_cannot_be_classified_as_tenant_isolation(self):
        current=dict(runtime_verified=True,active='active',restarts=0,runtime_path='path',binary_sha256='sha')
        with patch.object(c,'status',return_value=current),patch.object(c,'probe',side_effect=ValueError),patch.object(c,'unready') as unready:
            with self.assertRaises(ValueError):c.healthy('path','sha',c.HOSTS)
            unready.assert_not_called()


if __name__ == '__main__':unittest.main()
