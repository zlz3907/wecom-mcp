import importlib.util
import json
from pathlib import Path
import unittest

here=Path(__file__).parent

def module(name):
    spec=importlib.util.spec_from_file_location(name,here/(name+'.py'))
    result=importlib.util.module_from_spec(spec);spec.loader.exec_module(result);return result

u=module('zoop_oauth_units');m=module('zoop_oauth_release_plan')

class PlanTests(unittest.TestCase):
    def setUp(self):
        self.old=['20261106T203247Z-932ca8583c69','a'*64,'20261106T040805Z-1acaa4269334','b'*64,'c'*64]
        self.shared='20261107T193406Z-4c9c55635682';self.shared_sha='e'*64;self.sha='d'*64
        self.unit=b'[Service]\n'+u.PREFIX+' '.join(self.old).encode()+b'\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n'
        self.manifest={'schema_version':2,'environment':'zhycit-prod-01','artifact':'gnas-service-gin','source_clean':True,
            'target':{'goos':'linux','goarch':'amd64','cgo_enabled':False},'binary':{'name':'GNASServer','sha256':self.sha},
            'previous_release_id':self.shared,'previous_binary_sha256':self.shared_sha,
            'release_id':'20261108T144914Z-e3620ffda6aa','git_commit':'f'*40,'git_tree':'0'*40}
    def plan(self): return m.build(u,json.dumps(self.manifest).encode(),self.sha,self.unit,self.old,self.shared,self.shared_sha)
    def test_plan_binds_both_candidate_and_rebased_old_recovery(self):
        p=self.plan();self.assertEqual(p['old_release'],self.old[0]);self.assertEqual(p['shared_release'],self.shared)
        self.assertNotEqual(p['rollback_unit_sha256'],p['old_unit_sha256'])
        self.assertNotEqual(p['candidate_unit_sha256'],p['rollback_unit_sha256'])
    def test_wrong_baseline_or_candidate_is_rejected(self):
        for key,value in [('source_clean',False),('previous_release_id','20261101T000000Z-changed'),('binary',{'name':'GNASServer','sha256':'f'*64})]:
            with self.subTest(key=key):
                original=self.manifest[key];self.manifest[key]=value
                with self.assertRaises(ValueError): self.plan()
                self.manifest[key]=original
