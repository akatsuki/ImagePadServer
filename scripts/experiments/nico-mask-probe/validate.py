"""Full decode, CFR timestamps, AU/slice and probe failure checks."""
import argparse, importlib.util, json, pathlib, re, subprocess
spec=importlib.util.spec_from_file_location('runner',pathlib.Path(__file__).with_name('run.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

def run(args):
    p=subprocess.run(args,cwd=r.ROOT,env=r.ENV,capture_output=True,timeout=300)
    if p.returncode:raise RuntimeError(p.stderr.decode(errors='replace')[-1500:])
    return p.stdout

def validate(path):
    ffmpeg=str(r.SDK/'bin/ffmpeg.exe');ffprobe=str(r.SDK/'bin/ffprobe.exe')
    data=json.loads(run([ffprobe,'-v','error','-select_streams','v:0','-show_frames','-show_streams','-show_entries','stream=width,height,r_frame_rate,time_base,nb_frames:frame=pts','-of','json',str(path)]))
    stream=data['streams'][0];frames=data['frames'];fpsnum,fpsden=map(int,stream['r_frame_rate'].split('/'));tbnum,tbden=map(int,stream['time_base'].split('/'))
    assert len(frames)==int(stream['nb_frames']),(len(frames),stream)
    for n,frame in enumerate(frames):assert int(frame['pts'])*tbnum*fpsnum==n*fpsden*tbden,(n,frame)
    run([ffmpeg,'-v','error','-xerror','-threads','2','-i',str(path),'-f','null','-'])
    h264=run([ffmpeg,'-v','error','-i',str(path),'-map','0:v:0','-c','copy','-bsf:v','h264_mp4toannexb,h264_metadata=aud=insert','-f','h264','-'])
    access=[];count=0;started=False
    for nal in re.split(b'\x00\x00\x00\x01|\x00\x00\x01',h264):
        if not nal:continue
        kind=nal[0]&31
        if kind==9:
            if started:access.append(count)
            count=0;started=True
        if kind in (1,5):
            count+=1
            # first_mb_in_slice == 0 is ue(v) == bit 1, before emulation bytes.
            assert len(nal)>1 and nal[1]&128,'first_mb_in_slice != 0'
    if started:access.append(count)
    assert len(access)==len(frames) and all(n==1 for n in access),(len(access),len(frames),set(access))
    return {'file':r.relative(path),'decoded_frames':len(frames),'fps':stream['r_frame_rate'],'pts':'PASS','decode':'PASS','one_slice_per_AU':'PASS'}

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--glob',default='results/*/out.mp4');a=parser.parse_args()
    rows=[]
    for path in sorted(r.BASE.glob(a.glob)):
        config=path.parent/'config.json'
        if config.exists() and '--fail-at' in json.loads(config.read_text(encoding='utf8'))['argv']:
            continue  # Expected partial output; failure exit is asserted by safety.py.
        rows.append(validate(path));print(json.dumps(rows[-1]),flush=True)
    (r.BASE/'results').mkdir(exist_ok=True)
    (r.BASE/'results'/'validation.json').write_text(json.dumps(rows,indent=2),encoding='utf8')
if __name__=='__main__':main()
