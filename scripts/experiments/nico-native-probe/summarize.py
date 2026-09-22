"""Write machine-readable medians, keeping browser and native experiments separate."""
import json, pathlib, statistics
BASE=pathlib.Path(__file__).resolve().parents[3]/'build/nico-native-probe'
result={"native":[],"quality":[],"browser":[]}
for path in sorted(BASE.glob('*-compose.json'))+sorted(BASE.glob('*-encode.json'))+sorted(BASE.glob('*-pipe.json'))+sorted(BASE.glob('*-overlay.json')):
    rows=json.loads(path.read_text(encoding='utf8'))
    for variant in sorted({r['variant'] for r in rows}):
        group=[r for r in rows if r['variant']==variant]
        walls=[r['process_wall_s'] for r in group]
        profile={}
        for key in group[0]['profile']:
            vals=[r['profile'][key] for r in group]
            profile[key]=statistics.median(vals) if isinstance(vals[0],(float,int)) else vals[0]
        result['native'].append(dict(fixture=group[0]['fixture'],mode=group[0]['mode'],variant=variant,runs=len(group),median_s=statistics.median(walls),min_s=min(walls),max_s=max(walls),profile_median=profile))
for path in sorted((BASE/'quality').glob('*/result.json')):
    result['quality'].append(json.loads(path.read_text(encoding='utf8')))
for path in sorted((BASE/'browser').glob('*/nico-native-profile.json')):
    data=json.loads(path.read_text(encoding='utf8'));batches=data['batches']
    row=dict(run=path.parent.name,duration_ms=data['durationMs'],frames=data['frameCount'],startup_ms=data['browserStartupMs'],wait_ready_ms=data['browserWaitReadyMs'],initial_browser=data.get('initialBrowser'))
    for key in ('drawMs','takeMs','decodeMs','writeToDiscardMs','textures','commands'):
        row['total_'+key]=sum(b[key] for b in batches)
    row['worst_draw_batch']=max(batches,key=lambda b:b['drawMs'])
    row['browser_calls_total']={k:sum(b['browser'][k] for b in batches)+(data.get('initialBrowser') or {}).get(k,0) for k in batches[0]['browser']}
    result['browser'].append(row)
(BASE/'summary.json').write_text(json.dumps(result,ensure_ascii=False,indent=2),encoding='utf8')
for r in result['native']:print(f"{r['fixture']} {r['mode']} {r['variant']}: {r['median_s']:.6f} ({r['min_s']:.6f}..{r['max_s']:.6f})",flush=True)
for r in result['browser']:
    print(json.dumps({k:v for k,v in r.items() if k not in ('worst_draw_batch',)},ensure_ascii=False),flush=True)
