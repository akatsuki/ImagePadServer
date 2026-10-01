import json, statistics
import run as r

rows=json.loads((r.BASE/'real-benchmark.json').read_text(encoding='utf8'))
assert len(rows)==12
validation=json.loads((r.BASE/'video-validation.json').read_text(encoding='utf8'));assert len(validation)==16
q1=json.loads((r.BASE/'quality/real-hardware-gpu-rgba-rgba/result.json').read_text(encoding='utf8'))
q2=json.loads((r.BASE/'quality/real-gpu-rgba-gpu-i420-yuv420p/result.json').read_text(encoding='utf8'))
labels={'warp':'WARPコメント＋CPU合成・色変換','hardware':'GPUコメント＋CPU合成・色変換','gpu-rgba':'GPU全体合成＋CPU色変換','gpu-i420':'GPU全体合成・色変換'}
med={v:statistics.median(x['wall_s'] for x in rows if x['variant']==v) for v in r.VARIANTS}
lines=['# コメント＋元動画のGPU合成試験（2026-09-17）','',
'## 判定','',
'**全体合成・色変換のGPU移行は、今回の構成では採用しない。** 元動画と完成画像を別プロセスへ渡す構成では、コメントだけGPUで描いてFFmpegに合成させる比較条件を上回らなかった。色変換も既存CPU方式と完全一致しない。これはGPU内完結や同一プロセスでの将来構成の性能上限を示す試験ではない。','',
'ユーザーの追加指示に従い委託を停止し、GPU実装・測定はメインが担当。実動画6秒に限定し、warmup各1回＋交互順序3回で短く検証した。F1高密度・長尺・5回比較は未実施。本番コード、稼働アプリ、Git履歴は変更していない。','',
'## 実速度','',
'同じ実動画、1920×1080/60fps/360frame、CPUデコード・bicubic拡大、libx264 veryfast/CRF26/threads8、AAC128k。入力開始から全process終了・MP4 faststart完了まで。背景RGBA生成と追加パイプも含む。動画の先頭6秒の測定であり、動画全体の予測値ではない。','',
'|処理|中央値|範囲（3回）|GPUコメントのみ比|','|---|---:|---:|---:|']
for v in r.VARIANTS:
    values=[x['wall_s'] for x in rows if x['variant']==v]
    lines.append(f'|{labels[v]}|{med[v]:.3f}秒|{min(values):.3f}〜{max(values):.3f}秒|{(med[v]/med["hardware"]-1)*100:+.1f}%|')
lines+=['',f'全体GPU方式はGPUコメントのみ方式より処理時間が{(med["gpu-i420"]/med["hardware"]-1)*100:.1f}%長かった。GPU全体合成＋CPU色変換も高速化にならなかった。各runのGPU負荷とprocess argv/終了コードはbuild/nico-gpu-compose/resultsに保存。GPUは他用途でも使われており、この3回を統計的に確定した一般値とは扱わない。','',
'## 画素検査','',
f'- 合成RGBA：同じGPUコメント平面を使い、CPU合成とGPU合成を全360frame比較。最大差{q1["planes"]["RGBA"]["max"]}/255、平均絶対差{q1["planes"]["RGBA"]["mae"]:.6f}/255。完全一致ではないが差は丸め1段階以内。',
'- 色変換：同じGPU合成RGBAに対し、CPU swscaleとGPUのBT.709 limited I420変換を全360frame比較。',
'','|平面|最大差 /255|平均絶対差 /255|PSNR|','|---|---:|---:|---:|']
for k,s in q2['planes'].items():lines.append(f'|{k}|{s["max"]}|{s["mae"]:.6f}|{s["psnr_db"]:.2f} dB|')
lines+=['',
'GPU側は2×2平均でU/Vを作り、swscaleのbilinear・center指定と色サンプリングが同一ではない。輝度より色境界に差が残った。画質設定維持の指示に対し、CRFが同じことだけを理由に同等画質とは認定しない。低ビットパレット化は使用していない。','',
'黒・白・赤・緑・青・灰色の規定YUV値を確認。色境界fixtureも独立したCPUの2×2計算と全バイト一致。コメントなしの不透明RGBA入力はGPU合成後も完全一致。短い背景・余分な背景・不正シーンの3件は非ゼロ終了で拒否した。','',
'比較器でraw RGBA入力に `-alpha_mode premultiplied` を明示すると、コメント縁の最大39差が1差へ減った。FFmpeg 9で透明度の入力解釈を揃えるための試験条件修正。修正後に正式測定を開始した。本番への影響はこの試験では評価していない。','',
'CPU変換行列・rangeはfilterで指定し、MP4にもBT.709/tvのmetadataを付与。従来の試験とはこの指定やGOP条件も異なるため、前回の8秒等の測定値と今回の秒数を直接比較しない。現在稼働中のアプリそのものの速度比較でもない。','',
'## 転送と検証','',
'GPU→CPUの読み戻しはRGBAの2,985,984,000 byteからI420の1,119,744,000 byteへ62.5%減る。一方、背景RGBAを渡す追加パイプが2,985,984,000 byte必要で、画像のプロセス間転送は合計4,105,728,000 byteとなる。コメントRGBAだけを渡す比較方式に対して37.5%増える。GPU合成＋RGBA出力の場合は5,971,968,000 byte。転送量減少を全体に適用して説明してはいけない。','',
'warmupを含む16本のMP4すべてで、FFmpegエラーなしdecode、360frame、frame index/60のPTS、H.264 1frame=1sliceを確認。NPS3の整数msはPTSへ流用していない。時間や速度の値は重なるpipelineの実時間であり、各段階のCPU待ち時間を加算していない。','',
'## 再現・成果物','',
'- 試験コード：`scripts/experiments/nico-gpu-compose/`。`generate.py`は既存の検証済みNPS3 parserを隔離複製し、`build.ps1`で専用EXEを生成する。',
'- `verify.py calibrate`、`verify.py quality`、`quick.py`の順。quick.pyは色変換比較、4条件×3回測定、動画検証を実施する。',
'- 生データ：`build/nico-gpu-compose/real-benchmark.json`、`quality/*/result.json`、`calibration/result.json`、`video-validation.json`。各resultにhash、argv、stderr。',
f'- GPU試験EXE SHA256：`{r.sha(r.EXE)}`。',
'- GPU出力見本：`build/nico-gpu-compose/results/real-gpu-i420-1/out.mp4`。比較見本：`build/nico-gpu-compose/results/real-hardware-1/out.mp4`。',
'- 画像：`build/nico-gpu-compose/quality/real-gpu-rgba-gpu-i420-yuv420p/a-75.png` と `b-75.png`。',
'',
'実装根拠：[Microsoft D3D11 compute shader](https://learn.microsoft.com/en-us/windows/win32/direct3d11/direct3d-11-advanced-stages-compute-create)、[FFmpeg filter仕様](https://ffmpeg.org/ffmpeg-filters.html#scale)。実際に使用したFFmpeg 9のCLI helpでrange・matrix・alpha_modeの対応も確認した。','']
path=r.ROOT/'docs/verification/niconico-comments/gpu-compose-2026-09-17.md';path.write_text('\n'.join(lines),encoding='utf8')
print(json.dumps(dict(medians=med,report=str(path),validated_mp4=len(validation)),ensure_ascii=False))
