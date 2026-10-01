# コメント画像合成の隔離実験

本番コード・稼働アプリを変更しない。2026-09-17 の `nico-native-render-test-plan.md` に対応。

## 再現

リポジトリ直下から、Windows / MSVC x64 / Python + numpy + Pillow を使う。

```powershell
rtk proxy pwsh -NoProfile -File scripts/experiments/nico-native-probe/build.ps1
rtk proxy python scripts/experiments/nico-native-probe/probe.py prepare
rtk proxy python scripts/experiments/nico-native-probe/probe.py quality --name real --variants baseline --original
rtk proxy python scripts/experiments/nico-native-probe/probe.py quality --name real --variants instanced,crop,hardware,hardware-instanced
rtk proxy python scripts/experiments/nico-native-probe/probe.py bench --name real --mode compose
rtk proxy python scripts/experiments/nico-native-probe/probe.py bench --name real --mode encode
rtk proxy python scripts/experiments/nico-native-probe/validate.py malformed
rtk proxy python scripts/experiments/nico-native-probe/validate.py videos
rtk proxy python scripts/experiments/nico-native-probe/validate.py artifact --mp4 <out.mp4> --playlist <playlist.m3u8> --frame-rate 30/1 --frames 180 --output <validation.json>
```

`artifact` はprobe固有の`config.json`が無いworker成果物にも期待FPS/フレーム数を明示して適用でき、MP4/HLSのdecode・PTS・AU/slice・segment keyframe・音声duration・byte/hashを1つのJSONへ保存する。

`f1` も同じ手順で検査する。`stress` は色・半透明・重なり・異なる rect/color・画面端・負の矩形幅・59.94fps を含む小さな人工入力。実素材の RGBA は全画素で RGB <= alpha を確認する。stress 内の透明 RGB 非ゼロだけは意図的な非 PMA 異常値である。

`diagnose.py` は raw RGBA のFFmpeg受渡しと、元動画の合成・色変換まで（エンコードなし）を各3回比較する。`summarize.py` → `report.py` の順に実行すると集計JSONと日本語検証書を生成する。`environment.py` は環境・実行物・ソースのハッシュを保存する。

`quality` は2プロセスを並行実行して全フレームを逐次比較する。これは品質検査専用で、性能測定には利用しない。`bench` は1条件ずつ実行し、各条件ウォームアップ1回の後、順序を変えて5回測る。プロセスは毎回新規で、入力のOSキャッシュが温まった条件。ブラウザ計測・コンパイルなど重い処理と同時に実行しない。

`compose` は fwrite だけ省略し、D3D11 の描画・CopyResource・staging Map を保持する。`encode` は実動画をデコードして1080p60fpsへ変換し、元の PMA RGBA コメントを重ねて libx264 CRF26 / veryfast / threads8、filter threads2 で MP4 にする。音声 AAC、faststart 完了まで計時する。元動画の取り込み・コメント取得・ブラウザ生成時間は別であり、この測定に含めない。

入力元は `build/nico-mask-probe/fixtures/*.reference.nmf1`。旧マスク実験の `.mask.nmf1` は禁止。実動画は `%TEMP%/imagepad-nico-perf-20260916/source.mp4`、FFmpeg は `build/nico-native-integration/tools` を使用。各実行の config.json に実際の argv と SHA256 を保存する。

## 判定

画素一致に失敗した候補は高速でも自動採用しない。CPU の描画投入時間は GPU の実行時間ではない。GPU timestamp を取得できた場合も、同期計測ありの実行を通常性能値に混ぜない。エンコードに GPU エンコーダーを使用しない。

参考: [Microsoft DrawInstanced API](https://learn.microsoft.com/en-us/windows/win32/api/d3d11/nf-d3d11-id3d11devicecontext-drawinstanced)。
## T11 matrix report

`t11_matrix.py` が保存した manifest は、ケース結果を上書きせずに次のコマンドで監査用JSON/Markdownへ変換できる。未実行・失敗・timeoutは `invalid_runs` に残り、`complete=false` のままなのでPASSへ読み替えない。

```powershell
& $python scripts/experiments/nico-native-probe/t11_report.py `
  --manifest build/nico-cpu20/t11-matrix-<run-id>/manifest.json `
  --output build/nico-cpu20/t11-matrix-<run-id>/report.json `
  --markdown-output build/nico-cpu20/t11-matrix-<run-id>/report.md
```

保存済みartifactの`runner_report`、`artifact`、`metrics`をケース結果へ含めると、CPU秒/動画秒、CPU時間率、出力bytes、SHA256、AB/BA paired reductionも集計する。

実production matrixは、先に`t11_matrix.py`で専用runを作り、`t11_execute.py`を再開可能な状態で実行する。Aはbrowser baseline、Bはnative candidateが既定で、既存caseはスキップされる。

```powershell
rtk proxy python scripts/experiments/nico-native-probe/t11_matrix.py --run-root build/nico-cpu20 --run-id <run-id>
rtk proxy python scripts/experiments/nico-native-probe/t11_execute.py `
  --run-dir build/nico-cpu20/<run-id> `
  --runner build/nico-cpu20-tools/nico-budget-runner.exe `
  --test-executable build/nico-cpu20-tools/video-production-embedded.exe `
  --snapshot <snapshot.json> --snapshot-high-density <snapshot-high-density.json> `
  --source-fixed <source-6s.mp4> `
  --source-real <source.mp4> --ffmpeg <ffmpeg.exe>
```

`--compositor <nico-compositor.exe>`は、埋込みpayloadと明示helperの差を切り分ける診断専用指定である。指定したhelperのhashはcase結果へ保存するが、埋込み本番runtimeの成功や採用へ読み替えない。

native runtime失敗後のbrowser再生成を再現する場合は、`build.ps1`が生成する失敗注入helperを明示する。これは`--self-test`だけABI文字列を返し、`--stdin`では終了する診断専用プロセスである。`--allow-native-fallback`も診断時だけautoのbrowser結果を受入れ、通常の`auto`/`native`でnative必須という受入れ条件は変更しない。

```powershell
rtk proxy pwsh -NoProfile -File scripts/experiments/nico-native-probe/build.ps1
rtk proxy python3 scripts/experiments/nico-native-probe/t11_execute.py `
  --run-dir build/nico-cpu20/<run-id> `
  --runner build/nico-cpu20-tools/nico-budget-runner.exe `
  --test-executable build/nico-cpu20-tools/video-production-fallback-current.exe `
  --snapshot <snapshot.json> --source-fixed <source-6s.mp4> `
  --source-real <source.mp4> --ffmpeg <ffmpeg.exe> `
  --backend-a browser --backend-b auto `
  --compositor build/nico-native-probe/nico-native-failing-compositor.exe `
  --allow-native-fallback --scenario fixed-6s --limit 3
```

成功時はcase結果に`native_render_encode`の失敗タイミング、`native_compositor_runtime_failure`、browser fallback理由、MP4/HLS検証、CPU20% Jobの`verified=true`が残る。これは合成runtime failureからの再生成E2Eであり、実GPUクラッシュまたはVRChat併用受入れの代替ではない。

`--browser <browser.exe>`も同じく診断専用で、Playwright Chromiumや別Chrome版を指定した場合の実行物hashをcaseへ保存する。未指定時は環境変数/renderer既定のブラウザを使う。

`--limit 1`は再開確認用であり、受入れ完了には使わない。高密度素材は`--source-high-density`で別素材を明示する。

高密度診断用の派生入力は、元snapshotを変更せず次で生成できる。synthetic入力なので、実素材155秒の受入れ証拠とは分けて扱う。

```powershell
rtk proxy python scripts/experiments/nico-native-probe/make_t11_fixture.py `
  --source <snapshot.json> --output build/nico-cpu20/t11-fixtures/snapshot-high-density.json `
  --duration-ms 10000 --multiplier 4
```

## T11観測

`t11_execute.py`は各caseを`nico-budget-runner`へ渡すと同時に、`observation.json`と`observation.jsonl`を保存する。観測内容はCPU20 Jobのrunner reportとは分離し、プロセスPID/CPU秒/WorkingSet/Handle、終了後の残留、`nvidia-smi`のGPU/エンコーダ/VRAM、VRChatプロセスの存在と未取得frame timeを記録する。VRChatのframe timeがないrunは必ず`vrc_acceptance_pending=true`となる。

WMI `Win32_Process`が拒否されるWindowsでは`Get-Process`へフォールバックする。この場合は親PIDが取れないため、`child_residue.tree_complete=false`となり、CPU20% Jobの所属とCPU秒は`runner-report.json`を正とする。GPU/VRAMが利用できない場合もゼロへ置き換えず、`unavailable_reasons`に残す。

レポートには観測ゲートが追加される。

```powershell
& $python scripts/experiments/nico-native-probe/t11_report.py `
  --manifest build/nico-cpu20/t11-observe-<run-id>/manifest.json `
  --output build/nico-cpu20/t11-observe-<run-id>/report.json `
  --markdown-output build/nico-cpu20/t11-observe-<run-id>/report.md
```

`observation_gate.vrc_gate`は実VRChat frame-time計測を別途完了するまで`pending`のままであり、観測が存在することだけではT11完了にならない。`observation_summary.ranges`には観測CPU秒、WorkingSet、Handle、GPU使用率、VRAMのmin/maxが入り、`cpu20_verified_cases`はJob reportの`verified=true`だけを数える。

外部PresentMonを別収録した場合は、`presentmon_report.py`で書き出し区間と同一CSV内の開始前baselineを分離し、`t11_report.py --vrchat-presentmon <summary.json>`で監査レポートへ添付できる。要約にはp50/p95/p99を保存し、PresentMonのframe interval p99をT11契約の1% low相当として扱う。外部計測が添付されても、paired反復・前後baseline整合性・継続的hitchの判定が揃わなければ`vrc_gate`は`pending`のままである。
外部acceptanceが`pass`になっても、`t11_report.py`は各caseのCPU20% Job `verified=true`、CPU観測値、GPU/VRAM観測、VRChat PIDの存在とPresentMon対象PID一致、child residueの消失（tree completeを含む）、matrix完了を同時に確認する。いずれかが欠ければ`vrc_integrated_gate=pending`、外部acceptanceが`fail`なら`vrc_gate=fail`とし、外部PASS単独や欠測値を本番受入れPASSへ変換しない。

```powershell
rtk proxy python scripts/experiments/nico-native-probe/presentmon_report.py `
  --csv build/nico-cpu20/vrchat-presentmon-real-<run-id>.csv `
  --process-id <vrchat-pid> `
  --start-time <csv-clock-export-start> --end-time <csv-clock-export-end> `
  --baseline-start-time <csv-clock-capture-start> `
  --baseline-end-time <csv-clock-export-start> `
  --post-baseline-start-time <csv-clock-post-baseline-start> `
  --post-baseline-end-time <csv-clock-post-baseline-end> `
  --continuous-hitching false `
  --output build/nico-cpu20/vrchat-presentmon-real-<run-id>.summary.json

rtk proxy python scripts/experiments/nico-native-probe/t11_report.py `
  --manifest build/nico-cpu20/<run-id>/manifest.json `
  --output build/nico-cpu20/<run-id>/report-vrchat-presentmon.json `
  --vrchat-presentmon build/nico-cpu20/vrchat-presentmon-real-<run-id>.summary.json
```

`--output`はPowerShellのリダイレクトを経由せずUTF-8でJSONを書き出す。これを使わず`>`で保存すると、Windows PowerShellの版によっては`t11_report.py`のUTF-8読み込みと合わないため、監査用summaryではシェルリダイレクトを使わない。

ブラウザ境界の診断では`IMAGEPAD_NICONICO_RENDER_GPU`（`disabled`が既定）と`IMAGEPAD_NICONICO_RENDER_CAPTURE=2d|none`を任意で指定できる。`2d`はWebGL2を使わずCanvas 2Dでreadbackし、`none`は描画/readbackを省略してCDP/WebSocketだけを確認する診断専用経路であり、どちらも性能・画質・本番受入れのPASSには使わない。

Chrome 153のDawn Graphite persistent cache切り分けには`IMAGEPAD_NICONICO_RENDER_GPU=disabled-no-dawn-cache`を使える。これは`--disable-features=SkiaGraphiteUsePersistentCache`を付ける診断専用経路で、既定値・採用判定は変更しない。

GPU sandbox境界の再確認には`IMAGEPAD_NICONICO_RENDER_GPU=disabled-no-gpu-sandbox`を使える。`--disable-gpu-sandbox`を付ける診断専用経路であり、sandboxを弱めるため既定値・採用経路へ昇格させない。

native compositor層の繰返し安定性は、raw frameを保存せずCPU20% Jobとobserverを組み合わせる`t11_stability.py`で切り分けられる。これはbrowser/worker統合やVRChat受入れの代替ではない。

```powershell
rtk proxy python scripts/experiments/nico-native-probe/t11_stability.py `
  --run-dir build/nico-cpu20/t11-native-stability-<run-id> `
  --runner build/nico-cpu20-tools/nico-budget-runner.exe `
  --python <bundled-python.exe> `
  --compositor build/nico-native-integration/ci-without-rtk/nico-compositor.exe `
  --fixture build/nico-native-probe/fixtures/real.nps3 `
  --duration-s 1800 --min-iterations 1 --sample-ms 1000
```

`IMAGEPAD_NICONICO_RENDER_CDP_TRACE=1`を追加すると、CDPメソッドごとの経過時間と切断位置をstderrへ記録する。`IMAGEPAD_NICONICO_RENDER_DEBUG=1`と併用するとブラウザstderrも保存され、`t11_execute.py`がGPU helper終了・CDP切断・native scene切断などを`failure_classification`へ分類する。これは失敗原因の固定化専用で、既定動作と受入れ判定を変更しない。

## T6受入れ評価

`worker_t11_stability.py`はCPU20% Jobの検証結果、backend、出力モード、worker/FFmpeg/source/snapshotとrendererのhashを保存する。baseline/candidateの保存済みレポートは次の評価器で比較する。

```powershell
rtk proxy python scripts/experiments/nico-native-probe/t6_acceptance.py `
  --baseline-report build/nico-cpu20/<baseline>/stability-report.json `
  --candidate-report build/nico-cpu20/<candidate>/stability-report.json `
  --baseline-matrix-report build/nico-cpu20/<baseline-matrix>/report.json `
  --candidate-matrix-report build/nico-cpu20/<candidate-matrix>/report.json `
  --operational-report build/nico-cpu20/<operational>/operational.json `
  --statistics-report build/nico-cpu20/<statistics>/statistics.json `
  --candidate-output-mode tee `
  --vrchat-report build/nico-cpu20/<vrchat>/vrchat-acceptance.json `
  --output build/nico-cpu20/<run-id>/t6-acceptance.json
```

評価器は、完走・CPU20%・成果物/event・共通入力hash/実行パラメータ・全シナリオ/品質matrix・cancel/re-run・30分連続・40ペア/条件・10,000回bootstrap・VRChatのbaseline-before/candidate/baseline-after各60秒、p95/p99、実プレイヤー映像音声をすべて要求する。欠測・失敗・未実施は`blocked`となり、速度改善率は算出しない。native backendではbrowser hashの代わりにcompositor hashを比較する。

速度統計は、各シナリオの完了済み`worker_t11_stability.py`レポートをペア化して`t6_statistics.py`で作成する。40ペア未満、失敗run、CPU合計欠測、5%/0.25秒の候補閾値未達は`blocked`として出力し、bootstrapは固定seed・10,000再標本・type 7 p95を使用する。

```powershell
rtk proxy python scripts/experiments/nico-native-probe/t6_statistics.py `
  --input build/nico-cpu20/<statistics-input>.json `
  --output build/nico-cpu20/<statistics>/statistics.json
```

入力JSONは`scenarios`配下に`fixed-6s`、`high-density-10s`、`real-source-155s`を置き、それぞれに`baseline_reports`と`candidate_reports`のレポートパス配列を指定する。

保存済みRGBAとnative連続raw streamの同一frame比較は、入力欠損・サイズ不一致を失敗として扱う`raw_pixel_compare.py`で行う。`--frames`が全frameでない場合は結果の`scope`が`sparse`になり、完全一致しても全pixel parityの受入れPASSにはならない。

```powershell
rtk proxy python scripts/experiments/nico-native-probe/raw_pixel_compare.py `
  --browser-dir build/nico-mask-probe/fixtures `
  --native build/nico-cpu20/t11-scene-real-nps3-20260921/frames.bin `
  --width 1920 --height 1080 --frames 0,30,75,180,300,359 --total-frames 360 `
  --output build/nico-cpu20/t11-scene-real-nps3-20260921/raw-pixel-sparse-compare.json
```
