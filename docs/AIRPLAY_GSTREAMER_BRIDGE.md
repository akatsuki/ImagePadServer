# AirPlay GStreamerブリッジ

## 目的

AirPlayのH.264/L16 RTPをGStreamerで受信・デコードし、同一パイプライン時計でH.264/AACへ整えて、セッション専用MediaMTXへRTSP/TCPで直接publishする。iPhoneの回転、アプリ切り替え、一時的なRTP欠落でFFmpegを再起動せず、欠落後はIDRまで壊れたPフレームを出さない。

## 起動

通常の`gstreamer`指定は直接RTSP publish経路を選ぶ。検証中は以下を指定する。

```powershell
$env:IMAGEPAD_AIRPLAY=1
$env:IMAGEPAD_AIRPLAY_PIPELINE="gstreamer"
$env:IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE="C:\path\to\airplay-gstreamer-bridge.exe"
```

`IMAGEPAD_AIRPLAY_PIPELINE`を空、`ffmpeg`、`0`にすると既存のFFmpeg RTP/SDP経路を使う。direct経路ではFFmpeg publishプロセスを起動しない。GStreamer経路の起動失敗時に、未検証のまま自動フォールバックはしない。

direct経路の処理は次のとおり。

```text
UxPlay RTP -> Go relay -> GStreamer decode/clock/encode
            -> rtspclientsink (TCP) -> MediaMTX -> HLS/RTSP
```

`rtspclientsink`にはRTPペイロード済みデータではなくH.264/AACを渡す。RTSPのRTP化はGStreamer内部で行う。停止時はstop requestを使ってEOSを送り、`mp4mux`を確定してからMediaMTXを閉じる。

## 配布物の作成

GStreamerのDevelopment MSIとRuntime MSIは公式配布物を使用する。Developmentはビルド専用、Runtimeは配布用で分ける。

```powershell
.scripts\build-airplay-gstreamer-bridge.ps1 `
  -GStreamerRoot "$env:GSTREAMER_1_0_ROOT_MSVC_X86_64" `
  -GStreamerRuntimeRoot "$env:GSTREAMER_1_0_RUNTIME_ROOT_MSVC_X86_64"

.scripts\package-airplay-gstreamer-runtime.ps1 `
  -GStreamerRuntimeRoot "C:\path\to\gstreamer\runtime\msvc_x86_64" `
  -BridgeBuildDirectory "build\airplay-gstreamer-bridge\Release" `
  -OutputDirectory "dist\airplay-gstreamer"
```

出力先は`airplay-gstreamer-bridge.exe`と同じディレクトリをGStreamer runtime rootとする。`bin`、`lib\gstreamer-1.0`、依存DLLを親プロセスのPATHに依存せず解決する。

## 検証

```powershell
rtk go test ./internal/airplay -count=1
rtk cmake --build <cmake-build> --config Release --parallel
rtk ctest --test-dir <cmake-build> -C Release --output-on-failure
```

FFmpeg/ffprobeとブリッジを指定すると、合成H.264/L16 RTPの映像・音声が一つのMatroskaに入り、映像1920x1080、音声48kHz stereoになるE2Eテストも実行できる。実機iPhoneの回転・アプリ切り替え確認は別の受入れゲートであり、ローカルPASSだけではproduction-readyと判定しない。
