"""Serial experiments. Never launches or changes ImagePadServer."""
import argparse, csv, hashlib, json, os, pathlib, statistics, subprocess, time

ROOT = pathlib.Path(__file__).resolve().parents[3]
BASE = ROOT / 'build/nico-mask-probe'
SDK = BASE / 'deps/ffmpeg-9.0.1-full_build-shared'
ENV = dict(os.environ, PATH=str(SDK/'bin') + os.pathsep + 'C:/msys64/ucrt64/bin' + os.pathsep + os.environ['PATH'])

def relative(p):
    return str(pathlib.Path(p).relative_to(ROOT))

def sha(p):
    h=hashlib.sha256()
    with open(p,'rb') as f:
        for b in iter(lambda:f.read(1<<20),b''):h.update(b)
    return h.hexdigest()

def invoke(scene,bits,mode,run_id,frames=0,quality=False,extra=(),expect=0):
    dest=BASE/'results'/run_id
    dest.mkdir(parents=True,exist_ok=True)
    cmd=[relative(BASE/'mask-probe.exe'),relative(scene),str(bits),mode,relative(dest/'out.mp4')]
    if frames:cmd+=['--frames',str(frames)]
    if quality:cmd+=['--dump',relative(dest/'pixels')]
    cmd+=list(extra)
    (dest/'config.json').write_text(json.dumps({'argv':cmd,'scene_sha256':sha(scene),'exe_sha256':sha(BASE/'mask-probe.exe'),'quality_run':quality},indent=2),encoding='utf8')
    start=time.monotonic()
    p=subprocess.run(cmd,cwd=ROOT,env=ENV,capture_output=True,text=True,timeout=900)
    elapsed=time.monotonic()-start
    (dest/'stderr.log').write_text(p.stderr,encoding='utf8')
    (dest/'stdout.log').write_text(p.stdout,encoding='utf8')
    if p.returncode!=expect:raise RuntimeError(f'{run_id}: exit {p.returncode}: {p.stderr[-1800:]}')
    if not p.stdout.strip():return {'run_id':run_id,'exit':p.returncode}
    row=json.loads(p.stdout.strip().splitlines()[-1]);row.update(run_id=run_id,process_wall_s=elapsed,fixture=scene.stem,backend='CPU',boundary='frame_handoff',cold_warm='cold-renderer/cached-input',quality_status='separate run' if not quality else 'pending',errors=0,htod_B=None,dtoh_B=None,dtod_B=None)
    (dest/'result.json').write_text(json.dumps(row,indent=2),encoding='utf8')
    print(json.dumps({k:row[k] for k in ('run_id','wall_s','compose_ms','copy_ms','cpu_copy_B','packed_mask_B','cache_B')}),flush=True)
    return row

def save(rows,name):
    dest=BASE/'results';dest.mkdir(exist_ok=True)
    (dest/f'{name}.json').write_text(json.dumps(rows,indent=2),encoding='utf8')
    with (dest/f'{name}.csv').open('w',newline='',encoding='utf8') as f:
        w=csv.DictWriter(f,fieldnames=list(rows[0]));w.writeheader();w.writerows(rows)

def bench(args):
    scene=ROOT/args.scene
    conditions=[(8,'C'),(2,'C'),(8,'D'),(2,'D')]
    rows=[]
    for i in range(args.repeats):
        order=conditions[i%4:]+conditions[:i%4]
        if i%2:order=order[::-1]
        for bits,mode in order:
            rows.append(invoke(scene,bits,mode,f'{args.name}-R{bits}-{mode}-{i+1}',args.frames))
            save(rows,args.name)
    for bits,mode in conditions:
        group=[r for r in rows if r['bits']==bits and r['mode']==mode]
        print(f'R{bits}-{mode}: median={statistics.median(r["wall_s"] for r in group):.4f}s range={min(r["wall_s"] for r in group):.4f}..{max(r["wall_s"] for r in group):.4f}',flush=True)

def quality(args):
    import numpy as np
    from PIL import Image, ImageFilter
    scene=ROOT/args.scene
    header=np.fromfile(scene,dtype='<u4',count=7);w,h=int(header[1]),int(header[2])
    rows=[]
    for bits,mode in [(8,'C'),(8,'D'),(2,'C'),(2,'D'),(4,'D'),(1,'D')]:rows.append(invoke(scene,bits,mode,f'{args.name}-R{bits}-{mode}',args.frames,True))
    for bits in (8,2):
        pair=[r for r in rows if r['bits']==bits]
        assert pair[0]['raw_md5']==pair[1]['raw_md5'],f'R{bits} C/D raw difference'
        assert sha(BASE/'results'/pair[0]['run_id']/'out.mp4')==sha(BASE/'results'/pair[1]['run_id']/'out.mp4'),f'R{bits} encoded difference'
    reference=BASE/'results'/f'{args.name}-R8-D'/'pixels'
    diffs=[]
    for ref in reference.glob('*.rgba'):
        a=np.fromfile(ref,np.uint8).reshape(h,w,4);bg=np.array([32,48,64,255],np.uint8)
        for bits in (1,2,4):
            candidate=BASE/'results'/f'{args.name}-R{bits}-D'/'pixels'/ref.name
            b=np.fromfile(candidate,np.uint8).reshape(h,w,4)
            roi=np.any(a!=bg,axis=2)|np.any(b!=bg,axis=2)
            roi=np.array(Image.fromarray(roi.astype('uint8')*255).filter(ImageFilter.MaxFilter(7)))>0
            d=np.abs(a[:,:,:3].astype(np.int16)-b[:,:,:3].astype(np.int16));v=d[roi]
            diffs.append(dict(frame=ref.stem,bits=bits,roi_pixels=int(roi.sum()),changed_pixels=int(np.any(d>0,axis=2)[roi].sum()),max_rgb=int(v.max()) if v.size else 0,mean_abs_rgb=float(v.mean()) if v.size else 0,mse_rgb=float((v.astype(float)**2).mean()) if v.size else 0))
            if ref.stem=='frame-180':
                Image.fromarray(a).save(reference/'A8.png')
                Image.fromarray(b).save(candidate.parent/f'A{bits}.png')
                Image.fromarray(np.minimum(d*4,255).astype('uint8')).save(candidate.parent/f'A{bits}-diff4.png')
    refhash=next(row['raw_md5'] for row in rows if row['bits']==8 and row['mode']=='D')
    for row in rows:
        row['quality_status']='identical to A8' if row['raw_md5']==refhash else 'approximate'
        (BASE/'results'/row['run_id']/'result.json').write_text(json.dumps(row,indent=2),encoding='utf8')
    save(rows,args.name);save(diffs,args.name+'-roi')
    print('C/D: full-frame MD5 and encoded MP4 SHA256 identical for A8 and A2',flush=True)

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('mode',choices=['bench','quality']);p.add_argument('--scene',required=True);p.add_argument('--name',required=True);p.add_argument('--repeats',type=int,default=5);p.add_argument('--frames',type=int,default=0);a=p.parse_args()
    (bench if a.mode=='bench' else quality)(a)
