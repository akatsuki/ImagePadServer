# AirPlay 不安定化の修正実装計画

> 実装担当者向け: `superpowers:executing-plans` を基本に、下記の依存順・検証ゲートに従う。独立した安全な作業を委譲する場合は、リポジトリの `swarm-planner` / `parallel-task` 手順とファイル所有範囲を守る。起動処理・実行ランタイム・状態を持つ統合はメイン担当者が扱う。チェックボックスは実測後に更新する。

**目的:** 現在コードとAirPlayランタイムの不一致を解消し、配信世代変更時の表示モードを保持する。HLS準備判定と障害診断を改善し、開始・切替・復旧・実機再接続を同じ成果物で検証する。

**構成:** 既存のUxPlay → source-clock → GStreamer → MediaMTX → RTSP/HLSを維持する。ランタイム互換性の事前検査、VideoViewの世代管理、完成済みHLS素材による準備判定を局所修正する。未再現のreject/startup問題には先に再現試験を追加する。

**技術:** Go（`go.mod`: 1.25.0、ローカルSDK: 1.26.3）、C/GLib/GStreamer、MediaMTX v1.19.2、PowerShell/Pester、CMake/CTest、FFmpeg/ffprobe。

**根拠:** `docs/RTSP_H264_COMPATIBILITY_CONTRACT.md`、`docs/AIRPLAY_DISTRIBUTION_LICENSES.md`、`docs/AIRPLAY_IPHONE_UXPLAY_REFERENCE.md`、本書の再調査結果。既存の `docs/superpowers/plans/2026-09-18-airplay-source-clock-protocol-recovery.md` と `docs/superpowers/plans/2026-09-20-rtsp-long-session-reliability.md` は重複箇所の参照元とし、本書から無条件に再実装しない。

**状態:** 2026-09-22 計画作成。以下の修正・テスト・パッケージ作成・実機受入れは未実施。今回の成果物は本計画のみ。

## 1. 基準と前回提案の訂正

- HEAD: `71dc49cd`、ブランチ: `fix/airplay-source-clock-protocol-recovery`。HEADだけでなく、多数の既存未コミット変更を含む作業ツリーを調査した。
- 直近: `f56e5db0` VideoView、`59012609` reject policy、`cbc4e946` reject境界テスト、`71dc49cd` HLS preview / delivery switching。
- `release/airplay-inputs.json` は `v1.8.0` / `single-slice-release-v1.8.0`、archive SHA-256 は `129bd9c95cf35d425168927cb9b2c73a471c71c99f307866486214cbdeae5b20`。
- インストール済み同ランタイムのcapability manifestを今回再読し、`video-bootstrap-reconnect` の欠落を確認した。ただし、そのEXEが現在稼働していることは今回確認していない。
- 配布文書にはv1.7.1、旧計画にはGStreamer 1.26という過去値が残る。今回のビルド入力は実際のprovenance/manifestから確定し、旧記述から転記しない。既知の配布検証記録はGStreamer 1.28.6。

| ID | 現在の根拠 | 判断・計画への反映 |
|---|---|---|
| F1 | `source_clock_session.go:146-190` はreconnect機能と実EXEハッシュを要求。配布入力とインストールmanifestは旧版 | 互換性不一致は確認済み。T1/T6で修正する。現在の実機症状すべての原因とは断定しない |
| F2 | `directReadinessProbe` と `waitForDirectCandidateHLS` はRTSPモードではHLS検査を省く | 前回の「RTSP commitからHLS条件を外す」は既存動作。撤回し、保持テストを追加する |
| F3 | `mediamtx.go:1023-1036` は完成済みpartに加えて未来のpreload hintもGET完了を要求。previewの総期限は750ms | 正常なblocking requestでも準備不可になり得る。T3で決定論的に再現して修正する。実際の症状との対応は実測する |
| F4 | `initializeVideoView` は初回起動からしか呼ばれない。candidate commit/retryで世代・readyパスが変わる一方、`m.videoView` が旧世代を保持。native新世代の初期modeはcontain | 表示モード/制御先の引継ぎ不足をソースで確認。T2の優先対象 |
| F5 | `VideoViewController.Set` はstateファイルの書込み成功前にメモリーrevisionを進め、`SetVideoView` はcontrol送信より先に設定を保存 | I/O失敗時に要求・永続設定・実適用が食い違う。T2で失敗注入と状態整合性を修正する |
| F6 | rejectはloopbackのUxPlay→bridge間。EOFはカウント対象外。一度認証すると`ever_authenticated`は同一publisher中ずっとtrue | iPhoneのprobeや配信中の通常再接続を直接数えるという前回の推測は不正確。peer別カウンタへの変更は現時点で採用しない |
| F7 | PAUSED/pending PLAYING + clock選択済みを起動許可。ライブsinkは最初のbuffer待ちでASYNCを維持し得る | PAUSEDだけではバグではない。入力開始前に完全PLAYINGを強制する変更は行わず、T4で起動循環待ちと本当の失敗を試験する |

T3の根拠は、PRELOAD-HINTが未完成素材への先行要求でありサーバーが応答を待たせられること。参照: [HLS第2版ドラフト16 §4.4.5.3](https://datatracker.ietf.org/doc/html/draft-pantos-hls-rfc8216bis-16#section-4.4.5.3)。これはドラフトの規定を参照した設計判断であり、現行MediaMTXでの再現はT3/T7で行う。

T4の根拠: [GStreamer Live sources](https://gstreamer.freedesktop.org/documentation/additional/design/live-source.html)、[Latency / State Changes](https://gstreamer.freedesktop.org/documentation/additional/design/latency.html)。Context7でも公式GStreamer/MediaMTX資料を確認した。`main`のAPI・既定値をそのまま固定版へ適用せず、使用バージョンで検証する。

## 2. 全体制約

- 共有中の既存差分を保護する。一括stage、全体整形、reset/restore/cleanは禁止。別checkoutへ移す場合も必要な未コミット差分を落とさず、実装前に所有範囲を記録する。
- 本計画はcommit/push/tag/releaseや現用サービスの停止・入替えを許可しない。実装依頼後はローカル候補と検証まで進め、公開・現用切替は別の明示指示で行う。
- 公開済みv1.8.0のasset、タグ、runtimeSetIDを別の内容で上書きしない。候補IDは `airplay-stability-candidate-20260922` とし、同名出力が存在すれば別IDを採番する。製品の次版番号は公開準備時の最新リリースと照合して確定する。
- capability条件・ハッシュ検査を緩めない。既存の自動取得・固定ハッシュ・検証・Bonjour・設定・既定有効化のライフサイクルを維持する。
- 送出H.264は1 AUにつきVCL NALが厳密に1つ。`sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0`、SPS/PPS、IDR、RTP timestamp/marker/sequenceを維持する。入力のmulti-sliceは拒否しない。
- fixed PCM/UDPビルドフラグ、MediaMTX版、GOP/preset/bitrate、PC/Android共通URLを同時に変更しない。現在の実成果物からフラグを記録し、その値を再ビルドへ渡す。
- 片側無音、static HOLD、読者一人の切断、HLS準備待ちだけを理由にreceiver全体を再起動しない。
- source token、RTSP資格情報、実argv内の秘密値を証拠へ出さない。識別情報はsession/generation、PID、実行ファイルパス・ハッシュで揃える。
- Goの対象テストは前回Go telemetry/cacheアクセス拒否で完了しなかった。ソース不良ともPASSとも扱わず、T0で実行環境を確定する。
- 実機未確認中は `production_ready=false`。合成試験、ブラウザ、パッケージ起動、実iPhone、VRChatを別行で報告する。

## 3. 検証時に特に見る5条件

1. integrityは正常だが機能不足のランタイムで「開始可能」と表示しない。T1/T6。
2. cover選択中の画質変更・publisher復旧で制御先を旧世代へ戻さない。切替中の連打と旧ACKも含める。T2/T5。
3. 映像partは完成済み、次partだけ未完成のLL-HLSで「準備中」が続かない。T3。
4. 無音開始、後着音声、入力前ASYNC、認証後の不正再接続で不要な全体終了をしない。T4/T7/T8。
5. 設定・sidecar・診断保存のI/O失敗、取消、Stop競合で架空のactiveや子プロセス残留を発生させない。T2/T4/T5/T7。

## 4. 依存関係・担当範囲

| ID | depends_on | 内容 | 主担当・編集範囲 |
|---|---|---|---|
| T0 | [部分完了] | 基準・再現・実行環境の確定 | 記録を追加。稼働サービスなし、Go telemetry ACLを分離 |
| T1 | [実装済み] | ランタイム互換性の事前判定 | runtime compatibility/preflight/statusを実装。旧runtimeは意図どおり拒否 |
| T2 | [実装済み] | VideoViewの書込み整合性と世代引継ぎ | identity、atomic state、persistence retry、candidate/retry世代引継ぎを実装 |
| T3 | [実装済み] | 完成済みHLS素材による準備判定 | future preload artifactをreadiness fetchから除外し回帰テスト済み |
| T4 | [既存診断確認] | native起動/rejectの再現と診断 | source-clock既存診断・fixtureを保持。native再ビルドはSDK不足で未実施 |
| T5 | [部分実装] | API/UI・切替/復旧・既存RTSP診断の統合 | API 400/409分離、永続化失敗の状態表示、対象回帰テスト済み |
| T6 | [T5] | 新ランタイムと対応ソースの候補作成 | メイン。ビルド/パッケージ/候補descriptor |
| T7 | [T6] | 配布候補での自動・ブラウザ受入れ | メイン。隔離データ・専用ポート・証拠 |
| T8 | [T7] | 実iPhone/VRChat・長時間受入れ | 実機協力を伴う検証。証拠文書 |
| R1 | [T8] | 正式版の入力更新・公開・現用切替 | 別の明示指示が必要なリリース工程 |

Wave 0: T0。Wave 1: T1とT3。Wave 2: T2。Wave 3: T4。Wave 4以降: T5→T6→T7→T8。T3担当は`manager.go`/UI/nativeを編集せず、必要な統合変更をT5へ報告する。各wave後にメインが差分と実測結果を確認してから次へ進む。

## T0. 基準と再現条件を固定する

**depends_on:** []。**status:** 部分完了。自動検証記録は `docs/verification/2026-09-22-airplay-stability-acceptance.md`。

**location:** 新規 `docs/verification/2026-09-22-airplay-stability-acceptance.md`。既存実装ファイルは変更しない。

**description / steps:**

- [ ] `rtk git status --short --branch`、`rtk git log -8 --oneline`、対象差分を保存し、T1〜T5の既存変更の所有範囲を記録する。旧計画の実装済みチェックだけを根拠にしない。
- [ ] 現用の待受ポートを読み取り確認する。8080/8082を決め打ちせず、PID・EXEパス・ハッシュ、`/healthz`、`/api/state`の非秘密項目、runtimeSetIDを対応付ける。現在サービスがない場合は`NOT_RUNNING`と記録する。
- [ ] installed/runtime ZIP/embedded payload/ソースの4者を照合し、manifestの機能不足と稼働中EXEの問題を分ける。APIから失敗操作を誘発しない。
- [ ] 同梱SDKで `rtk proxy ./.tools/go-sdk/go/bin/go.exe version` を確認する。検証用子プロセス内だけで `GOCACHE` を新しい専用ディレクトリへ向ける。telemetryアクセス拒否は正確なコマンドと終了コードを保存し、必要なら権限付き実行を利用する。ユーザー全体のACL・telemetry設定を変更しない。`GOTELEMETRY`/`GOTELEMETRYDIR`の環境変数指定だけで解消したとみなさない。
- [ ] `rtk proxy ./.tools/go-sdk/go/bin/go.exe test ./internal/airplaycontract -count=1` を完走させ、次にT1〜T5の対象テストをbaselineとして実行する。失敗があれば既存失敗・今回修正対象・環境失敗を記録する。

**validation:** 基準HEAD+対象差分、実行可能なSDK/テストコマンド、現用プロセスの有無、四者のartifact照合結果が揃う。環境失敗が残る場合は、実装を読取調査に限定する必要はないが、テスト通過や完了を宣言しない。

## T1. 起動前にランタイム互換性を確定する

**depends_on:** [T0]。**status:** 実装済み。旧インストールruntimeは互換性不足として開始不可になる。

**location:** `internal/airplay/source_clock_session.go`、`runtime_preparation.go`、`provision.go`、`manager.go`と対応テスト。新規 `internal/airplay/runtime_compatibility.go` / `_test.go`。UI接続はT5。

**interfaces:** 既存 `validateSourceClockReceiverCapabilities(string) error` を維持し、同じ検査本体を起動準備と開始直前で利用する。新規結果型は次の意味とする。

```go
type RuntimeCompatibility struct {
    State string `json:"state"` // unknown / compatible / incompatible
    Reason string `json:"reason,omitempty"`
    MissingFeatures []string `json:"missingFeatures,omitempty"`
}
// 新規: 準備完了時と開始直前だけ呼ぶ。Status()からディスクを再検査しない。
func inspectSourceClockReceiverCapabilities(receiverPath string) (RuntimeCompatibility, error)
```

**description / steps:**

- [ ] 現在のcapabilityテストfixtureに、旧manifestでinstallが完了しても開始可能にならないケースを追加する。schema不一致・manifest欠落・EXE hash不一致・必須機能欠落を別reasonで固定する。
- [ ] 新規結果を`RuntimePreparationStatus`に保持し、通常の`PrepareOnStartup`成功条件へ互換性検査を追加する。エラー時は既存failed経路を利用し、runtimeSetIDと構造化reasonを残す。
- [ ] `Status().Available`の意味を「この開始経路を利用可能」に揃える。`ResolveReceiverPath()`の成功だけで非互換判定を上書きしない。`unknown`を`compatible`と解釈しない。
- [ ] 明示receiver指定では使用経路を判定する。source-clock経路では同じ検査を適用し、legacy経路に無関係なsource-clock manifestを要求しない。
- [ ] 明示指定のsource-clock経路にも非同期の事前検査を一度実行し、unknownのまま開始ボタンが永久に無効になる状態を避ける。結果はruntimeSetID/選択パスに結び付け、選択変更時に無効化する。実行中のセッションは採用済みの識別情報で報告する。
- [ ] 開始直前は必ず実EXEを再検査する。準備後のEXE差し替えやmanifestのみの書換えを拒否する。status取得ごとの全ファイルハッシュ計算は追加しない。

**validation:** 旧v1.8.0 fixtureは開始前から`incompatible`、不足機能名を取得可能。正しいmanifestとEXEの組だけが通る。準備後改変は開始直前で拒否され、子プロセスが起動しない。`TestSourceClock`、`TestRuntime`、`TestPrepare`、`TestManager`に該当するAirPlayテストを完走する。

## T2. VideoViewを配信世代に結び付け、失敗時も整合させる

**depends_on:** [T1]。**status:** 実装済み。Go/native候補の実描画確認はT7/T8に残す。

**location:** `internal/airplay/video_view.go`、`video_view_control.go`、`video_view_atomic_windows.go`、`source_clock_session.go`、`source_clock_candidate_delivery.go`と対応テスト。必要なnative変更は `source_clock_pipeline.c`、`video_view_control.c`、`tests/test_video_view_pipeline.c`。新規 `internal/airplay/video_view_generation_test.go`。

**interfaces:** controllerの準備とactiveへの昇格を分割する。新規helperの仕様は以下とする。

```go
func prepareVideoViewGeneration(paths airplaycontract.VideoViewPaths,
    sessionID string, generation uint64,
    mode airplaycontract.VideoViewMode) (*VideoViewController, error)
// native ACK: SessionID + PublisherGeneration + revisionが一致し、
// そのrevisionの実フレームが出力された場合だけAppliedを進める。
```

**description / steps:**

- [ ] 書込み先の親を通常ファイルで塞いだfixtureで、失敗した`Set`がConfiguredRevision/Modeを先行確定しないテストを書く。失敗時はError/Phaseだけを更新でき、再試行時に余分なrevisionを要求しない。

初期の失敗再現テスト（`os`、`path/filepath`、`testing`と既存airplaycontract importを使う）:

```go
func TestVideoViewSetWriteFailureDoesNotAdvanceRevision(t *testing.T) {
    blocked := filepath.Join(t.TempDir(), "blocked")
    if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
        t.Fatal(err)
    }
    c := NewVideoViewController(airplaycontract.VideoViewPaths{
        State: filepath.Join(blocked, "state.json"),
        Control: filepath.Join(blocked, "control.ini"),
    }, "session", 1)
    before := c.State()
    _, err := c.Set(VideoViewRequest{
        Mode: airplaycontract.VideoViewCover,
        ExpectedSessionID: "session", ExpectedPublisherGeneration: 1,
        ExpectedRevision: before.ConfiguredRevision,
    })
    if err == nil { t.Fatal("write failure was not reported") }
    after := c.State()
    if after.ConfiguredRevision != before.ConfiguredRevision ||
       after.ConfiguredMode != before.ConfiguredMode {
        t.Fatalf("failed request advanced desired state: before=%+v after=%+v", before, after)
    }
}
```

書込み所有権の修正後に`Set`からI/Oを分離する場合は、この失敗条件を受理操作の公開境界`SetVideoView`の注入テストへ移し、検査内容を保持する。

- [ ] 世代1/coverから画質切替で世代2へ移る統合fixtureを追加する。現在の実装で、世代2のcontrol未作成またはAPIの旧generation保持が検出されることを確認する。
- [ ] `SetVideoView`を一つの受理操作として直列化し、stale判定、制御ファイルのatomic publish、controller確定を順に行う。control publish前の失敗なら旧状態を保持する。publish後にsettings永続化が失敗した場合は、適用済みの可能性を認めてrevisionを巻き戻さず、「ライブ要求は受理済み・永続化失敗」を明示する。エラーを消してpendingのまま成功応答しない。
- [ ] Go所有の`persistenceState`（saved/failed）と`persistenceError`をVideoView状態へ追加し、nativeのapplied ACKで上書きしない。control公開成功→settings保存失敗→publisher復旧を連続注入し、セッション内の最後に受理したdesiredと未保存状態が新controllerへ継承されることを確認する。復旧時に古いsettingsを読み直してdesiredを失わない。
- [ ] 保存失敗後、現在のsession/generation/revisionと同一modeを指定した再試行はsettings保存だけを行い、revisionを増やさずcontrolを再送しない。別modeは通常の新要求として受理する。アプリ再起動を越える保持は保存成功時だけ保証する。
- [ ] sidecarの書込み所有者を明確にする。Goがdesired/controlを所有し、nativeがapplied ACKを所有する。Goのpending保存でnativeの新しいACKを上書きしない。固定`.tmp`競合が残る場合は同一ディレクトリ内の一意な一時ファイル＋atomic replaceへ変更する。
- [x] candidateのreadyパスから新世代のVideoViewを準備し、candidate起動前にcontrolを渡す。nativeは初回pollで初期controlを反映してから最初の実映像を出す。view revisionは世代を跨いで保持し、modeは最後に受理したdesiredを引き継ぐ。publisher generationとview revisionを混同しない。
- [ ] candidate controllerはprivateに保持し、既存の原子的commitでdelivery generationと同時にactiveへ差し替える。ファイルI/Oは`m.mu`保持中に行わない。commit失敗時にcandidate controllerを公開しない。
- [ ] candidateの昇格前に新generation・revision・modeのnative applied ACKを確認する。これは既存post-watermark映像証拠/private RTSP probeへの追加条件であり、代用しない。ACK書込み失敗は既存candidate期限内に明示エラーへ収束させる。prepareはcontrolとGoメモリーだけを所有し、nativeのapplied sidecarを書き換えない。
- [ ] 手動retry・自動retry・managed recovery・compensationの全てに同じ準備/昇格を適用する。旧publisher終了後に復旧が失敗した場合は旧controllerをactive扱いせず、変更APIは復旧待ちエラーを返す。
- [ ] 切替中の表示モード変更は既存状態を返した上で再試行可能な競合応答とする。受理済みStop、旧ACK、旧リクエストが新世代を更新しないよう、要求受理・generation予約・commitを同じ所有権規則で直列化する。
- [ ] 稼働中は非空session・非ゼロgeneration・現在revisionを要求する。省略は400、旧世代/旧revisionは409で拒否し、応答に現在状態を添える。世代1と2が同じrevision=1でも遅延要求を受け付けない。停止中の次回用設定は世代識別子を要求しない別契約とし、T5でUIの送信項目を合わせる。

**validation:** 初回/切替/復旧後にcover維持、HOLD中再描画、旧generation拒否、Stop優先、I/O失敗後の再試行をGo/native fixtureで確認。candidate失敗ではnative適用を偽装しない。公開RTSP/HLS/録画で表示が一致することはT7/T8で確認する。

## T3. LL-HLSで未来のpartを準備完了条件にしない

**depends_on:** [T0]。**status:** 実装済み。対象LL-HLS回帰テストはPASS。

**location:** `internal/obsrtmp/mediamtx.go`、`mediamtx_test.go`、`mediamtx_hls_timeout_test.go`、`direct_backend_hls_test.go`、`direct_candidate_delivery_test.go`、`direct_publish_test.go`。共有`manager.go`の編集はT5へ渡す。

**interfaces:** `hlsReady(context.Context, LatencyProfile) bool`、`HLSPreviewReady`、`waitForDirectCandidateHLS`の既存呼出し契約を維持する。previewがreadyでもdecode済みとは解釈しない。

**description / steps:**

- [ ] httptestでmaster/media/init/完成済みpartは即200、`PRELOAD-HINT`だけはrequest contextが切れるまで待つfixtureを作る。現行の総期限内にfalseとなることを確認する。

最小の失敗再現（`context`、`fmt`、`net/http`、`net/http/httptest`、`net/url`、`strconv`、`testing`、`time`を使う）:

```go
func TestLLHLSReadyDoesNotWaitForFuturePart(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        switch r.URL.Path {
        case "/obs_session/init.mp4", "/obs_session/part.m4s":
            fmt.Fprint(w, "completed fixture bytes")
        case "/obs_session/next.m4s":
            <-r.Context().Done()
        default:
            http.NotFound(w, r)
        }
    }))
    defer srv.Close()
    u, err := url.Parse(srv.URL)
    if err != nil { t.Fatal(err) }
    port, err := strconv.Atoi(u.Port())
    if err != nil { t.Fatal(err) }
    runtime := newMediaMTXRuntime("unused", mediaMTXSessionConfig{
        Path: "obs_session", Ports: mediaMTXPorts{HLS: port},
    })
    playlist := []byte("#EXTM3U\n#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES\n" +
        "#EXT-X-PART-INF:PART-TARGET=0.333\n#EXT-X-MAP:URI=\"init.mp4\"\n" +
        "#EXT-X-PART:DURATION=0.333,URI=\"part.m4s\"\n" +
        "#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"next.m4s\"\n")
    ctx, cancel := context.WithTimeout(context.Background(), time.Second)
    defer cancel()
    if !runtime.llhlsMediaReady(ctx, playlist) {
        t.Fatal("completed part was hidden by a future part")
    }
}
```

このfixtureの非空byteはHTTP判定だけの証拠。fMP4のdecode・実ブラウザでの再生開始はT7で別途確認する。

- [ ] 次の期待値を固定する。完成済み素材がないケースは引き続きfalseにする。

| 素材 | 期待 |
|---|---|
| 有効LL playlist + init + 完成済みpart、未来part応答待ち | true。未来partを準備判定としてGETしない |
| playlistのみ、initまたは完成済みpartが404/空/タイムアウト | false |
| 通常HLS/fmp4のinit + 完成済みsegment | true |
| RTSP path ready / decoded media ready / HLSなし | RTSP deliveryはready、previewはfalse |
| HLS profileのcandidate、HLS素材なし | commitしない。有限時間で失敗しcleanupする |
| 旧runtimeに素材あり、新generationにはなし | 新世代はfalse。旧素材を再利用しない |

- [ ] `llhlsMediaReady`では構文確認と再生開始用素材の取得を分け、完成済みinit/partを検査する。preload hintの存在・形式検査と、未来レスポンスの取得成功を混同しない。現在の範囲では新しいHLS parserや外部依存を導入しない。
- [ ] 総期限内でrequestが終了し、応答Bodyが閉じられることを確認する。新しい無制限retryを追加しない。
- [ ] RTSP/HLSそれぞれのcandidate判定を保持する。HLS-highは4秒segment/GOPであり、private probeとHLS待ちで共有する10秒deadlineの内訳を測る。期限変更は実測不足分が確認できた場合だけ別差分で行い、no-signal/Stop期限を超えない。

**validation:** `rtk proxy ./.tools/go-sdk/go/bin/go.exe test ./internal/obsrtmp -run 'Test.*(HLS|DirectReadiness|DirectBackend|Candidate)' -count=1` が完走する。既存testの期待値変更が必要なら、未来partと完成済み素材の仕様差をテスト名・根拠へ明記し、missing init/part拒否の検査を維持する。

## T4. 起動・rejectを再現し、障害段階を記録する

**depends_on:** [T2]。**status:** 既存診断確認。native再ビルド・実入力再現は未実施。

**location:** `native/airplay-gstreamer-bridge/source_clock_pipeline.c`、`source_clock_reject_policy.c`、`tests/test_video_input.c`、`tests/test_source_clock_reject_policy.c`、`CMakeLists.txt`。Goでは `source_clock_receiver_output.go` / `_test.go`、`source_clock_event_log.go` / `_test.go` の必要箇所。

**interfaces:** 既存wire protocol/schema、`publisher-ready`/`media-ready`の意味、exit 21=非再起動、22=publisher再起動を維持する。追加診断は既存process logの構造化行とし、既存event parserへ未知の必須イベントを送り込まない。

**description / steps:**

- [ ] native fixtureでPLAYING、PAUSED/pending PLAYING+clockあり、clockなし、state failure、listener bind失敗、scheduler/thread開始失敗を個別に再現する。PAUSED許可から入力を流して実decoded-videoまで進む試験を含める。
- [ ] video/audio両ソケットに対し、未認証bad magic、認証後bad packet、EOF、交互reject、5秒以上のslow trickleを注入する。共有カウンタとsticky認証の現仕様を検証し、iPhone側の接続と混同しない。
- [ ] 1回の未認証reject→正常接続は継続、同publisherで認証成功後のprotocol rejectは全体停止にしない、callback errorは22、永続的な未認証不整合は既存21となることを保持する。
- [ ] 診断にsession/generation、stage、stream kind、reader status、認証状態、拒否回数、state current/pending/result、clock有無、経過時間、exit codeを追加する。tokenやpayloadは記録しない。
- [ ] readerの現仕様で正常receiverが21へ至る再現ができた場合に限りpolicy変更を追加する。loopbackの送信元ephemeral portごとの無制限カウンタや、5秒窓の無根拠なリセットは採用しない。変更前後で永続的な非互換を検出できることを要求する。
- [ ] `publisher-ready`後もmedia待ちを継続し、最初の実decoded frame、現在generationのmedia-ready、native bus errorを対応付ける。診断の保存失敗だけでreceiverを止めず、ログをrate limitする。

**validation:** 対象CTestが存在して実行されることを`ctest --show-only=json-v1`で確認し、reject/video_input/video_viewを完走する。入力前完全PLAYING待ちの循環停止を導入していないこと、無音開始がvideoを止めないことを確認する。

## T5. 状態表示・切替失敗・既存RTSP診断を統合する

**depends_on:** [T1,T2,T3,T4]。**status:** 部分実装。API/UI/切替境界を実装し、candidate/配布候補の受入れは後続。

**location:** `internal/airplay/manager.go`、`source_clock_session.go`、`source_clock_reconfigure.go`、`internal/obsrtmp/manager.go`、`direct_delivery_transaction.go`、`internal/server/airplay_route.go`、`ui_script_preview_controller.go`と対応テスト。新規 `internal/airplay/runtime_compatibility.go` の型を再利用する。

**description / steps:**

- [ ] 既存`runtimeState`/`bridgeRunning`/`mediaReady`/`deliveryPhase`とpreviewURLを使い、重複する独立booleanを5つ新設しない。互換性結果と必要な失敗理由のみ加える。
- [ ] 状態表示を以下へ固定する。

| 状態 | 表示/操作 |
|---|---|
| 展開完了・capability不足 | 「ランタイム更新が必要」、不足機能を詳細へ。受信開始不可 |
| receiver/bridge起動・mediaなし | 「iPhoneの映像待ち」 |
| RTSP media配信中・HLS素材なし | 「配信中・プレビュー準備中」。receiver/deliveryを失敗扱いしない |
| HLS profile切替検証中 | 「配信設定を切替中」。現在request/generationを診断へ |
| old停止後のcandidate失敗 | 「受信を維持・配信の復旧が必要」。古いactive/previewを出さない |
| VideoView control失敗 | 適用未完了と理由を表示。appliedを先行更新しない |

- [ ] candidate cancel/timeout/Stop/二重要求とcompensationを実経路fixtureで試験する。HTTP待機取消とreceiver-session取消を混同せず、候補の終了確認→Abort→所有権解放を検査する。旧publisher終了済みなのに「旧配信を維持」と報告しない。
- [ ] publisherとValidate workerの終了をそれぞれ確認し、Abort成功後だけ候補の所有権を解放する。join未確認/Abort失敗時は予約と回収責任を保持し、競合復旧・ポート/世代資源の再利用を禁止する。既存のfail-closed処理を維持し、join timeout・Abort失敗・遅延終了で未回収状態を隠さないことを試験する。
- [ ] VideoViewの稼働中要求へsession/generation/revisionを必ず付ける。ライブ受理後の保存失敗は受理済みrevisionと現在状態を応答・表示し、保存だけの再試行を可能にする。native applied到達とsettings保存成功を別々に表示する。
- [ ] preview retry timerは一つ、停止/別session切替時に解除する。ユーザーのpause/mute操作を自動再試行で上書きしない。状態APIがHLS素材の待機を繰り返してUI全体を止めないか測定する。
- [ ] 既存RTSP長時間修正のworker join/diagnostic closeテストを再利用し、AirPlay切替と共存するか確認する。`rtsp_gate.go`等の別作業差分を再実装しない。失敗した場合のみ当該計画の担当範囲へ切り出す。

**validation:** Go AirPlay/airplaycontract、対象OBS/serverテストを完走。race対応ツールチェーンがある場合はVideoView/reconfigure/gateの対象へ`-race`を追加する。未対応の場合は`NOT_RUN`とし、通常テストPASSをrace合格の代用にしない。

## T6. 一致した新ランタイムと対応ソースを作る

**depends_on:** [T5]。**status:** 未着手。

**location:** `scripts/build-uxplay-source-clock.ps1`、`build-airplay-gstreamer-bridge.ps1`、`package-airplay-source-clock.ps1`、`tests/verify-airplay-source-clock-package.ps1`と既存パッケージ検査。生成物は一意な `build/airplay-stability-candidate-20260922/` 配下。

**description / steps:**

- [ ] receiverの固定source commitと現在のpatch順序を記録する。現行build scriptは0001〜0014を参照するため、0013だけが存在するmanifestを手書きしない。0012 bootstrap cache・0013 reconnectを含む実ビルドとnative testを行う。
- [ ] T2/T4を含むbridgeを既存scriptでReleaseビルドする。T0で確認したGStreamer root/versionおよびPCM/UDPフラグを指定し、native provenanceに記録する。
- [ ] single-slice試験を省略せず、実行成功後のbridge hashでprovenanceを生成する。既定scriptで別扱いの長時間audio診断も個別に有界実行し、除外をPASSに含めない。
- [ ] `package-airplay-source-clock.ps1`へ実receiver/bridge出力とGStreamer runtime rootを渡し、新しい出力ディレクトリに候補を作る。`verify-airplay-source-clock-package.ps1`の `-PackageDirectory` / `-ReceiverBinary` / `-BridgeBinary` で実バイト列と機能を照合する。
- [ ] 変化したreceiver/bridgeと全DLLに対応する完全ソース・patch・ビルド手順・license manifestを揃える。runtime ZIPと対応ソースZIPを同じ候補版から作り、`verify-airplay-h264-archive.py` と `verify-airplay-license-archive.py` を実ZIPの組に適用する。
- [ ] ローカル候補descriptor/bootstrapを実サイズ・SHA-256から生成し、通常の検証・埋め込み準備経路で候補EXEを作る。ローカル試験の入力は公開用`release/airplay-inputs.json`と分離する。取得不可の将来URLへ正式入力だけ先行更新しない。

**validation:** receiver、bridge、2 ZIP、候補EXE、runtimeSetID、source manifestが相互一致し、検査記録から辿れる。次の2検査はcandidate実ファイルを引数として実行する。

```powershell
rtk proxy python scripts/verify-airplay-h264-archive.py build/airplay-stability-candidate-20260922/release/ImagePadServer-AirPlay-Runtime.zip
rtk proxy python scripts/verify-airplay-license-archive.py build/airplay-stability-candidate-20260922/release/ImagePadServer-AirPlay-Runtime.zip build/airplay-stability-candidate-20260922/release/ImagePadServer-AirPlay-Sources.zip
```

上記`release/`をT6の出力先とする。作成済みファイルへの上書きは避け、ID変更時はコマンド・証拠のパスも同時更新する。既存成功版のruntime/cacheは保持する。

## T7. 配布候補の自動・ブラウザ受入れ

**depends_on:** [T6]。**status:** 未着手。

**location:** 既存 `scripts/test-airplay-source-clock.ps1`、`test-airplay-lifecycle.ps1`と対応Pester、T0の証拠文書。追加が必要なfixtureは専用テストファイルへ限定する。

**description / steps:**

- [ ] 新規データディレクトリ・未使用ポート・候補専用receiver名で起動する。現用設定/サービスへ接続せず、生成プロセスのPIDを記録する。Windows補助プロセスはhiddenで起動する。
- [ ] 開発PATHのGStreamer/FFmpegに依存しない環境で、展開・capability/EXE hash・plugin scanner/registry・Bonjour・`/healthz`・`/api/state`・実receiver/bridgeパスを照合する。
- [ ] synthetic入力で無音開始→音声後着、横縦回転、HOLD、contain/cover切替、画質変更、publisher復旧を実行する。RTSP直結/公開gate/HLS/録画それぞれでframes・音声・generationの連続性を検査する。
- [ ] HLS欠落/未来part待ち、candidate起動失敗、Stop競合、sidecar書込み失敗を注入し、状態とcleanupを確認する。failed候補から自動的に別ランタイムへ逃げない。
- [ ] ブラウザで初回プレビュー、遅延準備、503後回復、pause/mute、表示モード、設定切替を操作し、API状態と実描画を合わせて確認する。
- [ ] 30回の切替/再接続と30分の合成連続試験を行い、PID/socket/メモリー増加、CPU、遅延、終端ログを記録する。終了後に候補所有子プロセスと一時listenerが消えることを確認する。

**validation:** 各試験にコマンド・最終exit code・成果物hash・実測結果を残す。合成長時間試験成功と実iPhone成功を別判定にする。FFmpeg/native testのSKIPは配布合格にしない。

## T8. 実iPhone・VRChat受入れ

**depends_on:** [T7]。**status:** 未着手。

**location:** T0の証拠文書、候補別のログ/録画。実機入力のみユーザー協力が必要。

- [ ] iPhoneホーム画面で無音開始し、後から音声のあるアプリを再生する。映像先行・音声後着・A/V同期・音声終了後の映像維持を確認する。
- [ ] 縦横回転、アプリ切替、static HOLD中のcontain/cover変更、画質/HLS↔RTSP変更、再ミラーリングを確認する。coverが新generationでも維持されることをAPI ACKと画面で確認する。
- [ ] 公開URLを維持し、PC/Androidと以前の問題が出たVRChatワールドで、初回/途中参加・全面表示・fps・音声を確認する。
- [ ] 30回の再接続/切替、30分以上の連続動作を同じ候補で記録する。再現条件が長時間だった場合は報告された時間以上へ延長する。

**validation:** 実機・クライアント・条件別PASS/FAIL/NOT_RUN、runtimeSetIDと全実行hash、再現時刻が揃う。実機を利用できない場合は自動検証済み範囲までを完了として報告し、実機認定は保留する。

## R1. 公開・現用切替の引継ぎ

**depends_on:** [T8]。**status:** 別指示待ち。T0〜T8の実装/検証と公開権限を混同しない。

次の未公開版を確定し、本体Version、releaseTag、runtimeSetID、2 ZIP、`release/airplay-inputs.json`、生成bootstrapを同じ候補に合わせる。正式URLの2 ZIPを取得し直してhash/ソース/ライセンスを確認し、通常release workflowを通す。現用切替時は前のEXE+runtime+descriptor一式を保持し、互換性のある組単位で復旧可能にする。新コード+旧runtimeだけの組合せへ戻さない。

## 5. 完了判定と証拠形式

T1〜T5は対象回帰テスト、T6は実ZIP検査、T7は候補EXEでの自動/ブラウザ試験、T8は実機条件別の結果を満たした時だけ完了にする。未再現のreject policy変更は「変更不要・現仕様の検査追加」として閉じられる。PAUSEDの許可も、正常なlive startupを確認できれば削除しない。

各結果行は `test ID / layer / session / generation / artifact SHA-256 / command / exit code / expected / actual / PASS|FAIL|NOT_RUN|BLOCKED` を記録する。障害は `firstObservedComponent` と判明した `initiator` を区別し、先にログへ出たコンポーネントを原因と決めない。

本計画の優先順は **互換性判定 → 表示モードの世代引継ぎ → HLS準備の誤判定 → 診断・切替統合 → 一致した配布候補 → 自動/実機受入れ**。T3は独立して並行実施できるが、共有ファイルの統合と実行ランタイム更新はメイン担当者が一貫して扱う。

**計画レビュー記録:** 2026-09-22、読取専用の独立レビューを実施。世代識別子の省略、候補/Validate workerの終了未確認、ライブ受理後の設定保存失敗からの復旧という3点をT2/T5へ反映した。並列waveの編集範囲と依存関係を確認した。これは計画のレビューであり、実装・テスト・実機検証の完了を示さない。

**実装進捗記録:** 2026-09-22、T1〜T3とT5の対象コード・回帰テストを実装。T4は既存native診断とfixtureを維持し、GStreamer開発SDK不足のため再ビルドは保留。T6〜T8は一致した新runtime candidateの生成後に実施する。自動検証の詳細は `docs/verification/2026-09-22-airplay-stability-acceptance.md`。
