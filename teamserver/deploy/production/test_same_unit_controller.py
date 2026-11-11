import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class ArchiveTest(unittest.TestCase):
    def test_isolated_zipapp_loads_modules_and_rejects_untrusted_location(self):
        source = Path(__file__).with_name('build-same-unit-controller.py')
        spec = importlib.util.spec_from_file_location('archive_builder', source)
        builder = importlib.util.module_from_spec(spec); spec.loader.exec_module(builder)
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / 'controller.pyz'
            builder.build(archive)
            probe = subprocess.run([sys.executable, '-I', '-c',
                'import sys;sys.path.insert(0,sys.argv[1]);import same_unit_guarded_ops,same_unit_live_gates;print("imports-ok")',
                str(archive)], capture_output=True, text=True)
            self.assertEqual((probe.returncode, probe.stdout.strip()), (0, 'imports-ok'), probe.stderr)
            result = subprocess.run([sys.executable, '-I', str(archive)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('untrusted controller archive', result.stderr)
            self.assertNotIn('ModuleNotFoundError', result.stderr)


if __name__ == '__main__': unittest.main()
