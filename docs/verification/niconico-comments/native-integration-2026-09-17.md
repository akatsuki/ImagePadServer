# コメント合成の通常経路への統合

ユーザーが依頼した2回のスプライト試験の本実装と、追加資料 niconico_comment_verification_handoff.md の適用判断。画質設定を維持する。

## 実装

- Windows amd64配布ビルドにNPS3対応D3D11 WARP補助EXEを埋め込む。外部コンパイラや追加DLLのダウンロードは実行時に不要。
- 既存niconicommentsの文字画像と描画命令を30フレーム単位で生成し、WARP合成と既存FFmpegをOS pipeで接続する。CPU libx264、CRF26、AAC160k、30fps、1フレーム1スライスは従来の設定。
- 画像は可逆パレット、濃淡＋アルファ、RGBAから選択。バッチ・命令buffer、3枚のreadbackを再利用。IDは単調増加し、ブラウザーで削除された画像をバッチ末尾で解放する。
- Backend=auto は準備できればnative、非対応OS・未同梱・自己診断失敗はbrowser。明示nativeや処理開始後の失敗はエラー。IMAGEPAD_NICO_RENDERER=browser で既存経路を明示できる。
- 進捗はnativeが処理したフレーム数から通知。生成・合成・エンコードの終了をすべて回収し、完了件数を確認後に一時MP4を完成先へ置換する。失敗時は既存完成ファイルを保持する。
- Windowsの補助EXEはMSVC /MT。依存検査と開発PATHを除いた自己診断を通し、実行物・ソースのSHA256をmanifestに保存する。Goのタグなしビルドは既存browser経路を利用する。
- Windows CIでhelperを検証・artifact化し、Ubuntu/macOSのクロスビルドとリリースビルドが取得して埋め込む。workflowは静的レビュー済みで、リモートCIはまだ実行していない。
- 可逆Deflateを通常設定で有効にする。1KiB以上かつ小さくなる画像だけ圧縮し、API未対応ブラウザーは可逆パレット等へ戻る。SpriteCompression=none で追加圧縮を無効化できる。スレッド数の強制指定は過去試験で優位がなく、既存の自動値を維持する。

## 追加圧縮の実測

初期化・ブラウザー・helper・FFmpeg終了を含むMP4完成時間を計測。HLS生成と完全性検査は計時外。ダウンロードは含まない。実行順を交互/反転し、すべて直列で実行した。

|6秒・3回ずつ|各回（秒）|中央値|
|---|---|---|
|可逆パレット等のみ|2.997 / 2.505 / 2.896|2.896|
|上記＋Deflate|2.826 / 2.637 / 2.667|2.667|

この短尺では中央値7.9%短縮。13画像のpayloadは988,122→112,881B（88.6%削減）。元RGBAからは94.6%削減。JSON/base64や描画命令は含まない。全条件で180フレーム、MP4/HLSデコード、1スライスを確認した。

全長235画像は可逆パレット等のみ38,590,805B、Deflate併用2,560,362B（追加93.4%削減）。元RGBA57,777,432Bから95.6%削減。これはブラウザーからGoへ渡す画像payloadの容量であり、完成MP4の圧縮率ではない。

|約155秒・実行順|MP4完成（秒）|
|---|---:|
|1: 既存browser|52.256|
|2: native＋Deflate|31.560|
|3: native、追加圧縮なし|34.488|
|4: native＋Deflate|32.791|
|5: 既存browser|50.249|

同じFFmpegでの既存browser平均51.253秒→native＋Deflate平均32.175秒、37.2%短縮（約1.59倍の処理速度）。各方式2回の局所測定であり、他素材で同率になる保証はない。追加圧縮なしの全長対照はこの順序内では1回なので、Deflate単独の長尺効果量は参考扱いとする。先行した全長none試験39.145秒は別順序であり平均へ混ぜない。

通常オプションを省略した最終確認でもnativeが選択され、6秒payload112,881Bとなった。Deflate後の実180フレームも同じブラウザー描画との差が最大1に収まる。境界ごとのCPU/VRAMピークや遅延p95は未計測。

## 追加資料の採否

### A1/A2/A4

実6秒の13画像・517,987画素を調べた。アルファ>0の222,123画素で、A1/A2/A4の最大誤差は127/42/8、平均誤差は47.037/10.873/1.486。画質不変の条件に合わないため未採用。A8は一致するが、既存RGBAには縁取りと塗りの色差も含まれるため、単一マスク化の成功とは扱わない。

詳細は [低ビット監査](lowbit-audit-2026-09-17.md)。低ビット索引の境界試験は自己生成fixtureの往復検査であり、実コメントのA2描画成功を意味しない。

### コピー削減

staging画像のRowPitchが幅×4のとき、Mapした領域を直接fwriteへ渡す。送信完了後にUnmapし、同じslotの再利用を許可する。行にpaddingがある場合は従来の行詰めbufferを再利用する。

同じ保存シーン180フレーム・1,492,992,000Bで、コピーあり／省略は両方とも次のSHA256に完全一致した。

705a0d428e8a350c3d17aa2366936fb9166ff2eabedbc98faed6440cbf24a664

内部の行詰めコピーは1,492,992,000B→0B。単体所要時間は1.047→1.038秒の各1回であり、速度優位は確認できたとはしない。D3D readback、CRT、OS pipe、FFmpeg内部のコピーは残る。帯域カウンターやPCIe実測ではない。

FFmpegは現在、別プロセスの静的CLIを使用する。既存配布ディレクトリーとMSYS2の調査範囲にはlibav開発ライブラリがなく、AVFrame/AVBufferRefを共有する同一プロセス経路は未実装・未測定。今回の変更をその直接共有やゼロコピーと呼ばない。画質条件を満たさないA2を含む4条件比較、GPU共有、輪郭の再生成は実施していない。

## 検証済み

- helper: 65件の形式・画素・短い末尾・画像寿命試験。切断、不正時刻、未知ID、削除済みIDの再利用も拒否。
- 同じブラウザーflushを参照にした比較: 色付き・上下固定・改行の80フレーム、および1080p実コメント180フレーム。各RGBA値の最大差1で既存許容差を維持。
- 固定保存シーンは以前の試作と全画素一致。新しいブラウザーを別々に起動した動画同士の全画素一致は主張しない。
- 通常APIで埋め込みhelperを選択。6秒は180フレーム、約155秒は生成/進捗4,649、動画デコード4,647。元動画の終端と既存 -shortest による差は従来と同じ。
- MP4/HLS全編デコードと、実H.264の全アクセスユニット1スライスを確認。
- 子プロセス起動失敗・途中失敗・生成失敗・キャンセル・完了通知不足・件数不正のライフサイクル試験。
- 既存出力の置換成功と、失敗時の既存出力保持・一時ファイル削除。実際のauto準備失敗からbrowserで3フレームを完成させる試験。
- 通常/埋め込み両方のアプリビルド成功。nicorenderのvet、Nico対象のvideo/serverテスト成功。video全体のvetは既存のnative_mapping_windows.go:44、shared_ring_mmap_windows.go:39のunsafe.Pointer警告2件で失敗した。別経路のコードは変更していない。
- RTKがないPATHでMSVCビルド・自己診断も成功。リモートCI・実配布ZIPの新規発行は行っていない。

## 実行環境と再実行

Go 1.26.3 windows/amd64、Chrome153.0.8010.48、FFmpeg9.0.1。今回確認したFFmpegは過去試験記録の8.1.1と異なるため、過去の絶対時間との比較ではなく同じ現行実行物で再計測する。

- 元動画SHA256: 1CD47C58711AC701721DBD1B76912ADAFE0D91634188224057EE608EBE38578B
- snapshot SHA256: B7BE86D86016175C99925F5F1B5651D43279EEFFCC54DEB1C22C5B2E6E15E946
- FFmpeg SHA256: 72A489ECCD008C2EC2C0A5856C5C75BC3D8BBFA90166C4566865C246445E6AA3

補助EXE: scripts/build-nico-compositor.ps1 -Stage。
形式試験: scripts/test-nico-compositor.py。
固定シーン試験: scripts/experiments/nico-production-parity.py。
実ブラウザー/FFmpeg試験は IMAGEPAD_NICO_PRODUCTION_TEST=1 で TestProductionSpritePixels / TestNicoProductionPipeline。
後者の実素材の指定と比較順は scripts/experiments/test-nico-production.ps1。
ログ・MP4・HLS・framemd5は build/nico-native-integration。

正式なVRChat実機、全長全画素、全CA/スクリプト、超長時間のメモリー上限到達試験は未実施。稼働中サーバーは停止・再起動していない。

起動用の埋め込みビルドは build/nico-native-integration/imagepadserver.exe。稼働中PID36316（開始2026-09-16 22:06:17）は維持した。切替はまだ実行していない。
