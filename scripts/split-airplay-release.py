"""Split a verified complete runtime into EXE payload and same-release sources."""
import argparse
import importlib.util
import json
from pathlib import Path
import re
import zipfile
import hashlib


def gate(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def encode(value): return (json.dumps(value, ensure_ascii=False, indent=2) + '\n').encode()
def sha(data): return hashlib.sha256(data).hexdigest()


def split(runtime, output, tag, repository):
    if not re.fullmatch(r'v[A-Za-z0-9_.-]+', tag) or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository):
        raise ValueError('invalid release identity')
    license_gate = gate('verify-airplay-license-archive')
    license_gate.verify(runtime)
    gate('verify-airplay-h264-archive').verify(runtime)
    output.mkdir(parents=True, exist_ok=True)
    sources = output / 'ImagePadServer-AirPlay-Sources.zip'
    payload = output / 'ImagePadServer-AirPlay-Runtime.zip'
    if sources.exists() or payload.exists():
        raise ValueError('use a new output directory')
    runtime_id = 'single-slice-release-' + tag
    base = f'https://github.com/{repository}/releases/download/{tag}/'
    with zipfile.ZipFile(runtime) as original:
        replacements = {}
        for name, key in [('source-manifest.json', 'runtimeSetID'), ('license-manifest.json', 'runtimeSetID'),
                          ('imagepad-airplay-runtime.json', 'runtimeSetID'),
                          ('imagepad-airplay-source-clock-package.json', 'version'),
                          ('gstreamer/airplay-gstreamer-manifest.json', 'version')]:
            value = json.loads(original.read(name).decode('utf-8-sig'))
            value[key] = runtime_id
            replacements[name] = encode(value)
        replacements['SOURCE-OFFER.md'] = (f'''# AirPlay ライセンスと対応ソース

runtimeSetID: {runtime_id}

通常の利用にはexeだけで動作します。再ビルドは不要です。
ライセンス本文・著作権表示とソースの取得案内はexe内に保持しています。
このバイナリに対応する完全ソース、変更、ビルド用スクリプトは同じリリースの次のZIPから無償で取得できます。

{base}{sources.name}

設定 → アプリ情報 → AirPlayのライセンスと対応ソース からも取得できます。
正確なZIPのSHA-256とサイズは source-distribution.json、対応一覧は source-manifest.json、再ビルド手順は sources/BUILD.md を参照してください。
ImagePadServer本体はMIT、第三者ランタイムは各コンポーネントのGPL/LGPL等の許諾条件が適用されます。
''').encode()
        shared = {'source-manifest.json', 'license-manifest.json', 'SOURCE-OFFER.md'}
        with zipfile.ZipFile(sources, 'w', zipfile.ZIP_DEFLATED, compresslevel=6) as dest:
            for name in sorted(original.namelist()):
                if name.endswith('/') or not (name in shared or name.startswith(('sources/', 'licenses/', 'gstreamer/share/licenses/'))):
                    continue
                data = replacements.get(name)
                if data is None: data = original.read(name)
                compression = zipfile.ZIP_STORED if name.endswith(('.gz', '.xz', '.bz2', '.zip', '.zst')) else zipfile.ZIP_DEFLATED
                dest.writestr(name, data, compress_type=compression)
        source_asset = {'assetName': sources.name, 'url': base + sources.name,
                        'sha256': license_gate.file_sha256(sources), 'size': sources.stat().st_size}
        replacements['source-distribution.json'] = encode({'schema':1, 'runtimeSetID':runtime_id,
                                                          'repository':repository, 'releaseTag':tag, 'sourceArchive':source_asset})
        outer = 'imagepad-airplay-source-clock-manifest.json'
        files = []
        with zipfile.ZipFile(payload, 'w', zipfile.ZIP_DEFLATED, compresslevel=6) as dest:
            for name in sorted(set(original.namelist()) | set(replacements)):
                if name.endswith('/') or name == outer or (name.startswith('sources/') and not name.endswith(('.md', '.json'))):
                    continue
                data = replacements.get(name)
                if data is None: data = original.read(name)
                dest.writestr(name, data)
                files.append({'path':name, 'size':len(data), 'sha256':sha(data)})
            dest.writestr(outer, encode({'schema':1, 'version':runtime_id, 'architecture':'windows-amd64', 'files':files}))
    evidence = license_gate.verify(payload, sources)
    gate('verify-airplay-h264-archive').verify(payload)
    bootstrap = {'schema':1, 'runtimeSetID':runtime_id, 'archiveUrl':base + payload.name,
                 'archiveSize':payload.stat().st_size, 'archiveSha256':license_gate.file_sha256(payload),
                 'licenseManifestSha256':sha(replacements['license-manifest.json']),
                 'sourceOfferSha256':sha(replacements['SOURCE-OFFER.md'])}
    manifest = {'schema':1, 'repository':repository, 'releaseTag':tag, 'runtimeAssetName':payload.name,
                'bootstrap':bootstrap, 'sourceArchive':source_asset}
    (output / 'runtime-bootstrap.json').write_bytes(encode(bootstrap))
    (output / 'release-inputs.json').write_bytes(encode(manifest))
    print(json.dumps({'runtimeBytes':payload.stat().st_size, 'sourceBytes':sources.stat().st_size, **evidence}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--repository', required=True)
    args = parser.parse_args()
    split(args.runtime, args.output, args.tag, args.repository)
