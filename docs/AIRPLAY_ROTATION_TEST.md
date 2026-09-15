# AirPlay 実機 iPhone 回転テスト手順

## 目的

縦横回転時の SPS/PPS 変化に対して、ブリッジの nvenc 再エンコードが
映像を継続できることを実機で確認する。旧実装（`-c:v copy`）は回転時の
stale PPS で RTMP ドロップ（-10054）を起こしていた。f10a4829 で
`video.SelectVideoEncoder` 経由の再エンコードに変更済み。

## 前提

| 要素 | 値 |
|---|---|
| ビルド | `dist/1.7.0/dev/dev4/win/imagepadserver-v1.7.0-dev4-windows-amd64.exe` |
| FFmpeg | `%APPDATA%\ImagePadServer\bin\v1.7.0-dev3\ffmpeg.exe`（必要なら `IMAGEPAD_FFMPEG`） |
| UxPlay | 初回起動時に自動準備（2.0.0.1736、Bonjour は UAC 昇格で自動インストール）。既存を使う場合は `IMAGEPAD_UXPLAY` に絶対パス |
| ネットワーク | iPhone と PC を同一 LAN、mDNS/ポート/Firewall 許可 |

## 手順

1. 環境変数 `IMAGEPAD_AIRPLAY=1` を設定して ImagePadServer（dev4）を起動する。
   初回は UxPlay バンドルの自動準備（ダウンロード + Bonjour インストール）が走る。
2. Live 画面 → OBS 入力パネル → **iOS AirPlay画面共有** カード → **AirPlay受信を開始**。
   （HTTP API なら `POST /api/airplay/start`）
3. iPhone のコントロールセンター → **画面ミラーリング** → ImagePadServer/UxPlay を選択。
4. 縦向き（720×1280）→ 横向き（1280×720）へ回転。さらに横向き → 縦向きへ戻す。
5. 出力（RTMP / FLV）を確認する。

## 確認項目

- [ ] 回転時に RTMP 接続が途切れない（旧実装は -10054 でクラッシュ）
- [ ] 解像度が縦横で切り替わる（720×1280 ↔ 1280×720）
- [ ] 映像が正常（歪み・フリーズ・緑画面なし）
- [ ] 音声が継続（L16→AAC 変換、48kHz stereo）

## 期待結果

回転の瞬間にブリッジが再エンコードで新しい SPS/PPS を吸収し、接続を維持したまま
解像度が切り替わる。出力は `video=h264 / audio=aac,48000,2` を維持する。

## 参考（E2E で実測済み）

- 経路: H.264/L16 リレー → SDP → nvenc → FLV
- 実測出力: `video="h264,720,1280,yuv420p"` / `audio="aac,48000,2"`
- 単体テスト: `go test ./internal/airplay/ -run TestAirPlayRelayBridgeEndToEnd`
