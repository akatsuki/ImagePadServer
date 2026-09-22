# ニコニココメント付き事前エンコード検証

実施日: 2026-09-16 (JST)

## 実測済み

- `IMAGEPAD_NICONICO_LIVE_TEST=1 rtk go test ./internal/niconico -run '^TestClientFetchesLivePublicVideo$' -count=1 -v`
  - 公開動画 `sm9` をCookieなしで取得。
  - threads HTTP/JSON 200、main 850件、easy 166件、owner 0件、合計1,016件。
- `rtk go test ./internal/niconico ./internal/nicorender`
  - 19件PASS。URL、snapshot正規化、fork選択、vpos有理数時刻、描画入力変換を確認。
- `IMAGEPAD_NICONICO_RENDER_TEST=1 IMAGEPAD_NICONICO_RENDER_BROWSER=<Chromium> rtk go test ./internal/nicorender -run TestRenderSingleFrameWithHeadlessBrowser -count=1`
  - 固定bundle `@xpadev-net/niconicomments` 0.4.1をheadless Chromiumで実行し、PNGをRGBAへ変換。
- `rtk go test ./internal/video -run TestEncodeNicoCommentedAndCreateHLS -count=1`
  - 60fps入力をコメント時計30fpsへ正規化し、合成MP4のH.264/yuv420p、AAC、`+faststart`、HLS playlist ENDLISTとセグメントを検査。
- `IMAGEPAD_NICONICO_PIPELINE_TEST=1 IMAGEPAD_NICONICO_RENDER_BROWSER=<Chromium> rtk go test ./internal/video -run TestEncodeNicoCommentedWithBrowserRenderer -count=1`
  - Chromium描画→2フレームbounded pipe→FFmpegの実接続を3フレームで確認。
- `rtk go test ./internal/niconico ./internal/nicorender ./internal/video ./internal/server -count=1`
  - 対象4パッケージの回帰テスト **1,210件PASS**。
  - 公開切り替え時に完成MP4を削除しない回帰を含む。
  - コメント描画フレーム数を同期取り込みの進捗へ反映する経路を含む。

## 未実施・有効化判定

30秒/長尺のフレーム欠落、VFR・回転・遅延音声、キーフレーム境界、private snapshot/manifestの永続化、再起動復旧、ブラウザーE2E、実機VRChat再生は未実施。したがって、本検証はコメント取得・描画・単独合成の成立を示すもので、配布・本番有効化のPASSではない。
