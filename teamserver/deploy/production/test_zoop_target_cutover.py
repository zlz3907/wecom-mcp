import contextlib
import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

path=Path(__file__).with_name('zoop_target_cutover.py')
spec=importlib.util.spec_from_file_location('target_cutover',path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class TargetCutoverTests(unittest.TestCase):
    def setUp(self):
        self.old=(b'proxy_pass http://127.0.0.1:7703;\n'*2 +
          b'include /root/nginx/snippets/gnas-oauth21-proxy.conf;\n'*3 + b'unchanged setting;\n')
        self.common=b'proxy_pass http://127.0.0.1:7501;\nproxy_buffering off;\n'
    def test_exact_target_transformation_and_shape(self):
        new,include=m.expected_files(self.old,self.common)
        self.assertEqual(new.count(b'127.0.0.1:7705'),2)
        self.assertEqual(new.count(b'gnas-oauth21-zhycit-isolated.conf'),3)
        self.assertIn(b'unchanged setting;',new)
        self.assertEqual(include,b'proxy_pass http://127.0.0.1:7503;\nproxy_buffering off;\n')
        with self.assertRaisesRegex(ValueError,'shape'):m.expected_files(self.old+b'proxy_pass http://127.0.0.1:7703;',self.common)
    def test_receipt_rejects_wrong_nginx_or_canary_identity(self):
        a='APR-20261106T203409Z-4e6b6a0e50e2';grant='a'*64;new,inc=m.expected_files(self.old,self.common)
        now=dt.datetime.now(dt.timezone.utc);deps={'dep':'d'*64}
        obj={'schema_version':1,'approval_id':a,'environment':'zhycit-prod-01','action':'cutover-target-oauth-mcp',
          'target':'zhycit.com','grant_sha256':grant,'old_target_sha256':m.sha_bytes(self.old),
          'new_target_sha256':m.sha_bytes(new),'isolated_include_sha256':m.sha_bytes(inc),'nginx_config_sha256':'n'*64,
          'nginx_main_pid':111,'oauth_canary_pid':222,'mcp_canary_pid':333,'mcp_binary_sha256':m.MCP_SHA,
          'gnas_binary_sha256':m.GNAS_SHA,'controller_sha256':'c'*64,'dependencies_sha256':deps,
          'approved_at':now.isoformat(),'expires_at':(now+dt.timedelta(minutes=5)).isoformat(),'approved_by':'delegated-root'}
        with tempfile.TemporaryDirectory() as tmp,patch.object(m,'APPROVALS',Path(tmp)),patch.object(m,'trusted'),patch.object(m,'sha',return_value='c'*64):
            p=Path(tmp)/(a+'.json');p.write_text(json.dumps(obj));p.chmod(0o400)
            m.check_receipt(a,grant,self.old,new,inc,'n'*64,111,(222,333),deps)
            with self.assertRaisesRegex(ValueError,'does not bind'):
                m.check_receipt(a,grant,self.old,new,inc,'n'*64,111,(222,334),deps)
            with self.assertRaisesRegex(ValueError,'does not bind'):
                m.check_receipt(a,grant,self.old,new,inc,'x'*64,111,(222,333),deps)
    def test_atomic_replace_and_exclusive_backup(self):
        with tempfile.TemporaryDirectory() as tmp:
            p=Path(tmp)/'route';p.write_bytes(b'old');m.replace(p,b'new',0o644)
            self.assertEqual(p.read_bytes(),b'new');self.assertEqual(p.stat().st_mode&0o777,0o644)
            with self.assertRaises(FileExistsError):m.write_new(p,b'cannot-overwrite',0o440)
            self.assertEqual(p.read_bytes(),b'new')


    def test_cutover_success_and_automatic_recovery(self):
        vector=(("mcp.wesiyu.com",401,200),("mcp.rtyouth.com",401,200),("mcp.jianpinke.com",503,503))
        for broken in (False,True):
            with self.subTest(broken=broken),tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
                root=Path(tmp); (root/'used').mkdir();(root/'release.lock').touch();gnas_lock=root/'gnas.lock';gnas_lock.touch()
                target=root/'target';target.write_bytes(self.old);common=root/'common';common.write_bytes(self.common)
                isolated=root/'isolated';grant=root/'grant';grant.write_bytes(b'grant');grant.chmod(0o400)
                controller=root/'controller';controller.write_bytes(b'controller')
                mcp=types.SimpleNamespace(snapshot=lambda:('unchanged',))
                for name,value in {'ROOT':root,'GNAS_LOCK':gnas_lock,'TARGET':target,'COMMON':common,'ISOLATED':isolated,
                                   'GRANT':grant,'SELF':path.resolve(),'MCP_CONTROL':controller,
                                   'GNAS_CONTROL':controller,'HELPER':controller,'ORIGINAL_SHA':m.sha_bytes(self.old),
                                   'COMMON_SHA':m.sha_bytes(self.common)}.items():stack.enter_context(patch.object(m,name,value))
                stack.enter_context(patch.object(m.os,'geteuid',return_value=0))
                stack.enter_context(patch.object(m,'trusted'));stack.enter_context(patch.object(m,'load',return_value=mcp))
                stack.enter_context(patch.object(m,'active_canaries',return_value=(222,333)))
                stack.enter_context(patch.object(m,'other_public_vector',return_value=vector))
                stack.enter_context(patch.object(m,'unit',return_value={'MainPID':'111','ActiveState':'active'}))
                stack.enter_context(patch.object(m,'tls_probe',side_effect=lambda h,p:401 if p=='/mcp' else 200))
                stack.enter_context(patch.object(m,'check_receipt'))
                calls=[]
                def run(*args,**kwargs):
                    calls.append(args)
                    if args[:2]==('/usr/sbin/nginx','-T'):return b'original nginx config'
                    if args[0]=='/usr/bin/curl':
                        if broken:raise ValueError('public scope metadata mismatch')
                        return b'{"resource":"https://zhycit.com/mcp","scopes_supported":["zoop.read","zoop.write"]}'
                    return b''
                stack.enter_context(patch.object(m,'run',side_effect=run))
                if broken:
                    with self.assertRaisesRegex(ValueError,'metadata mismatch'):m.main('test-approval',m.sha(grant))
                    self.assertEqual(target.read_bytes(),self.old)
                    self.assertEqual(sum(x[:3]==('/usr/bin/systemctl','reload','nginx.service') for x in calls),2)
                else:
                    m.main('test-approval',m.sha(grant))
                    self.assertEqual(target.read_bytes(),m.expected_files(self.old,self.common)[0])
                    self.assertTrue((root/'target-route-activation.json').exists())
                self.assertFalse(any('restart' in x for x in calls))

if __name__=='__main__':unittest.main()
