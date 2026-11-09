import importlib.util
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('check_discovery', HERE / 'check-discovery-candidate.py')
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)


class DiscoveryEvidenceTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.release = '20261105T001200-21462d2dfb22'
        self.bundle = self.root / self.release
        self.bundle.mkdir()
        (self.bundle / 'wecom-mcp-team').write_bytes(b'binary')
        (self.bundle / 'discovery-policy.json').write_bytes(b'policy')
        self.manifest = self.bundle / 'fallback-manifest.json'
        self.manifest.write_text(json.dumps({'release_id': self.release,
            'binary_sha256': CHECK.sha(self.bundle / 'wecom-mcp-team'),
            'policy_sha256': CHECK.sha(self.bundle / 'discovery-policy.json')}))
        self.catalogue = self.bundle / 'SHA256SUMS'
        self.catalogue.write_text(''.join(CHECK.sha(self.bundle / name) + '  ' + name + '\n' for name in
            ('discovery-policy.json', 'fallback-manifest.json', 'wecom-mcp-team')))
        self.smoke = self.root / 'smoke.json'
        self.smoke.write_bytes(b'smoke')
        self.archive = self.root / 'fallback.tar.gz'
        with tarfile.open(self.archive, 'w:gz') as tar:
            tar.add(self.bundle, arcname=self.release)
        self.checked = {'expected_current_release_id': self.release,
            'expected_current_binary_sha256': CHECK.sha(self.bundle / 'wecom-mcp-team'),
            'expected_current_policy_sha256': CHECK.sha(self.bundle / 'discovery-policy.json'),
            'expected_current_archive_sha256': CHECK.sha(self.archive),
            'expected_current_manifest_sha256': CHECK.sha(self.manifest),
            'expected_current_catalogue_sha256': CHECK.sha(self.catalogue),
            'legacy_smoke_sha256': CHECK.sha(self.smoke)}

    def verify(self):
        CHECK.verify_evidence(self.checked, self.archive, self.manifest, self.catalogue, self.smoke)

    def test_matching_evidence_is_accepted(self):
        self.verify()

    def test_evidence_hash_drift_is_rejected(self):
        for field in ('expected_current_archive_sha256', 'expected_current_manifest_sha256',
                      'expected_current_catalogue_sha256', 'legacy_smoke_sha256'):
            with self.subTest(field=field):
                original = self.checked[field]
                self.checked[field] = '0' * 64
                with self.assertRaisesRegex(ValueError, 'evidence digest mismatch'):
                    self.verify()
                self.checked[field] = original

    def test_archive_and_separate_metadata_must_match(self):
        self.catalogue.write_bytes(b'changed')
        self.checked['expected_current_catalogue_sha256'] = CHECK.sha(self.catalogue)
        with self.assertRaisesRegex(ValueError, 'metadata mismatch'):
            self.verify()

    def test_symlink_evidence_is_rejected(self):
        link = self.root / 'link'
        link.symlink_to(self.smoke)
        with self.assertRaisesRegex(ValueError, 'canonical regular file'):
            CHECK.verify_evidence(self.checked, self.archive, self.manifest, self.catalogue, link)

    def test_policy_must_match_independently_bound_baseline(self):
        self.checked['expected_current_policy_sha256'] = '0' * 64
        with self.assertRaisesRegex(ValueError, 'fallback manifest baseline mismatch'):
            self.verify()

    def test_invalid_catalogue_in_archive_is_rejected(self):
        original = self.catalogue.read_bytes()
        for content in (b'invalid\n', b'\n'.join(original.splitlines()[:2]) + b'\n', original + original):
            with self.subTest(content=content[:15]):
                self.catalogue.write_bytes(content)
                with tarfile.open(self.archive, 'w:gz') as tar:
                    tar.add(self.bundle, arcname=self.release)
                self.checked['expected_current_archive_sha256'] = CHECK.sha(self.archive)
                self.checked['expected_current_catalogue_sha256'] = CHECK.sha(self.catalogue)
                with self.assertRaisesRegex(ValueError, 'fallback catalogue invalid'):
                    self.verify()

    def test_candidate_archive_must_match_every_checked_file(self):
        candidate = self.root / 'candidate'
        candidate.mkdir()
        for name in set(CHECK.d.FILES) | {'SHA256SUMS'}:
            (candidate / name).write_bytes(name.encode())
        archive = self.root / 'candidate.tar'
        with tarfile.open(archive, 'w') as tar:
            for name in set(CHECK.d.FILES) | {'SHA256SUMS'}:
                tar.add(candidate / name, arcname=name)
        CHECK.verify_candidate_archive(candidate, archive)
        (candidate / 'wecom-mcp-team').write_bytes(b'different')
        with self.assertRaisesRegex(ValueError, 'differs'):
            CHECK.verify_candidate_archive(candidate, archive)

    def test_candidate_archive_rejects_duplicates_and_links(self):
        candidate = self.root / 'candidate'
        candidate.mkdir()
        names = set(CHECK.d.FILES) | {'SHA256SUMS'}
        for name in names:
            (candidate / name).write_bytes(name.encode())
        archive = self.root / 'candidate.tar'
        with tarfile.open(archive, 'w') as tar:
            for name in names:
                tar.add(candidate / name, arcname=name)
            tar.add(candidate / 'wecom-mcp-team', arcname='wecom-mcp-team')
        with self.assertRaisesRegex(ValueError, 'unexpected candidate archive'):
            CHECK.verify_candidate_archive(candidate, archive)
        (candidate / 'wecom-mcp-team').unlink()
        (candidate / 'wecom-mcp-team').symlink_to(candidate / 'manifest.json')
        with tarfile.open(archive, 'w') as tar:
            for name in names:
                tar.add(candidate / name, arcname=name)
        with self.assertRaisesRegex(ValueError, 'unexpected candidate archive'):
            CHECK.verify_candidate_archive(candidate, archive)


if __name__ == '__main__':
    unittest.main()
