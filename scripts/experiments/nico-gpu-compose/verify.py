import argparse, hashlib, importlib.util, json, math, re, struct, subprocess
import numpy as np
from PIL import Image
import run as r

def read_exact(f,n):
    result=bytearray()
    while len(result)<n:
        b=f.read(n-len(result))
        if not b:raise RuntimeError(f'truncated output {len(result)}/{n}')
        result.extend(b)
    return result

def empty_scene(path,w,h,nf):
    b=struct.pack('<II',0,nf)+b''.join(struct.pack('<QQI',i,i*1000//60,0) for i in range(nf))+struct.pack('<I',0)
    path.write_bytes(struct.pack('<6I',0x3353504e,w,h,nf,60,1)+struct.pack('<I',len(b))+b+struct.pack('<I',0))

def calibrate():
    dest=r.BASE/'calibration';dest.mkdir(parents=True,exist_ok=True)
    w,h,nf=128,64,7;scene=dest/'bars.nps3';empty_scene(scene,w,h,nf)
    colors=np.array([[0,0,0],[255,255,255],[255,0,0],[0,255,0],[0,0,255],[128,128,128]],np.uint8)
    data=np.zeros((nf,h,w,4),np.uint8);data[:,:,:,3]=255
    for i,c in enumerate(colors):data[i,:,:,:3]=c
    y,x=np.indices((h,w));data[6,:,:,:3]=colors[(x//7+y//5)%len(colors)]
    outcomes={}
    for pix in ('rgba','i420'):
        out=subprocess.run([str(r.EXE),'--scene',str(scene)]+(['--rgba'] if pix=='rgba' else []),input=data.tobytes(),capture_output=True,timeout=30)
        (dest/f'{pix}.log').write_bytes(out.stderr);assert out.returncode==0,out.stderr.decode(errors='replace')
        assert len(out.stdout)==nf*w*h*(4 if pix=='rgba' else 1.5)
        outcomes[pix]=out.stdout
    assert outcomes['rgba']==data.tobytes(),'Empty comments must preserve all opaque RGBA exactly'
    expected=[[16,128,128],[235,128,128],[63,102,240],[173,42,26],[32,240,118],[126,128,128]]
    observed=[]
    for i,values in enumerate(expected):
        f=np.frombuffer(outcomes['i420'],np.uint8,count=w*h*3//2,offset=i*w*h*3//2)
        actual=[int(f[0]),int(f[w*h]),int(f[w*h*5//4])];assert actual==values,(i,actual,values);observed.append(actual)
    rgb=data[:,:,:,:3].astype(np.float64)/255
    luma=np.floor(16+219*np.sum(rgb*np.array([.2126,.7152,.0722]),axis=3)+.5).astype(np.uint8)
    avg=rgb.reshape(nf,h//2,2,w//2,2,3).mean(axis=(2,4))
    u=np.floor(128+224*np.sum(avg*np.array([-.2126/(2*(1-.0722)),-.7152/(2*(1-.0722)),.5]),axis=3)+.5).astype(np.uint8)
    v=np.floor(128+224*np.sum(avg*np.array([.5,-.7152/(2*(1-.2126)),-.0722/(2*(1-.2126))]),axis=3)+.5).astype(np.uint8)
    ref=b''.join(luma[i].tobytes()+u[i].tobytes()+v[i].tobytes() for i in range(nf))
    diff=np.abs(np.frombuffer(ref,np.uint8).astype(np.int16)-np.frombuffer(outcomes['i420'],np.uint8))
    assert diff.max()<=1,'GPU conversion disagrees with independent 2x2 CPU reference'
    bad=[]
    for name,content in [('short',data.tobytes()[:-1]),('extra',data.tobytes()+b'x')]:
        proc=subprocess.run([str(r.EXE),'--scene',str(scene)],input=content,capture_output=True,timeout=30);assert proc.returncode!=0;bad.append(name)
    truncated=dest/'bad.nps3';truncated.write_bytes(scene.read_bytes()[:-8])
    proc=subprocess.run([str(r.EXE),'--scene',str(truncated)],input=data.tobytes(),capture_output=True,timeout=30);assert proc.returncode!=0;bad.append('bad_scene')
    row=dict(opaque_rgba_exact=True,solid_yuv=observed,independent_formula_max=int(diff.max()),independent_formula_mae=float(diff.mean()),invalid_rejected=bad,exe_sha256=r.sha(r.EXE));r.save(dest/'result.json',row);print(json.dumps(row),flush=True)

def quality(name,a,b,pix):
    dest=r.BASE/'quality'/f'{name}-{a}-{b}-{pix}';dest.mkdir(parents=True,exist_ok=True)
    pa=r.Pipeline(r.FIX/f'{name}.nps3',a,dest/'a',pix);pb=r.Pipeline(pa.scene,b,dest/'b',pix)
    size=pa.w*pa.h;n=size*4 if pix=='rgba' else size*3//2
    planes=[('RGBA',0,n)] if pix=='rgba' else [('Y',0,size),('U',size,size*5//4),('V',size*5//4,n)]
    stats={k:dict(max=0,total=0,squared=0,changed=0) for k,_,_ in planes};hashes=[hashlib.sha256(),hashlib.sha256()];changed_frames=0
    try:
        pa.start();pb.start()
        for i in range(pa.nf):
            x=read_exact(pa.raw,n);y=read_exact(pb.raw,n);hashes[0].update(x);hashes[1].update(y)
            aa=np.frombuffer(x,np.uint8);bb=np.frombuffer(y,np.uint8);d=np.abs(aa.astype(np.int16)-bb);changed_frames+=int(bool(np.any(d)))
            for k,lo,hi in planes:
                dd=d[lo:hi];st=stats[k];st['max']=max(st['max'],int(dd.max()));st['total']+=int(dd.sum());st['squared']+=int(np.square(dd.astype(np.int32)).sum());st['changed']+=int(np.count_nonzero(dd))
            if i in (75,180,pa.nf-1):
                if pix=='rgba':
                    Image.fromarray(aa.reshape(pa.h,pa.w,4)).save(dest/f'a-{i}.png');Image.fromarray(bb.reshape(pa.h,pa.w,4)).save(dest/f'b-{i}.png')
                    Image.fromarray(np.minimum(d.reshape(pa.h,pa.w,4)[:,:,:3]*8,255).astype(np.uint8)).save(dest/f'diff-{i}-x8.png')
                elif i==75:
                    for label,frame in [('a',x),('b',y)]:
                        raw=dest/f'{label}-{i}.yuv';raw.write_bytes(frame)
                        subprocess.run(r.ff()+['-f','rawvideo','-pixel_format','yuv420p','-video_size',f'{pa.w}x{pa.h}','-i',str(raw),'-vf','scale=in_color_matrix=bt709:in_range=tv:out_range=pc,format=rgb24','-frames:v','1',str(dest/f'{label}-{i}.png')],capture_output=True,check=True,timeout=30)
        assert not pa.raw.read(1) and not pb.raw.read(1);pa.finish();pb.finish()
    finally:pa.close();pb.close()
    for k,lo,hi in planes:
        st=stats[k];count=(hi-lo)*pa.nf;st['mae']=st.pop('total')/count;st['rmse']=math.sqrt(st.pop('squared')/count);st['psnr_db']=20*math.log10(255/st['rmse']) if st['rmse'] else None;st['changed_fraction']=st['changed']/count
    result=dict(fixture=name,a=a,b=b,pixel_format=pix,frames=pa.nf,changed_frames=changed_frames,planes=stats,hashes=[v.hexdigest() for v in hashes],commands=[pa.commands,pb.commands],exe_sha256=r.sha(r.EXE));r.save(dest/'result.json',result);print(json.dumps({k:v for k,v in result.items() if k!='commands'}),flush=True)

def videos():
    rows=[]
    for path in sorted((r.BASE/'results').glob('*/out.mp4')):
        cfg=json.loads((path.parent/'config.json').read_text(encoding='utf8'));nf=cfg['frames'];num,den=map(int,cfg['fps'].split('/'))
        output=subprocess.run([str(r.FFPROBE),'-v','error','-select_streams','v:0','-show_frames','-show_entries','frame=best_effort_timestamp_time','-of','json',str(path)],capture_output=True,check=True,timeout=30)
        ts=[float(v['best_effort_timestamp_time']) for v in json.loads(output.stdout)['frames']];assert len(ts)==nf and all(abs(t-i*den/num)<2e-6 for i,t in enumerate(ts))
        subprocess.run(r.ff()+['-xerror','-i',str(path),'-f','null','-'],capture_output=True,check=True,timeout=60)
        stream=subprocess.run(r.ff()+['-i',str(path),'-map','0:v:0','-c','copy','-bsf:v','h264_mp4toannexb,h264_metadata=aud=insert','-f','h264','pipe:1'],capture_output=True,check=True,timeout=30).stdout
        counts=[];current=None
        for nal in re.split(b'\x00\x00\x00?\x01',stream):
            if not nal:continue
            kind=nal[0]&31
            if kind==9:
                if current is not None:counts.append(current)
                current=0
            elif kind in (1,5):assert current is not None;current+=1
        if current is not None:counts.append(current)
        assert len(counts)==nf and set(counts)=={1}
        rows.append(dict(path=str(path.relative_to(r.ROOT)),frames=nf,decode=True,pts=True,single_slice=True,sha256=r.sha(path)))
    r.save(r.BASE/'video-validation.json',rows);print(f'{len(rows)} MP4 passed decode, frame count, PTS, one slice/AU',flush=True)

if __name__=='__main__':
    ap=argparse.ArgumentParser();ap.add_argument('action',choices=['calibrate','quality','videos']);ap.add_argument('--name',default='real');ap.add_argument('--a',default='hardware');ap.add_argument('--b',default='gpu-rgba');ap.add_argument('--pix',default='rgba',choices=['rgba','yuv420p']);a=ap.parse_args()
    if a.action=='calibrate':calibrate()
    elif a.action=='quality':quality(a.name,a.a,a.b,a.pix)
    else:videos()
