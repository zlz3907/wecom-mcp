import importlib.util
from pathlib import Path
import unittest
p=Path(__file__).with_name('zoop_oauth_units.py')
s=importlib.util.spec_from_file_location('zoop_oauth_units',p); m=importlib.util.module_from_spec(s);s.loader.exec_module(m)

class UnitTests(unittest.TestCase):
    def setUp(self):
        self.old=['20261106T203247Z-932ca8583c69','a'*64,'20261106T040805Z-1acaa4269334','b'*64,'c'*64]
        self.new=['20261108T144914Z-e3620ffda6aa','d'*64,'20261107T193406Z-4c9c55635682','e'*64,'c'*64]
        self.unit=b'[Service]\n'+m.PREFIX+' '.join(self.old).encode()+b'\nRestart=on-failure\n[Install]\nWantedBy=multi-user.target\n'
    def test_candidate_and_rollback_rebase_without_changing_other_settings(self):
        candidate=m.rewrite(self.unit,self.old,self.new)
        self.assertIn(' '.join(self.new).encode(),candidate)
        rollback=m.rewrite(self.unit,self.old,[self.old[0],self.old[1],*self.new[2:]])
        self.assertIn((self.old[0]+' '+self.old[1]+' '+self.new[2]).encode(),rollback)
        for value in (candidate,rollback): self.assertIn(b'Restart=on-failure\n',value)
    def test_probe_only_uses_loopback_and_is_not_enabled_at_boot(self):
        probe=m.rewrite(self.unit,self.old,self.new,probe=True)
        self.assertIn(b'zoop-oauth-probe-env-exec.py ',probe)
        self.assertIn(b' 7505\n',probe)
        self.assertNotIn(b'WantedBy',probe)
        self.assertIn(b'Restart=no\n',probe)
    def test_drift_and_injected_arguments_fail_closed(self):
        for unit,args in ((self.unit+b'ExecStart=other\n',self.new),(self.unit.replace(b'Restart',b'ExecStart=x\nRestart'),self.new),(self.unit,self.new[:-1]+['c'*63+';'])):
            with self.subTest(args=args):
                with self.assertRaises(ValueError): m.rewrite(unit,self.old,args)
