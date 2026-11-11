import contextlib
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

HERE = Path(__file__).parent


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


swap = load('policy_swap', 'zoop_oauth_policy_swap_prod.py')
units = load('policy_units', 'zoop_oauth_policy_units.py')
plan = load('policy_plan', 'zoop_oauth_policy_release_plan.py')


class PolicyOAuthSwapTests(unittest.TestCase):
    def test_existing_adaptive_unit_is_exact_rollback(self):
        original = (b'[Service]\n' + units.ADAPTIVE_PREFIX +
                    f'{swap.OLD} {swap.OLD_SHA} {swap.GRANT} live\n'.encode() +
                    b'Restart=on-failure\n[Install]\nWantedBy=multi-user.target\n')
        with mock.patch.object(swap, 'OLD_UNIT', hashlib.sha256(original).hexdigest()):
            generated = swap.units({'units': units}, original)
        self.assertEqual(generated['rollback'], original)
        self.assertIn(swap.RID.encode(), generated['candidate'])
        self.assertIn(b' probe\n', generated['probe_candidate'])
        self.assertNotIn(b'WantedBy', generated['probe_candidate'])

    def test_unfinished_rollback_gate_blocks_before_receipt_consumption(self):
        with mock.patch.object(swap, 'locks', return_value=contextlib.nullcontext()), mock.patch.object(swap, 'consume') as consume:
            with self.assertRaisesRegex(ValueError, 'rollback compatibility gate'):
                swap.prepare({}, 'unused')
            consume.assert_not_called()
        with self.assertRaisesRegex(ValueError, 'rollback compatibility gate'):
            swap.ProductionOps({}, '').install_and_restart_rollback(None, {})

    def test_plan_binds_isolated_predecessor_and_shared_dependency_separately(self):
        original = (b'[Service]\n' + units.ADAPTIVE_PREFIX +
                    f'{swap.OLD} {swap.OLD_SHA} {swap.GRANT} live\n'.encode() +
                    b'Restart=on-failure\n[Install]\nWantedBy=multi-user.target\n')
        manifest = {'schema_version': 2, 'environment': 'zhycit-prod-01', 'artifact': 'gnas-service-gin',
                    'source_clean': True, 'target': {'goos': 'linux', 'goarch': 'amd64', 'cgo_enabled': False},
                    'binary': {'name': 'GNASServer', 'sha256': swap.SHA}, 'release_id': swap.RID,
                    'previous_release_id': swap.OLD, 'previous_binary_sha256': swap.OLD_SHA,
                    'git_commit': 'a' * 40, 'git_tree': 'b' * 40}
        build = lambda value: plan.build(units, json.dumps(value).encode(), swap.SHA, original,
                                         swap.OLD, swap.OLD_SHA, swap.MAIN_RID, swap.MAIN_SHA, swap.GRANT)
        result = build(manifest)
        self.assertEqual(result['old_release'], swap.OLD)
        self.assertEqual(result['shared_release'], swap.MAIN_RID)
        self.assertEqual(result['rollback_unit_sha256'], result['old_unit_sha256'])
        manifest['previous_release_id'] = swap.MAIN_RID
        with self.assertRaisesRegex(ValueError, 'isolated predecessor'):
            build(manifest)

    def test_interrupted_before_unit_change_keeps_original_process_running(self):
        original = b'original unit'
        transaction = mock.Mock()
        op = swap.ProductionOps({'txn': transaction}, '')
        op.record = {'old_unit': original.decode(), 'rollback_unit': original.decode(),
                     'candidate_unit': 'candidate', 'before': {'old_pid': 123}}
        op.ensure_restart_job_settled = mock.Mock()
        op.verify_runtime = mock.Mock()
        with mock.patch.object(swap, 'require_policy_rollback_gate'), mock.patch.object(swap, 'UNIT') as unit, \
             mock.patch.object(swap, 'unit_no_overrides'), mock.patch.object(swap, 'runtime', return_value=123):
            unit.read_bytes.return_value = original
            op.install_and_restart_rollback(None, {})
        op.verify_runtime.assert_called_once_with(swap.OLD_SHA, original)
        transaction.activate.assert_not_called()

        op.verify_runtime.side_effect = ValueError('unhealthy')
        with mock.patch.object(swap, 'require_policy_rollback_gate'), mock.patch.object(swap, 'UNIT') as unit, \
             mock.patch.object(swap, 'unit_no_overrides'), mock.patch.object(swap, 'runtime', return_value=123):
            unit.read_bytes.return_value = original
            op.install_and_restart_rollback(None, {})
        transaction.activate.assert_called_once()

    def test_changed_controller_or_dependency_blocks_recovery(self):
        prepared = {'prepared_controller_sha256': 'controller', 'prepared_dependencies_sha256': {'helper': 'old'}}
        with mock.patch.object(swap, 'sha', return_value='controller'), mock.patch.object(swap, 'dependencies', return_value={'helper': 'new'}):
            with self.assertRaisesRegex(ValueError, 'dependency changed'):
                swap.verify_prepared_dependencies(prepared)
        with mock.patch.object(swap, 'sha', return_value='new-controller'), mock.patch.object(swap, 'dependencies', return_value={'helper': 'old'}):
            with self.assertRaisesRegex(ValueError, 'dependency changed'):
                swap.verify_prepared_dependencies(prepared)

    def test_changed_dependency_blocks_recover_and_manual_rollback_before_actions(self):
        with tempfile.TemporaryDirectory() as parent:
            root = Path(parent)
            journal, prepared = root / 'journal', root / 'prepared'
            prepared_data = {'prepared_controller_sha256': 'old', 'prepared_dependencies_sha256': {'helper': 'old'}}
            records = {journal: {}, prepared: prepared_data, root / 'success': {}}
            txn = mock.Mock()
            op = swap.ProductionOps({'txn': txn}, '')
            with mock.patch.object(swap, 'locks', return_value=contextlib.nullcontext()), \
                 mock.patch.object(swap, 'require_policy_rollback_gate'), \
                 mock.patch.object(swap, 'JOURNAL', journal), mock.patch.object(swap, 'PREP', prepared), \
                 mock.patch.object(swap, 'SUCCESS', root / 'success'), mock.patch.object(swap, 'ROLLED', root / 'rolled'), \
                 mock.patch.object(swap, 'read_json_trusted', side_effect=lambda path: records[path]), \
                 mock.patch.object(swap, 'sha', return_value='changed'), \
                 mock.patch.object(swap, 'dependencies', return_value={'helper': 'new'}), \
                 mock.patch.object(swap, 'consume') as consume:
                with self.assertRaisesRegex(ValueError, 'dependency changed'):
                    op.verify_recovery_journal(None, {})
                with self.assertRaisesRegex(ValueError, 'dependency changed'):
                    swap.manual_rollback({'txn': txn}, aid='unused')
                consume.assert_not_called()
                txn.activate.assert_not_called()
    def test_immutable_installed_artifact_rechecks_all_files(self):
        with tempfile.TemporaryDirectory() as parent:
            directory = Path(parent) / 'release'
            directory.mkdir()
            binary = b'nonproduction test binary'
            smoke = b'nonproduction smoke contract'
            sha = hashlib.sha256(binary).hexdigest()
            smoke_sha = hashlib.sha256(smoke).hexdigest()
            manifest = json.dumps({'release_id': swap.RID, 'previous_release_id': swap.OLD,
                                   'previous_binary_sha256': swap.OLD_SHA, 'legacy_smoke_sha256': smoke_sha,
                                   'binary': {'name': 'GNASServer', 'sha256': sha}}).encode()
            sums = b'test checksum receipt'
            contents = {'GNASServer': binary, 'legacy-smoke.json': smoke, 'manifest.json': manifest, 'SHA256SUMS': sums}
            for name, data in contents.items():
                (directory / name).write_bytes(data)
                (directory / name).chmod(0o555 if name == 'GNASServer' else 0o444)
            directory.chmod(0o555)
            try:
                with mock.patch.object(swap, 'trusted'), mock.patch.object(swap, 'SHA', sha), \
                     mock.patch.object(swap, 'SMOKE', smoke_sha), \
                     mock.patch.object(swap, 'MANIFEST', hashlib.sha256(manifest).hexdigest()), \
                     mock.patch.object(swap, 'SUMS', hashlib.sha256(sums).hexdigest()):
                    swap.verify_installed_artifact(directory)
                    (directory / 'legacy-smoke.json').chmod(0o600)
                    with self.assertRaises(ValueError):
                        swap.verify_installed_artifact(directory)
            finally:
                directory.chmod(0o700)


if __name__ == '__main__':
    unittest.main()
