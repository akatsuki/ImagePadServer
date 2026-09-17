# Nico compositor

ImagePadServerのコメント動画合成用。MIT（リポジトリのLICENSE）。D3D11 WARPで固定版niconicommentsから受け取るスプライトを合成する。ハードウェアGPUと映像エンコードは担当しない。

Windows x64 / Visual Studio 2022 C++ Build Tools / Windows SDKでビルド:

```powershell
rtk proxy pwsh -NoProfile -File scripts/build-nico-compositor.ps1 -Stage
rtk proxy go build -tags nico_native_embedded -o build/nico-native-integration/imagepadserver.exe ./cmd/imagepadserver
```

MSVCの静的CRT（/MT）を使用。Windows標準のD3D11/DXGI/D3DCompilerとシステムDLLのみを参照し、実行時にGCC・libwinpthread・VC再頒布DLL・コンパイラを要求しない。ビルドスクリプトが依存DLLを検査し、開発用PATHを取り除いた自己診断に合格してからステージする。helper EXEとソースのSHA256をmanifestに記録する。Microsoft DLLは同梱せずOSのものを使用する。

`--version` はABIを表示、`--self-test` はWARP/shader/readbackを検査、`--stdin` はNPS3を入力してRGBAをstdoutへ出力。stderrの `NICO_PROGRESS` / `NICO_DONE` はフレーム実績。`--copy-output` は検査専用の比較条件で、既定はrow pitchが一致する際にstagingから直接同期書込みする。これはAVFrame共有やゼロコピーではない。

NPS3仕様は `nico-native-integration-plan.md` を参照。画像IDは単調増加、削除はバッチ末尾。参照中のstagingはfwrite完了までMapを保持し、上書きしない。異常入力は非0終了し完了通知を出さない。
