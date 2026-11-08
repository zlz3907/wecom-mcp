import contextlib
import copy
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import subprocess
import types
import unittest
from unittest.mock import patch

here=Path(__file__).parent

def module(name):
    p=here/(name+'.py');s=importlib.util.spec_from_file_location(name,p);m=importlib.util.module_from_spec(s);sys.modules[name]=m;s.loader.exec_module(m);return m

m=module('zoop_oauth_swap_prod');core=module('zoop_oauth_swap_core');txn=module('zoop_oauth_unit_transaction')

class ControllerTests(unittest.TestCase):
    def execute(self,fail=False,manual=False,interrupted=False,drift=False):
        with tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
            root=Path(tmp).resolve();(root/'used').mkdir();unit=root/'unit';unit.write_bytes(b'old-unit')
            prep=root/'prep';before={'shared':{'main_pid':'1','mcp_pid':2},'other_enterprises':[['other',401,200]]}
            prep.write_text(json.dumps({'old_unit':'old-unit','old_sha256':m.OLD_SHA,'candidate_sha256':m.SHA,'prepare_approval_id':'APR-20261108T150000Z-test001','binding':'expected','target_routes':{'nginx_config_sha256':'test'},'prepared_dependencies_sha256':{'helper':'original'}}));prep.chmod(0o440)
            (root/'used'/'APR-20261108T150000Z-test001').write_text('consumed');(root/'used'/'APR-20261108T150000Z-test001').chmod(0o440)
            paths={'ROOT':root,'UNIT':unit,'PREP':prep,'JOURNAL':root/'journal','SUCCESS':root/'success','ROLLED':root/'rolled','ROLLBACK_START':root/'rollback-start'}
            for name,value in paths.items():stack.enter_context(patch.object(m,name,value))
            generated={'candidate':b'new-unit','rollback':b'rebased-old','probe_candidate':b'pnew','probe_rollback':b'pold'}
            stack.enter_context(patch.object(m,'units',return_value=generated));stack.enter_context(patch.object(m,'fields',return_value={'binding':'expected','prepared_dependencies_sha256':{'helper':'changed' if drift else 'original'}}))
            stack.enter_context(patch.object(m,'trusted'));stack.enter_context(patch.object(m,'target_ready'));stack.enter_context(patch.object(m,'unit_no_overrides'));stack.enter_context(patch.object(m,'routes',return_value={'nginx_config_sha256':'test'}));stack.enter_context(patch.object(m,'ensure_unit_job_settled')); stack.enter_context(patch.object(m,'sha',side_effect=lambda p:(m.OLD_SHA if p.parent.name==m.OLD else m.SHA) if p.name=='GNASServer' else (m.MANIFEST if p.name=='manifest.json' else m.digest(p.read_bytes()))))
            stack.enter_context(patch.object(m,'common',side_effect=lambda c,**kw:copy.deepcopy(before)));stack.enter_context(patch.object(m,'runtime',return_value=22));stack.enter_context(patch.object(m,'locks',return_value=contextlib.nullcontext()))
            stack.enter_context(patch.object(m,'receipt'));stack.enter_context(patch.object(m,'run',return_value=b''))
            stack.enter_context(patch.object(m.ProductionOps,'verify_public_and_other_tenants'))
            def verify(self,binary,expected):
                if fail and binary==m.SHA:raise RuntimeError('candidate failure')
                if unit.read_bytes()!=expected:raise ValueError('wrong unit')
            stack.enter_context(patch.object(m.ProductionOps,'verify_runtime',verify))
            # The candidate directory is checked for existence and digest, not installed by deploy.
            candidate=root/'releases'/m.RID;candidate.mkdir(parents=True);(candidate/'GNASServer').touch();(candidate/'manifest.json').touch()
            olddir=root/'releases'/m.OLD;olddir.mkdir();(olddir/'GNASServer').touch()
            c={'core':core,'txn':txn};plan=core.Plan(m.RID,m.SHA,m.MANIFEST,m.OLD,m.OLD_SHA,m.MAIN_RID,m.MAIN_SHA,m.OLD_UNIT,m.digest(generated['candidate']),m.digest(generated['rollback']),m.GRANT)
            ops=m.ProductionOps(c,'APR-20261108T150000Z-test002')
            if drift:
                with self.assertRaisesRegex(ValueError,'prepared baseline changed'):core.switch(plan,ops)
                self.assertEqual(unit.read_bytes(),b'old-unit');self.assertFalse((root/'journal').exists())
                self.assertFalse((root/'used'/'APR-20261108T150000Z-test002').exists())
                return
            if interrupted:
                original=m.ProductionOps.install_and_restart_candidate
                def crash(self,*args):original(self,*args);raise SystemExit('simulated abrupt exit')
                with patch.object(m.ProductionOps,'install_and_restart_candidate',crash):
                    with self.assertRaises(SystemExit):core.switch(plan,ops)
                self.assertEqual(unit.read_bytes(),b'new-unit')
                before['shared']={'main_release':'20261109T000000Z-newmain','main_sha256':'f'*64,'main_pid':'9','mcp_pid':3}
                before['other_enterprises']=None
                self.assertEqual(core.recover_incomplete(plan,m.ProductionOps(c,'')),'interrupted_swap_rolled_back')
                self.assertEqual(unit.read_bytes(),b'rebased-old');self.assertTrue((root/'rolled').exists())
            elif fail:
                with self.assertRaisesRegex(core.SwapError,'rollback verified'):core.switch(plan,ops)
                self.assertEqual(unit.read_bytes(),b'rebased-old');self.assertTrue((root/'rolled').exists());self.assertFalse((root/'success').exists())
            else:
                self.assertEqual(core.switch(plan,ops),'isolated_oauth_switched')
                self.assertEqual(unit.read_bytes(),b'new-unit');self.assertTrue((root/'success').exists())
                if manual:
                    m.manual_rollback(c,'APR-20261108T150000Z-test003')
                    self.assertEqual(unit.read_bytes(),b'rebased-old');self.assertTrue((root/'rolled').exists())
            self.assertTrue((root/'journal').exists())
    def test_direct_invocation_without_gnas_dispatch_is_refused(self):
        with patch.object(m.os,'geteuid',return_value=0),patch.object(m,'SELF',Path(m.__file__).resolve()),patch.dict(os.environ,{},clear=True),patch.object(m,'components') as components:
            with self.assertRaisesRegex(ValueError,'controller context'):m.main()
            components.assert_not_called()

    def test_existing_release_lock_excludes_another_process(self):
        with tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
            root=Path(tmp).resolve();main=root/'main';oauth=root/'oauth';mcp=root/'mcp'
            for directory in (main,oauth,mcp):directory.mkdir();(directory/'release.lock').touch()
            for name,value in {'MAIN':main,'ROOT':oauth,'MCP_ROOT':mcp}.items():stack.enter_context(patch.object(m,name,value))
            stack.enter_context(patch.object(m,'trusted'))
            code='import fcntl,sys; f=open(sys.argv[1],"r+"); fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)'
            with m.locks():
                blocked=subprocess.run([sys.executable,'-c',code,str(main/'release.lock')],capture_output=True)
                self.assertNotEqual(blocked.returncode,0)
            allowed=subprocess.run([sys.executable,'-c',code,str(main/'release.lock')],capture_output=True)
            self.assertEqual(allowed.returncode,0)

    def test_systemd_job_must_settle_before_recovery(self):
        with patch.object(m,'run',side_effect=[b'45 /org/freedesktop/job/45',b'']),patch.object(m.time,'sleep'):
            m.ensure_unit_job_settled(Path('/unit'))

    def test_drifted_nginx_target_or_full_config_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
            root=Path(tmp).resolve();site=root/'site';target=root/'target';oauth=root/'oauth'
            site.write_bytes(b'include /root/nginx/snippets/zhycit-mcp-routes.conf;\n')
            target.write_bytes(b'proxy_pass http://127.0.0.1:7709;\n'*2+b'include /root/nginx/snippets/gnas-oauth21-zhycit-isolated.conf;\n'*3)
            oauth.write_bytes(b'proxy_pass http://127.0.0.1:7504;\n')
            for name,value in {'SITE_ROUTE':site,'TARGET_ROUTE':target,'OAUTH_ROUTE':oauth,'SITE_ROUTE_SHA':m.digest(site.read_bytes()),'TARGET_ROUTE_SHA':m.digest(target.read_bytes()),'OAUTH_ROUTE_SHA':m.digest(oauth.read_bytes())}.items():stack.enter_context(patch.object(m,name,value))
            stack.enter_context(patch.object(m,'trusted'));c={'route':types.SimpleNamespace(trusted_nginx_tree=lambda:None)}
            config=b''.join((f'# configuration file {p}:\n'.encode()+p.read_bytes()) for p in (site,target,oauth))
            mocked=stack.enter_context(patch.object(m,'run',return_value=config))
            baseline=m.routes(c);mocked.return_value=config+b'other global configuration changed\n'
            self.assertNotEqual(m.routes(c)['nginx_config_sha256'],baseline['nginx_config_sha256'])
            mocked.return_value=config.replace(b'include /root/nginx/snippets/zhycit-mcp-routes.conf;',b'')
            with self.assertRaisesRegex(ValueError,'topology changed'):m.routes(c)
            mocked.return_value=config;target.write_bytes(b'proxy_pass http://127.0.0.1:7708;\n'*2)
            with self.assertRaisesRegex(ValueError,'route changed'):m.routes(c)

    def test_systemd_override_refuses_restart(self):
        unit=Path('/etc/systemd/system/gnas-oauth-zhycit-next.service')
        with patch.object(m,'UNIT',unit),patch.object(m,'run',return_value=(f'FragmentPath={unit}\nDropInPaths=/etc/systemd/system/override.conf\n').encode()) as run:
            with self.assertRaisesRegex(ValueError,'override'):m.guarded_systemd('/usr/bin/systemctl','restart',unit.name)
            self.assertEqual(run.call_count,1)
            self.assertIn('show',run.call_args.args)

    def test_interrupted_probe_is_stopped_only_after_durable_bound_evidence(self):
        with tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
            root=Path(tmp).resolve();(root/'used').mkdir();unit=root/'probe.service';unit.write_bytes(b'pold')
            aid='APR-20261108T150000Z-test004';(root/'used'/aid).write_text('consumed');(root/'used'/aid).chmod(0o440)
            for name,value in {'ROOT':root,'PROBE':unit,'PREP':root/'prep'}.items():stack.enter_context(patch.object(m,name,value))
            generated={'candidate':b'new','rollback':b'old','probe_candidate':b'pnew','probe_rollback':b'pold'}
            start={'approval_id':aid,'old_unit':'old-unit','controller_sha256':'a'*64,'dependencies_sha256':{'h':'b'},
              'probe_rollback_sha256':m.digest(b'pold'),'probe_candidate_sha256':m.digest(b'pnew')}
            txn.write_exclusive(m.probe_record(aid),(json.dumps(start)+'\n').encode(),0o440)
            actual_sha=m.sha
            stack.enter_context(patch.object(m,'sha',side_effect=lambda p:'a'*64 if p==m.SELF else (m.OLD_SHA if str(p).startswith('/proc/') else actual_sha(p))))
            stack.enter_context(patch.object(m,'dependencies',return_value={'h':'b'}));stack.enter_context(patch.object(m,'units',return_value=generated))
            stack.enter_context(patch.object(m,'trusted'));stack.enter_context(patch.object(m,'unit_no_overrides'));stack.enter_context(patch.object(m,'routes',return_value={'nginx_config_sha256':'test'}));stack.enter_context(patch.object(m,'ensure_unit_job_settled'));stack.enter_context(patch.object(m,'check_probe_absent'))
            stack.enter_context(patch.object(m,'locks',return_value=contextlib.nullcontext()))
            stack.enter_context(patch.object(m,'properties',side_effect=[{'MainPID':'42','ActiveState':'active'},{'MainPID':'0','ActiveState':'inactive'}]))
            commands=[]
            stack.enter_context(patch.object(m,'run',side_effect=lambda *args,**kw:commands.append(args) or b''))
            m.recover_probe({'txn':txn},aid)
            m.check_probe_absent.assert_called_once_with(require_memory=False)
            self.assertFalse(unit.exists());self.assertTrue((root/('open-consent-probe-recovered-'+aid+'.json')).exists())
            self.assertIn(('/usr/bin/systemctl','stop',unit.name),commands)
            self.assertLess(commands.index(('/usr/bin/systemctl','daemon-reload')),commands.index(('/usr/bin/systemctl','stop',unit.name)))
            self.assertFalse(any('gnas-oauth-zhycit-next.service' in x for x in commands))

    def test_mcp_authority_check_rejects_wrong_or_duplicate_values(self):
        with tempfile.TemporaryDirectory() as tmp:
            proc=Path(tmp);pid=proc/'42';pid.mkdir();env=pid/'environ'
            env.write_bytes(b'GNAS_BASE_URL=http://127.0.0.1:7504\0SECRET=hidden\0')
            self.assertTrue(m.mcp_authority_matches(42,proc))
            env.write_bytes(b'GNAS_BASE_URL=http://127.0.0.1:7503\0')
            self.assertFalse(m.mcp_authority_matches(42,proc))
            env.write_bytes(b'GNAS_BASE_URL=http://127.0.0.1:7504\0GNAS_BASE_URL=http://127.0.0.1:7504\0')
            self.assertFalse(m.mcp_authority_matches(42,proc))

    def test_recovery_snapshot_tolerates_unrelated_tenant_outage(self):
        route=types.SimpleNamespace(other_public_vector=lambda:(_ for _ in ()).throw(RuntimeError('unavailable')))
        with patch.object(m,'shared',return_value={'valid':True}):
            with self.assertRaisesRegex(RuntimeError,'unavailable'):m.common({'route':route})
            self.assertIsNone(m.common({'route':route},allow_advanced=True)['other_enterprises'])

    def test_recovery_accepts_only_verified_current_shared_release_after_upgrade(self):
        with tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
            root=Path(tmp).resolve();rid='20261109T000000Z-newmain';directory=root/'releases'/rid;directory.mkdir(parents=True)
            binary=directory/'GNASServer';binary.write_bytes(b'new-main');binary_sha=m.digest(b'new-main')
            manifest={'schema_version':2,'environment':'zhycit-prod-01','artifact':'gnas-service-gin','release_id':rid,'binary':{'name':'GNASServer','sha256':binary_sha}}
            (directory/'manifest.json').write_text(json.dumps(manifest))
            status=f'release_id={rid}\nservice_state=active\nmain_pid=55\nrestarts=0\nruntime_path={binary}\nruntime_sha256={binary_sha}\n'.encode()
            stack.enter_context(patch.object(m,'MAIN',root));stack.enter_context(patch.object(m,'trusted'));stack.enter_context(patch.object(m,'run',return_value=status))
            stack.enter_context(patch.object(m,'properties',return_value={'ActiveState':'active','NRestarts':'0','MainPID':'44'}));stack.enter_context(patch.object(m,'mcp_authority_matches',return_value=True))
            actual_sha=m.sha
            stack.enter_context(patch.object(m,'sha',side_effect=lambda p:m.MCP_SHA if str(p).startswith('/proc/') else actual_sha(p)))
            with self.assertRaisesRegex(ValueError,'changed'):m.shared()
            self.assertEqual(m.shared(allow_advanced=True)['main_release'],rid)
            manifest['environment']='other';(directory/'manifest.json').write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError,'manifest'):m.shared(allow_advanced=True)

    def test_manual_rollback_receipt_binds_actual_upgraded_shared_release(self):
        captured=[]
        c={'approval':types.SimpleNamespace(validate=lambda path,aid,expected:captured.append(expected))}
        fields={'shared_snapshot':{'shared':{'main_release':'20261109T000000Z-newmain','main_sha256':'f'*64}}}
        with patch.object(m,'sha',return_value='a'*64),patch.object(m,'dependencies',return_value={}):
            m.receipt(c,'APR-20261108T150000Z-test001','rollback-isolated-oauth-open-consent',fields)
        self.assertEqual(captured[0]['shared_release'],'20261109T000000Z-newmain')
        self.assertEqual(captured[0]['shared_sha256'],'f'*64)

    def test_prepare_validates_old_before_new_without_touching_live_unit(self):
        for fail in (False,True):
            with self.subTest(fail=fail),tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
                root=Path(tmp).resolve();(root/'used').mkdir();unit=root/'unit';unit.write_bytes(b'old-unit')
                for name,value in {'ROOT':root,'UNIT':unit,'PREP':root/'prep','JOURNAL':root/'journal'}.items():stack.enter_context(patch.object(m,name,value))
                before={'shared':{'main_pid':'1','mcp_pid':2},'other_enterprises':[['other',401,200]]}
                generated={'candidate':b'new-unit','rollback':b'rebased-old','probe_candidate':b'pnew','probe_rollback':b'pold'}
                for name,value in [('common',lambda c:copy.deepcopy(before)),('runtime',lambda *a:22),('fields',lambda *a:{'binding':'expected'}),('units',lambda *a:generated)]:stack.enter_context(patch.object(m,name,side_effect=value))
                for name in ('trusted','target_ready','check_probe_absent','receipt','install_artifact','unit_no_overrides'):stack.enter_context(patch.object(m,name))
                stack.enter_context(patch.object(m,'locks',return_value=contextlib.nullcontext()))
                stack.enter_context(patch.object(m,'sha',return_value='a'*64));stack.enter_context(patch.object(m,'dependencies',return_value={}))
                probes=[]
                def probe(c,unit_bytes,sha):
                    probes.append((unit_bytes,sha))
                    if fail and sha==m.SHA:raise RuntimeError('new probe failed')
                stack.enter_context(patch.object(m,'probe_version',side_effect=probe))
                c={'gnas':types.SimpleNamespace(oauth_fingerprint=lambda p:('same',)),'txn':txn}
                if fail:
                    with self.assertRaisesRegex(RuntimeError,'probe failed'):m.prepare(c,'APR-20261108T150000Z-test004')
                    self.assertFalse((root/'prep').exists())
                else:
                    m.prepare(c,'APR-20261108T150000Z-test004');self.assertTrue((root/'prep').exists())
                self.assertEqual([x[1] for x in probes],[m.OLD_SHA,m.SHA])
                self.assertEqual(unit.read_bytes(),b'old-unit')
                self.assertTrue((root/'used'/'APR-20261108T150000Z-test004').exists())

    def test_success_uses_candidate_and_keeps_durable_journal(self):self.execute()
    def test_candidate_failure_restores_rebased_old_unit(self):self.execute(fail=True)
    def test_dependency_drift_after_probes_refuses_deploy(self):self.execute(drift=True)
    def test_interrupted_switch_can_recover_even_when_candidate_process_is_gone(self):self.execute(interrupted=True)
    def test_manual_rollback_uses_separate_receipt_and_rebased_old(self):self.execute(manual=True)

if __name__=='__main__':unittest.main()
