"""Validate offline source/notice inventory against the exact runtime ZIP.

This checks packaging evidence, not the legal sufficiency of a source mapping.
Mapping review remains necessary when changing a dependency or its build.
"""
import hashlib
import json
from contextlib import ExitStack
from pathlib import Path, PurePosixPath
import re
import sys
import zipfile


def file_sha256(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as source:
        for block in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def validate_names(archive):
    names = archive.namelist()
    if len(names) != len(set(names)):
        raise ValueError('duplicate ZIP entry')
    for name in names:
        p = PurePosixPath(name)
        if p.is_absolute() or '..' in p.parts or '\\' in name or ':' in name:
            raise ValueError('unsafe ZIP entry: ' + name)


class ReleaseArchives:
    """One logical inventory, with identical metadata shared by both assets."""
    def __init__(self, runtime, sources):
        self.entries = {}
        for archive in (runtime, sources):
            validate_names(archive)
            for name in archive.namelist():
                if name in self.entries:
                    old = self.entries[name]
                    if old.getinfo(name).file_size != archive.getinfo(name).file_size or old.read(name) != archive.read(name):
                        raise ValueError('release asset disagreement: ' + name)
                self.entries[name] = archive

    def namelist(self): return list(self.entries)
    def read(self, name): return self.entries[name].read(name)
    def open(self, name): return self.entries[name].open(name)
    def getinfo(self, name): return self.entries[name].getinfo(name)


def verify(path, sources_path=None):
    with ExitStack() as stack:
        archive = stack.enter_context(zipfile.ZipFile(path))
        validate_names(archive)
        distribution = None
        if 'source-distribution.json' in archive.namelist():
            if archive.getinfo('source-distribution.json').file_size > 16384:
                raise ValueError('oversized source distribution metadata')
            distribution = json.loads(archive.read('source-distribution.json'))
            asset = distribution['sourceArchive']
            repository, tag, name = distribution['repository'], distribution['releaseTag'], asset['assetName']
            if (distribution.get('schema') != 1 or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository)
                    or not re.fullmatch(r'v[A-Za-z0-9_.-]+', tag) or not re.fullmatch(r'[A-Za-z0-9_.-]+\.zip', name)
                    or asset['url'] != f'https://github.com/{repository}/releases/download/{tag}/{name}'):
                raise ValueError('invalid same-release source URL')
            if not sources_path:
                raise ValueError('corresponding source archive is required')
            if Path(sources_path).stat().st_size != asset['size'] or file_sha256(sources_path) != asset['sha256']:
                raise ValueError('corresponding source archive size/hash mismatch')
            sources = stack.enter_context(zipfile.ZipFile(sources_path))
            if any(n.lower().endswith(('.exe', '.dll', '.pyd')) for n in sources.namelist()):
                raise ValueError('source asset must not supply runtime binaries')
            for metadata in ('source-manifest.json', 'license-manifest.json', 'SOURCE-OFFER.md'):
                if metadata not in sources.namelist() or metadata not in archive.namelist():
                    raise ValueError('missing shared release metadata: ' + metadata)
            archive = ReleaseArchives(archive, sources)
        names = archive.namelist()
        if len(names) != len(set(names)):
            raise ValueError('duplicate ZIP entry')
        for name in names:
            p = PurePosixPath(name)
            if p.is_absolute() or '..' in p.parts or '\\' in name or ':' in name:
                raise ValueError('unsafe ZIP entry: ' + name)
        def read_json(name):
            if name not in names or archive.getinfo(name).file_size > 8 * 1024 * 1024:
                raise ValueError('missing or oversized metadata: ' + name)
            return json.loads(archive.read(name).decode('utf-8-sig'))
        record = read_json('source-manifest.json')
        if record.get('schema') != 1 or record.get('unresolved'):
            raise ValueError('source inventory is unresolved or unsupported')
        runtime = read_json('imagepad-airplay-runtime.json')
        if distribution and distribution.get('runtimeSetID') != runtime.get('runtimeSetID'):
            raise ValueError('source distribution runtime version mismatch')
        licenses = read_json('license-manifest.json')
        if not record.get('runtimeSetID') or record['runtimeSetID'] != runtime.get('runtimeSetID') or record['runtimeSetID'] != licenses.get('runtimeSetID'):
            raise ValueError('source/license/runtime version mismatch')
        if 'SOURCE-OFFER.md' not in names or archive.getinfo('SOURCE-OFFER.md').file_size == 0:
            raise ValueError('missing offline source access instructions')
        checked = {}
        for entry in record.get('files', []):
            name = entry['path']
            if name in checked or name not in names:
                raise ValueError('missing or duplicate source/notice: ' + name)
            if entry.get('size', 0) <= 0 or archive.getinfo(name).file_size != entry['size']:
                raise ValueError('source/notice size mismatch: ' + name)
            digest = hashlib.sha256()
            with archive.open(name) as source:
                for block in iter(lambda: source.read(1024 * 1024), b''):
                    digest.update(block)
            if digest.hexdigest() != entry['sha256']:
                raise ValueError('source/notice hash mismatch: ' + name)
            checked[name] = entry
        components = {}
        for component in record.get('components', []):
            key = component['id']
            if not key or key in components or not component.get('version'):
                raise ValueError('invalid component identity')
            for field in ('sourceFiles', 'licenseFiles', 'buildFiles'):
                paths = component.get(field)
                if not isinstance(paths, list) or not paths or any(p not in checked for p in paths):
                    raise ValueError('missing ' + field + ': ' + key)
            if not any(p.startswith('sources/') and p.endswith(('.tar.gz', '.tar.xz', '.tar.bz2', '.tgz', '.zip', '.tar.zst')) for p in component['sourceFiles']):
                raise ValueError('full source archive required: ' + key)
            components[key] = component
        if not components:
            raise ValueError('empty component inventory')
        sets = record.get('componentSets', {})
        for key, values in sets.items():
            if not values or any(value not in components for value in values):
                raise ValueError('unknown component set: ' + key)
        binaries = {name for name in names if name.lower().endswith(('.exe', '.dll', '.pyd'))}
        seen = set()
        for binary in record.get('binaries', []):
            name = binary['path']
            if name not in binaries or name in seen or binary.get('componentSet') not in sets:
                raise ValueError('invalid binary mapping: ' + name)
            if hashlib.sha256(archive.read(name)).hexdigest() != binary['sha256']:
                raise ValueError('binary provenance changed: ' + name)
            seen.add(name)
        if seen != binaries:
            raise ValueError('unmapped binaries: ' + ', '.join(sorted(binaries-seen)))
        # The shipped version inventory is independent of the source manifest.
        # A removed component record must not make a new runtime pass silently.
        if 'gstreamer/share/versions.txt' in names:
            versions = dict(line.split(None, 1) for line in archive.read('gstreamer/share/versions.txt').decode().splitlines() if len(line.split(None, 1)) == 2)
            shipped = {n.split('/')[3] for n in names if n.startswith('gstreamer/share/licenses/') and len(n.split('/')) > 4}
            shipped.update({'gst-libav-1.0', 'gst-plugins-good-1.0', 'gst-plugins-ugly-1.0'})
            for name in shipped:
                component = components.get(name)
                if component is None or component['version'] != versions.get(name):
                    raise ValueError('GStreamer source version mismatch: ' + name)
        return {'components': len(components), 'binaries': len(binaries), 'sourceAndNoticeFiles': len(checked)}


if __name__ == '__main__':
    try:
        result = verify(sys.argv[1], sys.argv[2] if len(sys.argv) > 2 else None)
    except (OSError, ValueError, KeyError, TypeError, zipfile.BadZipFile) as error:
        sys.exit('AirPlay license archive rejected: ' + str(error))
    print('AirPlay license archive: PASS ' + json.dumps(result))
