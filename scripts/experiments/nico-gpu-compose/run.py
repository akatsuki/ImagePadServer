"""Isolated GPU composition benchmark. Does not start or modify ImagePadServer."""
import argparse, hashlib, importlib.util, json, os, pathlib, statistics, struct, subprocess, time

ROOT = pathlib.Path(__file__).resolve().parents[3]
BASE = ROOT / 'build/nico-gpu-compose'
FIX = ROOT / 'build/nico-native-probe/fixtures'
EXE = BASE / 'nico-gpu-compose.exe'
OLD = ROOT / 'build/nico-native-probe/nico-native-probe.exe'
FFMPEG = ROOT / 'build/nico-native-integration/tools/ffmpeg.exe'
FFPROBE = FFMPEG.with_name('ffprobe.exe')
SOURCE = pathlib.Path(os.environ['TEMP']) / 'imagepad-nico-perf-20260916/source.mp4'
VARIANTS = ['warp', 'hardware', 'gpu-rgba', 'gpu-i420']
COLOR = 'scale=iw:ih:flags=bilinear:in_range=pc:out_range=tv:out_color_matrix=bt709:out_chroma_loc=center,format=yuv420p'
META = ['-color_range','tv','-colorspace','bt709','-color_trc','bt709','-color_primaries','bt709','-chroma_sample_location','center']

def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2), encoding='utf8')

def sha(path):
    with path.open('rb') as f: return hashlib.file_digest(f, 'sha256').hexdigest()

def header(scene):
    with scene.open('rb') as f: magic,w,h,nf,num,den = struct.unpack('<6I',f.read(24))
    assert magic == 0x3353504e
    return w,h,nf,num,den

def background_filter(w,h,num,den):
    return f'fps={num}/{den},scale={w}:{h}:flags=bicubic:in_color_matrix=bt709:in_range=tv:out_range=pc,format=rgba,setsar=1'

def ff(): return [str(FFMPEG),'-hide_banner','-loglevel','error','-y']

def raw_input(w,h,num,den,pix):
    return ['-f','rawvideo','-pixel_format',pix,'-video_size',f'{w}x{h}','-framerate',f'{num}/{den}']+(['-alpha_mode','premultiplied'] if pix=='rgba' else [])+['-i','pipe:0']

def background_command(scene):
    w,h,nf,num,den=header(scene)
    return ff()+['-filter_threads','2','-threads','2','-i',str(SOURCE),'-vf',background_filter(w,h,num,den),'-an','-sn','-dn','-frames:v',str(nf),'-c:v','rawvideo','-threads','1','-pix_fmt','rgba','-f','rawvideo','pipe:1']

def gpu_state():
    r=subprocess.run(['nvidia-smi','--query-gpu=utilization.gpu,utilization.encoder,utilization.decoder,memory.used','--format=csv,noheader'],capture_output=True,text=True,timeout=15)
    return r.stdout.strip() if r.returncode==0 else 'unavailable'

class Pipeline:
    """Connect OS pipes directly; Python does not copy images in timed runs."""
    def __init__(self, scene, variant, dest, output='encode'):
        self.scene,self.variant,self.dest,self.output=scene,variant,dest,output
        self.procs=[];self.files=[];self.commands=[];self.raw=None
        self.w,self.h,self.nf,self.num,self.den=header(scene)
        dest.mkdir(parents=True,exist_ok=True)

    def spawn(self, tag, command, stdin=None, capture=True):
        log=(self.dest/f'{tag}.log').open('wb');self.files.append(log)
        process=subprocess.Popen(command,stdin=stdin,stdout=subprocess.PIPE if capture else subprocess.DEVNULL,stderr=log)
        self.procs.append(process);self.commands.append(dict(tag=tag,argv=command))
        return process

    def start(self):
        w,h,nf,num,den=self.w,self.h,self.nf,self.num,self.den
        encode=self.output=='encode'
        finalpix='rgba' if self.output=='rgba' else 'yuv420p'
        if self.variant in ('warp','hardware'):
            f=self.scene.open('rb');self.files.append(f)
            native=self.spawn('native',[str(OLD),'--stdin']+(['--hardware'] if self.variant=='hardware' else []),f)
            graph=f'[0:v]{background_filter(w,h,num,den)}[bg];[bg][1:v]overlay=alpha=premultiplied:format=rgb:shortest=1'
            graph+=(',format=rgba' if finalpix=='rgba' else ','+COLOR)+'[v]'
            cmd=ff()+['-filter_complex_threads','2','-threads','2','-i',str(SOURCE)]+raw_input(w,h,num,den,'rgba')+['-filter_complex',graph,'-map','[v]']
            if encode:cmd+=['-map','0:a?']
            upstream=native
        else:
            decoder=self.spawn('decoder',background_command(self.scene))
            args=[str(EXE),'--scene',str(self.scene)]+(['--rgba'] if self.variant=='gpu-rgba' else [])
            native=self.spawn('native',args,decoder.stdout);decoder.stdout.close()
            upstream=native
            # GPU RGBA can be observed without a redundant rawvideo FFmpeg relay.
            if self.variant=='gpu-rgba' and self.output=='rgba':
                self.raw=native.stdout;return self
            assert not(self.variant=='gpu-i420' and self.output=='rgba')
            cmd=ff()+['-filter_threads','2']+raw_input(w,h,num,den,'rgba' if self.variant=='gpu-rgba' else 'yuv420p')
            if encode:cmd+=['-threads','2','-i',str(SOURCE),'-map','0:v','-map','1:a?']
            else:cmd+=['-map','0:v']
            if self.variant=='gpu-rgba':cmd+=['-vf',COLOR]
        cmd+=['-frames:v',str(nf)]
        if encode:
            cmd+=['-c:v','libx264','-preset','veryfast','-crf','26','-threads','8','-g',str(round(4*num/den)),'-keyint_min',str(round(4*num/den)),'-sc_threshold','0','-force_key_frames','expr:gte(t,n_forced*4)','-x264-params','sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0','-c:a','aac','-b:a','128k','-shortest']+META+['-movflags','+faststart',str(self.dest/'out.mp4')]
        else:
            cmd+=['-an','-c:v','rawvideo','-threads','1','-pix_fmt',finalpix,'-f','rawvideo','pipe:1']
        sink=self.spawn('encoder' if encode else 'raw',cmd,upstream.stdout,capture=not encode);upstream.stdout.close()
        self.raw=sink.stdout
        return self

    def finish(self):
        exits=[p.wait(timeout=180) for p in self.procs]
        assert set(exits)=={0},(str(self.dest),exits)
        return exits

    def close(self):
        for p in self.procs:
            if p.poll() is None:p.kill();p.wait()
            if p.stdout:p.stdout.close()
        for f in self.files:f.close()

def invoke(name,variant,run):
    scene=FIX/f'{name}.nps3';dest=BASE/'results'/f'{name}-{variant}-{run}'
    pipe=Pipeline(scene,variant,dest)
    before=gpu_state()
    fingerprints={str(p.relative_to(ROOT)) if p.is_relative_to(ROOT) else str(p):sha(p) for p in (EXE,OLD,FFMPEG,SOURCE,scene)}
    start=time.perf_counter()
    try:
        pipe.start();exits=pipe.finish();elapsed=time.perf_counter()-start
    finally:pipe.close()
    cfg=dict(commands=pipe.commands,frames=pipe.nf,width=pipe.w,height=pipe.h,fps=f'{pipe.num}/{pipe.den}',hashes=fingerprints,warmup=run=='warmup',color_filter=COLOR,gpu_before=before)
    save(dest/'config.json',cfg)
    stats={}
    for line in (dest/'native.log').read_text(encoding='utf8',errors='replace').splitlines():
        for prefix in ('NICO_GPU_PROFILE ','NICO_PROFILE '):
            if line.startswith(prefix):stats=json.loads(line[len(prefix):])
    assert stats,'Missing native profile'
    row=dict(fixture=name,variant=variant,run=run,wall_s=elapsed,exit_codes=exits,profile=stats,gpu_before=before,gpu_after=gpu_state())
    save(dest/'result.json',row)
    print(json.dumps({k:row[k] for k in ('fixture','variant','run','wall_s','gpu_before')},ensure_ascii=False),flush=True)
    return row

def bench(name,variants,repeats):
    for v in variants:invoke(name,v,'warmup')
    rows=[]
    for i in range(repeats):
        order=variants[i%len(variants):]+variants[:i%len(variants)]
        if i%2:order=order[::-1]
        for v in order:
            rows.append(invoke(name,v,str(i+1)));save(BASE/f'{name}-benchmark.json',rows)
    for v in variants:
        values=[r['wall_s'] for r in rows if r['variant']==v]
        print(f'{name} {v}: median={statistics.median(values):.6f} min={min(values):.6f} max={max(values):.6f}',flush=True)

if __name__=='__main__':
    ap=argparse.ArgumentParser();ap.add_argument('--name',default='real');ap.add_argument('--variants',default=','.join(VARIANTS));ap.add_argument('--repeats',type=int,default=5);ap.add_argument('--once',action='store_true');a=ap.parse_args()
    if a.once:
        for v in a.variants.split(','):invoke(a.name,v,'smoke')
    else:bench(a.name,a.variants.split(','),a.repeats)
