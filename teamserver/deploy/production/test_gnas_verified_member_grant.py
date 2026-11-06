import importlib.util
from pathlib import Path
import time
import unittest

p=Path(__file__).with_name('gnas_verified_member_grant.py')
s=importlib.util.spec_from_file_location('verified_member_grant',p);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)

class VerifiedMemberGrantTests(unittest.TestCase):
    def setUp(self):
        self.binding={'binding_id':'test-binding','public_resource':'https://zhycit.com'}
        now=int(time.time())
        self.good={'active':True,'iss':'https://zhycit.com/gnas/oauth','aud':['https://zhycit.com/mcp'],
          'token_use':'access','tenant':'test-binding','wecom_userid':'member-1','sub':'wecom:test-binding:member-1',
          'client_id':'test-client','scope':'zoop.read zoop.write','iat':now,'exp':now+300,'ai_subject':''}
    def test_valid_identity_and_wrong_scope_or_tenant(self):
        self.assertEqual(m.validate_member(self.good,self.binding,'test-client'),'member-1')
        for key,value in [('active',False),('tenant','other'),('aud',['https://other/mcp']),('scope','zoop.read'),
                          ('client_id','other'),('wecom_userid','external_userid_1'),('ai_subject','agent')]:
            bad=dict(self.good);bad[key]=value
            with self.subTest(key=key),self.assertRaisesRegex(ValueError,'not verified'):
                m.validate_member(bad,self.binding,'test-client')
    def test_exactly_one_target_binding(self):
        self.assertEqual(m.target_binding([self.binding,{'public_resource':'https://other'}]),self.binding)
        with self.assertRaisesRegex(ValueError,'not unique'):m.target_binding([self.binding,self.binding])
    def test_go_digest_preserves_order_and_escapes_html(self):
        import hashlib
        raw=b'{"binding_id":"a\\u003cb","public_resource":"https://zhycit.com"}'
        self.assertEqual(m.binding_digest({'binding_id':'a<b','public_resource':'https://zhycit.com'}),hashlib.sha256(raw).hexdigest())
    def test_grant_is_specific_and_never_wildcard(self):
        self.assertIn('wecom_record_apply',m.TOOLS)
        self.assertNotIn('*',m.TOOLS)
        self.assertNotIn('wecom_schema_migration_apply',m.TOOLS)
        self.assertEqual(len(m.TOOLS),len(set(m.TOOLS)))
if __name__=='__main__':unittest.main()
