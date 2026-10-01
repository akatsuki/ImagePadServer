# ニコニココメント書き出し CPU20% 実装計画

> 実装担当: 本書の依存関係とファイル所有権に従い、`parallel-task` で開始可能なwaveだけを実行する。各タスクはチェックボックスで追跡し、mainが結果を検証する。共有の性能測定機は同時に1試験だけ使用する。

**作成日:** 2026-09-21

**Goal:** VRChat併用・書き出し系CPU約20%以内で、現在のコメント表示とMP4/HLS互換性を保ち、測定に基づく高速化を実装する。

**Architecture:** まず一意なrun保存・全プロセスCPU予算・30fps本番相当の計測を整備する。段階別の比較で支配要因を確定し、thread/filter調整、必要なら転送・合成構造変更を選抜する。合格候補だけを既存native/browserパイプラインへ統合する。

**Tech Stack:** Go、Python3、MSVC C++、Windows Job Object、D3D11 WARP、既存FFmpeg/libx264、NPS3。GPU/libav経路は条件付き試作。

**Spec:** [計測と24案の再監査](docs/verification/niconico-comments/cpu20-audit-2026-09-21.md)、[H.264互換性契約](docs/RTSP_H264_COMPATIBILITY_CONTRACT.md)。

**Status:** 実装中。T1/T2の基盤、T3の段階計測、T10のCPU20% worker/staging・queue・公開commit・HTTP fake経路、実worker HTTP（native/fallback/cancel）とfallback後HLS失敗cleanup、起動時の古いNico staging回収、対象回帰を実装/検証済み。T4はCPU20%診断プローブでthread/filter候補の反復比較まで進行したが、本番昇格判定は未完了。現行ソースのbrowser worker 30分再測定は完了したが、fallback途中失敗からの実compositor再生成、prune/cleanup失敗診断、実動画固定sceneの完全受入れ、alpha parity、VRChat併用受入れ、T11の30分統合受入れは未完了。

## 1. 共通条件と判断基準

- CPU約20%は書き出しに関係する全プロセスの合算。Chrome子プロセス、renderer/Go worker、native helper、FFmpeg、HLS remuxを含む。VRChatとサーバー全体を制限Jobへ入れない。
- Windows基準は `ENABLE | HARD_CAP`、`CpuRate=2000`。全論理CPUでの並列性を保つ。上位Jobの制約、実効affinity、OSの論理CPU数、Job所属を記録する。制約を証明できないrunは `budget_unverified`。1CPU固定は参考比較としてのみ保持。
- 画質本線は同じ出力解像度、30/1fps、選択済み品質presetのCRF/音声設定、veryfast、FrameClock、描画順、字形、色、縁取りを維持する。速度のためのCRF増加・fps低下・量子化は既定に入れない。
- `sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0` と実AU=1sliceを維持。4秒keyframe/HLS独立segment、PTS、音声、失敗時の既存公開物保持も必須。
- dirty worktreeの無関係変更を保護。変更前にHEADだけでなく対象ファイルhashと差分を記録。commit/push/release、稼働アプリの切替はこの計画作成に含めない。
- 大幅改善の目標は**有効な同条件E2E baseline比で所要時間50%以上短縮**。これは目標であり見込み値ではない。通常の候補採用は5%以上の短縮と反復の一貫性を目安にする。
- CPU採用目安: 1秒集計の対象Job使用率が約20%以内、10秒平均が22%を継続して超えるrunは原因確認。単発250msの値はcap判定に使わず時系列として保存。CPU時間とcycle quotaの測定差も扱う。
- VRChat同一場面・同一FPS上限で、書き出し前/中/後のframe timeを比較。既存のVRChat設定を勝手に変更せず、モード・場面・描画設定・FPS上限を記録する。暫定gateはp95悪化10%以内、1% low悪化10%以内、新規の継続的な引っ掛かりなし。前後の基準同士が10%以上変わればそのrunは比較不能。frame timeを取得できない場合は `vrc_acceptance_pending` とし、本番受入れ完了にしない。
- 低リスク最適化でも新しいCPU20%性能runの全出力を品質検証する。旧ログのPASSは実行物が異なる候補に流用しない。

## 2. レビューで重点的に見る境界

1. VFR/29.97/59.94fps、非ゼロ開始PTS、短い末尾、無音/短い音声でもコメントFrameClockと終端が崩れないこと → T3/T5/T10。
2. PMA/straight alpha、色範囲、細い色文字、半透明、4:3/1:1/縦画面で見た目が変わらないこと → T3/T5/T7/T8。
3. 子プロセスがJob外へ逃げる、起動失敗、親Job制約、キャンセル時にCPU capとcleanupが成立すること → T2/T10。
4. GPU/driver不適合・device lost・EOF・encoder EAGAIN・進捗停止でも部分MP4を公開しないこと → T7/T8/T9/T10。
5. 同時書き出し、native失敗からbrowserへのfallback、既存公開中の映像に影響しないこと → T10/T11。

## 3. ファイル責務と作業分担

|範囲|既存/追加ファイル|責務・所有者|
|---|---|---|
|実験記録|`scripts/experiments/nico-native-probe/probe.py`、新規 `budget_bench.py` / `test_budget_bench.py` / `report_budget.py`|T1/T4担当。run管理・manifest・実験行列。既存fixtureに上書きしない|
|予算制御|新規 `internal/nicoexportbudget/{job_windows.go,job_other.go,job_windows_test.go}`、`cmd/nico-budget-runner/main.go`|T2担当。benchmark/製品両方で使える全子プロセスJob所有権|
|本番相当計測|`internal/video/niconico_production_test.go`、新規 `niconico_budget_profile_test.go`、native probe `main.cpp`|T3担当。タイマー・raw参照・FFmpeg段階別診断|
|引数|`internal/video/niconico_args.go`、`niconico_encode.go`、新規 `niconico_policy.go` / `niconico_policy_test.go`|T5/T10で順番に所有。テスト用設定を通常APIへ広げすぎない|
|合成試作|新規 `scripts/experiments/nico-cpu20-transfer/`|T7担当。既存native/mask試作を参照、正規helperを直接置換しない|
|GPU試作|新規 `scripts/experiments/nico-cpu20-gpu/`|T6/T8/T9で各ファイル分離。既存GPU試作を基準として参照|
|製品統合|`cmd/imagepadserver/main.go`、新規 `internal/nicoexportworker/`、`internal/server/niconico_upload.go`、`internal/video/niconico_native.go` / `niconico_pipeline.go`|T10はmainが担当。server/trayを起動しない専用worker entryと進捗/公開処理を統合|
|完成物の登録|新規 `internal/video/niconico_prepared.go` / `niconico_prepared_test.go`、`internal/library/nico_prepared.go` / `nico_prepared_test.go`、`internal/server/niconico_upload_test.go`|T10はmainが担当。完成HLSの命名/検査、snapshotを含む公開確定、エラー時の旧状態保持|
|結果文書|新規 `docs/verification/niconico-comments/cpu20-results-2026-09-21.md`|T11担当。有効/無効run、未完了実機gateを分ける|

既存 `internal/video/ffmpeg_job_windows.go` はkill-on-close専用でCPU quotaはない。共有領域の既存挙動を一律変更せず、新しい予算パッケージからNicoの対象だけを管理する。nested Jobの干渉はT2で検査する。

## 4. 依存関係

```text
T1 記録の再現性 ─┐
T2 CPU予算制御 ─┼→ T3 正規baseline・段階診断 → T4 thread選抜 ─┐
                 │                            T5 filter/音声 ─┤
                 │                            T6 HW decode ──┤
                 └───────────────────────────────────────────┤
                     条件付き T7 転送/合成 ← T4,T5 ──────────┤
                     条件付き T8 最終合成YUV ← T6,T7 ────────┤
                     条件付き T9 encode方式 ← T4,T6 ─────────┤
                                                            ↓
                                             T10 統合 → T11 全体受入れ
```

T7/T8/T9は全案を実装する義務ではない。開始条件を満たさなければ理由をdecision.jsonへ記録し「不採用/対象外」として依存を解決する。T10は実施した候補すべての判定後に開始する。

### T1: 再現可能なrun保存とmanifest

- **depends_on:** []
- **location:** `scripts/experiments/nico-native-probe/probe.py`、新規 `budget_bench.py` / `test_budget_bench.py` / `report_budget.py`
- **description:** 過去ログの上書きと試作/本番設定の混同を防止。
- **validation:** 同run IDの2回目起動は処理開始前に失敗、失敗runもexit/statusを保存、偶数nの中央値が正しい。
- **status:** Completed (2026-09-21, foundation)

- [ ] `--run-root` / `--run-id` / `--profile production30|legacy60` を追加。legacy60は既存比較専用でproduction集計へ混ぜない。保存先を関数引数へ渡し、runpyのglobals差替えに依存しない。
- [ ] `mkdir(exist_ok=False)` 相当で `build/nico-cpu20/<run-id>/` を排他的に作成。warmup、case、repeatを子フォルダーへ分離。既存runの更新を拒否する。
- [ ] manifestにschema_version、HEAD、dirty対象hash、helper/FFmpeg/Chrome/fonts/source/snapshot/scene hash、OS/GPU/driver、全argv、thread指定、budget、画質/時刻/alpha条件を保存。
- [ ] 各caseはstart/end、全PID/親PID、exit、timeout、timers、raw bytes、validation、除外理由を保存。CSVはJSONから生成する派生物にする。
- [ ] 成功/失敗/重複ID/2件medianのテストを実装して実行。

実装済み: `budget_bench.py`、`test_budget_bench.py`、`report_budget.py`。run ID排他、失敗case/manifest保存、profile別集計、偶数件中央値、artifact/hash/dirty snapshotを追加。`python3 -m unittest discover -s scripts/experiments/nico-native-probe -p "test_budget_*.py"` は4件PASS。

記録形式の最低契約:

```json
{"schema_version":1,"run_id":"unique","profile":"production30",
 "budget":{"requested_percent":20,"verified":false},
 "status":"pending","pids":[],"timers_ms":{},
 "cpu_samples":[],"validation":{"decode":null,"pts":null,"slice":null,"pixels":null},
 "invalid_reason":null}
```

### T2: 子プロセス全体のCPU20%制御と観測

- **depends_on:** []
- **location:** `internal/nicoexportbudget/`、`cmd/nico-budget-runner/main.go`
- **description:** Job Objectで測定対象の合算quotaを設定し、実効値とCPU時系列を残す。
- **validation:** CPU負荷を持つ親子2プロセスを含むjobが総20%相当へ収束し、対象外sentinelはJob外、失敗時に無制限実行しない。
- **status:** Completed (2026-09-21, foundation)

- [ ] 以下の共通APIを定義する。cancelは所有Jobだけ終了し、外部PIDは操作しない。

```go
type Options struct { Percent uint32; SampleInterval time.Duration }
type ProcessSpec struct {
    Exe string; Args []string; Dir string; Env []string
    Stdin io.Reader; Stdout io.Writer; Stderr io.Writer
}
type Report struct { Verified bool; ExitCode int; CPUSeconds float64; Samples []Sample; Reason string }
type Sample struct { At time.Duration; CPUPercent float64; PIDs []uint32 }
func Run(ctx context.Context, spec ProcessSpec, options Options) (Report, error)
```

- [ ] Jobのhard cap=2000とkill-on-closeを設定。子をsuspended生成し、Job所属確認後に再開する。実装上この順序を満たせないなら、最初にreadyハンドシェイクして重い処理を開始しないworker方式を使う。対応外は明示エラー。
- [ ] OS全体とprocessから見える論理CPU数、affinity、親Job、子Job所属を記録。上位quotaの有無が確定できなければOS全体20%として認定しない。通常ユーザー・nested Job・Chrome sandboxで実測する。
- [ ] Job accountingを使い、終了済みの短命子も含めたCPU時間を250ms間隔で採取。OS総CPU/VRChat CPU/GPU使用率は別レコード、1秒と10秒へ再集計。別プロセス名の一括停止は実装しない。
- [ ] frame timeはデスクトップモードなら対象PIDのPresentMon記録、VRモードならVRランタイムが出すapp/compositor timingを使用し、採取元・単位・欠測を保存する。取得手段が利用できなければFPSを推測せずpending。検査用の合成CPU負荷はT2のquota自己試験だけに使い、VRChat併用性能の代用にしない。
- [ ] `Stdin` はversion付き有限request、`Stdout` は進捗/完了JSON、`Stderr` は診断として独立に同時drainする。nilの入出力はEOF/Discardとして扱う。Runは対象process終了に加えI/O goroutine終了まで待つ。JSON parserは1行64KiB、stderr保存は末尾64KiB上限。無制限のbytes.Bufferや進捗channel待ちを作らない。
- [ ] 上流入力、出力writer、parserのどれかが失敗したら同じcontextをcancelし、pipeをclose、所有Jobを終了してWait/drainする。callerが与えるReader/Writerはcancelで終了可能なものを使い、返却後にgoroutineを残さない。大量stdout/stderrを同時に出すfixture、遅い受信、EPIPE、途中cancel、完了後EOF欠落をT2の試験に含める。
- [ ] parent crash、途中子spawn、nested Job拒否、cancel、stdout切断をテスト。VRChatを実験用負荷生成器として操作しない。非Windowsはcap未対応を明示してビルドを保つ。
- [ ] CLIは `nico-budget-runner --cpu-percent 20 --record <path> -- <exe> <args...>` とし、記録失敗/制約失敗を非ゼロで返す。

実装済み: `internal/nicoexportbudget/` と `cmd/nico-budget-runner/`。Windows Job Object hard cap、kill-on-close、suspended起動→Job所属→resume、同時stdout/stderr drain、CPU accounting、PID収集、cancel cleanup、64KiB stdout行/stderr tail、JSON recordを追加。CPU率は論理CPU数で正規化し、親子プロセスの合算を20%制約として観測する。Windows単体テスト3件、CLIテスト2件、Windows/Linux build、CLI smokeを実施。

### T3: 30fps基準、alpha契約、段階別タイマー

- **depends_on:** [T1, T2]
- **location:** `internal/video/niconico_budget_profile_test.go`、`niconico_production_test.go`、`scripts/experiments/nico-native-probe/{main.cpp,ffmpeg_diagnostic.py}`、既存 `native_profile_test.go`
- **description:** 本番APIと同じ引数のbaselineを確立し、FFmpeg下流を切り分ける。alpha差が確認されたら速度変更とは別の正しさ修正として基準を再生成。
- **validation:** production30の実argvと `nicoEncodeArgs` が一致。各段階の開始/終了とbyte数、MP4/HLS総時間、CPU、品質が保存される。
- **status:** In Progress (2026-09-21)

実装済み: `NicoStageTiming` に開始/終了時刻・経過時間・バイト数を追加し、browser/nativeのraw frame送出・native readback・出力検証を記録。30fps/1080p/CRF26/AAC160k/one-sliceのproduction30契約テストを追加。native probeにinit/shader/input/texture/draw/CopyResource/Map/row-copy/fwrite/flushのwallタイマーとbyte countersを追加し、instrumented/uninstrumentedの同一fixture比較を実施。`ffmpeg_diagnostic.py` は `ffmpeg -h full` で benchmark capability を確認し、未対応フラグを拒否する。実FFmpeg 8.1.1でbenchmark_all+verboseのsynthetic null runはPASS。CPU20 browser試験ではJob内のGPU helper障害を診断したが、`--disable-gpu-sandbox` はreadback停止へ変化したため採用せず、元の起動引数を維持した。実動画固定scene、PMA/straight alpha照合、CPU20+VRChat実測は未実施。

- [ ] 実動画+snapshotから本番30fpsの固定sceneを生成。既存60fps sceneをheader変更で流用しない。選択済みアプリ品質と固定1080p/CRF26/AAC160kの再現用profileを識別して記録する。
- [ ] PMA/straightを既知の半透明色画素・白黒/色背景・文字境界で検査。native/browser/probeの全系統の実pixelとFFmpeg入力alpha metadataを照合。renderer差は既存許容差を維持、同じ入力RGBAの後段比較は完全一致を要求。
- [ ] nativeへinit(device/shader)、input、texture、draw submit、CopyResource呼出し、Map、row copy、fwrite、flushをそれぞれ計測するprofile版を用意。CPU時間とwall時間を区別し、instrumentationあり/なしの1組で計測負荷を確認。
- [x] FFmpeg `-benchmark`、利用可能なら `-benchmark_all`、verbose filter negotiationを別diagnostic runへ保存。通常timing runのverbosityは固定。未知オプションはhelpで拒否し、有効と偽らない。
- [ ] 次の比較を直列実行。差分の引き算を厳密な段階時間とは呼ばない。

|case|残す処理|問う仮説|
|D0|native compose/readback、出力discard|WARP/MapがCPU時間を消費しているか|
|D1|native→pipe→raw受取→null|copy/syscall/pipeが支配的か|
|D2|元動画decode→null|decoderが支配的か|
|D3|decode+本番scale/pad/fps→null|解像度とframe処理が支配的か|
|D4|本番overlay+YUV→null、音声なし|alpha合成/色変換が支配的か|
|D5|D4+libx264→null、音声なし|H.264追加の影響|
|D6|本番MP4+AAC+faststart|audio/mux/末尾処理を含む完成物|
|D7|browser生成を含む通常API+HLS|採用判断に使うE2E|

- [ ] 固定YUV参照からのencoder単独runも用意し、ファイルIO時間を別記。Chrome起動/初期化/take/Go展開/書込も既存profileを再利用。初回shader等とsteady-stateを区別する。
- [ ] 全probeが終了してから別工程で品質検査。失敗runは集計から除外しログ保持。CPU cap/VRChat計測が欠けたrunを本番採用の基準へ入れない。

### T4: CPU予算に合うthread数の選抜

- **depends_on:** [T3]
- **location:** `budget_bench.py`、`report_budget.py`
- **description:** decoder/filter/encoderの過剰並列化を単変量で調べる。24案で欠けていた本線の優先候補。
- **validation:** 同じ入力・品質・capで実argvと実CPUが記録され、勝者が反復と長尺の双方で改善。
- **status:** In Progress (CPU20%診断プローブの短尺反復比較まで実施。本番30fps/長尺・E2E昇格判定は未完了)

- [ ] 最初にencoder threadsを1/2/4/8、他は固定して比較。次に勝者固定でfilter_complex_threads=1/2、最後にdecoder threads=1/2を比較する。全組合せを無条件に総当たりしない。
- [ ] inputの `-threads` とoutputの `-threads:v` の位置を別々に指定し、FFmpegログで実効設定を確認する。
- [ ] 各候補のcoded outputが変わり得ることを明示し、pre-encode frame一致、slice、decode後の品質を検査。thread数変更による微小な符号化差は別記し、自動で許容しない。
- [ ] 短尺の勝者のみ155秒/高密度/長時間へ進め、選抜設定を `decision.json` に保存。無改善なら元設定を維持する。

2026-09-22診断プローブ: 実素材NPS3の360 frame/60fps native encodeを`nico-budget-runner --cpu-percent 20`下で比較した。encoder=1/2/4/8（filter=2、decoder=2）の一回比較はそれぞれwall 10.90/11.11/11.05/11.05秒で、全候補がJob `verified=true`、MP4の360 frame/60fps/PTS/decode/AU 1 sliceをPASSした。追試の3回中央値では`filter=1,decoder=1,encoder=1`が10.849秒、`filter=2,decoder=2,encoder=8`が11.080秒（約2.1%短縮）で、CPU秒はrunner全体70.421875対73.25、出力hashは`036068d...`対`e89aea8...`と異なった。これはWARP/60fpsの診断プローブであり、本番30fps・browser/worker・155秒/高密度・VRChat同時実行を含まないため、本番引数へはまだ昇格しない。
2026-09-22本番相当worker再測定: `NicoEncodeOptions`とworker protocolへ任意thread指定（0は従来FFmpeg既定）を通す診断経路を追加し、現行専用workerをCPU20% Jobで実素材6秒/30fps native各3回実行した。`default-0-0-0`の中央値7.187秒に対し、`filter=1,decoder=1,encoder=1`は7.547秒（+5.0%）、`2/2/8`は7.594秒（+5.7%）。全caseでJob `verified=true`・exit 0、MP4 180 frame/30fps・PTS/decode/AU 1 slice、HLS 2 segment/keyframe/decode・音声6.016秒をPASSした。明示thread候補は現行既定より遅いため不採用とし、本番デフォルトは変更しない。結果は`build/nico-cpu20/t4-worker-thread-sweep-20260922/`に保存した。

### T5: 冗長filter・音声・最終muxの削減

- **depends_on:** [T3]
- **location:** `internal/video/niconico_policy.go` / `niconico_policy_test.go`、`niconico_args.go`、試験用 `budget_bench.py`
- **description:** #1–5の条件付き最適化とfps配置を1個ずつ検証する。
- **validation:** 同条件pre-encode全frame/PTS一致、音声/MP4/HLSの互換性、予算内E2E改善。
- **status:** Not Completed

- [ ] identity scale/pad判定は幅高さ・SAR・rotation・色変換の役割を含める。未知metadataは従来経路。テストは同サイズSAR1、同サイズ非正方SAR、720→1080、縦、4:3、1:1を含む。
- [ ] fps省略は証明済みCFR同値の場合だけ試す。VFR、29.97→30、非ゼロPTS、末尾欠落には適用しない。fpsをscale前へ置く候補は参照frameの選択が同じか全frame比較する。
- [ ] 実auto_scale列を記録し、alpha解釈を保って変換回数を削る。サブサンプリング順序が変わる候補は無劣化扱いにしない。
- [ ] 音声copyはno-filter、MP4/HLS対応AAC、duration/priming正常で、音声契約が元の圧縮bitrate維持を許す場合のみ。指定bitrateが必須なら入力が別bitrateのcopyは採用しない。AAC非対応profile、無音、音声先行/遅延、短い音声、48kHz以外を比較。拒否時は既存AAC経路。
- [ ] faststartなしは内部中間MP4専用flagで比較。現在公開される最終MP4には適用しない。最終muxが短ければ採用対象から外す。
- [ ] T4と独立に各効果を評価した後、勝者同士の組合せをT10で再検証する。削減率を足し算しない。

### T6: HW decodeの限定試作

- **depends_on:** [T3]
- **location:** `scripts/experiments/nico-cpu20-gpu/decode.py`
- **description:** #6、同じCPU側filter/encoderへdownloadする条件でCPU削減とwallを確認。
- **validation:** source codec/device capabilityを実行前に確認し、失敗/未対応は記録。正常候補は同pixel/PTS、GPU/VRAM、VRChat影響、全転送量を評価。
- **status:** Not Completed

- [ ] 手元のGPUで利用できるdecoderだけ列挙して1方式から試す。別vendor用オプションを無条件に選択しない。
- [ ] device初期化、decode、downloadをtimingに含める。HW decode単独の速度だけでは採用しない。
- [ ] CPU decodeとの画素比較、10bit/非対応codecのfallback、device lostをテストし、T3と同じ出力で評価。

### T7: 転送・readback・合成の選択的構造変更

- **depends_on:** [T3, T4, T5]
- **location:** `scripts/experiments/nico-cpu20-transfer/`、参照 `nico-mask-probe/` / `nico-native-probe/`
- **description:** #13–16、#19–22。D0/D1とCPUプロファイルが支配要因を示した部分だけ試作。
- **validation:** 同一RGBA全frame一致、ownership/EOF/cancel正当性、総CPU/総byte/全体wallで改善。
- **status:** Not Completed (conditional)

- [ ] pipe/syscall/コピーが支配的なら、既存libav試作を使い **同一native worker内のAVFrame受渡し** を先に比較。人工背景を実動画decodeへ置換し、既存RGBAを保持する。libav SDK版/配布条件も記録。
- [ ] 共有メモリを選ぶ場合はconsumerを同時実装する。ABIにversion、width/height/stride、format/alpha、frame index、有理数PTS、slot state、終了/エラーを持たせる。初期3slot、上限8slot。書込済→読取中→解放の所有権とcreditを検査。stock FFmpegの `pipe:0` にringが直接つながる前提を置かない。
- [ ] AVFrame経路はbuffer ref寿命、EAGAIN時の同frame再送、packet duration、flush/drainを既存mask試験に合わせて検証する。
- [ ] Map支配なら1/3/4/8 readbackを比較。GPU timingを追加する場合は実timestamp/disjointを取得し、未実装の `requested_unavailable` を値として使わない。
- [ ] compose CPU支配ならSIMDまたはdirty/cacheから1つを選ぶ。SIMDにはscalar対照、dirtyには旧新rect和集合/重なり再描画、cacheにはtexture世代/順序/行列/alphaのキーを必須とする。
- [ ] batchingはD1でsyscall支配の根拠がある場合だけ追加し、最大8frameとメモリー上限を明示。上流queue増加で総時間が変わらなければ棄却する。

### T8: 最終合成・色変換をまとめた経路

- **depends_on:** [T3, T6, T7]
- **location:** `scripts/experiments/nico-cpu20-gpu/compose/`
- **description:** 修正版#17/#18。D4が依然大きい場合のみ、コメントplaneのalphaを維持して背景と最終合成してからNV12/I420を生成。
- **validation:** 色/alpha/PTS・総転送量・VRChat負荷とE2Eが合格。過去GPU色差を再導入しない。
- **status:** Not Completed (conditional)

- [ ] 過去の「CPU背景RGBAを追加pipeで渡す」方式を再実装しない。device内decode→scale→RGBAコメント合成→最終YUVを基本候補にし、転送図とbyte見積りを先に作る。
- [ ] CPU x264継続案は最終YUVのみreadback。HW encoder案はT9の入力契約と共有textureで接続。GPU encodeを共有textureの必須条件としない。
- [ ] adapter LUID、texture format/stride、handle権限、同期、frame番号、寿命、device lostの契約を定義。旧CPU/FFmpegと同じrange/matrix/chroma sitingを再現する。
- [ ] U/V最大差13だった既存試作を負の対照にし、補間・丸めが合わない候補は速度に関係なく本線へ入れない。

### T9: エンコーダー変更の独立比較

- **depends_on:** [T3, T4, T6]
- **location:** `scripts/experiments/nico-cpu20-gpu/encode.py`、結果の `encoder-options.json`
- **description:** #7–12。D5とencoder単独で支配的と確認された場合の比較。品質変更を伴う候補は既定採用と切り離す。
- **validation:** CPU/GPU予算、実H.264全AU1slice、MP4/HLS、画質・容量・VRChat影響の比較表が完成。
- **status:** Not Completed (conditional)

- [ ] superfast、lookahead=0、B=0は各々独立にveryfast基準から比較し、presetとの設定重複を記録。CRF増加は要求画質を変えるので今回の本線では実行しない。
- [ ] HW H.264は既存機で利用できる1種類から試作。同CRF互換を仮定せず、同じpre-encode参照に対する文字ROI誤差・動画SSIM/PSNR・容量・見た目を比較。quality acceptanceは候補表で具体化し、変更前に本線同等と宣言しない。
- [ ] OpenH264は現ビルド非対応なら「未実施」と記録し、このためだけのFFmpeg全面置換をしない。
- [ ] 品質/容量トレードオフを伴う案は結果見本と数値を提示する段階まで。本線既定へ反映するにはその具体的なトレードオフの選択が必要。

### T10: 合格候補とCPU予算の製品統合

- **depends_on:** [T1, T2, T3, T4, T5, T6, T7, T8, T9]
- **location:** `cmd/imagepadserver/main.go`、新規 `internal/nicoexportworker/`、`internal/server/niconico_upload.go`、`internal/video/niconico_args.go` / `niconico_encode.go` / `niconico_native.go` / `niconico_pipeline.go`
- **description:** 受入れ済み候補だけを統合し、native/browser両系統とHLSを同じCPU予算に収める。startup/stateを触るためmainが担当する。
- **validation:** 成功/失敗/cancel/fallback/同時要求で予算、公開物、cleanupが正しい。予算失敗は無制限へsilent fallbackしない。
- **status:** In Progress (worker/stagingと公開commit/HTTP経路の実装・対象回帰まで完了、実workerのHTTP本番経路・cancel/fallback総合・VRChat併用は未完了, 2026-09-22)

- [x] `imagepadserver nico-export-worker` を通常 `app.Run()` より前にdispatch。同一exeの埋込helperを使い、server/tray/配信を起動しない。入力はversion付きJSON（source/snapshot/output/HLS staging/options/run_id）、stdoutは上限付き進捗/結果JSON、診断はstderr。
- [x] 親がT2のJobを所有し、専用worker、Chrome全子、native、FFmpegを同じ予算へ入れる。worker内でrender/encode/HLSを実行し、Go生成処理も合算する。アプリ親プロセスとVRChatはJob外。6秒/155秒native・browser workerとbrowser worker 30分で `verified=true` / `job_cpu_rate=2000` を確認した。
- [x] 同時書き出しはNico専用のアプリ単位queueで同時1件を初期仕様とする。後続要求は待機し、複数20%Jobを勝手に並列起動しない。queue待ちはrender時間と分けて表示/計測する。`waitForNiconicoIngest`、`nicoExportMu` と対象HTTP/待機contextテストで直列化を確認した。
- [x] 完成物の登録には下記APIを追加する。`EnqueueNicoCommentedVideoForID` は現行 `runQueueJob` で必ず `runNicoHLSCopy` を起動するため、新経路から再enqueueしない。新経路は完成状態を直接登録し `watchConversion` に再処理させない。通常動画/音楽/旧Nicoのqueue挙動は維持する。HTTPテストでworker一回・直接公開を確認した。

```go
// package video: FFmpeg起動なし。staging内の完成HLSを検査/命名する。
type PreparedNicoHLS struct { Playlist string; Segments []string }
func PrepareNicoHLSForID(stagingDir, mediaID, runID string) (PreparedNicoHLS, error)

// package library: 完成物をadmitし、最後にだけcurrent/historyを確定する。
type PreparedNicoMedia struct {
    Info CurrentImage
    SourcePath, ThumbnailPath, SnapshotPath, HLSDir string
    SelectCurrent bool
    ExpectedRevision int64
}
func (s *Store) CommitPreparedNicoMedia(p PreparedNicoMedia) (CurrentImage, error)
```

- [x] 親がworker開始前に新規media IDとrun IDを割り当て、request/result双方へ含める。stagingの `playlist.m3u8` / `segment-%05d.ts` を `current-<id>.m3u8` / `current-<id>-<run>-<index>.ts` に対応付け、playlist内URIも更新する。id/runは英数字・ハイフン・下線だけを許可、別ID/絶対パス/`..`/欠落segment/ENDLIST欠落を拒否。既存 `GeneratedFiles` / `CurrentStatusForID` / converted配信がこの名前で動く試験を追加する。markerを付けて未完了HLSを完成扱いにはしない。
- [x] `CommitPreparedNicoMedia` は新規ID専用。MP4、thumbnail、snapshot、HLS全参照を検査してStore所有stagingへ揃え、root側HLSと `converted/<id>`、`history-<id>.*`、`niconico-snapshot-<id>.json` の新規ファイルを先に準備する。旧IDのファイルは置換しない。既存パス検証・ファイル準備関数を再利用し、他方式のcommit関数の意味は変えない。
- [x] current選択の場合はStoreのlock内で `ExpectedRevision` を照合し、変更後metadataを別値として作る。全成果物の準備後、`state.json` を既存atomic writeで確定してからin-memory current/history/Converted/resolution/revisionを入れ替える。queue追加はhistoryだけを確定してcurrentを変えない。重いコピー/ハッシュはlock前に終える。既存 `SetCurrentInfo` は先にcurrentを変えるため、この処理の途中には使わない。current公開とqueue隔離のテストを追加した。
- [ ] snapshot書込み、segment配置、metadata永続化、revision競合の各段階へエラーを注入し、失敗時は新IDの所有物だけを掃除し、旧current/history/公開HLSが同じままであることを確認する。確定後のprune/cleanup失敗は成功済み公開をエラーへ戻さず診断する。プロセス強制終了で未参照stagingが残っても旧IDを削除しない。アプリの既存起動時workspace初期化仕様まで変更する計画ではない。snapshot rename失敗、欠落segment、state書込み失敗、revision競合、worker HLS失敗、encode中cancel、親HTTPリクエストキャンセル時の実worker停止・staging掃除・旧current保持は確認済み。`cleanupAbandonedNicoStaging`を追加し、起動時に1時間より古い直接配下の`.niconico-export-*` / `.niconico-prepared-*`だけを回収、最近のstagingと旧公開物を保持する単体テストはPASSしたが、実プロセス強制終了後の復旧runとprune/cleanup失敗診断は未完了。
- [ ] native不在のbrowser fallbackにも同じquota。途中失敗は最初から再生成するか明示失敗とし、部分streamを継ぎ足さない。cancel/親終了で所有Jobだけ停止、前の公開物を保持する。実worker HTTPでnative helper欠落→auto→browser、CPU20% Job、MP4/HLS、current/history/converted公開をsandbox外opt-inで確認し、実browser fallback後の注入HLS失敗でもpartial MP4/HLS掃除を確認した。HTTP worker失敗時の旧current保持とstaging掃除も回帰テストで確認済み。native runtime途中失敗時はautoが一時MP4へ最初からbrowser再生成し、成功時だけ公開出力へpromoteする単体テスト、browser再生成失敗時の旧出力保持テスト、実browser HLS失敗cleanup、実worker HTTP auto→browser公開を確認した。promote成功後のbackup cleanup失敗は警告ログ診断とテストを追加済み。native runtime失敗時にも`native_render_encode`のwall timingを保持する回帰を追加した。診断専用失敗注入compositorで、CPU20% Job下の`native-warp`実行失敗→browser再生成、MP4/HLS検証、Job `verified=true`までE2E確認済み。ただしこれは合成runtime failureであり、実GPUクラッシュおよびVRChat併用受入れの代替ではない。
- [ ] thread/filter個別勝者を組み合わせたE2Eを再測定。性能が戻る場合は相互作用のある候補を外す。新規runtimeを使う候補は実配布物hash/依存DLL/ライセンス検証も追加し、実機未確認のまま自動昇格しない。

2026-09-22 T4連携前進: 上記`1/1/1`候補と既定相当`2/2/8`を同一CPU20% Jobで各warmup+3回測定し、候補の短縮傾向と出力差を確認した。次の採否には本番30fpsの同一入力、155秒/高密度、browser/workerのE2E、品質matrix、VRChat frame-timeが必要であり、現時点では診断結果の保存までとする。
2026-09-22 T4採否: 30fps native workerへ接続した3回比較で、明示thread候補は現行FFmpeg既定より遅かったため不採用。T4の本番採用候補は残さず、thread指定の任意フィールドは診断経路として保持する。

実装済み（骨格）: `internal/nicoexportworker` は version=1 の有限requestと64KiB以内の進捗/result JSONを扱い、`cmd/imagepadserver nico-export-worker` は通常起動より前に専用dispatchする。親の `internal/server/niconico_worker.go` は T2 Job を20%で所有し、stdoutの結果identity（run/media/output/playlist）とJob検証を確認する。`waitForNiconicoIngest` と `nicoExportMu` によりHTTP入口からNico処理を同時1件へ待機直列化する。`library.Store.CommitPreparedNicoMedia` は新IDのMP4/thumbnail/snapshot/HLSを先にstagingから準備し、HLSを `current-<id>-<run>-<index>.ts` へ改名、playlist/converted/currentを揃えてから state/current を確定する。6秒実素材のworker実レンダーはnative backendでCPU20% Job、MP4/HLS検証、result event、cleanupまで確認済み。公開commit/queue/HTTP（fake worker）経路、実workerを接続したHTTP E2E、native helper欠落からbrowserへfallbackする実worker HTTP E2E、fallback後HLS失敗時の実browser cleanup、親HTTPキャンセルの実worker停止・旧current保持・staging掃除、queue待機/cancel境界、worker部分失敗cleanup試験を追加済み。fallback途中失敗からの再生成、VRChat併用、30分統合受入れは未検証。

### T11: 最終比較・回帰・VRChat受入れ

- **depends_on:** [T10]
- **location:** 既存Nicoテスト、`scripts/experiments/nico-native-probe/{validate.py,media_validation.py,t11_matrix.py}`、新規結果文書
- **description:** 同条件baselineと統合candidateを比較し、実用上の改善を確認する。
- **validation:** 下記matrixを満たし、全未確認事項を明記した採用/不採用報告を作成。
- **status:** In Progress (33ケースCPU20%行列、MP4/HLS品質検証、current/current raw全frame比較、native層30分反復、native/browser 6秒/155秒worker実E2E、browser worker 30分反復、公開commit/queue/実worker HTTP E2Eと対象回帰まで完了。VRChat併用frame-time gate、30分統合受入れは未完了)

2026-09-22 T4診断候補の品質確認: `build/nico-cpu20/t4-thread-sweep-20260922b/`で`1/1/1`と`2/2/8`を各warmup+3回実行し、両候補のMP4で360 frame/60fps/PTS/decode/AU 1 sliceをPASSした。出力hashは異なるためthread変更を無劣化とは扱わず、T11の本番候補には未昇格。VRChat不在のため同時実行受入れも引き続きpending。
- 2026-09-22 T11/T4 integrated thread rejection: `build/nico-cpu20/t4-worker-thread-sweep-20260922/`で現行専用workerを実素材6秒/30fps/native、CPU20% Job、各3回で比較した。`default-0-0-0`は中央値7.187秒、`1/1/1`は7.547秒、`2/2/8`は7.594秒。全3反復がcomplete、runner `verified=true`、MP4/HLS validation PASS。明示thread候補は現行既定より遅いため本番引数へ昇格せず、T11へ性能改善として持ち込まない。

- [x] スナップショット固定の6秒実素材、10秒高密度、155秒実素材を各warmup1回+5組でAB/BA順を交互に実行。各caseのtimeoutは10分、長尺は初回20%baselineの3倍を上限に30分まで。timeoutは失敗として残す。`build/nico-cpu20/t11-tpad-no-debug-smoke-20260921a/`で33/33 complete、CPU20% Job verifiedを確認した。
- [x] コメントなし、半透明/多色/重なり、clip、負のrect、4:3/1:1/縦、30/29.97/59.94/VFR、音声なし/遅延、短い末尾を品質matrixに入れる。59.94等はAPI境界テストであり本番30fpsを変更しない。33ケースのmanifest/reportに15条件を登録し、各caseの成果物検証まで完了した。
- [x] MP4/HLSを `-xerror` で全decode、ffprobe全frame PTS、有理数FrameClock、全AU1slice、segment先頭keyframe、音声durationを検証。155秒の旧4649生成/4647decode差は原因を記録し、差を無条件な許容幅にしない。`-frames:v`、`-t` envelope、`tpad`で4649/4649へ修正し、音声はtimestamp timelineで判定する。
- [x] 同frame入力のraw全pixel比較を先に行い、エンコーダー設定を変える場合はdecode後品質も検査。crop/hardware旧失敗を新EXEの合格へ読み替えない。現行browserが生成した`real.reference.nmf1`からnative NPS3を再生成し、browser/native各360 frameを比較。完全一致92/360、変更channel 35,232、最大channel差1、平均絶対channel差`1.1799125514403292e-05`で、既存renderer差の許容実測（real-crop max7、mean `8.618298021490672e-05`）内。完全一致PASSとは扱わず、差分統計を保存した。
- 2026-09-22に比較器 `scripts/experiments/nico-native-probe/raw_pixel_compare.py` とテストを追加し、保存済み同一実素材のnative全360 frame streamとbrowser RGBAの選択6フレーム（0/30/75/180/300/359）を比較した。native 360 frame、比較6 frame、完全一致1 frame、変更449 channel、最大channel差1、平均絶対channel差 `9.022151491769547e-06`。比較対象が6/360の疎比較で、browser側の全frame保存がないため、この結果はraw parityの受入れPASSではなく、チェックボックスは未完了のまま保持する。
- 2026-09-22に実browser probeの全frame保存opt-in `NICO_MASK_PROBE_DUMP_ALL=1`を追加し、package cwdからの`capture.js`解決も修正した。CPU20% Jobで再実行した実browser probeは`wsarecv: An existing connection was forcibly closed by the remote host`で1.66秒終了し、全frameRGBAは生成されなかった。別のCPU mask-probe試験器で保存した360 frameは不透明背景を含むためnative透明rawとの比較が0/360一致・最大差255となり、browser parity証拠から除外した。
- 2026-09-22に同probeをsandbox外でCPU20% Job実行し、GPU helperの`STATUS_ACCESS_DENIED`が消え、CDP/実browser probeが15.93秒でPASSすることを確認。REAL snapshotを明示した全360 frame保存もPASSし、現行browser referenceからnative NPS3を再生成してcurrent/current比較を完了した。結果は`build/nico-cpu20/t11-browser-native-parity-current-20260922d/raw-pixel-full-compare.json`。sandbox内失敗はbrowser実装不良ではなく実行境界依存として分離記録する。
- [ ] 連続30分の繰返しでメモリー/handle/child残留/CPU quotaを確認。VRChat frame time、GPU、VRAM、CPU総量を同時記録。品質比較・コンパイル・別benchmarkは同時に走らせない。
- 2026-09-22にnative層だけの60秒スモークを追加実行。`build/nico-cpu20/t11-native-stability-smoke-20260922a/`で実素材NPS3を36回、全回exit 0、observer 61 samples、peak WorkingSet 8,814,592 bytes、peak handles 134を記録した。CPU20% Jobは`verified=true`・`job_cpu_rate=2000`、CPU秒97.890625だった。WMI拒否によるGet-Process fallbackのためchild treeは不完全、VRChat frame-timeはpending。60秒native smokeであり、30分・browser/worker統合受入れの完了には数えない。
- 2026-09-22にnative層の30分反復を実行。`build/nico-cpu20/t11-native-stability-30m-20260922a/`で実素材NPS3を1,800.828秒、1,088回投入し、1,088/1,088 exit 0・timeout 0・CPU20% Job `verified=true`・`job_cpu_rate=2000`を確認した。CPU秒2,912.328125、8論理CPU換算の総量は約20.22%。observer 1,802 samples、WorkingSet 8,708,096–16,334,848 bytes、handle 128–141、最大プロセス数1。WMI拒否によりchild treeは不完全、VRChat不在でframe-timeはpending、native層のみの証拠なのでT11統合受入れチェックは未完了のまま。
- 2026-09-22に6秒実素材のworker実E2Eをsandbox外のCPU20% Jobで実行。`build/nico-cpu20/t11-worker-current-escalated-20260922e/`でversion=1 request、native-warp、MP4/HLS生成、result event、Job `verified=true`・`job_cpu_rate=2000`・exit 0を確認した。MP4は180 frame/30fps、6.000秒、全decode、PTS、AU 1 slice。HLSは2 segment、先頭keyframe、全decode、動画6.000秒、音声6.016秒、映像との差0.016秒。CPU秒12.6875、profile残留0件。sandbox内のGPU helper失敗を回避した診断実行であり、公開commit、browser backend、VRChat併用、30分統合受入れの完了とは扱わない。
- 2026-09-22に155秒実素材のworker実E2Eもsandbox外のCPU20% Jobで実行。`build/nico-cpu20/t11-worker-real155-escalated-20260922f/`はnative-warp、Job `verified=true`・`job_cpu_rate=2000`・exit 0、CPU秒182.171875、profile残留0件を記録した。MP4は4649 frame/30fps、154.966667秒、全decode、PTS、AU 1 slice。HLSは39 segment、先頭keyframe、全decode、動画154.966667秒、音声154.880001秒、映像との差-0.086666秒。長尺workerの成功E2Eまで確認したが、公開commit、browser backend、VRChat併用、30分統合受入れの完了とは扱わない。
- 2026-09-22にbrowser backendの6秒実素材worker E2Eもsandbox外で実行。`build/nico-cpu20/t11-worker-browser-escalated-20260922g/`は既定browser起動引数、CPU20% Job `verified=true`・`job_cpu_rate=2000`・exit 0、CPU秒14.546875、profile残留0件を記録した。MP4は180 frame/30fps、6.000秒、全decode、PTS、AU 1 slice。HLSは2 segment、先頭keyframe、全decode、動画6.000秒、音声6.016秒、映像との差0.016秒。起動緩和フラグは追加していない。browser長尺、公開commit、VRChat併用、30分統合受入れは未完了。
- 2026-09-22にbrowser backendの155秒実素材worker E2Eもsandbox外で実行。`build/nico-cpu20/t11-worker-browser-real155-escalated-20260922h/`は既定browser起動引数、CPU20% Job `verified=true`・`job_cpu_rate=2000`・exit 0、CPU秒238.515625、profile残留0件を記録した。MP4は4649 frame/30fps、154.966667秒、全decode、PTS、AU 1 slice。HLSは39 segment、先頭keyframe、全decode、動画154.966667秒、音声154.880001秒、映像との差-0.086666秒。native/browser双方の長尺worker E2Eを確認したが、公開commit、VRChat併用、30分統合受入れは未完了。
- 2026-09-22にbrowser worker stabilityを同一CPU20% Jobで30分実行。`build/nico-cpu20/t11-worker-stability-browser-30m-20260922k/`は実測1,807.718秒、199/199反復成功、各result/output/playlist条件を満たし、`runner-report`は`verified=true`・`job_cpu_rate=2000`・exit 0・CPU秒2,905.84375。8論理CPU換算の対象Job総CPUは約20.093%。observer 1,808 samples、WorkingSet最大1,818,075,136 bytes、handles最大6,187、GPU平均0.004%/最大1%、VRAM平均1,791MiB。最終反復のMP4/HLSも180 frame/30fps、全decode、PTS、AU 1 slice、HLS 2 segment/keyframe/decode、動画6.000秒、音声6.016秒、差0.016秒をPASSした。WMI拒否でchild treeは不完全（RuntimeBroker 1件を残留候補として記録）、ただしworker/compositor/budget-runner/profileの終了後残留は0件。VRChat不在でframe-timeはpendingのため、30分統合受入れチェックは未完了のまま。
- 2026-09-22にfallback公開境界変更後の現行専用workerを再ビルドし、browser backendを同一CPU20% Jobで30分再測定。`build/nico-cpu20/t11-worker-stability-browser-30m-20260922-current/`は1,802.875秒、199/199反復成功、failed=0、worker complete=true、runner `verified=true`・`job_cpu_rate=2000`・exit 0・CPU秒2,910.703125。8論理CPU換算の対象Job総CPUは約20.181%、worker時間はmin 8.766s/median 8.969s/max 10.750s。observer 1,804 samples、最大WorkingSet約3.39GiB、child treeはcompleteだが終了時にDCv2/OneDrive/vmmemCmZygoteの外部プロセス残留を記録。最終worker-0199のMP4/HLSは180 frame/30fps、PTS/decode/single-slice、HLS 2 segment/keyframe/decode、動画6.000秒、音声6.016秒、差0.016秒をPASS（MP4 SHA-256 `77dfcc4b...`, HLS SHA-256 `3beefdc0...`）。VRChatは不在でframe-timeはpendingのため、CPU20% worker安定性の現行ソース証拠には昇格するが、VRChat併用・30分統合受入れチェックは未完了のまま。
- [x] 結果文書へn、範囲、paired短縮率、CPU秒/動画秒、CPU時間率、GPU/VRAM、VRChat指標、出力byte、hash、無効runを記載。5回のジョブ時間から精密なp95を主張しない。VRChat frame-time未取得は`pending`として明記し、PresentMonの外部証拠と混同していない。
- [x] native/browser通常テスト、変更した予算/workerテスト、Nico serverテスト、該当H.264実出力テストを完了。browser通常経路とauto→browser fallbackはsandbox外opt-inでPASSし、SKIPは実機合格に含めていない。

## 5. 実行waveと採否

|wave|開始可能なタスク|並列化の限界|
|---|---|---|
|1|T1/T2|ファイル分離して実装可。CPU負荷検証は単独|
|2|T3|baseline確定はmain管理、先行最適化を混ぜない|
|3|T4/T5/T6|実装・fixture作成は分担可。性能runは全て直列|
|4|T7、条件成立後T8/T9|支配項に該当する最小の試作だけ着手。共通GPUファイルの同時編集禁止|
|5|T10|mainが統合・worker/startup/公開物管理を担当|
|6|T11|独立review→修正→必要な再検証。最後に採用状態を記録|

T1/T2/T3が終わるまでは、48.385秒を「CPU20%本番baseline」と呼ばない。測定基盤が合格し、FFmpeg filter/threadで目的を満たせばT7–T9を打ち切る。大幅改善に届かずGPU/別encoderしか残らない場合は、実際の品質見本とVRChat影響を示して次の判断にする。

## 6. 実行コマンド契約

以下は実装完了後に実行するコマンドであり、この計画作成時点の実行済み結果ではない。新規CLI/テストは上記タスクが追加する。

```powershell
rtk proxy python3 -m unittest discover -s scripts/experiments/nico-native-probe -p "test_budget_*.py"
rtk proxy go test ./internal/nicoexportbudget -count=1
rtk proxy go test ./internal/nicoexportworker -count=1
rtk proxy go test ./internal/library -run 'Test.*Nico' -count=1
rtk proxy go test ./internal/nicorender -count=1
rtk proxy go test ./internal/video -run 'Test(Nico|EncodeNico)' -count=1
rtk proxy go test ./internal/server -run 'TestNico' -count=1
rtk proxy go build -o build/nico-cpu20-tools/nico-budget-runner.exe ./cmd/nico-budget-runner
rtk proxy python3 scripts/experiments/nico-native-probe/budget_bench.py --run-id cpu20-production30-validation-001 --profile production30 --cpu-percent 20 --repeats 5
```

実ブラウザ/FFmpegのopt-in変数は既存 `TestNicoProductionPipeline` に合わせ、`IMAGEPAD_NICO_PRODUCTION_TEST=1`、`IMAGEPAD_NICO_PERF_SOURCE`、`IMAGEPAD_NICO_PERF_SNAPSHOT`、`IMAGEPAD_NICO_PERF_FFMPEG` をmanifestと同じ絶対パスで設定する。`IMAGEPAD_NICO_PRODUCTION_ARTIFACTS` は毎runの専用フォルダー、durationは6000/154955。利用するGo/FFmpeg/Chromeの実体はT1で解決し、依存が取得不能なら成功と見なさない。

## 7. 計画レビュー記録

本書は今回の24案を監査した結果として作成した。実装開始時はこの計画と監査書を一緒に読み、対象HEAD/dirty差分と実行物hashを再照合する。

独立レビュー初回の有効な指摘（worker I/O契約、完成HLS登録/公開確定手順）をT2/T10へ反映した。「VRChat不在を必須にする」という指摘はreview依頼文の誤記に由来するため採用しない。ユーザー要件はVRChat併用であり、性能runと併用影響は同時に観測する。

2026-09-21の差分再レビューは **APPROVED（計画として）**。同時I/O drain・上限・cancel、完成HLSの二重変換回避、公開前の成果物準備・revision照合・永続化、失敗注入試験の追加を確認し、指摘は解消した。これは計画の静的レビュー結果であり、新規benchmark・実装・VRChat実機受入れの完了を意味しない。
