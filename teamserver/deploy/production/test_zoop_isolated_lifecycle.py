import contextlib
import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

path=Path(__file__).with_name('zoop_isolated_lifecycle.py')
spec=importlib.util.spec_from_file_location('lifecycle',path);m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class LifecycleTests(unittest.TestCase):
    def setUp(self):
        self.g=b'[Unit]\nAfter=network-online.target\n[Service]\nRestart=no\n'
        self.c=b'[Unit]\nAfter=network-online.target gnas-oauth-zhycit-isolated.service\n[Service]\nRestart=no\n'
    def test_boot_order_restart_policy_and_existing_settings(self):
        g,c=m.build_units(self.g,self.c)
        self.assertIn(b'After=network-online.target gnas-server.service\n',g)
        for unit in (g,c):
            self.assertIn(b'Restart=on-failure\nRestartSec=5s\n',unit)
            self.assertTrue(unit.endswith(b'[Install]\nWantedBy=multi-user.target\n'))
        self.assertIn(b'gnas-oauth-zhycit-isolated.service',c)
        with self.assertRaisesRegex(ValueError,'unit content'):m.build_units(self.g.replace(b'Restart=no',b'Restart=always'),self.c)
    def test_exact_receipt_and_expiry(self):
        approval='APR-20261107T010646Z-0541a94db7ab';digest='a'*64;now=dt.datetime.now(dt.timezone.utc)
        ng,nm=m.build_units(self.g,self.c);deps={'one':'b'*64}
        obj={'schema_version':1,'approval_id':approval,'environment':'zhycit-prod-01','action':'persist-isolated-zoop-units',
          'target':'zhycit.com','grant_sha256':digest,'oauth_canary_pid':22,'mcp_canary_pid':33,
          'old_gnas_unit_sha256':m.sha_bytes(self.g),'new_gnas_unit_sha256':m.sha_bytes(ng),
          'old_mcp_unit_sha256':m.sha_bytes(self.c),'new_mcp_unit_sha256':m.sha_bytes(nm),
          'controller_sha256':'c'*64,'dependencies_sha256':deps,'approved_at':now.isoformat(),
          'expires_at':(now+dt.timedelta(minutes=5)).isoformat(),'approved_by':'delegated-root'}
        with tempfile.TemporaryDirectory() as tmp,patch.object(m,'APPROVALS',Path(tmp)),patch.object(m,'trusted'),patch.object(m,'sha',return_value='c'*64):
            p=Path(tmp)/(approval+'.json');p.write_text(json.dumps(obj));p.chmod(0o400)
            m.check_receipt(approval,digest,(22,33),self.g,self.c,ng,nm,deps)
            with self.assertRaisesRegex(ValueError,'does not bind'):m.check_receipt(approval,digest,(23,33),self.g,self.c,ng,nm,deps)
    def test_lifecycle_does_not_restart_and_recovers_failed_enable(self):
        for broken in (False,True):
            with self.subTest(broken=broken),tempfile.TemporaryDirectory() as tmp,contextlib.ExitStack() as stack:
                root=Path(tmp).resolve();(root/'used').mkdir();(root/'release.lock').touch();gl=root/'glock';gl.touch();w=root/'wants';w.mkdir()
                gu=root/'gnas.service';gu.write_bytes(self.g);mu=root/'mcp.service';mu.write_bytes(self.c)
                grant=root/'grant';grant.write_bytes(b'grant');grant.chmod(0o400);control=root/'control';control.write_bytes(b'control')
                mcp=types.SimpleNamespace(snapshot=lambda:'same',OLD_GNAS_SHA='old',unit_content=lambda:self.c)
                gnas=types.SimpleNamespace(unit_content=lambda p:self.g)
                route=types.SimpleNamespace(GNAS_SHA='candidate',TARGET=root/'target',ORIGINAL_SHA='old-target',ISOLATED=root/'isolated',
                    sha=lambda p:'old-target',active_canaries=lambda *a:(22,33))
                modules={control:None}
                for name,value in {'MROOT':root,'GLOCK':gl,'WANTS':w,'GUNIT':gu,'MUNIT':mu,'GRANT':grant,'SELF':path.resolve(),
                                   'MCP':root/'mcp-control','GNAS':root/'gnas-control','ROUTE':root/'route-control','HELPER':root/'helper'}.items():
                    if name in ('MCP','GNAS','ROUTE','HELPER'):value.write_bytes(b'control')
                    stack.enter_context(patch.object(m,name,value))
                modules={m.MCP:mcp,m.GNAS:gnas,m.ROUTE:route,m.HELPER:object()}
                stack.enter_context(patch.object(m.os,'geteuid',return_value=0));stack.enter_context(patch.object(m,'trusted'))
                stack.enter_context(patch.object(m,'check_receipt'));stack.enter_context(patch.object(m,'load',side_effect=lambda n,p:modules[p]))
                calls=[]
                def run(*args,**kwargs):
                    calls.append(args)
                    if args[1]=='enable':
                        (w/gu.name).symlink_to(gu)
                        if broken:raise RuntimeError('simulated enable failure')
                        (w/mu.name).symlink_to(mu)
                    if args[1]=='is-enabled':return b'enabled\n'
                    if args[1]=='show':return b'on-failure\n'
                    return b''
                stack.enter_context(patch.object(m,'run',side_effect=run))
                if broken:
                    with self.assertRaises(RuntimeError):m.main('approval',m.sha(grant))
                    self.assertEqual(gu.read_bytes(),self.g);self.assertEqual(mu.read_bytes(),self.c)
                    self.assertFalse((w/gu.name).is_symlink())
                else:
                    m.main('approval',m.sha(grant));self.assertTrue((w/gu.name).is_symlink());self.assertTrue((w/mu.name).is_symlink())
                self.assertFalse(any('restart' in c or 'start' in c for c in calls))
if __name__=='__main__':unittest.main()
