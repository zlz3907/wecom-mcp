import importlib.util
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


if __name__ == '__main__':
    unittest.main()
