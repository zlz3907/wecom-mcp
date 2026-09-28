import contextlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('completion',Path(__file__).with_name('complete-shared-migration.py'))
x=importlib.util.module_from_spec(spec);spec.loader.exec_module(x)
c=x.c


class CompletionTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name).resolve();self.rid='20260924T034323Z-'+'a'*12;self.aid='APR-20260924T080000Z-testonly'
        self.releases=self.root/'releases';(self.releases/self.rid).mkdir(parents=True)
        (self.releases/self.rid/'service.conf').write_bytes(b'approved hybrid')
        self.control=self.root/'control';self.record=self.control/'unit-migrations'/self.rid;self.record.mkdir(parents=True)
        (self.record/'expected.json').write_bytes(b'original immutable baseline')
        self.dropin=self.root/'managed.conf';self.dropin.write_bytes(b'existing static')
        self.m={'release_id':self.rid};self.expected={'current_main_pid':123,'current_override_sha256':'a'*64,'previous_controller_sha256':'b'*64}
        self.stack=contextlib.ExitStack();self.addCleanup(self.stack.close)
        for name,value in (('RELEASES',self.releases),('CONTROL',self.control),('DROPIN',self.dropin)):
            self.stack.enter_context(patch.object(c,name,value))
        self.fields=self.stack.enter_context(patch.object(x,'fields',return_value=(self.m,self.expected)))
        self.approval=self.stack.enter_context(patch.object(c,'approval'))
        self.check=self.stack.enter_context(patch.object(c,'check_approval'))
        self.stack.enter_context(patch.object(c,'check_gnas'))
        self.preflight=self.stack.enter_context(patch.object(x.u,'preflight'))
        self.finish=self.stack.enter_context(patch.object(x.u,'finish',return_value={'state':'migrated_hybrid'}))

    def test_declined_receipt_does_not_preflight_or_switch(self):
        self.check.side_effect=ValueError('receipt mismatch')
        with self.assertRaises(ValueError):x.apply(self.rid,self.aid)
        self.preflight.assert_not_called();self.approval.assert_not_called();self.finish.assert_not_called()
        self.assertEqual(self.dropin.read_bytes(),b'existing static')

    def test_preflight_journal_process_or_controller_drift_refuses_consumption(self):
        for key in ('original_journal_sha256','current_main_pid','previous_controller_sha256'):
            with self.subTest(key=key):
                self.fields.side_effect=[(self.m,self.expected),(self.m,dict(self.expected,**{key:'changed'}))]
                with self.assertRaisesRegex(ValueError,'baseline changed'):x.apply(self.rid,self.aid)
                self.approval.assert_not_called();self.finish.assert_not_called()
                self.assertEqual(self.dropin.read_bytes(),b'existing static')

    def test_completion_reuses_candidate_and_preserves_original_journal(self):
        self.assertEqual(x.apply(self.rid,self.aid)['state'],'migrated_hybrid')
        self.assertEqual(self.dropin.read_bytes(),b'approved hybrid')
        self.assertEqual((self.record/'expected.json').read_bytes(),b'original immutable baseline')
        self.finish.assert_called_once_with(self.m,self.expected,self.record)
        self.approval.assert_called_once_with(self.aid,'complete-shared-migration',self.expected)

    def test_failed_hybrid_uses_same_candidate_static_and_reports_failure(self):
        self.finish.side_effect=[ValueError('not ready'),{'state':'recovered_static'}]
        with self.assertRaisesRegex(ValueError,'same-version static recovery active'):x.apply(self.rid,self.aid)
        self.assertEqual(self.finish.call_count,2)
        self.assertEqual(self.finish.call_args.args,(self.m,self.expected,self.record))
        self.assertEqual(self.finish.call_args.kwargs,{'recovery':True})

    def test_drift_after_consumption_keeps_mode_unchanged(self):
        self.fields.side_effect=[(self.m,self.expected),(self.m,self.expected),(self.m,dict(self.expected,current_main_pid=456))]
        with self.assertRaisesRegex(ValueError,'before mode switch'):x.apply(self.rid,self.aid)
        self.assertEqual(self.dropin.read_bytes(),b'existing static');self.finish.assert_not_called()
        self.assertTrue((self.record/('completion-'+self.aid+'.json')).exists())
