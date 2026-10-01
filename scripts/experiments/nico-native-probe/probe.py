"""Isolated native RGBA experiments; never starts/stops ImagePadServer."""
import argparse, hashlib, io, json, os, pathlib, statistics, struct, subprocess, time
import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[3]
BASE = ROOT / 'build/nico-native-probe'
OUTPUT_ROOT = BASE
FIX = BASE / 'fixtures'
EXE = BASE / 'nico-native-probe.exe'
ORIGINAL = ROOT / 'build/nico-compositor/nico-compositor.exe'
FFMPEG = ROOT / 'build/nico-native-integration/tools/ffmpeg.exe'
FFPROBE = FFMPEG.with_name('ffprobe.exe')
SOURCE = pathlib.Path(os.environ['TEMP']) / 'imagepad-nico-perf-20260916/source.mp4'
CMD = struct.Struct('<I24f')
FILTER_THREADS = 2
DECODER_THREADS = 2
ENCODER_THREADS = 8
FLAGS = {'baseline': [], 'instanced': ['--instanced'], 'crop': [], 'hardware': ['--hardware'], 'hardware-instanced': ['--hardware','--instanced']}

def sha(path):
    with open(path,'rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()

def save(path, value):
    path.parent.mkdir(parents=True,exist_ok=True)
    path.write_text(json.dumps(value,ensure_ascii=False,indent=2),encoding='utf8')

def read_nmf(path):
    f=io.BytesIO(path.read_bytes())
    magic,w,h,num,den,nt,nf=struct.unpack('<7I',f.read(28))
    assert magic==0x31464d4e and 0<nf<=1000000 and nt<=10000
    textures=[]
    for _ in range(nt):
        ident,tw,th,kind,fill,outline,radius,n=struct.unpack('<8I',f.read(32))
        assert kind==0 and n==tw*th*4, 'Only original RGBA reference allowed'
        textures.append((ident,tw,th,f.read(n)))
    frames=[]
    for _ in range(nf):
        nc=struct.unpack('<I',f.read(4))[0]
        frames.append([list(CMD.unpack(f.read(100))) for _ in range(nc)])
    assert not f.read()
    return (w,h,num,den),textures,frames

def write_nps(path, header, textures, frames):
    w,h,num,den=header
    with path.open('wb') as f:
        f.write(struct.pack('<6I',0x3353504e,w,h,len(frames),num,den))
        for first in range(0,len(frames),30):
            b=io.BytesIO();ts=textures if first==0 else []
            b.write(struct.pack('<I',len(ts)))
            for ident,tw,th,pixels in ts:
                b.write(struct.pack('<3I',ident,tw,th));b.write(pixels)
            batch=frames[first:first+30];b.write(struct.pack('<I',len(batch)))
            for j,commands in enumerate(batch,first):
                b.write(struct.pack('<QQI',j,j*den*1000//num,len(commands)))
                for c in commands:b.write(CMD.pack(*c))
            b.write(struct.pack('<I',0));data=b.getvalue()
            assert len(data)<=512*1024*1024
            f.write(struct.pack('<I',len(data)));f.write(data)
        f.write(struct.pack('<I',0))

def build_encoder_args(ffmpeg, source, output, width, height, frame_count, fps_num, fps_den, *, filter_threads=2, decoder_threads=2, encoder_threads=8):
    fps = f'{fps_num}/{fps_den}'
    return [str(ffmpeg), '-hide_banner', '-loglevel', 'error', '-y',
            '-filter_complex_threads', str(filter_threads), '-threads', str(decoder_threads),
            '-i', str(source), '-f', 'rawvideo', '-pixel_format', 'rgba',
            '-video_size', f'{width}x{height}', '-framerate', fps, '-i', 'pipe:0',
            '-filter_complex', f'[0:v]fps={fps},scale={width}:{height}:flags=bicubic,setsar=1[bg];[bg][1:v]overlay=alpha=premultiplied:format=auto:shortest=1,format=yuv420p[v]',
            '-map', '[v]', '-map', '0:a?', '-frames:v', str(frame_count),
            '-c:v', 'libx264', '-preset', 'veryfast', '-crf', '26',
            '-threads:v', str(encoder_threads),
            '-x264-params', 'sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0',
            '-c:a', 'aac', '-b:a', '128k', '-movflags', '+faststart', str(output)]

def cropped(textures,frames):
    new=[];boxes={};stats=[]
    for ident,tw,th,data in textures:
        p=np.frombuffer(data,np.uint8).reshape(th,tw,4)
        # Require zero in all channels; keeps even unusual transparent RGB intact.
        ys,xs=np.where(np.any(p!=0,axis=2))
        x0=max(0,int(xs.min())-1) if xs.size else 0
        y0=max(0,int(ys.min())-1) if ys.size else 0
        x1=min(tw,int(xs.max())+2) if xs.size else 1
        y1=min(th,int(ys.max())+2) if ys.size else 1
        boxes[ident]=(x0,y0,x1-x0,y1-y0,tw,th)
        new.append((ident,x1-x0,y1-y0,p[y0:y1,x0:x1].tobytes()))
        stats.append(dict(id=ident,original=[tw,th],crop=[x0,y0,x1-x0,y1-y0]))
    changed=[]
    for frame in frames:
        out=[]
        for original in frame:
            c=original.copy()
            if c[0]:
                x0,y0,cw,ch,tw,th=boxes[c[0]]
                x,y,w,h=c[1:5]
                c[1:5]=[x+w*x0/tw,y+h*y0/th,w*cw/tw,h*ch/th]
            out.append(c)
        changed.append(out)
    return new,changed,stats

def prepare():
    FIX.mkdir(parents=True,exist_ok=True)
    write_nps(FIX/'f0.nps3',(1920,1080,60,1),[],[[] for _ in range(600)])
    records=[]
    for name in ('f1','real'):
        src=ROOT/f'build/nico-mask-probe/fixtures/{name}.reference.nmf1'
        header,textures,frames=read_nmf(src)
        write_nps(FIX/f'{name}.nps3',header,textures,frames)
        ct,cf,boxes=cropped(textures,frames)
        write_nps(FIX/f'{name}-crop.nps3',header,ct,cf)
        commands=sum(map(len,frames))
        runs=sum(sum(i==0 or c[0]!=frame[i-1][0] for i,c in enumerate(frame)) for frame in frames)
        non_pma=0
        for _,tw,th,pixels in textures:
            rgba=np.frombuffer(pixels,np.uint8).reshape(-1,4)
            non_pma+=int(np.count_nonzero(np.any(rgba[:,:3]>rgba[:,3,None],axis=1)))
        assert not non_pma,'Reference must be premultiplied RGBA'
        records.append(dict(name=name,header=header,frames=len(frames),commands=commands,consecutive_texture_runs=runs,textures=len(textures),raw_bytes=sum(len(t[3]) for t in textures),crop_bytes=sum(len(t[3]) for t in ct),non_pma_pixels=non_pma,boxes=boxes,source_sha256=sha(src)))
    # Small mixed-color, translucent, negative-size, clipped and subpixel stress.
    w,h=321,181
    p=np.zeros((17,29,4),np.uint8);p[3:14,4:24]=[60,20,0,100];p[6:11,8:21]=[128,32,64,192]
    q=np.zeros((23,19,4),np.uint8);q[2:21,1:18]=[0,80,45,128]
    # Deliberately non-PMA transparent RGB is an ABI stress case only, not reference content.
    q[0,0]=[3,1,2,0]
    textures=[(1,29,17,p.tobytes()),(2,19,23,q.tobytes())]
    proj=[2/w,0,0,0,0,-2/h,0,0,0,0,1,0,-1,1,0,1]
    frames=[]
    for i in range(120):
        rects=[(1,10.25+i*.7,20.1,87,51,.7),(1,35.3,32.6,29,17,.55),(1,35.3,32.6,29,17,0),(2,39,29,57,69,.8),(1,-5+i*.1,6.75,58,34,1),(0,35,32,33,22,.4),(0,40,35,33,22,.6),(2,335-i*.1,122,-57,46,.8)]
        frame=[]
        for ident,x,y,rw,rh,alpha in rects:
            color=[alpha,0,0,0] if ident else [.1*alpha,.2*alpha,.3*alpha,alpha]
            frame.append([ident,x,y,rw,rh]+proj+color)
        frames.append(frame)
    header=(w,h,60000,1001)
    write_nps(FIX/'stress.nps3',header,textures,frames)
    ct,cf,boxes=cropped(textures,frames);write_nps(FIX/'stress-crop.nps3',header,ct,cf)
    save(BASE/'fixtures.json',records)
    print(json.dumps(records,ensure_ascii=False),flush=True)

def scene(name,variant):return FIX/f'{name}{"-crop" if variant=="crop" else ""}.nps3'

def exact_read(pipe,n):
    data=bytearray()
    while len(data)<n:
        b=pipe.read(n-len(data))
        if not b:raise RuntimeError(f'Unexpected EOF at {len(data)} of {n}')
        data.extend(b)
    return data

def quality(name,variant,original=False):
    a_scene=scene(name,'baseline');b_scene=scene(name,variant)
    _,w,h,nf,num,den=struct.unpack('<6I',a_scene.read_bytes()[:24])
    dest=BASE/'quality'/f'{name}-{variant}{"-original" if original else ""}';dest.mkdir(parents=True,exist_ok=True)
    ah=hashlib.sha256();bh=hashlib.sha256();changed=0;maxdiff=0;totaldiff=0;changedframes=0
    procs=[]
    try:
        with a_scene.open('rb') as fa,b_scene.open('rb') as fb,(dest/'a.log').open('wb') as ea,(dest/'b.log').open('wb') as eb:
            pa=subprocess.Popen([str(ORIGINAL if original else EXE),'--stdin'],stdin=fa,stdout=subprocess.PIPE,stderr=ea);procs.append(pa)
            pb=subprocess.Popen([str(EXE),'--stdin']+FLAGS[variant],stdin=fb,stdout=subprocess.PIPE,stderr=eb);procs.append(pb)
            for index in range(nf):
                a=exact_read(pa.stdout,w*h*4);b=exact_read(pb.stdout,w*h*4);ah.update(a);bh.update(b)
                if a!=b:
                    aa=np.frombuffer(a,np.uint8);bb=np.frombuffer(b,np.uint8);d=np.abs(aa.astype(np.int16)-bb)
                    maxdiff=max(maxdiff,int(d.max()));changed+=int(np.count_nonzero(d));totaldiff+=int(d.sum());changedframes+=1
                    if changedframes==1:
                        from PIL import Image
                        Image.fromarray(aa.reshape(h,w,4)).save(dest/'reference.png');Image.fromarray(bb.reshape(h,w,4)).save(dest/'candidate.png')
                        Image.fromarray(np.minimum(d.reshape(h,w,4)[:,:,:3]*8,255).astype('uint8')).save(dest/'difference-x8.png')
            assert not pa.stdout.read(1) and not pb.stdout.read(1)
            assert pa.wait(timeout=30)==0 and pb.wait(timeout=30)==0
    finally:
        for p in procs:
            if p.poll() is None:p.kill();p.wait()
    row=dict(fixture=name,variant=variant,original=original,frames=nf,fps=f'{num}/{den}',reference_sha256=ah.hexdigest(),candidate_sha256=bh.hexdigest(),max_channel_diff=maxdiff,changed_channels=changed,changed_frames=changedframes,mean_abs_channel=totaldiff/(nf*w*h*4),baseline_exe_sha256=sha(ORIGINAL if original else EXE),candidate_exe_sha256=sha(EXE))
    save(dest/'result.json',row);print(json.dumps(row),flush=True);return row

def invoke(name,variant,mode,run):
    source=scene(name,variant);_,w,h,nf,num,den=struct.unpack('<6I',source.read_bytes()[:24])
    dest=OUTPUT_ROOT/'results'/f'{name}-{mode}-{variant}-{run}';dest.mkdir(parents=True,exist_ok=True)
    cmd=[str(EXE),'--stdin']+FLAGS[variant]
    if mode=='compose':cmd+=['--discard-output']
    if mode=='gpu':cmd+=['--discard-output','--gpu-timing']
    enc=[]
    if mode in ('encode','overlay'):
        enc=build_encoder_args(FFMPEG, SOURCE, dest/'out.mp4', w, h, nf, num, den, filter_threads=FILTER_THREADS, decoder_threads=DECODER_THREADS, encoder_threads=ENCODER_THREADS)
        if mode=='overlay':
            enc=enc[:enc.index('-c:v')]+['-an','-f','null','-']
    if mode=='pipe':
        enc=[str(FFMPEG),'-hide_banner','-loglevel','error','-f','rawvideo','-pixel_format','rgba','-video_size',f'{w}x{h}','-framerate',f'{num}/{den}','-i','pipe:0','-frames:v',str(nf),'-f','null','-']
    gpu_query=subprocess.run(['nvidia-smi','--query-gpu=utilization.gpu,utilization.encoder,utilization.decoder,memory.used','--format=csv,noheader'],capture_output=True,text=True,timeout=15)
    ambient_gpu=gpu_query.stdout.strip() if gpu_query.returncode==0 else 'unavailable'
    save(dest/'config.json',dict(argv=cmd,encoder=enc,scene_sha256=sha(source),exe_sha256=sha(EXE),ffmpeg_sha256=sha(FFMPEG),source_sha256=sha(SOURCE),warmup=run=='warmup',gpu_before=ambient_gpu))
    p=e=None;start=time.perf_counter()
    try:
        with source.open('rb') as data,(dest/'native.log').open('wb') as log,(dest/'encoder.log').open('wb') as elog:
            p=subprocess.Popen(cmd,stdin=data,stdout=subprocess.PIPE if enc else subprocess.DEVNULL,stderr=log)
            if enc:
                e=subprocess.Popen(enc,stdin=p.stdout,stdout=subprocess.DEVNULL,stderr=elog);p.stdout.close()
            rc=p.wait(timeout=600);ec=e.wait(timeout=600) if e else 0
            elapsed=time.perf_counter()-start
            if rc or ec:raise RuntimeError(f'{dest.name}: native={rc} encoder={ec}')
    finally:
        for process in (p,e):
            if process and process.poll() is None:process.kill();process.wait()
    logs=(dest/'native.log').read_text(encoding='utf8',errors='replace')
    stats={}
    for line in logs.splitlines():
        if line.startswith('NICO_PROFILE '):stats=json.loads(line[len('NICO_PROFILE '):])
    assert stats,'Missing profiling result'
    row=dict(fixture=name,variant=variant,mode=mode,run=run,process_wall_s=elapsed,profile=stats,gpu_before=ambient_gpu)
    save(dest/'result.json',row);print(json.dumps(row),flush=True);return row

def bench(name,variants,mode,repeats):
    rows=[]
    for variant in variants:invoke(name,variant,mode,'warmup')
    for i in range(repeats):
        order=variants[i%len(variants):]+variants[:i%len(variants)]
        if i%2:order=order[::-1]
        for variant in order:
            rows.append(invoke(name,variant,mode,str(i+1)));save(OUTPUT_ROOT/f'{name}-{mode}.json',rows)
    for variant in variants:
        vals=[r['process_wall_s'] for r in rows if r['variant']==variant]
        print(f'{name} {mode} {variant}: median={statistics.median(vals):.6f}s min={min(vals):.6f} max={max(vals):.6f}',flush=True)

if __name__=='__main__':
    ap=argparse.ArgumentParser();ap.add_argument('action',choices=['prepare','quality','bench']);ap.add_argument('--name',default='real');ap.add_argument('--variants',default='baseline,instanced,crop,hardware');ap.add_argument('--mode',default='compose',choices=['compose','encode','gpu','pipe','overlay']);ap.add_argument('--repeats',type=int,default=5);ap.add_argument('--original',action='store_true');ap.add_argument('--exe');ap.add_argument('--output-root');ap.add_argument('--filter-threads',type=int,default=2);ap.add_argument('--decoder-threads',type=int,default=2);ap.add_argument('--encoder-threads',type=int,default=8);args=ap.parse_args()
    if args.exe:EXE=pathlib.Path(args.exe).resolve()
    if args.output_root:OUTPUT_ROOT=pathlib.Path(args.output_root).resolve()
    FILTER_THREADS=args.filter_threads;DECODER_THREADS=args.decoder_threads;ENCODER_THREADS=args.encoder_threads
    if args.action=='prepare':prepare()
    elif args.action=='quality':
        for variant in args.variants.split(','):quality(args.name,variant,args.original)
    else:bench(args.name,args.variants.split(','),args.mode,args.repeats)
