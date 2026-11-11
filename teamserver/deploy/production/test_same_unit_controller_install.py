import hashlib
import importlib.util
import io
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('install-same-unit-controller.py'))
installer = importlib.util.module_from_spec(spec); spec.loader.exec_module(installer)


def stream(files):
    result = io.BytesIO()
    with tarfile.open(fileobj=result, mode='w') as archive:
        for name, data in files.items():
            member = tarfile.TarInfo(name); member.size = len(data); archive.addfile(member, io.BytesIO(data))
    result.seek(0)
    return result


class InstallTest(unittest.TestCase):
    def test_install_is_immutable_idempotent_and_does_not_change_services(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            for path in ('usr/local/lib', 'var/lib/wecom-mcp-isolated', 'etc/gnas'):
                (root / path).mkdir(parents=True, mode=0o755)
            files = {'same-unit-execution.pyz': b'archive', 'fleet-readonly-probe': b'probe'}
            hashes = {k: hashlib.sha256(v).hexdigest() for k, v in files.items()}
            calls = []
            def observe(): calls.append(True); return {'service': 123}
            for _ in range(2):
                result = installer.install(stream(files), root, os.getuid(), observe, hashes)
                self.assertFalse(result['service_restarted']); self.assertFalse(result['routes_changed'])
            self.assertEqual(len(calls), 4)
            target = root / 'usr/local/lib/wecom-mcp-open-bootstrap/fleet-readonly-probe'
            self.assertEqual(target.stat().st_mode & 0o777, 0o555)
            target.chmod(0o755)
            with self.assertRaises(ValueError): installer.install(stream(files), root, os.getuid(), observe, hashes)

    def test_new_directory_uses_exact_mode_under_restrictive_umask(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve(); directory = root / 'controller'
            previous = os.umask(0o077)
            try: installer.ensure_dir(directory, root, os.getuid(), 0o755)
            finally: os.umask(previous)
            self.assertEqual(directory.stat().st_mode & 0o777, 0o755)
            installer.ensure_dir(directory, root, os.getuid(), 0o755)

    def test_interrupted_atomic_publish_can_be_retried(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve(); target = root / 'artifact'; data = b'controller'
            sha = hashlib.sha256(data).hexdigest()
            with patch.object(installer.os, 'link', side_effect=OSError('simulated interruption')):
                with self.assertRaises(OSError): installer.publish(target, data, sha, os.getuid())
            self.assertFalse(target.exists())
            self.assertEqual(list(root.iterdir()), [])
            installer.publish(target, data, sha, os.getuid())
            self.assertEqual(target.read_bytes(), data)

    def test_crash_leftover_is_removed_before_retry(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            orphan = root / ('.mcp-install-' + 'a' * 32)
            orphan.write_bytes(b'partial'); orphan.chmod(0o600)
            installer.cleanup_orphans(root, os.getuid())
            self.assertEqual(list(root.iterdir()), [])
            orphan.symlink_to(root / 'elsewhere')
            with self.assertRaises(ValueError): installer.cleanup_orphans(root, os.getuid())

    def test_input_digest_mismatch_has_no_install_side_effect(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp).resolve()
            with self.assertRaises(ValueError):
                installer.install(stream({'a': b'wrong'}), root, os.getuid(), lambda: {}, {'a': '0'*64})
            self.assertEqual(list(root.iterdir()), [])


if __name__ == '__main__': unittest.main()
