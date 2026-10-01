# Nico GPU NVENC isolated probe

T3の隔離試作です。本番の`internal/`、`native/`、worker、docsには接続しません。

この試作が確認するのは次の最小ゲートだけです。

1. 同じD3D11 deviceでNV12 textureを4面作成し、NVENCへ登録できること。
2. 登録resourceをmapしてNVENCが受理し、同期bitstreamを進行させられること。
3. pool容量の4倍（16 frame）をEOS前に送信し、最後にEOSを受理できること。

compositor、NPS3、FFmpeg、MP4/HLS、production integrationは対象外です。`compose.hlsl`は後続検討用の未使用スケッチです。

## 実行

既存のNVIDIA Video Codec SDKを`-SdkRoot`で明示指定します。SDK/DLLのダウンロード、コピー、配置、PATH変更は行いません。

```powershell
python run.py --output .\result.json --sdk-root 'C:\path\to\Video_Codec_SDK'
```

SDK、MSVC x64、`System32\NvEncodeAPI64.dll`、`nvidia-smi`のいずれかが見つからない場合、runnerは終了コード2で`status: unavailable`のmanifestを書きます。ビルド後の実行でD3D11/NVENCが拒否した場合は`status: failed`です。`status: passed`にはtexture登録、NVENC受理、pool進行、pool容量4倍超過、EOS完了の全てが必要です。

PythonテストはSDK不要です。

```powershell
python -m unittest discover -s . -p 'test_*.py' -v
```
