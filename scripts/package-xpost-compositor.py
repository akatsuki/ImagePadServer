"""Stage and verify a target-specific X compositor with its locked license bundle."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
from nico_timeline_manifest import write_dependency_bundle

ROOT = Path(__file__).resolve().parents[1]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_hash():
    root = ROOT / 'gpu/xpost-compositord'
    files = [root / 'Cargo.toml', root / 'Cargo.lock', *sorted(p for p in (root / 'src').rglob('*') if p.is_file())]
    h = hashlib.sha256()
    for path in files:
        h.update(path.relative_to(root).as_posix().encode() + b'\0' + path.read_bytes().replace(b'\r\n', b'\n'))
    return h.hexdigest()


def verify(directory, target):
    m = json.loads((directory / 'manifest.json').read_text())
    if m['schema'] != 1 or m['target'] != target or m['sourceSHA256'] != source_hash():
        raise ValueError('X compositor target/source identity mismatch')
    expected = 'xpost-compositord.exe' if 'windows' in target else 'xpost-compositord'
    if m['binary'] != expected:
        raise ValueError('X compositor executable name mismatch')
    actual_files = {p.relative_to(directory).as_posix() for p in directory.rglob('*') if p.is_file() and p.name != 'manifest.json'}
    if actual_files != set(m['files']):
        raise ValueError('X compositor payload inventory mismatch')
    for name, sha in m['files'].items():
        path = directory / name
        if not path.is_file() or not path.resolve().is_relative_to(directory.resolve()) or digest(path) != sha:
            raise ValueError('X compositor payload changed: ' + name)
    for required in (expected, 'THIRD_PARTY_NOTICES.md', 'Cargo.lock', 'Cargo.toml'):
        if required not in m['files']:
            raise ValueError('X compositor payload missing ' + required)
    if not any(name.startswith('third-party-licenses/wgpu-') for name in m['files']):
        raise ValueError('X compositor wgpu license texts missing')
    print(json.dumps({'target': target, 'binarySHA256': m['files'][expected], 'files': len(m['files'])}))


def stage(binary, directory, target):
    directory.mkdir(parents=True, exist_ok=True)
    name = 'xpost-compositord.exe' if 'windows' in target else 'xpost-compositord'
    shutil.copyfile(binary, directory / name)
    (directory / name).chmod(0o755)
    write_dependency_bundle(ROOT / 'gpu/xpost-compositord/Cargo.toml', target, directory)
    notice = directory / 'THIRD_PARTY_NOTICES.md'
    notice.write_text(notice.read_text(encoding='utf-8').replace('This NCT1 compositor', 'This X-post compositor'), encoding='utf-8')
    files = {p.relative_to(directory).as_posix(): digest(p) for p in sorted(directory.rglob('*')) if p.is_file() and p.name != 'manifest.json'}
    (directory / 'manifest.json').write_text(json.dumps({'schema': 1, 'target': target, 'sourceSHA256': source_hash(), 'binary': name, 'files': files}, indent=2) + '\n')
    verify(directory, target)


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--binary', type=Path)
    p.add_argument('--directory', type=Path, required=True)
    p.add_argument('--target', required=True)
    args = p.parse_args()
    if args.binary:
        stage(args.binary, args.directory, args.target)
    else:
        verify(args.directory, args.target)
