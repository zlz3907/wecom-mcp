import importlib.util
import os
from pathlib import Path
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('recover_closed',Path(__file__).with_name('recover-closed-target.py'))
recover=importlib.util.module_from_spec(spec);spec.loader.exec_module(recover)


class FakeSocket:
    def __init__(self):self.reuse=False
    def __enter__(self):return self
    def __exit__(self,*args):pass
    def setsockopt(self,level,option,value):self.reuse=value==1
    def bind(self,address):
        if not self.reuse:raise OSError('TIME_WAIT requires reuse')


class RecoverTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.proc=Path(self.tmp.name);(self.proc/'self/ns').mkdir(parents=True);(self.proc/'1/ns').mkdir(parents=True);(self.proc/'net').mkdir()
        first=self.proc/'self/ns/net';first.touch();os.link(first,self.proc/'1/ns/net')
        (self.proc/'net/tcp').write_text('header\n');(self.proc/'net/tcp6').write_text('header\n')
    def row(self,address,state):return '0: '+address+' 00000000:0000 '+state+' 0 0 0 0 0 0\n'
    def test_recovery_plan_binds_original_and_candidate(self):
        plan={'schema_version':1,'environment':'zhycit-prod-01','action':'recover-closed-target',
              'original_approval_id':recover.ORIGINAL_ID,'original_plan_sha256':recover.ORIGINAL_PLAN_SHA,
              'release_id':recover.RELEASE_ID,'candidate_sha256':recover.CANDIDATE_SHA,
              'unit':'wecom-mcp-zhycit-migration.service','recovery_script_sha256':'a'*64,
              'controller_archive_sha256':recover.ARCHIVE_SHA}
        recover.validate_recovery_plan(plan,'a'*64)
        for key in ('original_approval_id','original_plan_sha256','release_id','candidate_sha256','unit','recovery_script_sha256'):
            wrong=dict(plan);wrong[key]='different'
            with self.subTest(key=key),self.assertRaises(ValueError):recover.validate_recovery_plan(wrong,'a'*64)

    def test_expired_approval_is_not_consumed(self):
        consumed=[]
        def expired():raise ValueError('approval expired during preflight')
        with self.assertRaises(ValueError):recover.consume_after_recheck(expired,lambda:consumed.append(True))
        self.assertEqual(consumed,[])

    def test_systemd_argv_preserves_spaces_and_rejects_changed_arguments(self):
        expected=['/usr/bin/env','SCOPES=zoop.read zoop.write','/candidate','--check-config']
        value={'type':'a(sasbttttuii)','data':[[expected[0],expected,False,0,0,0,0,0,0,0]]}
        recover.validate_systemd_bus_property(value,expected)
        changed={'type':value['type'],'data':[[expected[0],['/usr/bin/env','SCOPES=zoop.read','zoop.write','/candidate','--check-config'],False,0,0,0,0,0,0,0]]}
        with self.assertRaises(ValueError):recover.validate_systemd_bus_property(changed,expected)
        changed={'type':value['type'],'data':[[expected[0],expected,True,0,0,0,0,0,0,0]]}
        with self.assertRaises(ValueError):recover.validate_systemd_bus_property(changed,expected)

    def test_time_wait_does_not_count_as_listener(self):
        (self.proc/'net/tcp').write_text('header\n'+self.row('0100007F:1E1D','06'))
        self.assertTrue(recover.no_listening_socket(7709,self.proc,lambda *_:FakeSocket()))
    def test_ipv4_ipv6_and_reuseport_listeners_are_refused(self):
        for table,address in (('tcp','00000000:1E1D'),('tcp6','00000000000000000000000000000000:1E1D')):
            with self.subTest(table=table):
                path=self.proc/'net'/table;path.write_text('header\n'+self.row(address,'0A'))
                self.assertFalse(recover.no_listening_socket(7709,self.proc,lambda *_:FakeSocket()))
                path.write_text('header\n')
    def test_foreign_network_namespace_is_refused(self):
        p=self.proc/'1/ns/net';p.unlink();p.touch()
        with self.assertRaises(ValueError):recover.no_listening_socket(7709,self.proc,lambda *_:FakeSocket())


if __name__=='__main__':unittest.main()
