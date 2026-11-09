import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import same_unit_staging_remote as staging


class StageTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.releases = self.root / 'releases'
        self.releases.mkdir()
        self.dest = self.releases / staging.RID
        self.addCleanup(lambda: self.dest.chmod(0o700) if self.dest.exists() else None)
        self.files = {name: name.encode() for name in staging.NAMES - {'manifest.json', 'SHA256SUMS'}}
        self.files['manifest.json'] = json.dumps({'release_id': staging.RID,
            'component': 'wecom-mcp-same-unit-bootstrap',
            'files': {name: hashlib.sha256(data).hexdigest() for name, data in self.files.items()}}).encode()
        self.files['SHA256SUMS'] = ''.join(hashlib.sha256(self.files[name]).hexdigest() + '  ' + name + '\n'
                                          for name in sorted(staging.NAMES - {'SHA256SUMS'})).encode()
        self.manifest_sha = hashlib.sha256(self.files['manifest.json']).hexdigest()

    def archive(self, extra=None):
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode='w') as archive:
            for name, data in self.files.items():
                info = tarfile.TarInfo(name); info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
            if extra:
                info = tarfile.TarInfo(extra); info.size = 1
                archive.addfile(info, io.BytesIO(b'x'))
        stream.seek(0)
        return stream

    def test_stage_immutable_package_and_never_overwrite(self):
        with patch.object(staging, 'MANIFEST_SHA', self.manifest_sha):
            result = staging.stage(self.archive(), self.releases, self.root, os.getuid(), lambda: 'stable')
            self.assertEqual(result['state'], 'staged')
            self.assertEqual(self.dest.stat().st_mode & 0o777, 0o555)
            self.assertFalse((self.dest / '.incomplete').exists())
            self.assertEqual((self.dest / 'wecom-mcp-team').stat().st_mode & 0o777, 0o555)
            with self.assertRaises(FileExistsError):
                staging.stage(self.archive(), self.releases, self.root, os.getuid(), lambda: 'stable')

    def test_invalid_members_or_bad_hash_do_not_create_release(self):
        with patch.object(staging, 'MANIFEST_SHA', self.manifest_sha):
            with self.assertRaises(ValueError):
                staging.stage(self.archive('../other'), self.releases, self.root, os.getuid(), lambda: 'stable')
            self.files['wecom-mcp-team'] = b'tampered'
            with self.assertRaises(ValueError):
                staging.stage(self.archive(), self.releases, self.root, os.getuid(), lambda: 'stable')
        self.assertFalse(self.dest.exists())

    def test_rewritten_checksums_cannot_override_signed_manifest(self):
        self.files['wecom-mcp-team'] = b'tampered'
        self.files['SHA256SUMS'] = ''.join(hashlib.sha256(self.files[name]).hexdigest() + '  ' + name + '\n'
                                          for name in sorted(staging.NAMES - {'SHA256SUMS'})).encode()
        with patch.object(staging, 'MANIFEST_SHA', self.manifest_sha), self.assertRaisesRegex(ValueError, 'payload does not match'):
            staging.stage(self.archive(), self.releases, self.root, os.getuid(), lambda: 'stable')
        self.assertFalse(self.dest.exists())

    def test_live_drift_before_mutation_refuses_staging(self):
        states = iter(('before', 'changed'))
        with patch.object(staging, 'MANIFEST_SHA', self.manifest_sha), self.assertRaisesRegex(ValueError, 'before staging'):
            staging.stage(self.archive(), self.releases, self.root, os.getuid(), lambda: next(states))
        self.assertFalse(self.dest.exists())

    def test_live_drift_after_staging_is_reported_without_deleting_artifact(self):
        states = iter(('before', 'before', 'changed'))
        with patch.object(staging, 'MANIFEST_SHA', self.manifest_sha), self.assertRaisesRegex(ValueError, 'staged but live baseline changed'):
            staging.stage(self.archive(), self.releases, self.root, os.getuid(), lambda: next(states))
        self.assertTrue((self.dest / 'manifest.json').is_file())
        self.assertFalse((self.dest / '.incomplete').exists())


if __name__ == '__main__':
    unittest.main()
