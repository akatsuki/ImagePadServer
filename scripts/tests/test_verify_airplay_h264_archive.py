import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import warnings
import zipfile


class ArchiveContractTests(unittest.TestCase):
    def setUp(self):
        script = Path(__file__).resolve().parents[1] / 'verify-airplay-h264-archive.py'
        spec = importlib.util.spec_from_file_location('archive_contract', script)
        self.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.module)
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.archive = Path(self.tmp.name) / 'runtime.zip'
        self.bridge = b'tested bridge fixture'
        self.record = {
            'schema': 1, 'configuration': 'Release',
            'executable': 'airplay-gstreamer-bridge.exe',
            'executableSha256': hashlib.sha256(self.bridge).hexdigest(),
            'videoContract': {'id': 'rtsp-h264-single-slice-v1', 'slicesPerFrame': 1,
                              'testName': 'airplay_source_clock_single_slice', 'testPassed': True},
        }

    def write(self, proof=True, duplicate=False):
        with zipfile.ZipFile(self.archive, 'w') as archive:
            archive.writestr(self.module.BRIDGE, self.bridge)
            if proof:
                archive.writestr(self.module.PROOF, json.dumps(self.record))
            if duplicate:
                archive.writestr(self.module.BRIDGE, b'other')

    def test_accepts_matching_tested_archive(self):
        self.write()
        self.module.verify(self.archive)

    def test_rejects_missing_or_old_contract(self):
        self.write(proof=False)
        with self.assertRaises(ValueError): self.module.verify(self.archive)
        del self.record['videoContract']
        self.write()
        with self.assertRaises(ValueError): self.module.verify(self.archive)

    def test_rejects_changed_executable(self):
        self.bridge = b'old bridge'
        self.write()
        with self.assertRaisesRegex(ValueError, 'hash'): self.module.verify(self.archive)

    def test_rejects_wrong_or_unpassed_test(self):
        for key, value in [('id', 'old'), ('slicesPerFrame', 8), ('slicesPerFrame', True),
                           ('testPassed', False), ('testPassed', 'true'), ('testName', 'other')]:
            with self.subTest(key=key, value=value):
                original = self.record['videoContract'][key]
                self.record['videoContract'][key] = value
                self.write()
                with self.assertRaises(ValueError): self.module.verify(self.archive)
                self.record['videoContract'][key] = original

    def test_rejects_duplicate_bridge(self):
        with warnings.catch_warnings():
            warnings.simplefilter('ignore', UserWarning)
            self.write(duplicate=True)
        with self.assertRaises(ValueError): self.module.verify(self.archive)


if __name__ == '__main__':
    unittest.main()
