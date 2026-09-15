import importlib.util
import json
import pathlib
import tempfile
import unittest
import zipfile
import hashlib

PATH = pathlib.Path(__file__).resolve().parents[1] / 'verify-airplay-license-archive.py'
spec = importlib.util.spec_from_file_location('license_gate', PATH)
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)

class LicenseArchiveTests(unittest.TestCase):
    def fixture(self, change=None, extra=None):
        files = {'licenses/COPYING': b'Full license text', 'sources/source.tar.gz': b'full source',
                 'sources/BUILD.md': b'build instructions', 'gstreamer/test.dll': b'binary',
                 'SOURCE-OFFER.md': b'source access instructions'}
        manifest = {'schema': 1, 'runtimeSetID': 'test', 'components': [{
            'id': 'example', 'version': '1', 'licenseFiles': ['licenses/COPYING'],
            'sourceFiles': ['sources/source.tar.gz'], 'buildFiles': ['sources/BUILD.md']}],
            'componentSets': {'runtime': ['example']},
            'binaries': [{'path': 'gstreamer/test.dll', 'componentSet': 'runtime',
                          'sha256': hashlib.sha256(files['gstreamer/test.dll']).hexdigest()}],
            'files': [{'path': name, 'size': len(data), 'sha256': hashlib.sha256(data).hexdigest()}
                      for name, data in files.items() if name.startswith(('sources/', 'licenses/'))]}
        if change: change(manifest, files)
        if extra: files.update(extra)
        files['source-manifest.json'] = json.dumps(manifest).encode()
        files['license-manifest.json'] = json.dumps({'schema':1, 'runtimeSetID':'test'}).encode()
        files['imagepad-airplay-runtime.json'] = json.dumps({'runtimeSetID':'test'}).encode()
        temp = tempfile.NamedTemporaryFile(suffix='.zip', delete=False); temp.close()
        self.addCleanup(pathlib.Path(temp.name).unlink)
        with zipfile.ZipFile(temp.name, 'w') as z:
            for name, data in files.items(): z.writestr(name, data)
        return temp.name

    def test_complete_inventory_passes(self): gate.verify(self.fixture())
    def test_missing_source_fails(self):
        with self.assertRaises(ValueError): gate.verify(self.fixture(lambda m,f:f.pop('sources/source.tar.gz')))
    def test_tampered_source_fails(self):
        with self.assertRaises(ValueError): gate.verify(self.fixture(lambda m,f:f.update({'sources/source.tar.gz':b'wrong'})))
    def test_unmapped_binary_fails(self):
        with self.assertRaises(ValueError): gate.verify(self.fixture(extra={'gstreamer/new.dll':b'new'}))
    def test_binary_hash_change_fails(self):
        with self.assertRaises(ValueError): gate.verify(self.fixture(lambda m,f:f.update({'gstreamer/test.dll':b'changed'})))
    def test_empty_source_fails(self):
        with self.assertRaises(ValueError): gate.verify(self.fixture(lambda m,f:m['components'][0].update(sourceFiles=[])))
    def test_runtime_version_mismatch_fails(self):
        with self.assertRaises(ValueError): gate.verify(self.fixture(lambda m,f:m.update(runtimeSetID='other')))
    def test_unresolved_source_fails(self):
        with self.assertRaises(ValueError): gate.verify(self.fixture(lambda m,f:m.update(unresolved=['missing dependency'])))
    def test_traversal_fails(self):
        with self.assertRaises(ValueError): gate.verify(self.fixture(extra={'sources/../escape':b'bad'}))

    def pair(self, change=None):
        runtime = pathlib.Path(self.fixture())
        source = runtime.with_name(runtime.stem + '-sources.zip')
        self.addCleanup(source.unlink)
        with zipfile.ZipFile(runtime) as z:
            files = {n:z.read(n) for n in z.namelist()}
        with zipfile.ZipFile(source, 'w') as z:
            for n, data in files.items():
                if n.startswith(('sources/', 'licenses/')) or n in ('source-manifest.json', 'license-manifest.json', 'SOURCE-OFFER.md'):
                    z.writestr(n, data)
        descriptor = {'schema':1, 'runtimeSetID':'test', 'releaseTag':'v1', 'repository':'owner/repo',
                      'sourceArchive':{'assetName':source.name, 'url':'https://github.com/owner/repo/releases/download/v1/'+source.name,
                                       'sha256':hashlib.sha256(source.read_bytes()).hexdigest(), 'size':source.stat().st_size}}
        files = {n:d for n,d in files.items() if not n.startswith('sources/')}
        files['source-distribution.json'] = json.dumps(descriptor).encode()
        if change: change(files, descriptor)
        with zipfile.ZipFile(runtime, 'w') as z:
            for n,d in files.items(): z.writestr(n,d)
        return runtime, source

    def test_external_pair_passes(self): gate.verify(*self.pair())
    def test_external_sources_required(self):
        with self.assertRaises(ValueError): gate.verify(self.pair()[0])
    def test_external_source_tampering_fails(self):
        runtime, source = self.pair()
        with source.open('ab') as f: f.write(b'changed')
        with self.assertRaises(ValueError): gate.verify(runtime, source)
    def test_external_version_mismatch_fails(self):
        def change(files, descriptor):
            descriptor['runtimeSetID'] = 'other'
            files['source-distribution.json'] = json.dumps(descriptor).encode()
        with self.assertRaises(ValueError): gate.verify(*self.pair(change))
    def test_external_manifest_disagreement_fails(self):
        def change(files, descriptor):
            m = json.loads(files['source-manifest.json']); m['components'][0]['version'] = '2'
            files['source-manifest.json'] = json.dumps(m).encode()
        with self.assertRaises(ValueError): gate.verify(*self.pair(change))
    def test_external_source_link_must_match_release(self):
        def change(files, descriptor):
            descriptor['sourceArchive']['url'] = 'https://example.com/wrong.zip'
            files['source-distribution.json'] = json.dumps(descriptor).encode()
        with self.assertRaises(ValueError): gate.verify(*self.pair(change))

if __name__ == '__main__': unittest.main()
