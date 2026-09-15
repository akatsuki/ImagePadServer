"""Fetch pinned draft-release inputs, validate the pair, and stage the EXE payload."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import zipfile
import hashlib


def gate(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + '.py'))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def prepare(manifest_path, inputs, download=False, env_file=None):
    root = Path(__file__).resolve().parents[1]
    m = json.loads(manifest_path.read_text(encoding='utf-8'))
    repository, tag = m['repository'], m['releaseTag']
    if m['schema'] != 1 or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository) or not re.fullmatch(r'v[A-Za-z0-9_.-]+', tag):
        raise ValueError('invalid release manifest identity')
    version = re.search(r'Version\s*=\s*"([^"]+)"', (root/'internal/about/about.go').read_text(encoding='utf-8')).group(1)
    if version != tag or (os.getenv('GITHUB_REF_TYPE') == 'tag' and os.environ['GITHUB_REF_NAME'] != tag):
        raise ValueError('release input tag differs from application/workflow tag')
    if os.getenv('GITHUB_REPOSITORY') and os.environ['GITHUB_REPOSITORY'] != repository:
        raise ValueError('release input repository mismatch')
    inputs.mkdir(parents=True, exist_ok=True)
    b, s = m['bootstrap'], m['sourceArchive']
    for name in (m['runtimeAssetName'], s['assetName']):
        if not re.fullmatch(r'[A-Za-z0-9_.-]+\.zip', name): raise ValueError('invalid asset name')
        if download:
            subprocess.run(['gh','release','download',tag,'--repo',repository,'--pattern',name,'--dir',str(inputs),'--clobber'], check=True)
    runtime, sources = inputs/m['runtimeAssetName'], inputs/s['assetName']
    license_gate = gate('verify-airplay-license-archive')
    for file, size, digest in ((runtime,b['archiveSize'],b['archiveSha256']), (sources,s['size'],s['sha256'])):
        if file.stat().st_size != size or license_gate.file_sha256(file) != digest:
            raise ValueError('pinned release input changed: ' + file.name)
    base = f'https://github.com/{repository}/releases/download/{tag}/'
    if b['archiveUrl'] != base+runtime.name or s['url'] != base+sources.name:
        raise ValueError('release inputs must be available on the same release')
    gate('verify-airplay-h264-archive').verify(runtime)
    evidence = license_gate.verify(runtime, sources)
    with zipfile.ZipFile(runtime) as z:
        d = json.loads(z.read('source-distribution.json'))
        if d['sourceArchive'] != s or d['runtimeSetID'] != b['runtimeSetID'] or d['releaseTag'] != tag or d['repository'] != repository:
            raise ValueError('embedded source distribution differs from release pins')
        for name, field in [('license-manifest.json','licenseManifestSha256'), ('SOURCE-OFFER.md','sourceOfferSha256')]:
            if hashlib.sha256(z.read(name)).hexdigest() != b[field]:
                raise ValueError('runtime metadata pin mismatch: ' + name)
    payload = root/'internal/airplay/payload'
    payload.mkdir(parents=True,exist_ok=True)
    shutil.copyfile(runtime,payload/'runtime.zip')
    (payload/'runtime-bootstrap.json').write_text(json.dumps(b,indent=2)+'\n',encoding='utf-8')
    values = {'AIRPLAY_RUNTIME_SET_ID':b['runtimeSetID'], 'AIRPLAY_RUNTIME_ARCHIVE_URL':b['archiveUrl'],
              'AIRPLAY_RUNTIME_ARCHIVE_SHA256':b['archiveSha256'], 'AIRPLAY_RUNTIME_ARCHIVE_SIZE':b['archiveSize'],
              'AIRPLAY_RUNTIME_LICENSE_MANIFEST_SHA256':b['licenseManifestSha256'], 'AIRPLAY_RUNTIME_SOURCE_OFFER_SHA256':b['sourceOfferSha256'],
              'AIRPLAY_RUNTIME_ARCHIVE_PATH':runtime.resolve().as_posix(), 'AIRPLAY_RUNTIME_SOURCES_PATH':sources.resolve().as_posix()}
    if env_file:
        with open(env_file,'a',encoding='utf-8') as f:
            for key,value in values.items(): f.write(f'{key}={value}\n')
    print(json.dumps({'releaseTag':tag, **evidence}))
    return values


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manifest',type=Path,default=Path(__file__).resolve().parents[1]/'release/airplay-inputs.json')
    parser.add_argument('--inputs',type=Path,required=True)
    parser.add_argument('--download',action='store_true')
    parser.add_argument('--env-file',type=Path)
    args=parser.parse_args()
    prepare(args.manifest,args.inputs,args.download,args.env_file)
