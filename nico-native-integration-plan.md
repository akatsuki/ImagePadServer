# コメント合成・検証済み最適化の本実装

2026-09-17。ユーザーの「この二回のテストを実実装に展開して実装して」に基づく。9/16スプライト試験と9/17直接接続・並行生成・可逆パレット試験の結果を通常の変換APIへ統合する。画質設定、30fpsのFrameClock、CPU libx264、1フレーム1スライス、公開成功条件を維持する。既存サーバーの停止・再起動、commit/push/releaseは行わない。

## 設計と契約

- Windows amd64は同梱のD3D11 WARP補助EXEを使用。コメントの配置・文字生成は固定版niconicommentsのブラウザー実装を使い、画像と命令だけを転送する。
- 256種以下のRGBAパレット／白黒の濃淡＋alpha／rawを可逆に選択し、実測で有効だったDeflateを追加。ネイティブ直前でRGBAへ復元。バッチ用buffer、命令scratch、3枚のstaging textureと出力bufferをジョブ内で再利用する。
- ブラウザー30フレーム単位の生成→ネイティブ合成→OS pipe→既存FFmpeg設定を並行実行。全編命令を保持しない。
- 新形式NPS3はlittle endian: header uint32[6] {0x3353504e,width,height,totalFrames,fpsNum,fpsDen}、各バッチはuint32 byteLength + payload。payloadはuint32 textureCount;各{id,width,height:uint32,rawRGBA}; uint32 frameCount;各{sequence:uint64,timeMs:uint64,commandCount:uint32,commands[100byte]}; uint32 deletedCount;uint32 IDs[]。byteLength=0が終端。フレーム順・時刻・件数・長さを検証する。削除IDは当該バッチ全フレーム後に解放する。
- テクスチャはブラウザーのdeleteTextureに従い解放。現存512MiB、各128MiB、バッチ512MiB、1バッチ30フレーム上限。元ライブラリが解放しない場合も上限超過で明示失敗する。
- ネイティブstderrに `NICO_PROGRESS completed total`、完了時 `NICO_DONE count`。RGBA転送とは別経路で既存プログレスバーへ通知し、最終件数と両子プロセスの成功を確認する。
- Backend=auto/browser/nativeを用意。autoはネイティブ未同梱・未対応OS・起動前自己診断不可なら既存ブラウザー経路を使う。稼働後の不正フレームや合成失敗はエラーとし、未完成MP4を公開しない。実験用環境変数を本番設定に流用しない。
- IDはジョブ全体で単調増加し再利用しない。未知/二重削除、過去IDの再登録を拒否。バッチ末尾でのみ削除する。fpsの分子・分母は各1,000,000以下、総フレームも1,000,000以下に制限する。
- キャンセルは生成pipeを閉じ、両子プロセスを停止し、stderr処理も含む各Waitと生成goroutine終了を回収する。完了は生成成功・NICO_DONE期待件数一致・native/FFmpeg成功・MP4非空をすべて要求する。
- helperはジョブ固有TEMPに一時名で書込み、SHA256照合後にrename。自己診断でABI/実行可能性/D3Dを確認し、終了後に回収する。回収失敗は記録し映像成功と区別。auto準備失敗はbrowser、明示nativeはエラー。
- Windows配布ビルドではhelperを再ビルドして埋め込む。通常のタグなしビルドは既存経路へフォールバック。実行時のコンパイラ・外部DLL探索・追加ダウンロードを不要にする。埋め込み実行物とソース、依存通知の対応を記録する。
- スレッド数は実測で安定した優位がないため既存自動値を維持。画質変更による速度向上を混ぜない。

## タスク

### T0: 統合設計レビュー
- **depends_on**: []
- **location**: 本計画
- **description**: 独立担当が画像寿命、パイプ終了、進捗、実行物同梱の抜けをレビュー。メインが採否を判断。
- **validation**: 指摘を検討し、API/形式を固定。
- **status**: 完了。ID寿命・終了回収・helper展開の指摘を反映。

### T1: ブラウザー画像・命令の通常コード化
- **depends_on**: [T0]
- **location**: internal/nicorender/sprite.go, sprite_codec.go, assets/sprites.js と専用テストのみ
- **description**: 独立実装担当。WriteSpriteStream(ctx,snapshot,RenderOptions,io.Writer)(SpriteReport,error)を追加。SpriteReportはRenderReportを埋め込みTextureBytes/PackedTextureBytes int64、Textures intを持つ。試験コードは保持し、NPS3・削除ID・buffer再利用・可逆圧縮を実装。Progressはこの段階では呼ばずnative出力実績で通知する。
- **validation**: 可逆性、256/257種、バッチ境界、画像解放、sequence/timeMs、cancel、writer失敗のRED→GREEN。実ブラウザーはメインが直列実行。
- **status**: 完了。flush捕捉・既定packing・ID寿命・圧縮backpressureをレビュー修正。Node/Goと実画素比較PASS。

### T2: 合成helperと同梱
- **depends_on**: [T0]
- **location**: native/nico-compositor, internal/nicorender/native_runtime*, scripts/build-nico-compositor.ps1, scripts/build-release.sh
- **description**: メイン担当。NPS3対応、WARPのみ、3枚readback、画像解放、厳密な終端、進捗・完了件数。埋め込みhelperの展開・自己診断・同梱ビルドと通知を追加。
- **validation**: 元試験のRGBAとの一致、短い末尾、不正長/ID/時刻、切断、実行依存DLL検査、自己診断、非対応経路を検査。
- **status**: 完了。65チェック、固定シーンSHA一致、静的CRT・PATH除去・RTKなしビルド、埋め込み検査PASS。Windows CI artifact経由のcross-build接続も追加。

### T3: 本番変換APIへの接続
- **depends_on**: [T1,T2]
- **location**: internal/video/niconico_encode.go, niconico_pipeline.go, 新規native pipeline/test; internal/nicorender/render.go
- **description**: メイン担当。エンコード引数を共通化して画質を固定し、auto/native/browser分岐と直接OS pipeを実装。全プロセス・生成処理の終了回収、進捗、完成条件、部分出力削除を追加。
- **validation**: プロセス失敗/起動失敗/cancel/不足・余剰done件数、fallback、設定一致、progress、既存サーバーの公開回帰テスト。
- **status**: 完了。既存APIのauto選択、実fallback、子プロセス終了回収、失敗時出力保持、進捗・server NicoテストPASS。

### T4: 統合検証と成果物
- **depends_on**: [T3]
- **location**: 統合テスト、docs/verification/niconico-comments、build/nico-native-integration
- **description**: メインが実ブラウザー・FFmpegを直列で検証。6秒同一性、全長の本番API実行、MP4/HLSデコード、1スライス、既存経路fallback、通常/埋め込みビルド。独立担当の最終レビューを受ける。
- **validation**: fresh exit 0、稼働中データ不変、検証済み範囲と未実機範囲を記録。起動可能な埋め込みEXEを作成。
- **status**: 完了。実6秒/155秒・MP4/HLS・全1スライス・通常/埋め込みビルドPASS。既存サーバーPIDと開始時刻を維持。最終包装レビュー済み。リモートCI/VRChat未実施と既存video vet警告2件は検証報告に明記。

## 実行

追加資料 `C:/Users/masah/Downloads/niconico_comment_verification_handoff.md` は検証前の提案として扱う。独立T5として、実保存画像からA1/A2/A4の量子化誤差と可逆な低bit索引の適用率を先に測定する。無劣化条件に落ちる案は通常経路へ入れない。AVFrame共有は外部CLIのOS pipeとは別の構成なので、利用可能なlibav実行物を調査し、実行できた範囲だけを報告する。

### T5: 追加資料の品質・コピー境界検証
- **depends_on**: [T0]
- **location**: scripts/experiments/nico-mask-audit.py、新規検証報告とbuild内の生成物
- **description**: 独立担当が既存保存シーンの実RGBAを解析し、低bit量子化・可逆索引の適用可能性を調査。メインはFFmpeg参照共有の実行環境と現存コピー境界を調査。画質不合格の段階で大規模性能試験へ進めない。
- **validation**: 元データhash、画像数、対象/例外件数、非透明ROI誤差、再実行可能な出力。実測/理論/未検証を分離。
- **status**: 完了。A1/A2/A4は画質不変条件で不採用、コピー省略は全画素一致で採用。Deflateは実3回短尺/2回全長を確認して既定採用。AVFrame参照共有・GPU共有は別構成として未実装/未測定を明記。

T0後にT1とT2を並行。T3/T4は統合後に順番に実行。試験は競合させない。ユーザーの実装指示をこの範囲の承認として扱う。共有変更を戻さず、skillのcommit指示よりユーザーのcommit禁止を優先する。

最終報告: docs/verification/niconico-comments/native-integration-2026-09-17.md。
埋め込み実行物: build/nico-native-integration/imagepadserver.exe（SHA256 c5a6d8554a03d07298d3ecfb020396096e79af3a2dca5f8c1995044f9938e2ad）。
同一現行FFmpegで約155秒の平均51.253→32.175秒、37.2%短縮。起動中のサーバーへの切替は行っていない。
