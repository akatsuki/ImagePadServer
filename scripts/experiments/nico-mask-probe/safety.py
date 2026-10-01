import importlib.util, json, pathlib, subprocess
p=pathlib.Path(__file__).parent
spec=importlib.util.spec_from_file_location('runner',p/'run.py');r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
spec=importlib.util.spec_from_file_location('validate',p/'validate.py');v=importlib.util.module_from_spec(spec);spec.loader.exec_module(v)
rows=[]
for args in [['--selftest'],['--encoder-selftest','build/nico-mask-probe/eagain.mp4']]:
    result=subprocess.run([r.relative(r.BASE/'mask-probe.exe')]+args,cwd=r.ROOT,env=r.ENV,capture_output=True,text=True,timeout=60)
    assert result.returncode==0,result.stderr
    rows.append(json.loads(result.stdout))
rows.append(v.validate(r.BASE/'eagain.mp4'))
scene=r.BASE/'smoke.nmf'
for mode in ('C','D'):
    row=r.invoke(scene,8,mode,'cancel-'+mode,extra=['--cancel-at','15'],expect=2)
    assert row['cancelled'] and row['frames']==15 and row['packets']==15
    rows.append(row);rows.append(v.validate(r.BASE/'results'/('cancel-'+mode)/'out.mp4'))
    row=r.invoke(scene,8,mode,'fail-'+mode,extra=['--fail-at','15'],expect=1);rows.append(row)
(r.BASE/'results'/'safety.json').write_text(json.dumps(rows,indent=2),encoding='utf8')
print('Safety: lifetime, retained output ref, failed submit, encoder EAGAIN retry/drain, cooperative cancellation and injected failure PASS')
