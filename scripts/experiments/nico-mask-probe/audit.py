"""Inspect mask fixtures and compare original browser output with CPU reference."""
import argparse, json, pathlib, struct
import numpy as np
from PIL import Image, ImageFilter
BASE=pathlib.Path('build/nico-mask-probe')

def scene(path):
    data=path.read_bytes();header=struct.unpack_from('<7I',data);pos=28;textures=[]
    for i in range(header[5]):
        t=struct.unpack_from('<8I',data,pos);pos+=32;payload=data[pos:pos+t[7]];pos+=t[7]
        textures.append(dict(id=t[0],width=t[1],height=t[2],kind=t[3],fillRGBA=t[4],outlineRGBA=t[5],outlineRadius=t[6],payload_B=t[7],nonzero=int(np.count_nonzero(np.frombuffer(payload,np.uint8)))))
    commands=[];command_bytes=data[pos:]
    for _ in range(header[6]):
        n=struct.unpack_from('<I',data,pos)[0];pos+=4+n*100;commands.append(n)
    assert pos==len(data)
    return dict(width=header[1],height=header[2],fps=f'{header[3]}/{header[4]}',frames=header[6],textures=textures,commands_min=min(commands),commands_max=max(commands),commands_total=sum(commands)),command_bytes

def compare(a,b):
    bg=np.array([32,48,64,255],np.uint8);roi=np.any(a!=bg,2)|np.any(b!=bg,2)
    roi=np.array(Image.fromarray(roi.astype('uint8')*255).filter(ImageFilter.MaxFilter(7)))>0
    d=np.abs(a[:,:,:3].astype(np.int16)-b[:,:,:3].astype(np.int16));v=d[roi]
    return dict(roi_pixels=int(roi.sum()),changed_pixels=int(np.any(d>0,2)[roi].sum()),max_rgb=int(v.max()) if v.size else 0,mean_abs_rgb=float(v.mean()) if v.size else 0)

def main():
    p=argparse.ArgumentParser();p.add_argument('--fixture',required=True);p.add_argument('--reference-run');p.add_argument('--mask-run');a=p.parse_args();stem=BASE/'fixtures'/a.fixture
    info,commands=scene(stem.with_suffix('.mask.nmf1'));ref,refcommands=scene(stem.with_suffix('.reference.nmf1'));assert commands==refcommands
    info['mask_reference_commands_identical']=True;info['rgba_texture_B']=sum(t['payload_B'] for t in ref['textures']);info['fallback_textures']=sum(t['kind']==0 for t in info['textures'])
    for t in info['textures']:
        if t['kind']==1:assert t['nonzero']>0
    if a.reference_run:
        metrics=[]
        for source in (BASE/'fixtures').glob(a.fixture+'.browser.frame-*.rgba'):
            frame=int(source.stem.split('-')[-1]);shape=(info['height'],info['width'],4)
            browser=np.fromfile(source,np.uint8).reshape(shape);rgb=browser[:,:,:3].astype(float)+np.array([32,48,64])*(1-browser[:,:,3:4]/255)
            opaque=np.concatenate((np.clip(np.floor(rgb+.5),0,255).astype(np.uint8),np.full(shape[:2]+(1,),255,np.uint8)),2)
            refpath=BASE/'results'/a.reference_run/'pixels'/f'frame-{frame}.rgba'
            if not refpath.exists():continue
            reference=np.fromfile(refpath,np.uint8).reshape(shape)
            entry=dict(frame=frame,browser_vs_cpu_rgba=compare(opaque,reference))
            if a.mask_run:
                mask=np.fromfile(BASE/'results'/a.mask_run/'pixels'/f'frame-{frame}.rgba',np.uint8).reshape(shape)
                entry['browser_vs_reconstructed_A8']=compare(opaque,mask)
                if frame==180:
                    # Crop a fixed representative text region; export full resolution too.
                    Image.fromarray(opaque).save(BASE/'results'/a.reference_run/'browser-180.png')
                    Image.fromarray(reference).save(BASE/'results'/a.reference_run/'reference-180.png')
                    Image.fromarray(mask).save(BASE/'results'/a.reference_run/'reconstructed-A8-180.png')
            metrics.append(entry)
        info['quality']=metrics
    dest=BASE/'results'/f'{a.fixture}-fixture-audit.json';dest.write_text(json.dumps(info,indent=2),encoding='utf8');print(json.dumps(info),flush=True)
if __name__=='__main__':main()
