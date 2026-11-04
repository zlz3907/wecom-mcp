import contextlib
import importlib.util
import hashlib
import os
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('staging', HERE / 'prepare-blue-green-staging.py')
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)


class BlueGreenStagingTests(unittest.TestCase):
    def green(self):
        return {
            'runtime_verified': True, 'active': 'active', 'restarts': 0,
            'runtime_path': '/releases/green/wecom-mcp-team', 'binary_sha256': 'a' * 64,
            'unit_fingerprint': 'b' * 64, 'runtime_config_fingerprint': 'c' * 64,
        }

    def test_check_returns_only_path_not_environment_contents_or_digest(self):
        green = self.green()
        with tempfile.TemporaryDirectory() as root:
            missing_dropin = Path(root) / 'missing.conf'
            with patch.object(s.c, 'status', return_value=green), \
                 patch.object(s.c, 'baseline_matches', return_value=True), \
                 patch.object(s.c, 'regular') as regular, \
                 patch.object(s, 'mem_available_bytes', return_value=s.MIN_AVAILABLE_BYTES), \
                 patch.object(s.b, 'BLUE_DROPIN', missing_dropin), \
                 patch.object(s, 'artifact_fields', return_value={'/fixed/artifact': 'd' * 64}), \
                 patch.object(s, 'inactive', side_effect=[{'active_state': 'inactive', 'main_pid': '0'}] * 2):
                value = s.fields()
        self.assertEqual(value['environment_file_path'], str(s.ENVIRONMENT_FILE))
        self.assertIsNone(value['environment_file_sha256'])
        self.assertEqual(value['traffic'], 'disabled')
        self.assertFalse(value['binding_write_permitted'])
        regular.assert_called_once_with(s.ENVIRONMENT_FILE, True)

    def test_low_memory_refuses_before_unit_state_is_read(self):
        green = self.green()
        with patch.object(s.c, 'status', return_value=green), \
             patch.object(s.c, 'baseline_matches', return_value=True), \
             patch.object(s.c, 'regular'), \
             patch.object(s, 'mem_available_bytes', return_value=s.MIN_AVAILABLE_BYTES - 1), \
             patch.object(s, 'inactive') as inactive:
            with self.assertRaisesRegex(ValueError, 'insufficient MemAvailable'):
                s.fields()
        inactive.assert_not_called()

    def test_source_has_no_service_start_or_reload_in_inert_path(self):
        raw = (HERE / 'prepare-blue-green-staging.py').read_text()
        apply = raw.split('def apply_inert(', 1)[1].split('def main()', 1)[0]
        self.assertNotIn('run(', apply)
        self.assertNotIn('nginx', apply.lower())

    def fixture(self):
        root = Path(tempfile.mkdtemp()).resolve()
        self.addCleanup(lambda: __import__('shutil').rmtree(root, ignore_errors=True))
        control = root / 'control'
        control.mkdir(mode=0o700)
        (control / 'used').mkdir(mode=0o700)
        target_parent = root / 'targets'
        target_parent.mkdir(mode=0o700)
        targets = tuple((source, target_parent / Path(target).name, mode)
                        for source, target, mode in s.TARGETS)
        blue_state = root / 'state' / 'discovery-blue'
        active = control / 'entrypoint' / 'active.json'
        value = {
            'environment': 'fixture',
            'artifacts': {
                str(target): hashlib.sha256((s.HERE / source).read_bytes()).hexdigest()
                for source, target, _ in targets
            },
            'blue_state_root': str(blue_state), 'receipt_parent': str(active.parent),
            'traffic': 'disabled', 'binding_write_permitted': False,
        }

        def digest(path):
            return hashlib.sha256(Path(path).read_bytes()).hexdigest()

        def trust(path):
            info = Path(path).lstat()
            if not Path(path).is_dir() or Path(path).is_symlink() or info.st_mode & 0o022:
                raise AssertionError('untrusted fixture directory: ' + str(path))

        return root, control, targets, blue_state, active, value, digest, trust

    def patched_fixture(self, fixture):
        root, control, targets, blue_state, active, value, digest, trust = fixture
        return patch.multiple(
            s,
            TARGETS=targets,
            regular=digest,
            trusted_directory=trust,
            service_identity=lambda: (os.getuid(), os.getgid()),
        ), patch.object(s.c, 'CONTROL', control), patch.object(s.b, 'BLUE_STATE', blue_state), patch.object(s.b, 'ACTIVE', active), patch.object(s.c, 'read_json', side_effect=lambda path, root=False: json.loads(Path(path).read_text()))

    def enter_fixture(self, fixture):
        stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        for patcher in self.patched_fixture(fixture):
            stack.enter_context(patcher)
        return stack

    def test_apply_consumes_approval_before_any_staging_write_and_is_idempotent(self):
        fixture = self.fixture()
        _, _, targets, _, _, value, _, _ = fixture
        events = []
        original_atomic = s.c.atomic
        original_mkdir = Path.mkdir

        def atomic(path, data, mode=0o644):
            if Path(path) in {target for _, target, _ in targets}:
                events.append('artifact')
            return original_atomic(path, data, mode)

        def mkdir(path, *args, **kwargs):
            events.append('mkdir')
            return original_mkdir(path, *args, **kwargs)

        def approval(*args):
            events.append('approval')

        with self.enter_fixture(fixture), patch.object(s.c, 'atomic', side_effect=atomic), patch.object(Path, 'mkdir', new=mkdir), patch.object(s.os, 'chown', side_effect=lambda *args: events.append('chown')), patch.object(s.c, 'check_approval'), patch.object(s.c, 'approval', side_effect=approval):
            result = s.apply_inert('APR-20261104T120000Z-fixture', value)
            self.assertEqual(result['state'], 'inert_artifacts_installed')
            first_approval = events.index('approval')
            first_write = min(index for index, event in enumerate(events) if event in ('mkdir', 'chown', 'artifact'))
            self.assertLess(first_approval, first_write)
            second = s.apply_inert('APR-20261104T120000Z-fixture', value)
            self.assertEqual(second['state'], 'inert_artifacts_already_installed')
            self.assertEqual(events.count('approval'), 1)

    def test_mid_artifact_failure_rolls_back_everything_and_cleans_journal(self):
        fixture = self.fixture()
        _, control, targets, blue_state, active, value, _, _ = fixture
        original_atomic = s.c.atomic
        failing_target = targets[1][1]

        def atomic(path, data, mode=0o644):
            if Path(path) == failing_target:
                raise OSError('fixture artifact failure')
            return original_atomic(path, data, mode)

        with self.enter_fixture(fixture), patch.object(s.c, 'atomic', side_effect=atomic), patch.object(s.c, 'check_approval'), patch.object(s.c, 'approval'):
            with self.assertRaisesRegex(OSError, 'fixture artifact failure'):
                s.apply_inert('APR-20261104T120000Z-fixture', value)
            self.assertFalse(any(target.exists() for _, target, _ in targets))
            self.assertFalse(blue_state.exists())
            self.assertFalse(active.parent.exists())
            self.assertFalse((control / 'blue-green-staging.APR-20261104T120000Z-fixture.json').exists())


if __name__ == '__main__':
    unittest.main()
