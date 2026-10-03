"""Check actual release ZIPs contain executable X compositor and dependency notices."""
import argparse
import hashlib
import json
from pathlib import Path
import struct
import zipfile
import importlib.util

spec = importlib.util.spec_from_file_location('xpost_package', Path(__file__).with_name('package-xpost-compositor.py'))
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


def architecture(data, target):
    if 'windows' in target:
        offset = struct.unpack_from('<I', data, 0x3c)[0]
        if data[:2] != b'MZ' or data[offset:offset+4] != b'PE\0\0' or struct.unpack_from('<H', data, offset+4)[0] != 0x8664:
            raise ValueError('helper is not Windows amd64 PE')
    elif 'linux' in target:
        expected = 183 if target.startswith('aarch64') else 62
        if data[:6] != b'\x7fELF\x02\x01' or struct.unpack_from('<H', data, 18)[0] != expected:
            raise ValueError('helper ELF architecture mismatch')
    elif target == 'universal-apple-darwin':
        if data[:4] != b'\xca\xfe\xba\xbe':
            raise ValueError('helper is not a macOS Universal binary')
        count = struct.unpack_from('>I', data, 4)[0]
        cpus = {struct.unpack_from('>I', data, 8+i*20)[0] for i in range(count)}
        if cpus != {0x01000007, 0x0100000c}:
            raise ValueError('macOS helper does not include both architectures')


def verify(path, target, version):
    with zipfile.ZipFile(path) as z:
        names = set(z.namelist())
        if target == 'universal-apple-darwin':
            main = 'ImagePadServer.app/Contents/MacOS/ImagePadServer'
            prefix = 'ImagePadServer.app/Contents/MacOS/xpost-compositord/'
            helper = prefix + 'xpost-compositord'
            manifests = [(prefix + 'targets/' + t + '/', t) for t in ('x86_64-apple-darwin', 'aarch64-apple-darwin')]
        else:
            osname = 'windows' if 'windows' in target else 'linux'
            arch = 'arm64' if target.startswith('aarch64') else 'amd64'
            main = f'imagepadserver-{version}-{osname}-{arch}' + ('.exe' if osname == 'windows' else '')
            prefix = 'xpost-compositord/'
            helper = prefix + 'xpost-compositord' + ('.exe' if osname == 'windows' else '')
            manifests = [(prefix, target)]
        exe = z.read(main)
        if version.encode() not in exe or b'react-tweet 3.3.1 MIT License' not in exe or b'const videoMedia =' not in exe:
            raise ValueError('application version/embedded licensed video fetcher missing')
        architecture(z.read(helper), target)
        for base, manifest_target in manifests:
            m = json.loads(z.read(base + 'manifest.json'))
            if m['target'] != manifest_target or m['sourceSHA256'] != package.source_hash():
                raise ValueError('helper source or target differs from this release')
            for name, sha in m['files'].items():
                if hashlib.sha256(z.read(base + name)).hexdigest() != sha:
                    raise ValueError('ZIP helper/license payload differs: ' + name)
            if 'THIRD_PARTY_NOTICES.md' not in m['files'] or not any(n.startswith('third-party-licenses/wgpu-') for n in m['files']):
                raise ValueError('locked helper license inventory missing')
        if 'windows' not in target and z.getinfo(helper).external_attr >> 16 & 0o111 == 0:
            raise ValueError('helper executable mode missing')
    print(json.dumps({'archive': path.name, 'target': target, 'version': version, 'sha256': package.digest(path)}))


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('archive', type=Path)
    p.add_argument('--target', required=True)
    p.add_argument('--version', required=True)
    args = p.parse_args()
    verify(args.archive, args.target, args.version)
