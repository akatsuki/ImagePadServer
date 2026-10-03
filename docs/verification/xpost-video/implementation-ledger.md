# X映像化・最短コース実装記録

2026-10-03。要求: バーなし、wgpu合成、CoverFlow、縦2画面・横全画面・縦1つの特例、VOICEVOX本文/引用のみ、最後の声設定、既存画像化維持。VRChat併用時CPU約20%を評価する。

Ruling: 利用者の「最短コース」に従い、まず既存取得/描画とFFmpeg/publicationへ接続するR1を実装する。常駐wgpu device・静止frame再利用・単一最終encoderは初版から使う。NV12 shader、高度なcache、hardware decode比較、独立配布資格化は後続最適化とし、未実施を明示する。理由: 動作する機能を先に届ける。cost if wrong: 転送/CPUの追加削減が必要になる。

Ruling: 既存の未追跡X画像化を必要とするため現在の共有checkoutで作業し、所有ファイルを分ける。無関係な変更のreset/restore/clean、commit/push、既存配信停止は行わない。cost if wrong: 共有変更に由来する検証失敗を分けて記録する必要がある。

Ruling: AGENTS指定のparallel-task手順を使い、独立する取得/カード・VOICEVOX・wgpu helperを並行実装する。主担当は時間軸・encoder・state/settings/UI統合と検証を担当。commit手順は明示許可がないため適用しない。各担当が自身のRED→GREENを記録する。

基点HEAD: 645de8a3ec91789512e0cc7edfb18d7f0f62e501。既存`internal/xpostimage/`・`internal/server/xpost_upload.go`は未追跡、go.mod/go.sumには既存変更がある。

## R1完了範囲

- 共通型と固定時間軸: 投稿/引用、UTF-16 entity、PCM実測sample数、整数frame clock、メディア順序、縦/横、縦1つの特例、元音声開始位置を実装。
- 取得/カード: 既存react-tweet/Go描画を再利用。原文を保持して改ページ。元投稿に添付がない場合だけ引用メディアを採用し、最大4つ。動画は公開HTTPS/IP検査・容量制限つきストリーム保存で、巨大な素材サイズのheap確保を避ける。失敗/容量超過で一時ファイルを削除。
- VOICEVOX: loopback HTTP API、話者UUIDとstyle IDの照合、話速、試聴。本文/引用本文だけを生成し、URL/メンションを除く。空本文ではエンジン接続を省く。実際の話者名でカード/パネルへクレジットを付与。
- wgpu: 0.20.1の共通シェーダー、CoverFlowの傾斜/移動/合成、ジョブ内device/pipeline/texture再利用、RGBA readback。静止シーンは完成frameを再利用し、条件を満たす横動画は合成を省く。X用native D3D12/NVENC surface interopは追加しない。
- codec: 素材の表示寸法/回転/SAR/時刻をprobeし、先頭/実際の最終frameを使用。1本のH.264 encoder、元音声とTTSの非重複配置、AAC/mux、全decodeとframe数/時刻/1-frame-1-VCL-slice検査、HLS stream-copyを実装。
- GUI: X URLを認識すると入力欄直下に動画化スイッチを表示。オン時に声・スタイル・話速・試聴・一覧更新を表示。非X URLとファイル入力では隠す。固定高さによる設定切れをブラウザで再現して修正。
- settings/job/publication: TTSを使う有効ジョブだけ最後の声を1件保存。試聴/受付失敗/空本文では変更しない。owned worker全体をWindows CPU20 Job Objectへ所属させ、キャンセルを子プロセスへ伝播。既存のprepared transactionを利用し、MP4/HLS/サムネイル/取得snapshotを検査後に履歴・公開・キューへ接続。従来のmode指定なし動画APIはyt-dlp経路を保持し、GUIの画像化は明示mode=imageで維持。
- 開発用Windows build: `scripts/build-xpost-video.ps1`でアプリ・wgpu helper・投稿取得runtimeを `build/xpost-video/` へ配置。一般リリースは実施していない。

## 実行した検証

| 層 | 結果と範囲 |
|---|---|
| Go回帰 | server/library/settingsと全X package、cmdの範囲で648件通過（11 packages）。既存動画URLのyt-dlp回帰も期待値を変えず通過。 |
| Node | `node --test internal/xpostimage/video_media_test.mjs`: 4件通過。混在順序、4つ上限、MP4選択、取得不能の失敗。 |
| Rust | `cargo test --manifest-path gpu/xpost-compositord/Cargo.toml`: 6件通過。専用release buildも成功。 |
| native codec | `XPOST_CODEC_INTEGRATION=1`で混在4素材を実wgpu/FFmpegで生成。MP4全decode/clock/音声開始offset/HLS検査を通過。追加の長い元音声の切り詰めとnegative offsetも通過。 |
| Windows CPU20 worker | 最終開発exeを実行。640x360の本文fixtureと模擬VOICEVOXで18frames/描画1回を生成し、CPU hard cap=2000、話者設定保存、実際のprepared HLS commitを確認。続く本文なしジョブはエンジン停止状態で45framesをh264_nvenc出力し、最後の声が変わらないことを確認。 |
| browser | テンプレート全JSを実Chromiumで操作。X認識時の表示、style ID=0/最後の話速、390px/1280pxで設定の操作領域、非Xで非表示を確認。API/話者一覧はfixtureであり、実サーバー+実VOICEVOXのE2Eではない。静的fixtureの未同梱icon/fontと模擬events応答由来のconsole errorは実アプリの判定に含めない。 |
| 静的検査 | 対象packageの `go vet` 通過。差分空白・文書リンクを最終確認。 |
| cross build | X関連Go packageのLinux/amd64とmacOS/arm64コンパイルを確認。実行やCPU budget、Rust backend、encoderのOS別受け入れにはしない。 |
| review | 独立したread-onlyレビューの音声指摘を修正し再レビュー。最終の音声境界/stream保存/route/CSSにblocking指摘なし。レビューは実行試験の代わりにはしない。 |

CPU証拠は [worker-cpu20-fixture.json](worker-cpu20-fixture.json)。一時出力パスは試験終了で削除される。短いfixtureのwall timeを1080p実投稿やVRChat併用の速度保証へ一般化しない。

主要なRED→GREEN: UTF-8境界近くの日本語/emojiとURL除去、空本文の視覚placeholderが誤ってTTSになる問題、本文のみ投稿の拒否、最後のportrait panel continuity、prepared側が要求する `playlist.m3u8` 名、元音声が映像より長いとFFmpeg EPIPEになる問題、negative音声offsetが前区間へ漏れる問題、巨大動画のbuffer経路、固定高さのGUI設定切れ。既存テストを削除/緩和して通過させていない。

## 再実行

```powershell
rtk go test ./internal/server ./internal/library ./internal/settings ./internal/xpostmodel ./internal/xposttts ./internal/xpostimage ./internal/xpostvideo ./internal/xpostgpu ./internal/xpostcodec ./internal/xpostexport ./cmd/imagepadserver -count=1
$env:XPOST_CODEC_INTEGRATION = '1'
rtk go test ./internal/xpostcodec -run 'TestEncodeMixedDemoWithLocalFFmpegAndCompositor|TestMixOneSpanDrainsLongAudioAfterCappedRead|TestMixAudioKeepsNegativeOffsetInsideVideoSpan' -count=1 -v
$env:XPOST_WORKER_INTEGRATION = '1'
$env:IMAGEPAD_XPOST_WORKER_EXE = (Join-Path (Get-Location) 'build/xpost-video/imagepadserver.exe')
$env:IMAGEPAD_XPOST_COMPOSITOR = (Join-Path (Get-Location) 'build/xpost-video/xpost-compositord.exe')
rtk go test ./internal/server -run TestXPostNativeWorkerCPU20AndVoiceAcceptance -count=1 -v
```

opt-in native testは必要なFFmpeg/FFprobe/helper/exeがない場合にskipする。PASS表示だけでなく、対象が実際にRUNされていることを確認する。

## 未実施・後続範囲

実X取得から公開までの実投稿、声ごとの音質/生成時間比較、VRChat再生/併用フレーム時間、VOICEVOXとserverを含めた総CPU約20%、他OS/GPU実行、HDR対応、長尺/容量上限付近の実素材、一般配布/ライセンス資格化は未確認。追加のアプリ内ENGINEにも独立owned Job Objectを適用するが、workerとの合算20%保証にはしない。外部共有エンジンへ上限を適用しない。Linux/macOS runtimeではWindows Job Object同等の上限を実証していない。

## VOICEVOX自動準備の追加検証

利用者の2026-10-03追加指定を実装。公式CPU ENGINE 0.25.2のVVPP（CORE/ONNX/モデル/辞書/規約を含む、Windows 1,894,411,533 bytes）を固定SHAで取得し、app-private toolsへ導入する。実取得→展開→起動→43話者一覧→4.821秒PCM生成を確認し、次回再利用はnative test内5.31秒で完了した。この時間は固定文の試験であり、一般的なTTS性能保証ではない。

最終開発artifactのSHA256と追加検証の範囲は [voicevox-runtime.json](voicevox-runtime.json) に記録した。

- `TestNativeOfficialInstallAndSpeech` を実行し、実公式バイナリのloopback API・PCM・owned shutdownがPASS。音声は `build/xpost-video/voicevox-preview.wav`。
- `TestNativeManagedVoiceAPIAndPreview` を実行し、server側のアプリ内voice一覧43話者・試聴WAV142,892 bytes・最後の声を保存しないこと・owned shutdownがPASS。
- 関連12 Go packagesの回帰672件を確認。その後のlifecycle競合修正はランタイムpackage全体のrace検査18件と競合テスト反復で確認。GUIのpending→ready/声選択/cancel/retryはNodeによるDOM/API fixtureで確認し、実ブラウザ+実ENGINEのE2Eとは区別する。
- レビューの外部50121誤認を明示Managed属性で修正（voices/previewのRED→GREEN）。crash時の`.install-*`はOS-exclusive lock取得後に安全な直下pathのみを空き容量検査前に回収する。active installerのstageは削除しない。壊れた版は削除せず退避する。
- レビューのClose/Retry競合を、pending Close中のStart/Retry拒否とmutex内cancelで修正。race検査中の境界を追加修正し、終了後の明示Retryを維持する。独立再レビューも行った。
- 新規初期値はアプリ内50121。既存外部engineは同じ50121でも自動採用/変更せず、GUIの明示切替を待つ。初回ダウンロードはHTTP startupをブロックせず、中止後に自動再開しない。公式ENGINE標準ユーザー辞書の参照先はそのまま、変更APIは無効。
- Linux/amd64とmacOS/arm64のruntime/X関連Go cross build、`go vet`、専用Windows開発buildを確認。実行検証はWindowsのみ。CPUはengine 1合成thread+独立20% capで、exportとの合算制限ではない。

```powershell
$env:VOICEVOX_NATIVE_INTEGRATION = '1'
$env:VOICEVOX_NATIVE_ROOT = Join-Path $env:APPDATA 'ImagePadServer/tools/voicevox'
rtk go test ./internal/voicevoxruntime -run TestNativeOfficialInstallAndSpeech -count=1 -v
$env:VOICEVOX_SERVER_INTEGRATION = '1'
rtk go test ./internal/server -run TestNativeManagedVoiceAPIAndPreview -count=1 -v
rtk go test -race ./internal/voicevoxruntime -count=1
```

NV12 shader、非同期readback、hardware decodeの比較、ジョブ間常駐worker、素材/TTS/完成動画cache、cold/warm比較は元の全体計画に残す。初版を「最高速」「全GPU」「ゼロコピー達成」と呼ばない。無関係な共有変更のリセット、コミット/プッシュ、既存配信の停止は行っていない。

## 実投稿の終端EOF修正とフレーム数・残り時間

利用者が提示した投稿 `2100851675305324760` で `decode media 0 frame 9878: EOF` を調査。実動画は720x1280・9878フレームだが、stream durationは `2963400060 × 1/9000000 = 329.266673333333秒` で、30fps境界を約6.67マイクロ秒超える。タイムラインの切り上げは9879フレームを必要とし、従来のfps filterの終端丸めは9878フレームで終了していた。

- `duration_ts × time_base`を小数表記より優先し、ffprobeのマイクロ秒丸めだけでフレームが増える別のケースも防ぐ。無効なtime baseでは従来のstream/format durationへ戻す。
- fps filterに `eof_action=pass`を指定し、入力の終端時刻まで最後のフレームを保つ。先頭/末尾posterも同じ規則を使う。途中のEOF、短いRGBAフレーム、decoder終了異常、完成映像のフレーム数/時刻/slice検査は引き続きエラーにする。
- 実FFmpegのRED→GREENを確認。30fps/CFRの68フレームで予定69・EOF68、29.97fpsの67フレームで予定68・EOF67を再現し、修正後はCFR68→68、NTSC67→68を縦/横とも通過。実素材も9879フレームを最後まで読み取り、終了コード/実フレーム数の検査を通過した。
- 追加指定の進捗は既存dashboardの `ingest.progressText`へ通知する。変換済み/全体フレーム、5秒・30フレーム後の実測速度による映像の残り時間、仕上げ・検証中の段階を表示。最大約2回/秒へ間引き、最後の数は必ず送る。取得/TTS/音声/mux/検証を映像ETAに含めない。
- 進捗の計算中・実測ETA・仕上げ区別・間引きの4件をRED→GREENで確認。UTF-8/JSONを1バイトずつ分割したworker出力から、表示用stateへフレーム数とETAが届くことも確認。関連X 7 packagesは51件、server進捗/ingest範囲は5件通過。対象 `go vet` とgofmt/差分空白検査を通過した。

最終開発exeで、実X取得→実VOICEVOX本文読み上げ→1080p/30fps・wgpu/Vulkan・NVENC→MP4全decode/9943フレーム/1-frame-1-slice→AAC/mux/HLSのworker全工程が成功。331.433333秒の出力を約123.81秒で生成した。Windows Job Object hard cap=2000、worker treeの平均CPUは約17.88%。VOICEVOX/server/VRChatを含めた合算値ではない。この1投稿・1環境の結果を一般的な速度保証にしない。

数値、進捗例、source/output/exe/helper SHA256は [eof-regression.json](eof-regression.json)。生成済みの確認用MP4は `build/xpost-video/eof-probe/real-progress-job/output.mp4`。稼働中8080のアプリは停止/更新実行せず、既存公開状態と最後の声設定は変更していない。実投稿のGUI公開操作・VRChat再生・他OS実行は引き続き未確認。

## 投稿・引用表示とテーマ背景の修正

利用者指定で高速化を [作業キュー](work-queue.md) の後続に移し、先に簡易カードを修正した。動画の投稿カード/右側パネルから、静止画版の `drawHeader`、本文/URL整形、角丸引用ボックス、配色を共通利用する。取得済みの日時/リンク情報を共通モデルへ保持し、プロフィール画像は作者ごとに1回取得する。取得不能時は静止画版と同じplaceholderを使う。引用の読み上げ中も元投稿のプロフィールと本文抜粋を残し、読み上げ対象の本文は実際のフォントと両画面幅に合わせて改ページする。取得レスポンスにentityがないURL/メンションも分割境界から保護する。

引用描画は目標解像度のフォントとアイコン寸法を直接使い、拡大による文字のぼやけを避ける。静止画版は倍率1の既存経路を維持し、ライト/ダーク双方の比較PNGのSHA256一致を確認した。読み上げは本文だけで、表示名/ID/日時/引用プレビューを重ねて読まない。

追加指定の映像背景は、ライトで白、ダークで黒。テーマをタイムラインへ保持し、wgpuのclear colorと、素材のデコード/先頭・最終posterのpadへ渡す。背景用の追加quadやframeごとの画像生成は使わない。

- 関連11 Go packagesの666件、対象 `go vet`、gofmt、差分空白検査が通過。Rustは6件通過。
- 実wgpuの既存4x4描画/更新/空frameと、両テーマの全pixel背景を検査した。
- 1080p/30fpsの確認用動画6本を実wgpu/Vulkan・NVENCで生成し、MP4全decode・frame数/時刻・1-frame-1-VCL-slice・AAC/mux/HLSを通過した。実投稿の先頭約3秒と以前生成した同投稿の実VOICEVOX音声を再利用した2画面出力、無音の引用表示fixture、写真+動画のCoverFlow/slideを両テーマで確認。fixtureの引用は実投稿の引用ではなく、視覚検証用サンプル。
- 背景検査の途中で、退場中のカードが覆うpixelを背景と誤認したため、実画像で確認したカード間の余白へ測定位置を訂正した。白/黒の期待値や色許容範囲は変えていない。
- 混在4素材のnative codecと、CFR68→68/NTSC67→68の縦横終端回帰も再実行して通過。
- 開発exe/helperを再ビルド。稼働中8080は停止/入れ替え実行せず、履歴・公開・声設定は変更していない。再起動後に動画を再生成すると反映する。

artifactのSHA256、画像/MP4へのパスと検証範囲は [card-style.json](card-style.json)。今回の短い表示確認は、CPU20性能評価、329秒動画全体の再生成、GUI公開、VRChat実機受け入れにはしない。高速化候補は未反映のままキューに残す。

## 実投稿のUTF-16位置エラー修正

投稿 `2106219507274731847` で `invalid UTF-16 entity range 122:145 for text length 188` を再現した。改ページ前の読み上げ検証で失敗しており、取得元のsyndication APIのentity位置（Unicodeコードポイント数）をUTF-16位置としてそのまま渡したことが原因だった。本文は186コードポイント・188 UTF-16単位で、URLより前の絵文字2文字により正しい範囲は124:147になる。取得ライブラリの本文処理も `Array.from(text)` に対して元のindicesを適用していることを確認した。

- `fetch_tweet.mjs` で実際に出力する `rawText` を基準に変換表を作り、URL/メンションの始点・終点をUTF-16へ変換する。元投稿と引用/リポストはそれぞれ自身の本文を使い、長文noteも同じ規則を適用する。
- 本文の長さを超えるソース位置は取得段階で失敗させる。Go側の範囲外・surrogate途中の検査や既存期待値は変更していない。
- ネットワーク応答だけをfixtureへ置き換え、実際の取得CLIを使った8件を追加。問題の122:145→124:147、BMP/絵文字、URL/メンション、引用、長文note、範囲外を検査し、修正前の失敗から修正後の成功を確認した。既存メディア取得テストを合わせたNode 12件も通過した。
- 同じ実投稿を取得し直し、開発用runtimeから実VOICEVOX→1080p/30fps・wgpu/Vulkan・NVENC→MP4全decode/1693フレーム/時刻/1-frame-1-VCL-slice→AAC/mux/HLSまで成功した。出力は56.433333秒。改ページ後の読み上げ本文を連結し、URLを除いた元本文が欠落なく保持されることも確認した。
- 関連11 Go packagesの666件、Nodeの構文検査、差分空白検査も通過した。workerは既存CPU20 Job Object下で実行した。実行時間はこの投稿の動作確認であり、性能比較やVRChat併用の合算CPU評価にはしない。稼働中アプリ・公開・履歴・最後の声設定は変更していない。

取得スクリプトを含む開発用buildを更新し、ソースと同梱runtimeのSHA256一致を確認した。Go exe/helperには今回のソース変更がないため、前回のSHA256を保持する。再実行条件と証拠は [utf16-regression.json](utf16-regression.json)。高速化は引き続き作業キューの後続に残す。

## 読み上げに合わせた本文スクロール

利用者の「枠のサイズが変わり連続性が失われる」という指摘と「任せる」に従い、本文の改ページを固定枠内のスクロールへ置き換えた。元投稿1画面と引用1画面だけを用意し、プロフィール・日時・枠・引用ボックス・フォント寸法を固定する。引用を読み始める際は親本文の最終位置を引き継ぎ、親終了PNGと引用開始PNGはpixel-identical。短文は静止し、読み上げ終了後のCoverFlow/縦パネルにも最終表示を使う。静止画版のrendererは変更していない。

本文の表示行をURL/メンションを除いたSpeechTextのrune境界へ対応させる。VOICEVOXの文・句queryを1回の合成にまとめ、音素長・休止・話速・疑問形・フレーム丸めと実WAVのsample数からcue時刻を求める。句内部は表示行の位置で補間する方式で、語単位の強制アラインメントではない。pause中は止まり、必要なときだけ0.45秒以内のsmoothstepで送る。句の分割では18:00や小数を保持し、区切りに休止がない場合は0.3秒のpause moraを合成にも含める。

文字は一度だけ1024px高までのタイルに描画し、wgpuのUVでviewport内だけを表示する。タイル寸法とRGBA容量を制限し、長文を黙って切り捨てない。レビューで見つかった欠落cue/終了画像、viewport外、sample clockの過大/不完全値を拒否する。sample数を使う場合の音声spanも同じ有効時間に合わせた。旧ScrollViewなしのPage互換、CPU20 worker、既存codec/EOF/1-slice検査を維持し、後続の高速化キューは変更していない。

- 関連11 Go packagesで716件、Rust10件、Node取得回帰12件が通過。対象go vet、gofmt、差分空白検査も通過。
- 長文の複数カード化をREDで再現し、固定枠/本文保持/タイル境界/親→引用連続性をGREENで検査。不均等cueとpause中の静止、640x1080でのpanel scale、縦1件のCoverFlow省略と終了表示保持も通過。
- 実wgpuで色付き帯のUV切り出しを全pixel検査し、旧描画と両テーマclear colorも確認。
- 短文静止と引用付き縦動画の1080p native fixtureを両テーマで計4本生成した。短文は75framesで描画1回。引用fixtureは413frames、描画257回。fixtureのcueは合成値で、実VOICEVOXの音声同期試験とは区別する。いずれもMP4全decode/frame数/clock/1-frame-1-VCL-slice/AAC/mux/HLSを通過し、初期・親終了・引用開始・引用終了・素材表示の映像を確認した。
- 実投稿 `2106219507274731847` を実取得/実VOICEVOXで生成。改ページ2画面から固定カード1画面へ変わり、URL/メンションを除いた本文の完全一致を検査。20.362667秒のWAV/6 cueからスクロールを作成し、添付横動画を含む55.833333秒・1675frames・描画47回の1080p/30fps MP4/HLSを生成。最終開発exeをCPU20 runner下で実行して全codec検査を通過した。

最終exe/helper/output SHA、実投稿のcue/scroll、fixtureのパスと数値は [scrolling.json](scrolling.json)。ライト実投稿は最終exe、ダーク実投稿の確認用映像は最後のsample-clock-only音声span修正前のexeで作成した（実投稿にはDuration/Samplesの双方があり、この入力の挙動とscrollコードは同じ）。CPU hard cap=2000はworker treeのみで、短い測定窓の瞬間値は20%を超え得る。回帰検査と同時実行したため生成時間を速度ベンチマークにはしない。VOICEVOX/server/VRChatとの合算CPU、GUI公開、VRChat再生、他OS/GPUの実行は未確認。

確認用動画は `build/xpost-video/scroll-probe/real-light-final-job/output.mp4`。更新ビルドは `build/xpost-video/imagepadserver.exe`。稼働中アプリを停止/置換しておらず、反映には更新ビルドで再起動して動画を再生成する。履歴・公開・最後の声設定・コミット/プッシュは変更していない。

```powershell
$env:XPOST_COMPOSITORD_BIN = Join-Path (Get-Location) 'build/xpost-video/xpost-compositord.exe'
rtk go test ./internal/xpostgpu -run 'TestSidecarUVScrollSamplesOnlySelectedBand|TestSidecarThemedBackground|TestSidecarPixels' -count=1 -v
$env:IMAGEPAD_XPOST_COMPOSITOR = $env:XPOST_COMPOSITORD_BIN
$env:XPOST_SCROLL_PREVIEW_DIR = Join-Path (Get-Location) 'build/xpost-video/scroll-probe/fixtures'
$env:XPOST_STYLE_PREVIEW_ENCODER = 'h264_nvenc'
rtk go test ./internal/xpostcodec -run TestEncodeScrollingCardPreview -count=1 -v
```

native再実行にはFFmpeg/FFprobeがPATH上に必要。fixture出力は実行ごとの専用ディレクトリへ置く。fixtureをCPU20で試験する場合は、テストexeを先にビルドし、packageを作業ディレクトリに指定してnico-budget-runnerから実行する。
