# AirPlay全画面クロップ実装計画

> **For agentic workers:** 実行には`superpowers:executing-plans`を使用し、下記の依存順に進める。過去のユーザー指定「君だけで」「トークン節約」に合わせ、主担当が順次実行する。今回の依頼は計画作成まで。

**Goal:** ブラウザからAirPlay配信映像を全体表示／中央クロップへ切り替え、音声と配信接続を維持する。

**Architecture:** デコードした元画像とcapsを保持し、既存schedulerが選んだ画像を配信の固定サイズへ変換する。表示変更はpublisher別の制御・状態ファイルで伝え、既存の画質切り替えによる再起動を利用しない。通常・HOLD・回復候補を同じ描画関数へ集約する。

**Tech Stack:** Go、既存のHTML/JavaScript、C、GStreamer 1.28.6、GLib、CMake/CTest、既存の配布検査スクリプト。

**Spec:** [設計書](docs/superpowers/specs/2026-09-16-airplay-fullscreen-design.md)

作成日: 2026-09-17更新。基準HEAD: `29450e6d474566fa525d316504a4ae78fc58df40`（公開版`v1.8.0`）。全タスク未実装。

## 共通制約

- UIは「画面を埋める」。OFF=`contain`、ON=`cover`、初期OFF、中央基準、設定を保存する。
- 全RTSP/HLS視聴者と録画に反映する。ブラウザのCSS変更だけで完了にしない。
- 表示変更でUxPlay・bridge・MediaMTXのPID、RTSP接続、publisher generation、録画ファイルを変えない。
- 出力解像度・fps・SAR=1:1・音声・時刻基準・エンコーダー設定を保持する。
- 出力H.264は実測で1フレーム1スライス。互換性契約の期待値を緩めない。
- 表示変更を入力到着・media-ready・無信号タイマーの更新理由にしない。
- cropとholdの制御はsource-clock経路だけに実装する。旧経路・旧bridgeは未対応表示とし、受信機能を壊さない。
- Windows実機、Mac/Linux CI、合成テストの結果を区別する。
- 既存の別作業を保護する。計画作成で製品コード変更・配信停止・commit/push/releaseを行わない。
- 実装時は作業開始時の差分を再確認し、隔離worktreeを使用する。関連する未コミット変更がある場合は由来を記録して取り込む。
- 公開済み`v1.8.0`のタグ・Release asset・`release/airplay-inputs.json`を上書きしない。実装成果物は次版`v1.8.1`（または実装時点の次の未公開版）として扱う。
- 現行v1.8.0のNiconico UI、native payload、Release workflowを保持する。AirPlay変更でこれらを削除・巻き戻し・置換しない。

## 依存関係と担当

```text
T1 契約・幾何計算
       ↓
T2 native描画・動的制御
       ↓
T3 Goの状態管理・世代引き継ぎ
       ↓
T4 HTTP API・ブラウザスイッチ
       ↓
T5 合成E2E・RTSP互換性・性能
       ↓
T6 実配布物作成・実機受入れ
```

各タスクの所有者は主担当。別担当を使う場合も下記ファイル所有範囲を守り、完了条件を確認してから次へ進む。
この変更はnativeの保持画像・Goのライフサイクル・配布物が結び付くため、並列作業を増やさない。
計画の設計・漏れ確認は主担当が実施する。

## T1: 表示契約と幾何計算を固定する

**depends_on:** `[]`

**ファイル:**

- 新規: `native/airplay-gstreamer-bridge/video_view.h`、`video_view.c`、`tests/test_video_view_geometry.c`
- 新規: `internal/airplaycontract/video_view.go`、`video_view_test.go`
- 変更: `native/airplay-gstreamer-bridge/CMakeLists.txt`

**インターフェース:**

```c
typedef enum {
  AIRPLAY_VIDEO_VIEW_CONTAIN = 0,
  AIRPLAY_VIDEO_VIEW_COVER = 1
} AirPlayVideoViewMode;
typedef struct { int x, y, width, height; } AirPlayVideoRect;
typedef struct { AirPlayVideoRect src, dst; } AirPlayVideoLayout;
/* FALSE: 寸法、PAR、crop境界、演算範囲が不正。 */
gboolean airplay_video_view_layout(
    const GstVideoInfo *input, const AirPlayVideoRect *visible,
    int output_width, int output_height, AirPlayVideoViewMode mode,
    AirPlayVideoLayout *layout);
```

```go
type VideoViewMode string
const (
    VideoViewContain VideoViewMode = "contain"
    VideoViewCover   VideoViewMode = "cover"
)
func ParseVideoViewMode(raw string) (VideoViewMode, error)
type VideoViewPaths struct { Control, State string }
func VideoViewPathsForReady(ready string) (VideoViewPaths, error)
// paths: ready + ".video-view.ini" / ready + ".video-view.json"
```

- [ ] 下表をCテーブルテストにし、関数未実装で失敗することを確認する。GstVideoInfoはI420で作り、PARを指定する。

| 入力→出力 | モード | 検査する矩形 |
|---|---|---|
| 1080×1920→1920×1080 | contain | src全体、dst=(656,0,608,1080) |
| 1080×1920→1920×1080 | cover | src=(0,656,1080,608)、dst全面 |
| 2560×1080→1920×1080 | cover | src=(320,0,1920,1080)、dst全面 |
| 2048×1536→1920×1080 | cover | src=(0,192,2048,1152)、dst全面 |
| 1920×1080→1280×720 | 両方 | src全体、dst全面 |
| 720×576、PAR=16/15→1920×1080 | cover | src=(0,72,720,432)、dst全面 |

```c
GstVideoInfo in;
AirPlayVideoLayout got;
gst_video_info_set_format(&in, GST_VIDEO_FORMAT_I420, 1080, 1920);
in.par_n = in.par_d = 1;
g_assert_true(airplay_video_view_layout(&in, NULL, 1920, 1080,
                                      AIRPLAY_VIDEO_VIEW_COVER, &got));
g_assert_cmpint(got.src.y, ==, 656);
g_assert_cmpint(got.src.height, ==, 608);
g_assert_cmpint(got.dst.width, ==, 1920);
```

- [ ] 表示比率を`有効幅 * PAR_n / (有効高さ * PAR_d)`で比較し、containは出力内矩形、coverは入力内矩形を求める。I420の切り抜き位置・寸法は2画素境界へ最近傍丸めして有効領域内に収める。四捨五入に伴う誤差は各辺2入力画素以内、coverの出力は常に全面とする。
- [ ] crop metadataあり／なし、奇数寸法、PARが0、0寸法、範囲外crop、極端な値によるオーバーフローをテストする。不正なframeは採用しない。設定文字列はHTTPでは未知値を400にし、保存済み設定の欠落だけをcontainに移行する。
- [ ] 制御・状態プロトコルを下記で固定し、Go/Cの共通fixtureをテストへ置く。nativeではGLib `GKeyFile`を使用し、新しいJSONパーサ依存を加えない。

```ini
[video-view]
schema=1
session-id=session-example
publisher-generation=1
revision=2
mode=cover
```

```json
{"schema":1,"sessionId":"session-example","publisherGeneration":"1","configuredRevision":"2","configuredMode":"cover","appliedRevision":"2","appliedMode":"cover","phase":"active","outputPtsNs":"1000000000","error":""}
```

revision・世代・PTSはJSONでは10進文字列として扱い、JavaScriptの整数精度に依存しない。制御ファイルは4KiB以下、必須キー各1個、未知キー・重複・未知mode・異なるidentityを拒否する。状態は8KiB以下で検査する。パスはサーバーが生成し、HTTPから受け取らない。

**検証:** 新規CTest `airplay_video_view_geometry`と`rtk proxy go test ./internal/airplaycontract -run TestVideoView -count=1`が成功する。

## T2: 元画像を保持してnativeで動的に切り替える

**depends_on:** `[T1]`

**ファイル:**

- 新規: `native/airplay-gstreamer-bridge/video_view_renderer.h/.c`、`video_view_control.h/.c`
- 新規: 同`tests/test_video_view_renderer.c`、`tests/test_video_view_control.c`、`tests/test_video_view_pipeline.c`
- 変更: 同`source_clock_pipeline.c`、`source_clock_candidate.inc`、`source_clock_events.h/.c`、`source_clock_pipeline_internal.h`、`CMakeLists.txt`
- 必要なfixture更新: 同`tests/test_candidate_pipeline.inc`、`tests/test_video_input.c`、`tests/test_scheduled_video_buffer.c`

**インターフェース:**

```c
typedef struct AirPlayVideoViewRenderer AirPlayVideoViewRenderer;
AirPlayVideoViewRenderer *airplay_video_view_renderer_new(
    const GstVideoInfo *output_info);
/* 戻り値は呼び出し側所有。画像/caps/crop/revisionが同じ場合はpixelメモリを再利用。 */
GstBuffer *airplay_video_view_render(AirPlayVideoViewRenderer *renderer,
    GstSample *source, AirPlayVideoViewMode mode, uint64_t revision,
    GError **error);
void airplay_video_view_renderer_free(AirPlayVideoViewRenderer *renderer);
```

- [ ] 四隅と中央の色・座標が分かる合成画像から実画素を検査するテストを先に追加する。中央クロップの範囲、containの黒帯、色レンジ、padding付きstrideを検証し、幾何関数の値だけで合格にしない。
- [ ] decode→appsinkから固定サイズ化を外し、`videoconvert ! video/x-raw,format=I420`までにする。`on_decoded_video_sample`で各sampleのcaps・buffer・cropを検査する。queueとlastの`opaque`は`GstSample`参照に統一し、停止・drop・回転・候補採用の全解放箇所を更新する。
- [ ] 元画像のqueueは既存64枚上限に加え合計256MiB、単一sampleは64MiBを上限とする。期限を過ぎた不要画像から解放する。全画像が将来PTSの場合は新着を拒否して最も近い将来画像を残し、容量不足で表示可能画像が永久に消える状態を防ぐ。通常の時刻選択ポリシーは維持する。上限境界と長い入力先行をテストする。
- [ ] schedulerが選んだsampleを`GstVideoFrame`でmapし、T1の矩形を使って固定出力のI420へ同期変換する。GStreamerのsource/destination矩形・黒帯塗りを使い、入力の色空間・rangeを出力契約へ正しく変換する。変換器を新しく構築して成功後に交換し、失敗で部分変更し得る既存変換器への`set_config`は使わない。

```c
/* GstStructureはgst_video_converter_newへ所有権を移す。 */
GstStructure *config = gst_structure_new("video-view",
    GST_VIDEO_CONVERTER_OPT_SRC_X, G_TYPE_INT, layout.src.x,
    GST_VIDEO_CONVERTER_OPT_SRC_Y, G_TYPE_INT, layout.src.y,
    GST_VIDEO_CONVERTER_OPT_SRC_WIDTH, G_TYPE_INT, layout.src.width,
    GST_VIDEO_CONVERTER_OPT_SRC_HEIGHT, G_TYPE_INT, layout.src.height,
    GST_VIDEO_CONVERTER_OPT_DEST_X, G_TYPE_INT, layout.dst.x,
    GST_VIDEO_CONVERTER_OPT_DEST_Y, G_TYPE_INT, layout.dst.y,
    GST_VIDEO_CONVERTER_OPT_DEST_WIDTH, G_TYPE_INT, layout.dst.width,
    GST_VIDEO_CONVERTER_OPT_DEST_HEIGHT, G_TYPE_INT, layout.dst.height,
    GST_VIDEO_CONVERTER_OPT_FILL_BORDER, G_TYPE_BOOLEAN, TRUE,
    GST_VIDEO_CONVERTER_OPT_BORDER_ARGB, G_TYPE_UINT, 0xff000000u,
    GST_VIDEO_CONVERTER_OPT_ASYNC_TASKS, G_TYPE_BOOLEAN, FALSE, NULL);
```

- [ ] 変換はscheduler専用とし、sample参照・表示snapshotを取得した後にロック外で実施する。bufferのPTS/DTS/durationは現在の固定周期処理を継続する。色・矩形に依存するmetadataを無条件コピーしない。入力cropを処理済みの出力へ再付与しない。
- [ ] cacheは入力sampleの同一性・caps・有効領域・表示revisionで識別する。HOLDは変換済みbufferの読み取り専用pixelメモリを共有し、タイムスタンプ用のbuffer headerだけを複製する。モード切り替え時は保持中の元sampleから再変換する。変換失敗は旧正常画像を保持し、新revisionの適用成功を通知しない。
- [ ] `source_clock_candidate_decoded`はbufferだけでなくsampleを保持し、`source_clock_candidate_tick`も同じ変換関数を通す。証拠用IDRの1回投入、順序、watermark、media-readyの条件を保持する。変換に失敗した候補の証拠を合格させない。
- [ ] `publisher-ready`へ任意フィールド`videoViewProtocol:1`を追加する。制御/状態パスは既存readyパスからT1の規則で導出する。CLIに新必須引数を追加しない。起動前に置かれた初期制御を読み、以後GLib main loopで100msごとに監視する。
- [ ] 制御ファイルを完全に読み込んで検証した後、希望snapshotを短いmutex区間で交換する。ファイルI/O・変換・状態書き出しを音声ロック内やdecoder callback内で実施しない。状態は既存のUTF-8対応・原子的スナップショット書き込みを共用し、変更があった時だけ書く。
- [ ] `configuredRevision`は有効な設定を受理した時点、`appliedRevision`はその設定で実画像またはHOLD画像を変換しappsrcへの投入に成功した時点で進める。画像未到着は`waiting_input`。停止が競合した場合は新たな適用成功を発行しない。nativeの不正制御は既存表示を維持し、回復可能な表示エラーとして扱う。

**検証:** `airplay_video_view_renderer/control/pipeline`が成功。実画像1枚だけを入力→HOLD→ON→OFFで画素が切り替わる。回転前の保持sampleを回転後のcapsで解釈しない。通常/候補/停止で参照リーク・二重解放なし。既存candidate、media-ready、single-sliceのテストも維持する。

## T3: Goで設定保存・状態確認・世代引き継ぎを管理する

**depends_on:** `[T1,T2]`

**ファイル:**

- 新規: `internal/airplay/video_view.go`、`video_view_control.go`、それぞれの`_test.go`
- 変更: `internal/settings/settings.go`とテスト、`internal/airplay/manager.go`、`source_clock_session.go`、`source_clock_reconfigure.go`
- 変更: `internal/airplaycontract/event.go`とテスト、`internal/airplay/source_clock_session_test.go`

**インターフェース:**

```go
type VideoViewRequest struct {
    Mode airplaycontract.VideoViewMode `json:"mode"`
    ExpectedSessionID string `json:"expectedSessionId"`
    ExpectedPublisherGeneration uint64 `json:"expectedPublisherGeneration,string"`
    ExpectedRevision uint64 `json:"expectedRevision,string"`
}
type VideoViewState struct {
    Supported bool `json:"supported"`
    Reason string `json:"reason,omitempty"`
    SessionID string `json:"sessionId"`
    PublisherGeneration uint64 `json:"publisherGeneration,string"`
    DesiredMode airplaycontract.VideoViewMode `json:"desiredMode"`
    DesiredRevision uint64 `json:"desiredRevision,string"`
    ConfiguredMode airplaycontract.VideoViewMode `json:"configuredMode,omitempty"`
    ConfiguredRevision uint64 `json:"configuredRevision,string"`
    AppliedMode airplaycontract.VideoViewMode `json:"appliedMode,omitempty"`
    AppliedRevision uint64 `json:"appliedRevision,string"`
    Phase string `json:"phase"`
    Error string `json:"error,omitempty"`
}
func (m *Manager) VideoViewStatus() VideoViewState
func (m *Manager) SetVideoView(ctx context.Context, request VideoViewRequest) (VideoViewState, error)
```

`phase`は`idle/pending/waiting_input/active/failed/unsupported`。保存設定は`AirPlayVideoViewMode string` / JSON=`airplayVideoViewMode`。revisionはプロセス内の設定系列で増加し、nativeはsession/publisherとの組で照合する。同値への再設定は正常状態ならno-op、failed時の明示再試行は新revisionを割り当てる。

- [ ] fake nativeを使い、保存成功だけではappliedが進まない、ACKが違う世代/古いrevisionなら無視、ブラウザ切断で配信が停止しないテストを先に作る。
- [ ] 既存設定が項目を持たない場合はcontainにする。稼働中は実際のreadyが対応を示した場合だけ操作を受け付ける。停止中は希望値の保存を許可し、対応は受信開始時に確認する。未対応nativeでは保存済みcoverを適用済みと表示しない。
- [ ] Goのセッション所有者で操作を直列化する。停止、回復、画質切り替え、世代不一致、同時操作を保存前に判定する。短時間の制御受付中は画質切り替え側も競合を返し、互いに古いpublisherへ書き込まない。画像待ちが長いだけでは他の設定を永久に止めない。
- [ ] 保存→control書き込み→状態照合を行う。controlは同じディレクトリの一時ファイルをcloseしてから置換する。Windowsの共有違反は最大500msの範囲で再試行し、失敗はstateへ表示する。設定保存失敗ならcontrolを書かない。保存後の制御失敗ならdesiredとappliedを分けて残し、成功を偽らない。
- [ ] HTTPの待機終了は操作取消と同一視しない。受付前のcancelは未受付、受付後はセッション所有の処理として継続する。state照合はpending中100ms、安定中1秒。停止時は所有者が終了させ、未確定操作を打ち切る。nativeのconfigured確認は2秒で失敗扱い、遅延ACKは現session/世代/最新revisionと一致する場合だけ実状態として再照合する。
- [ ] 初回・画質変更候補・回復候補の起動前に、それぞれのreadyパスへ最新希望値の初期制御を用意する。候補への操作をactiveとして表示せず、正式昇格後に状態を切り替える。旧ファイル/旧世代のACKは再利用しない。描画だけの操作でdelivery generationを増やさない。
- [ ] `VideoViewMode`を`DirectOutputConfig`やdelivery descriptorの同一性へ混ぜない。表示変更で既存のReconfigure/Restart経路が呼ばれないことをテストする。

**検証:**

```powershell
rtk proxy go test ./internal/settings -run TestAirPlayVideoView -count=1
rtk proxy go test ./internal/airplaycontract -run TestVideoView -count=1
rtk proxy go test ./internal/airplay -run TestVideoView -count=1
rtk proxy go test ./internal/airplay -run TestReconfigureSourceClockDelivery -count=1
```

レース検査は対応するGo/C toolchainの環境で対象テストに限定して実行する。通常設定、連打、二つのブラウザ、操作中停止、回復、quality変更、保存失敗、部分ファイル、状態ファイル消失を含める。

## T4: APIとブラウザスイッチを接続する

**depends_on:** `[T3]`

**ファイル:**

- 新規: `internal/server/airplay_video_view.go`、`airplay_video_view_test.go`、`ui_script_airplay_video_view.go`
- 変更: `internal/server/server.go`、`ui.go`、`ui_scripts.go`、`ui_script_domrefs.go`、`ui_script_statesync.go`、`ui_script_airplay.go`、`ui_script_uploadevents.go`
- テスト: `internal/server/airplay_ui_test.go`、新規`airplay_video_view_ui_test.go`

**インターフェース:**

```text
GET  /api/airplay/video-view → {"airplayVideoView": VideoViewState}
POST /api/airplay/video-view → VideoViewRequest
200: 希望値保存のみ（停止中）、または描画への反映まで確認済み
202: 受付済み・反映待ち（映像待ちを含む）
400: 不正JSON、未知mode、必須identity/revision欠落
409: 古い状態、操作競合、停止中への古い稼働要求、未対応runtime
500: 設定保存などのI/O失敗
```

すべて既存`admin`保護下へ登録する。HTTPエラーも機械可読のcodeと可能なら最新`airplayVideoView`を返す。稼働前の希望値保存は空session/世代0を要求し、稼働直前との競合をManagerで検査する。

- [ ] `httptest`で認証、GET/POST、余分なJSON・巨大body（上限4KiB）、不正値、409/202、保存失敗を検証する。fake controllerへ渡すidentityとmodeを確認し、OBSのrestartが呼ばれないテストを置く。
- [ ] `/api/state`の通常応答・状態配信の両方へ`airplayVideoView`を追加する。UI側は独立した保存中フラグを持ち、AirPlay操作/画質変更と競合する間だけスイッチを無効化する。
- [ ] AirPlay画質の近くへ通常のcheckboxをスイッチ表示で追加する。ブラウザ全画面APIと混同する「全画面」単独ラベルは使わない。
- [ ] v1.8.0で同じUIファイルに追加されたNiconicoコメント欄・DOM参照・`updateNiconicoCommentsOption`の動作を保持する。AirPlayスイッチの初期化・状態同期・イベント登録を既存Niconico処理の上書きやmedia intentの変更なしで追加する。

```html
<label for="airplayVideoViewCover">
  <input id="airplayVideoViewCover" type="checkbox" role="switch">
  <span>画面を埋める</span>
</label>
<p>縦横比を保ち、はみ出しを中央でカットします。</p>
<p id="airplayVideoViewStatus" role="status" aria-live="polite"></p>
```

- [ ] POSTには直前のサーバー状態のidentity/revisionを付ける。`pending`は「切り替え中」、`waiting_input`は「映像待ち」、`unsupported`は「このAirPlayランタイムでは利用できません」。通信失敗時はGETで最新状態を取り直し、古い成功応答や古いタブの状態で上書きしない。
- [ ] ブラウザ再読み込み・AirPlayタブへの移動でも保存値を再表示する。プレビュー要素の再作成、`load()`、再生URL変更は表示操作のハンドラから呼ばない。
- [ ] 実ブラウザでOFF/ON、二つのタブ、キーボード操作、受信前→開始、エラーからの復帰を確認する。ソース文字列テストのみでUI完了としない。

**検証:** `rtk proxy go test ./internal/server -run TestAirPlayVideoView -count=1`、実ブラウザの状態/API/コンソール確認。UIの変更だけで視聴側へ届いたと判断しない。

## T5: 実映像と接続継続を検証する

**depends_on:** `[T2,T3,T4]`

**ファイル:**

- 新規: `native/airplay-gstreamer-bridge/tests/test_video_view_encoded.c`
- 新規: `scripts/test-airplay-video-view.ps1`
- 変更: `native/airplay-gstreamer-bridge/tests/test_source_clock_single_slice.c`、`CMakeLists.txt`
- 関連回帰: `internal/airplay/source_clock_session_test.go`、`internal/server/airplay_delivery_test.go`

- [ ] 合成入力に座標目盛り・中央領域・移動マーカーを入れ、実bridge→RTSP読取→デコード画像で切り抜き位置を確認する。入力AU供給は既存source-clock試験の生成/送信部を再利用し、mockのH.264を互換性証拠にしない。
- [ ] 通常動画、実画像1枚＋無信号HOLD、portrait→landscape→portrait、送信停止→再開、音声の遅延開始を試す。ON/OFFを各形式で反復し、HOLDの再描画で無信号タイマーが延長されないことも確認する。
- [ ] 360p/720p/1080p、30/60fps、contain/coverで圧縮映像を検査する。既存single-slice検査のCPU数・向き・設定行列を残し、動的切り替え前後を追加する。各AUのVCL NAL=1、出力サイズ一定、SAR=1:1、PTS単調、デコードエラーなしを必須にする。
- [ ] ネットワーク上の接続数とプロセスIDを記録する。表示切り替えによるRTSP TEARDOWN/再ANNOUNCE、MediaMTX経路削除、HLS discontinuity、録画分割が0であることを確認する。GOP由来の既存更新は表示変更による切断と混同しない。
- [ ] 音声は連続音源のデコード結果で欠落・PTS飛びを調べる。合成E2Eでは切り替えに起因する100ms超の音声欠落を許容しない。映像/音声の差が切り替え前から100ms超増加しないことを確認する。
- [ ] 操作からnativeの正常投入ACKまでを計測し、通常・HOLDとも500ms以内を目標とする。画面到達時間はRTSP/HLSで別記する。API成功だけを描画検査の代わりにしない。
- [ ] 元入力サイズで保持するため、64枚/256MiB制限と単一64MiB制限を負荷試験する。2秒分の未来PTS入力を小さい試験予算で再現し、表示が飢餓にならないことを確認する。HOLD中はrevisionが変わらなければ変換回数が増えないことを検査する。
- [ ] 同一入力・画質・CPUで変更前contain、変更後contain、変更後coverを各60秒測る。変換p95が1フレーム時間の半分以下、達成fpsが基準の95%以上、10分反復で処理済みsample/変換器数が増え続けないことを受入目標とする。未達なら原因を記録し、余分な再変換を修正してから先へ進む。

**検証コマンド:**

```powershell
rtk proxy ctest --test-dir build/airplay-fullscreen -C Release -R airplay_video_view --output-on-failure
rtk proxy ctest --test-dir build/airplay-fullscreen -C Release -R airplay_source_clock_single_slice --output-on-failure
rtk proxy ctest --test-dir build/airplay-fullscreen -C Release -R airplay_candidate --output-on-failure
rtk proxy go test ./internal/airplay ./internal/airplaycontract ./internal/server ./internal/obsrtmp -count=1
```

新規PSハーネスは`-BridgePath`、`-RuntimeRoot`、`-EvidenceDirectory`を必須引数にし、専用ポート・一時データ領域を使う。既存の8080配信を停止しない。結果JSONに入力条件、各hash、操作revision、PID、generation、画素結果、音声結果、接続継続、測定値を残す。

## T6: 新ランタイムを配布物へ反映し、実機で受け入れる

**depends_on:** `[T5]`

**ファイル:**

- 変更: `scripts/build-airplay-gstreamer-bridge.ps1`、`scripts/airplay-h264-contract.ps1`、`scripts/package-airplay-gstreamer-runtime.ps1`、`.github/workflows/release.yml`、`scripts/build-release.sh`
- 変更: `scripts/verify-airplay-h264-archive.py`と対応テスト（表示機能の検査記録を追加する場合）
- 更新対象: bridgeの完全対応ソース・ビルド手順・license/source manifest、`release/airplay-inputs.json`、埋め込みpayloadの生成物。入力のreleaseTag/runtimeSetID/URLは次版`v1.8.1`へ更新する。
- 文書: `docs/AIRPLAY_DISTRIBUTION_LICENSES.md`、新規`docs/AIRPLAY_VIDEO_VIEW_ACCEPTANCE.md`

- [ ] 新規nativeテストを通常のビルド検査へ含める。成功したbridgeのハッシュに結び付いた`videoViewContract: airplay-video-view-v1`とテスト結果を生成する。文字列を手書きして合格にしない。古いbridgeは通常受信可能だが、この機能の配布合格にはしない。
- [ ] `release.yml`のNiconico compositor job、`actions/download-artifact`、`go test -p 1 ./...`を維持したうえで、AirPlayの実ZIP/source ZIP検査を追加する。`scripts/build-release.sh`の`nico_native_embedded` stagingも維持し、AirPlay runtimeの変更と混同しない。
- [ ] 成功済み配布物のprovenanceからGStreamerの版と固定PCM/UDP等のビルドフラグを読み、同じ条件でbridgeをビルドする。新しいruntimeSetIDを使い、既存IDや公開済みassetを上書きしない。
- [ ] bridgeとその対応ソースを更新する。GStreamer/UxPlay/その他DLLは今回のために更新しない。旧616本すべて不変という前回の記録を流用せず、意図したbridge変更と他バイナリの不変を再照合する。
- [ ] 完全ソースを含む検証用bundleから実行用ZIPとSources ZIPを生成し、実際の二つのZIPに両検査を適用する。

```powershell
rtk proxy python scripts/verify-airplay-h264-archive.py build/airplay-fullscreen/package/ImagePadServer-AirPlay-Runtime.zip
rtk proxy python scripts/verify-airplay-license-archive.py build/airplay-fullscreen/package/ImagePadServer-AirPlay-Runtime.zip build/airplay-fullscreen/package/ImagePadServer-AirPlay-Sources.zip
```

- [ ] exeを作り、新規データ領域・開発用PATHなしで展開/起動する。内蔵bridgeのhash、runtimeSetID、`videoViewProtocol:1`、UI状態、同じリリースのSources ZIP参照を照合する。ソースZIPをexeへ再内蔵しない。
- [ ] iPhone実機→そのexe→PC版VRChatの以前不具合が出たワールドで、縦/横/静止/動画/音声/途中参加/再接続/画質変更後のON/OFFを確認する。HLSでも再読み込み不要・音声継続を確認する。公開版の差し替えは実機確認後、公開を依頼された時に実施する。

**完了条件:** 自動テスト、実配布物、iPhone＋PC版VRChatの3段階の証拠が揃う。実機操作待ちの場合は「実装・自動検証完了、実機未確認」と報告し、合格を推定しない。Mac/Linuxのnative実機成功は別途確認されるまで未確認のまま。

## 重点回帰とエラー時の扱い

| 状況 | 期待する動作 |
|---|---|
| 新しい実画像が来ない | 元の保持画像を再描画。無信号判定は延長しない |
| 黒画像しかまだ出していない | 設定は受理、`waiting_input`。実画像適用済みとは扱わない |
| 回転中に切り替え | 各sample自身のcaps/cropを使用。固定出力を維持 |
| 保存後にファイル書き込み失敗 | desiredを残し、appliedを据え置いて失敗表示。配信継続 |
| 変換器の構築/画像map失敗 | 前の正常表示を保持。新モードを適用済みにしない |
| HTTP応答が消失 | サーバー状態を再取得。自動再POSTで古い操作を増やさない |
| 古い世代・古いタブの要求 | 409と最新状態。設定を書き換えない |
| 古い世代のACK | 無視。現世代のappliedを進めない |
| quality/回復と同時操作 | 所有者で直列化・競合応答。次世代は最新の保存設定で開始 |
| 古い/別経路のbridge | 通常受信を継続し、機能は未対応表示 |
| リリース検査だけ通過 | 実機未確認を保持。VRChat成功とは表記しない |

## トークン・作業量の節約方針

- 主担当で順次実行し、同じコード探索や全文の再読み込みを繰り返さない。
- 各段階は影響するテストだけを回し、結合段階で関連パッケージ全体を1回実施する。再実行は新しい変更/失敗がある場合に限る。
- 新機能に無関係なGPU、音楽表示、UX全体の改修を混ぜない。
- サンプルと制御の共通fixtureをGo/Cで共有し、同じ仕様を複数の場所へ書き直さない。
- runtime再パッケージ・対応ソース生成・exe埋め込みはnativeの受入後にまとめる。
- 実機テストは自動検証済みの単一成果物で行い、ユーザーへ途中ビルドを何度も試してもらわない。

## 計画セルフレビュー

- [x] 通常schedulerに加え、回復候補の別経路を対象に含めた。
- [x] 黒帯付き縮小画像の再拡大を避け、静止HOLDへの反映を規定した。
- [x] caps/stride/PAR/crop/色/メモリ上限/未来PTSの保持を規定した。
- [x] 保存値・native受理・実画素投入を分け、HTTP失敗と古いACKの扱いを規定した。
- [x] quality・停止・回復との競合と設定引き継ぎを対象に含めた。
- [x] 新runtimeの機能通知、実ZIP/対応ソース、exe内蔵、実機受入れまで含めた。
- [x] 公開済みv1.8.0を保護し、次版v1.8.1へ配布対象を繰り上げた。
- [x] v1.8.0で追加されたNiconico UI・payload・Release workflowを保持する条件をT4/T6へ追加した。
- [x] 実装・公開を実施済みと混同せず、全タスクを未実装として記録した。
