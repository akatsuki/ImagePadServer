import importlib.util, json, os, pathlib, platform, subprocess
spec=importlib.util.spec_from_file_location('probe',pathlib.Path(__file__).with_name('probe.py'))
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)
files=[p.EXE,p.ORIGINAL,p.BASE/'baseline.exe',p.FFMPEG,p.FFPROBE,p.SOURCE,p.BASE/'browser-profile.test.exe']
files+=list((p.BASE/'fixtures').glob('*.nps3'))
files+=list((p.ROOT/'scripts/experiments/nico-native-probe').glob('*.cpp'))+list((p.ROOT/'scripts/experiments/nico-native-probe').glob('*.py'))+list((p.ROOT/'scripts/experiments/nico-native-probe').glob('*.js'))
files+=[p.ROOT/'internal/nicorender/native_profile_test.go',p.ROOT/'native/nico-compositor/main.cpp',p.ROOT/'internal/nicorender/assets/bundle.js',p.ROOT/'internal/nicorender/assets/sprites.js']
gpu=subprocess.run(['nvidia-smi','--query-gpu=name,driver_version,memory.total,utilization.gpu,utilization.encoder','--format=csv,noheader'],capture_output=True,text=True,check=True).stdout.strip()
p.save(p.BASE/'environment.json',dict(platform=platform.platform(),logical_cpus=os.cpu_count(),cpu='AMD Ryzen 7 7800X3D',gpu=gpu,python=platform.python_version(),compiler='MSVC /std:c++17 /O2 /EHsc /MT /W4',files=[dict(path=str(f),bytes=f.stat().st_size,sha256=p.sha(f)) for f in files],scope='isolated experiments; running app not changed',live_app_pid=36316,live_app_path=str(pathlib.Path(os.environ['TEMP'])/'imagepad-nico-perf-20260916/imagepadserver-perf.exe')))
print(gpu,flush=True)
