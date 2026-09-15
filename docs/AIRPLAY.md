# iOS AirPlay画面共有入力

この機能はLive配信への実験的な入力経路です。デフォルトでは無効で、明示的に有効化した場合だけLive画面に表示されます。

> **現在の正規資料:** iPhone/UxPlayで確認された挙動は
> `docs/AIRPLAY_IPHONE_UXPLAY_REFERENCE.md`、次に実装するlifecycle・遅延音声修正は
> `docs/superpowers/specs/2026-09-03-airplay-lifecycle-late-audio-design.md`を参照してください。
> 本文中の旧RTP/FFmpeg経路は比較・rollback用の記録を含みます。

## 経路

```text
iPhone/iPad AirPlay
  -> UxPlay (AirPlay/Bonjour受信)
  -> H.264 RTP + L16 RTP (localhost UDP)
  -> FFmpeg (SDP入力、AAC/FLV化)
  -> 既存のOBS RTMP relay
  -> Live配信
```

既存のOBS/Live配信経路を置き換えず、AirPlay開始時だけ同じRTMP ingestへ接続します。AirPlay専用プロセスは停止時とサーバー終了時に終了させます。

## source-clock qualification経路（実験的）

音声・映像の元AirPlay NTPを保持したまま、UxPlayのcomplete AU/frameをloopback
TCPでnative GStreamer bridgeへ渡す経路です。既存のRTP relay/FFmpeg経路とは別の
feature gateで、同一session内の自動fallbackは行いません。

```text
iPhone/iPad AirPlay
  -> patched UxPlay source-clock egress
  -> complete H.264 AU + compressed audio / source NTP
  -> native GStreamer timeline/decode/hold/encode
  -> MediaMTX RTSP/TCP
  -> HLS/RTSP preview
```

ローカルqualification packageは、次のように作成します。

```powershell
rtk proxy pwsh -NoProfile -File scripts/package-airplay-source-clock.ps1 `
  -ReceiverBuildDirectory "$env:TEMP\imagepad-uxplay-source-clock-build" `
  -BridgeBuildDirectory ".\build\airplay-gstreamer-bridge\Release" `
  -GStreamerRuntimeRoot "C:\path\to\gstreamer\msvc_x86_64" `
  -MinGWRuntimeRoot "C:\msys64\mingw64\bin" `
  -OutputDirectory "$env:TEMP\imagepad-airplay-source-clock-package"
```

起動時はqualification package内のpatched receiverを明示し、source-clockを有効にします。

```text
IMAGEPAD_AIRPLAY=1
IMAGEPAD_AIRPLAY_PIPELINE=source-clock
IMAGEPAD_AIRPLAY_RECEIVER=C:\path\to\package\uxplay-source-clock\uxplay-source-clock.exe
```

capability manifestがない、protocol versionが違う、binary hashが一致しない場合は、
MediaMTXやUxPlayを起動する前に拒否します。現在の自動downloadされる旧UxPlayは
source-clock receiverではないため、この経路では意図的に使用しません。

## Windows 10 セットアップ
Windows amd64で `IMAGEPAD_AIRPLAY=1` を設定し、`IMAGEPAD_AIRPLAY_RECEIVER` を指定しない場合は、ImagePadServerが初回起動時にUxPlayのWindowsバンドルを自動準備します。

- 固定リリース: `2.0.0.1736`
- ダウンロード元: `https://github.com/leapbtw/uxplay-windows/releases/download/2.0.0.1736/uxplay-windows.zip`
- SHA-256: `9d3a51c15fc9db857351195e7eb7bbb21700d9ae25d936a54bcf8536b62cca18`
- キャッシュ: `%APPDATA%\ImagePadServer\modules\uxplay\2.0.0.1736`
- 不完全なダウンロード・展開は完了マーカーがないため再利用されません。
- Bonjour Serviceが未登録の場合はインストーラーをUAC昇格で実行します。UAC承認または管理者権限が必要です。

既存のUxPlayを使う場合は `IMAGEPAD_AIRPLAY_RECEIVER` に実行ファイルの絶対パスを指定してください。この指定がある場合、自動ダウンロードとBonjourの自動インストールは行いません。自動準備に失敗した場合でも、サーバーは起動を継続しますが、AirPlayの開始時に具体的なエラーを返します。


1. UxPlayをWindows向けに用意します。公式のWindowsビルド手順は [UxPlay README](https://github.com/FDH2/UxPlay/blob/master/README.md#building-uxplay-on-microsoft-windows-using-msys2-with-the-mingw-64-compiler) を参照してください。GStreamerとBonjour Service（またはUxPlayがサポートするサービス検出方式）が必要です。
2. UxPlayの実行ファイルが単体で起動できることを確認します。
3. ImagePadServerを起動するプロセスへ次の環境変数を設定し、アプリを再起動します。

```text
IMAGEPAD_AIRPLAY=1
IMAGEPAD_UXPLAY=C:\path\to\uxplay.exe
```

FFmpegは既存の経路と同じ方法で解決されます。必要なら`IMAGEPAD_FFMPEG`で実行ファイルを明示できます。

UxPlayとiPhone/iPadは同じLANに置いてください。Bonjour/mDNS、UxPlayが使用するTCP/UDPポート、Windows Defender Firewallの許可が必要です。

## 操作

1. Live画面を開きます。
2. OBS入力パネル内の **iOS AirPlay画面共有** カードで **AirPlay受信を開始** を押します。
3. iPhone/iPadのコントロールセンターから **画面ミラーリング** を選び、表示されたImagePadServer/UxPlayの受信先を選択します。
4. 配信に載せない場合は **受信を停止** を押します。

HTTP APIからは次を使用します。

```text
POST /api/airplay/start
POST /api/airplay/end
```

AirPlay入力は1セッション1クライアントです。既にOBSからのストリームが接続中の場合、開始要求は競合として拒否します。

## 現在のメディア境界

- 映像: UxPlayのH.264 RTPをFFmpegでコピーしてFLV/RTMPへ投入
- 音声: UxPlayのL16/44.1 kHz/ステレオRTPをFFmpegでAACへ変換
- AirPlayミラーリングのDRMコンテンツは対象外
- UxPlayのH.265経路は未実装
- 実際のiPhone/iPad接続、Bonjour、GStreamer、ファイアウォールの組み合わせは環境依存
- source-clockはGStreamer 1.28.6でローカルbuild/synthetic smokeを検証済みだが、
  GStreamer runtimeの配布版差分と実iPhone/iPadの長時間qualificationは未確認

## 巻き戻し

### 即時停止（推奨）

アプリを停止して、次回起動時に次を設定するか、`IMAGEPAD_AIRPLAY`を削除します。

```text
IMAGEPAD_AIRPLAY=0
```

この状態ではAirPlayカードは非表示になり、開始APIはfail-closedになります。停止APIはクリーンアップのため利用できます。既存のOBS/Live入力経路は変更されません。

### コードの撤回

AirPlay変更は専用ファイルとLive/OBSの接続点に分離し、専用コミットへまとめてからリリースしてください。撤回時はそのコミットをrevertします。リリース前に、必ず次の確認を行います。

```text
rtk test go test ./...
rtk proxy go build ./...
```

## 検証の境界

自動テストで検証しているのは、feature flag、SDP/RTP引数、子プロセスの起動・停止、同時起動の直列化、HTTP/UI配線です。UxPlay実行ファイルとiPhone/iPadがない環境では、実AirPlayの検出・認証・映像音声の実配信までは証明できません。
