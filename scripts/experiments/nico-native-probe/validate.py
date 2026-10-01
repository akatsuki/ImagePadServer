"""Acceptance checks for isolated native experiment artifacts."""
import argparse, importlib.util, json, pathlib, re, struct, subprocess
spec=importlib.util.spec_from_file_location('probe',pathlib.Path(__file__).with_name('probe.py'))
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)
from media_validation import (
    ValidationError,
    check_frame_pts,
    h264_access_unit_slice_counts,
    parse_hls_playlist,
    summarize_timestamped_durations,
)

def malformed():
    header=struct.pack('<6I',0x3353504e,33,19,1,60,1)
    proj=[2/33,0,0,0,0,-2/19,0,0,0,0,1,0,-1,1,0,1]
    def stream(commands=(),deletes=(),frames=1):
        b=struct.pack('<II',0,frames)+struct.pack('<QQI',0,0,len(commands))+b''.join(commands)+struct.pack('<I',len(deletes))+b''.join(struct.pack('<I',d) for d in deletes)
        return header+struct.pack('<I',len(b))+b+struct.pack('<I',0)
    good=stream()
    command=lambda ident,x:struct.pack('<I24f',ident,x,0,10,10,*proj,1,0,0,0)
    cases={'truncated':good[:-7],'trailing':good+b'x','unknown_id':stream([command(22,1)]),'unknown_delete':stream(deletes=[1]),'zero_batch':stream(frames=0),'nan':stream([command(0,float('nan'))]),'huge_batch':header+struct.pack('<I',512*1024*1024+1),'wrong_pts':good[:44]+struct.pack('<Q',123)+good[52:]}
    duplicate=struct.pack('<I',2)+b''.join(struct.pack('<III4B',ident,1,1,0,0,0,0) for ident in (1,2))+struct.pack('<IQQIIII',1,0,0,0,2,1,1)
    cases['duplicate_delete']=header+struct.pack('<I',len(duplicate))+duplicate+struct.pack('<I',0)
    rows=[]
    for name,flags in p.FLAGS.items():
        if name=='crop':continue # crop changes fixture only, same executable branch as baseline
        for case,data in cases.items():
            r=subprocess.run([str(p.EXE),'--stdin','--discard-output']+flags,input=data,capture_output=True,timeout=30)
            assert r.returncode!=0,(name,case)
            rows.append(dict(variant=name,case=case,exit=r.returncode,stderr=r.stderr.decode(errors='replace')))
    p.save(p.BASE/'malformed.json',rows);print(f'{len(rows)} malformed stream checks passed',flush=True)

def _ffprobe_json(path, *entries, select_streams=None):
    cmd=[str(p.FFPROBE),'-v','error']
    if select_streams:
        cmd += ['-select_streams',select_streams]
    cmd += ['-show_streams']
    if any(entry.startswith('frame=') for entry in entries):
        cmd += ['-show_frames']
    cmd += ['-show_entries',':'.join(entries),'-of','json',str(path)]
    result=subprocess.run(cmd,capture_output=True,check=True,timeout=120)
    return json.loads(result.stdout)


def _decode_all(path):
    subprocess.run([str(p.FFMPEG),'-v','error','-xerror','-i',str(path),'-map','0','-f','null','-'],capture_output=True,check=True,timeout=180)


def _probe_segment_keyframe(path):
    data=_ffprobe_json(path,'stream=codec_type,duration','frame=key_frame,pict_type',select_streams='v:0')
    frames=data.get('frames',[])
    if not frames or int(frames[0].get('key_frame',0)) != 1:
        raise ValidationError(f'HLS segment does not start with a keyframe: {path}')
    return True


def validate_hls(playlist, expected_video_duration=None):
    parsed=parse_hls_playlist(pathlib.Path(playlist))
    _decode_all(parsed['playlist'])
    video_duration=0.0
    audio_streams=[]
    for segment in parsed['segments']:
        _probe_segment_keyframe(segment)
        streams=_ffprobe_json(segment,'stream=codec_type,start_time,duration')
        video=[s for s in streams.get('streams',[]) if s.get('codec_type')=='video']
        audio=[s for s in streams.get('streams',[]) if s.get('codec_type')=='audio']
        if not video:
            raise ValidationError(f'HLS segment has no video stream: {segment}')
        if video[0].get('duration') is not None:
            video_duration += float(video[0]['duration'])
        if audio:
            audio_streams.append(audio[0])
    if not video_duration:
        raise ValidationError('HLS has no measurable video duration')
    if expected_video_duration is not None and abs(video_duration-expected_video_duration)>0.25:
        raise ValidationError(f'HLS video duration {video_duration} != expected {expected_video_duration}')
    audio_summary=summarize_timestamped_durations(audio_streams)
    audio_value=audio_summary['duration']
    duration_delta=(audio_value-video_duration) if audio_value is not None else None
    return {
        'playlist': str(parsed['playlist']),
        'segments': len(parsed['segments']),
        'segment_keyframes': 'passed',
        'decode': 'passed',
        'video_duration_s': video_duration,
        'audio_duration_s': audio_value,
        'audio_payload_duration_s': audio_summary['payload_duration'],
        'audio_timeline_start_s': audio_summary['timeline_start'],
        'audio_timeline_end_s': audio_summary['timeline_end'],
        'audio_duration_source': audio_summary['source'],
        'audio_video_duration_delta_s': duration_delta,
        'bytes': sum(path.stat().st_size for path in [parsed['playlist'],*parsed['segments']]),
        'sha256': p.sha(parsed['playlist']),
    }


def validate_artifacts(path, *, playlist=None, expected_frame_rate=None, expected_frames=None):
    path=pathlib.Path(path).resolve()
    config_path=path.parent/'config.json'
    if config_path.is_file():
        cfg=json.loads(config_path.read_text(encoding='utf8'))
        enc=cfg['encoder'];nf=int(enc[enc.index('-frames:v')+1]);num,den=map(int,enc[enc.index('-framerate')+1].split('/'))
        expected_frames=expected_frames or nf
        expected_frame_rate=expected_frame_rate or f'{num}/{den}'
    if expected_frames is None or expected_frame_rate is None:
        raise ValidationError('expected frame count/rate is required when config.json is absent')
    data=_ffprobe_json(path,'stream=width,height,time_base,avg_frame_rate,nb_frames','format=duration','frame=best_effort_timestamp,key_frame,pict_type',select_streams='v:0')
    frames=data.get('frames',[]);streams=data.get('streams',[])
    if not streams:
        raise ValidationError(f'video stream is missing: {path}')
    check_frame_pts(frames,streams[0]['time_base'],expected_frame_rate,expected_frames)
    _decode_all(path)
    h264=subprocess.run([str(p.FFMPEG),'-v','error','-i',str(path),'-map','0:v:0','-c','copy','-bsf:v','h264_mp4toannexb,h264_metadata=aud=insert','-f','h264','pipe:1'],capture_output=True,check=True,timeout=180).stdout
    counts=h264_access_unit_slice_counts(h264)
    if len(counts) != expected_frames:
        raise ValidationError(f'H.264 access units {len(counts)} != expected {expected_frames}')
    result=dict(path=str(path.relative_to(p.ROOT)),frames=expected_frames,fps=expected_frame_rate,pts='passed',decode='passed',single_slice='passed',bytes=path.stat().st_size,sha256=p.sha(path))
    if playlist:
        result['hls']=validate_hls(playlist)
    return result


def validate_mp4(path):
    return validate_artifacts(path)

def videos():
    rows=[]
    for path in sorted((p.BASE/'results').glob('*/out.mp4')):
        try:
            rows.append(dict(status='complete',**validate_mp4(path)))
        except (AssertionError, OSError, subprocess.CalledProcessError, ValidationError) as exc:
            rows.append(dict(status='invalid',path=str(path.relative_to(p.ROOT)),invalid_reason=str(exc)))
        p.save(p.BASE/'video-validation.json',rows)
    print(f'{len(rows)} MP4 files checked; invalid outputs remain recorded',flush=True)


def artifact(mp4, playlist, frame_rate, frames, output):
    try:
        row=dict(status='complete',**validate_artifacts(mp4, playlist=playlist, expected_frame_rate=frame_rate, expected_frames=frames))
    except (AssertionError, OSError, subprocess.CalledProcessError, ValidationError) as exc:
        row=dict(status='invalid',mp4=str(pathlib.Path(mp4).resolve()),playlist=str(pathlib.Path(playlist).resolve()) if playlist else None,invalid_reason=str(exc))
    p.save(pathlib.Path(output), row)
    print(json.dumps(row, ensure_ascii=False),flush=True)
    return 0 if row['status']=='complete' else 1

if __name__=='__main__':
    ap=argparse.ArgumentParser();ap.add_argument('mode',choices=['malformed','videos','artifact']);ap.add_argument('--mp4');ap.add_argument('--playlist');ap.add_argument('--frame-rate');ap.add_argument('--frames',type=int);ap.add_argument('--output');a=ap.parse_args()
    if a.mode=='malformed':
        malformed()
    elif a.mode=='videos':
        videos()
    else:
        missing=[name for name,value in {'--mp4':a.mp4,'--frame-rate':a.frame_rate,'--frames':a.frames,'--output':a.output}.items() if value is None]
        if missing: ap.error('artifact requires '+', '.join(missing))
        raise SystemExit(artifact(a.mp4,a.playlist,a.frame_rate,a.frames,a.output))
