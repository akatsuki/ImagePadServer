# Playlist Output Plane 規格改善計画

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 既存のplaylist効率化・ストリーミング計画を、素材生成、恒久出力、制御状態、GPU移行を混線させない実行可能な規格へ再定義する。

**Architecture:** playlistを「Materialization Plane（音源解析・recipe・manifest・cache）」「Output Plane（ProgramClock・compositor・encoder・MediaMTX/publisher）」「Control Plane（desired/active設定・readiness・recovery）」の3平面に分離する。最初は既存のpre-rendered MP4とCPU rendererを正規入力にし、同一scene契約を将来のwgpu backendへ渡す。copy/remux laneは互換性用の明示的な別セッションとして残し、同一publisherへpacket producerをhot-swapしない。

**Tech Stack:** Go、FFmpeg/ffprobe、MediaMTX、H.264/AAC、MPEG-TS、Rust + wgpu（GPUフェーズのみ）。

## Global Constraints

- 見た目、30 fps、H.264/yuv420p、AAC 48 kHz stereo、`-bf 0`、repeat headers/AUD、既存RTSP/HLS互換性を既定契約として固定する。
- `radio-track-<id>.mp4` はGPUフェーズが完了するまでtrack visualのsource of truthであり、live waveform renderingは導入しない。
- MediaMTX path、public RTSP URL、publisher、ProgramClockはsession-ownedであり、track/overlay切替では再起動しない。
- decoder/overlay故障はblack/silentまたはfallbackで回復し、encoder/publisher故障だけをsession-fatalとする。
- source PTSは転送せず、全出力時刻はsessionのProgramClockが生成する。
- CPU reference pathは常に残し、GPUやzero-copyは能力検出と実測ゲートを通過した場合だけopt-inにする。
- すべての最適化はpixel/golden、単調PTS、音声同期、受信機互換性、before/after resource計測の5点で受け入れる。

## 現在の到達点（3スレッドの統合結果）

- **完了・統合済み:** `#13` 保存済みmediaの所有権・materialization、`#15` publisher/fallbackのbounded failure state・generation completion。
- **進行中:** `#14` `RadioRenderRecipe`、save-time manifest、canonical resolution、legacy copyを壊さない再生成判定。
- **調査済み・実装待ち:** `#17A` immutable active-session snapshot、`#16` protocol-aware readiness、`#20A` RTSP reader/current copy baseline。
- **後続:** `#17B` desired/active API・UI、`#18` sequential cursor、`#19` persistent ProgramClock/compositor/encoder、`#20B` 5-profile acceptance/soak。
- **重要な未解決点:** recipe/manifestの整備だけではreceiver-visibleな連続出力を保証できない。出力境界、状態機械、readiness、回復規則を独立した規格として固定する必要がある。

## 改善する規格の要点

### 1. 三平面の所有権を明文化する

| 平面 | 所有するもの | 所有しないもの |
|---|---|---|
| Materialization | source hash、analysis、artwork、recipe、manifest、cache、pre-rendered MP4 | MediaMTX、RTSP URL、session PTS |
| Output | ProgramClock、scene compositor、encoder、MPEG-TS、publisher、MediaMTX path | desired設定、cache eviction、UI表示文言 |
| Control | desired/active設定、profile能力、readiness、generation、recovery状態 | raw frame、FFmpeg filtergraph、OS GPU handle |

Go Coreは`image.RGBA`、Metal/D3D/Vulkan handle、FFmpeg process objectをControl契約へ漏らさない。GPU backendはscene契約の実装詳細に限定する。

### 2. desired と active を別状態にする

設定変更は即時にpacket producerを変えず、`desired`を保存し、適用可能なtrack boundaryまたはsession restartまで`pending`として公開する。APIは少なくとも次を返す。

```text
desired: {profile, overlayMode, resolution}
active:  {profile, outputMode, encoder, resolution, generation}
readiness: {rtsp, hls, encoderPreflight, firstSegment}
pendingReason: boundary | restart-required | preflight | recovery
```

### 3. transitionを二段階commitにする

track変更は `prepare -> claim -> publish` の順序で処理する。新generationのfeederが準備完了するまで旧generationをcancelせず、公開後は旧feederをexact generation IDで停止する。失敗時はactive generationを維持し、ProgramClockがblack/silentを出す。

### 4. capability negotiationをprofile中心にする

5つのdelivery profileを「codec/mux/GOP/transport/readiness artifact」の不変セットとして定義する。`rtsp-ultra`等の名前だけで判定せず、起動時preflightで実際のencoder・MediaMTX・client条件を検証し、失敗理由を非機密なreadinessとして公開する。

### 5. Scene IRを先に固定し、backendを後から増やす

`SceneSnapshot`は時刻、解像度、背景、track texture、overlay commands、accent、progressだけを持つ。CPU adapterをgoldenの基準にし、wgpuは同じsnapshotを描画する。ASS/libassの文字は初期GPU版では既存経路を保持し、背景・画像・波形・進捗から段階移行する。

### 6. 観測可能性を機能仕様に含める

各sessionに`sessionID`、`generation`、`profile`、`encoder`、`readiness stage`、`queue depth`、`dropped overlay frames`、`audio/video drift`、`recovery count`を付与する。直近数秒の状態遷移とFFmpeg/MediaMTXの要約を保存するflight recorderを用意し、再現不能な一度きりの途切れを後から解析できるようにする。

## 実装タスク

### Task 1: 規格と不変条件を一つの契約に統合する

**Files:**
- Create: `docs/superpowers/specs/2026-07-13-playlist-output-plane.md`
- Modify: `docs/superpowers/plans/2026-07-11-playlist-streaming-remediation.md`
- Modify: `docs/superpowers/plans/2026-07-11-playlist-hybrid-overlay.md`
- Create: `internal/server/music_playlist_contract_test.go`
- Create: `internal/obsrtmp/radio_contract_test.go`

**Interfaces:**
- Produces the canonical names `MaterializationPlane`, `OutputPlane`, `ControlPlane`, `DesiredPlaylistState`, `ActivePlaylistState`, `ReadinessState`, and `PlaylistInvariant`.
- Every later task consumes the same `OutputMode` values: `compatibility-copy`, `program`, `recovering`, `failed`.

- [ ] 既存2計画から重複した制約を抽出し、上記specへ移す。
- [ ] `RTSPURL`、MediaMTX path、publisher identity、ProgramClockがtrack/overlay変更で不変であることをテストにする。
- [ ] `go test ./internal/server ./internal/obsrtmp -run 'Test(PlaylistContract|RadioContract)' -count=1` を実行し、契約テストを固定する。

**Acceptance:** 後続計画が異なる用語・状態名・責務境界を定義せず、1つのspecを参照する。

### Task 2: Materialization Planeをcontent-addressedに閉じる

**Files:**
- Modify: `internal/video/audio_analysis.go`
- Modify: `internal/video/radio_render.go`
- Modify: `internal/server/music_playlist.go`
- Modify: `internal/playlist/*`
- Create: `internal/video/render_cache.go`
- Create: `docs/PLAYLIST_RENDER_CACHE.md`

**Interfaces:**
- `AnalysisCacheKey`はsource hash、analyzer version、loudnorm設定、fpsを含む。
- `RenderRecipeKey`はsource/artwork hash、metadata digest、quality、encoder、renderer versionを含む。
- path/mtimeだけをcache identityに使わず、atomic writeとdigest検証を行う。

- [ ] `AnalyzeAudioForKind`が生成した`IntegratedLUFS`を再利用し、二重full-source scanを禁止する。
- [ ] recipe不一致（artwork、metadata、encoder、renderer version）は必ず再生成にする。
- [ ] 同一volumeの一時音源は可能な場合atomic renameでmaterializeし、copyを避ける。
- [ ] `go test ./internal/video ./internal/playlist ./internal/server -run 'Test.*(Cache|Recipe|Manifest)' -count=1` を通す。

**Acceptance:** 同一recipeの再公開はanalysis/renderを省略し、部分書込みや古いassetを返さない。

### Task 3: Control Planeのdesired/active/readinessを完成させる

**Files:**
- Modify: `internal/settings/settings.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/music_playlist.go`
- Modify: `internal/obsrtmp/radio.go`
- Modify: `internal/server/music_mode_test.go`

**Interfaces:**
- `GET/POST /api/video-quality` はdesired設定とactive状態を混同しない。
- readinessは`encoderPreflight`、`rtsp`、`hls`、`firstSegment`を個別に返す。
- APIとplaylist stateにはローカルパス、credential、FFmpeg raw stderrを含めない。

- [ ] overlay/profile/resolutionの設定変更を`pendingReason`付きで保存する。
- [ ] HLS URLはplaylistと最初の`.ts` segmentが揃うまで公開しない。
- [ ] 5 profileのreadiness fixtureを作り、profile名ではなく実artifactで判定する。
- [ ] `go test ./internal/server -run 'Test(VideoQuality|MusicPlaylist|HLS).*' -count=1` を通す。

**Acceptance:** UIが「設定した値」と「現在流れている値」を区別し、readiness前のURL公開や途中切替がない。

### Task 4: Output Planeを二段階commitとProgramClockで実装する

**Files:**
- Create: `internal/obsrtmp/radio_program.go`
- Create: `internal/obsrtmp/radio_program_feeder.go`
- Modify: `internal/obsrtmp/radio.go`
- Modify: `internal/video/video_encoder.go`
- Modify: `internal/video/radio_render.go`
- Create: `internal/obsrtmp/radio_program_test.go`

**Interfaces:**
- `ProgramClock`が30 fps video PTSと48 kHz audio sample clockを所有する。
- `ProgramEncoder`が`Start`、`WriteVideoRGBA`、`WriteAudioPCM`、`Healthy`、`Close`を提供する。
- feederは`prepare/claim/publish`を経て、source PTSをProgramClockへ変換する。

- [ ] source decoderのprepare完了前は旧generationを維持する。
- [ ] publish後にのみ旧generationをexact IDでcancelし、stale workerのcleanupを禁止する。
- [ ] 500 msのsource gapではblack/silentを出し、音声をdropしない。
- [ ] decoder/overlay故障ではpublisher identityを維持し、encoder/publisher故障では`failed`へ遷移する。
- [ ] `go test ./internal/obsrtmp ./internal/video -run 'Test(RadioProgram|ProgramClock|ProgramTrackFeeder|RadioRTSP)' -count=1` と360p ffprobe smokeを通す。

**Acceptance:** track境界・overlay切替・decoder再起動でRTSP URL、MediaMTX path、publisher、PTSが変わらない。

### Task 5: Scene IRとCPU/wgpuの差分検証を導入する

**Files:**
- Create: `internal/video/compositor_contract.go`
- Create: `internal/video/compositor_contract_test.go`
- Create: `native/playlist-compositor/Cargo.toml`
- Create: `native/playlist-compositor/src/lib.rs`
- Create: `native/playlist-compositor/src/scene.rs`
- Create: `native/playlist-compositor/src/cpu.rs`
- Create: `native/playlist-compositor/src/wgpu_backend.rs`
- Create: `docs/PLAYLIST_GPU_COMPATIBILITY.md`

**Interfaces:**
- Go/C ABIはscene snapshotと所有権・長さだけを定義し、OS handleを持たない。
- CPU adapterはexact golden、wgpu backendは文書化したper-channel toleranceで比較する。

- [ ] 固定fixtureのscene serializationとcommand orderを決定論的にする。
- [ ] GPU不可、readback不可、texture format不一致時はCPUへfail-closedする。
- [ ] Metal/D3D12/Vulkanのadapter、format、readback latency、失敗理由を記録する。
- [ ] `go test ./internal/video -run 'Test(Compositor|Scene)' -count=1` と `cargo test --manifest-path native/playlist-compositor/Cargo.toml` を通す。

**Acceptance:** CPUとwgpuが同じscene契約を使い、GPU導入でplaylist再生不能にならない。

### Task 6: Flight recorder、shadow mode、段階ロールアウトを追加する

**Files:**
- Create: `internal/obsrtmp/radio_flight_recorder.go`
- Create: `internal/obsrtmp/radio_flight_recorder_test.go`
- Modify: `internal/server/music_radio_e2e_test.go`
- Create: `docs/PLAYLIST_RENDERING_SMOKE.md`

**Interfaces:**
- `FlightRecorder.Record(event)`はbounded ring bufferへsession/generation/readiness/recoveryを保存する。
- shadow modeは実配信を変更せず、CPUとwgpuのscene hash・render timeだけ比較する。

- [ ] empty overlay、通知overlay、track transition、decoder failure、encoder failure、HLS readinessをE2Eに追加する。
- [ ] 30分720p/30fps soakで3回以上のtrack transitionを記録する。
- [ ] no reconnect、no non-monotonic DTS、audio gap 100 ms未満、p95 frame 33.3 ms未満を必須にする。
- [ ] GPU backendは最初shadow modeで実行し、差分と性能の両方が閾値内の環境だけcanary enableする。
- [ ] `go test ./internal/video ./internal/obsrtmp ./internal/server -count=1` と実機smoke結果を文書化する。

**Acceptance:** 失敗を再現できる証跡が残り、defaultはCPU/copy laneのまま安全にcanary展開できる。

## 追加の応用企画（独立decisionとして扱う）

1. **Predictive prewarm:** queue cursorが次曲を確定した時点でanalysis、artwork、decoder初期化だけを先行させ、publishはboundaryまで行わない。待ち時間を削るが、同時実行数とcache budgetを超えない。
2. **Adaptive degradation ladder:** `notifications -> overlay off -> generated fallback -> lower resolution -> session failed` の順に品質を落とす。encoder/publisherだけは即時fatalにする。
3. **Replayable visual timeline:** 音源hash、recipe hash、feature window、scene snapshotを短時間保存し、実際の途切れたフレームをローカル再生できるデバッグモードを用意する。
4. **Receiver capability profiles:** VRChat/AVPro、ブラウザ、LAN clientごとに必須GOP、HLS readiness、buffer policyを宣言し、送出側の設定を後付けのフラグ集合にしない。
5. **Zero-copyは測定後のみ:** Windows D3D shared texture、macOS Metal/VideoToolbox、Linux Vulkan/VAAPIを別spikeとして比較し、readbackよりp95が良い組合せだけをopt-inにする。
6. **Radical live compositor:** pre-rendered MP4を廃止して常時live renderへ移行する案は、互換client matrixと30分soakが通った後の別規格にする。現計画へ混ぜない。

## 推奨実行順

1. Task 1で規格・不変条件・用語を凍結する。
2. 既存の`#14`をTask 2へ合わせて完了させる。
3. Task 3で`#17A/#16/#17B`のdesired/active/readinessを閉じる。
4. `#18` cursorと`#20A` copy baselineを完了させ、legacy laneの退行を固定する。
5. Task 4（`#19`）をprogram mode disabled-by-defaultで実装する。
6. Task 6のE2E・flight recorderを先に用意し、Task 5のwgpuをshadow modeで検証する。
7. `#20B`を全profile・実機・30分soakの最終promotion gateとして実行する。

## Promotion / Stop 条件

- **Promote:** golden、PTS、音声同期、profile readiness、receiver matrix、resource budgetの全てがpass。
- **Hold:** GPU差分、Windows file lock、cache invalidation、readiness順序のいずれかが未説明。
- **Stop:** publisher/MediaMTXのidentity交換、credential漏洩、非単調PTS、100 ms超の音声欠落、copy lane退行。

## Self-review

- 既存計画のsafe optimization、persistent output、GPU、zero-copyを別ゲートへ分離した。
- `#13/#15`完了と`#14`進行中を前提にし、未完成のprogram laneを既成事実として扱っていない。
- live waveform、直接libavcodec、multi-layer配信は応用企画へ隔離し、現行互換性を壊す前提を導入していない。
- すべてのタスクに対象ファイル、契約、検証コマンド、受け入れ条件がある。
