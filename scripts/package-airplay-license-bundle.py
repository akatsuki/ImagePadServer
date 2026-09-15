"""Add a reviewed offline source bundle to a runtime without changing binaries.

Inputs: --runtime, --bundle (source-manifest.json plus its referenced files),
--output directory, --runtime-set-id. Does not stage files, build, or publish.
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path, PurePosixPath
import zipfile

def sha(data): return hashlib.sha256(data).hexdigest()
def encode(value): return (json.dumps(value, ensure_ascii=False, indent=2)+'\n').encode()
def load_gate(name):
    spec=importlib.util.spec_from_file_location(name,Path(__file__).with_name(name+'.py'))
    module=importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
    return module

def package(runtime, bundle, output, runtime_set_id):
    if not runtime_set_id or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._' for c in runtime_set_id):
        raise ValueError('invalid runtime set ID')
    output.mkdir(parents=True,exist_ok=True)
    target=output/'runtime.zip'
    if target.resolve()==runtime.resolve() or target.exists():
        raise ValueError('output archive must be a new path')
    record=json.loads((bundle/'source-manifest.json').read_text(encoding='utf-8'))
    record['runtimeSetID']=runtime_set_id
    replacements={}
    for file in record['files']:
        name=file['path']; p=PurePosixPath(name)
        if p.is_absolute() or '..' in p.parts or ':' in name or '\\' in name:
            raise ValueError('unsafe source bundle path')
        if not name.startswith(('sources/','licenses/','gstreamer/share/licenses/')):
            raise ValueError('source bundle cannot replace runtime files')
        f=bundle/name
        if f.is_file():
            if f.stat().st_size!=file['size'] or sha(f.read_bytes())!=file['sha256']:
                raise ValueError('source bundle hash changed: '+name)
            replacements[name]=f
    offer=(bundle/'SOURCE-OFFER.md').read_bytes()
    metadata={'source-manifest.json':encode(record),'SOURCE-OFFER.md':offer}
    with zipfile.ZipFile(runtime) as original:
        load_gate('verify-airplay-h264-archive').verify(runtime)
        license_manifest={'schema':1,'runtimeSetID':runtime_set_id,'sourceManifest':'source-manifest.json',
                          'components':record['components']}
        metadata['license-manifest.json']=encode(license_manifest)
        for name,key in [('imagepad-airplay-runtime.json','runtimeSetID'),
                         ('imagepad-airplay-source-clock-package.json','version'),
                         ('gstreamer/airplay-gstreamer-manifest.json','version')]:
            value=json.loads(original.read(name).decode('utf-8-sig')); value[key]=runtime_set_id
            metadata[name]=encode(value)
        if set(replacements)&{n for n in original.namelist() if n.endswith(('.exe','.dll','.pyd'))}:
            raise ValueError('binary replacement forbidden')
        files=[]; outer='imagepad-airplay-source-clock-manifest.json'
        with zipfile.ZipFile(target,'w',zipfile.ZIP_DEFLATED,compresslevel=6) as dest:
            for name in sorted(set(original.namelist())|set(replacements)|set(metadata)):
                if name.endswith('/') or name==outer: continue
                data=metadata.get(name)
                if data is None: data=replacements[name].read_bytes() if name in replacements else original.read(name)
                if len(data)>128*1024*1024: raise ValueError('runtime file limit exceeded: '+name)
                compression=zipfile.ZIP_STORED if name.startswith('sources/') and name.endswith(('.xz','.gz','.tgz','.bz2','.zip','.zst')) else zipfile.ZIP_DEFLATED
                dest.writestr(name,data,compress_type=compression)
                files.append({'path':name,'size':len(data),'sha256':sha(data)})
            if sum(f['size'] for f in files)>2*1024**3: raise ValueError('runtime extraction limit exceeded')
            dest.writestr(outer,encode({'schema':1,'version':runtime_set_id,'architecture':'windows-amd64','files':files}))
    load_gate('verify-airplay-h264-archive').verify(target)
    evidence=load_gate('verify-airplay-license-archive').verify(target)
    digest=hashlib.sha256()
    with target.open('rb') as f:
        for block in iter(lambda:f.read(1024*1024),b''): digest.update(block)
    bootstrap={'schema':1,'runtimeSetID':runtime_set_id,'archiveUrl':'https://example.invalid/embedded-airplay-runtime.zip',
               'archiveSha256':digest.hexdigest(),'archiveSize':target.stat().st_size,
               'licenseManifestSha256':sha(metadata['license-manifest.json']),'sourceOfferSha256':sha(offer)}
    (output/'runtime-bootstrap.json').write_bytes(encode(bootstrap))
    print(json.dumps({'runtimeSetID':runtime_set_id,'archiveBytes':target.stat().st_size,**evidence}))

if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    for key in ['runtime','bundle','output']: parser.add_argument('--'+key,type=Path,required=True)
    parser.add_argument('--runtime-set-id',required=True)
    args=parser.parse_args()
    package(args.runtime,args.bundle,args.output,args.runtime_set_id)
