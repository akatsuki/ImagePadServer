# 字形マスクとAVFrame共有のCPU試験

通常アプリから切り離した試験器。資料のR8-C / R2-C / R8-D / R2-Dを同一プロセスのFFmpeg APIで比較する。Cは試験用のコピー対照であり、既存アプリの実装・速度を表さない。

2026-09-17のユーザー方針: AVFrame共有の試作を保持し、劣化を伴う低bit量子化は採用しない。量子化コードは試験の再現用に保存する。既存の可逆palette/Deflateは継続。結果は `docs/verification/niconico-comments/mask-avframe-2026-09-17.md`。

## 構成と測定範囲

- `capture.js` / `internal/nicorender/mask_probe_test.go`: 実際のniconicommentsをChromeで動かし、fillだけを別canvasへ描く。既存spriteフックのWebGL texture・描画順・射影を保存し、元RGBAと同じcommandsのNMF1を作る。装飾・多色など検出した非対応textureはRGBAへ残す。
- `mask_renderer.h`: A8から1/2/4bitに量子化しMSB packing。最初にA8へ展開するE方式。縁取りを同じdisk最大値膨張で生成しキャッシュする。CPU bilinear合成は全条件共通。
- `main.cpp`: RGBA背景を初期化。Cはscratchへ描画後AVFrameへ全画面コピー、Dは最初からAVFrameへ描画。KEEP_REF付きbuffersrc、RGBA→YUV420P、libx264 CRF26 / veryfast / 8threads / 1slice、MP4へdrainする。
- 3枚のAVFrameを保持し、`av_frame_is_writable`がtrueのslotだけ再利用する。`av_frame_make_writable`による暗黙コピーは使用しない。CPU色変換・エンコーダー内部処理は残る。
- `wall_s`: NMF1読み込み開始からmux drainまで。Chromeの字形生成、元動画のdecode、OSキャッシュの消去は含まない。プロセス全体は`process_wall_s`に別記する。
- `packed_mask_B`: 実際に保持したpacked配列の長さ。初回入力ファイルはA8なので、Chromeからの転送削減の実測値ではない。
- `cache_B`: packed + 展開A8 + outline + RGBA例外のbuffer長の合計。元Scene、描画命令、管理領域、allocator余剰、AVFrame/encoderは別。process working set/commitも記録する。
- `cpu_copy_B`: frame_handoffの明示memcpy payloadだけ。実DRAM帯域・GPU転送・encoder内部コピーの値ではない。

CPU合成器は実験用で、既存D3D11 WARP合成器から置き換え可能と判定したものではない。新しい縁取りと元のCanvas2D.strokeTextの一致も別途測る。

## 再実行

リポジトリルートのPowerShell 7で実行する。実行中のImagePadServerを停止する必要はない。FFmpeg SDKは`build/nico-mask-probe/deps/ffmpeg-9.0.1-full_build-shared`へ置く。

固定SDK: [Gyan FFmpeg 9.0.1 full shared](https://www.gyan.dev/ffmpeg/builds/packages/ffmpeg-9.0.1-full_build-shared.7z)。SHA256: `cb4d5e8db6a3353bffdb2100d3eb4b76733457fa443215e236f57c99f9ffdca4`。試験用に展開するだけで、アプリのFFmpegは置換しない。

```powershell
rtk proxy pwsh -NoProfile -File scripts/experiments/nico-mask-probe/build.ps1
$env:PATH = "$(Get-Location)/build/nico-mask-probe/deps/ffmpeg-9.0.1-full_build-shared/bin;C:/msys64/ucrt64/bin;$env:PATH"
rtk proxy build/nico-mask-probe/renderer-selftest.exe
rtk proxy build/nico-mask-probe/mask-probe.exe --selftest
rtk proxy build/nico-mask-probe/mask-probe.exe --encoder-selftest build/nico-mask-probe/eagain.mp4

$env:GOCACHE = Join-Path (Get-Location) '.tmp/nico-go-cache'
rtk proxy go test -c -tags nico_mask_probe -o build/nico-mask-probe/fixtures.test.exe ./internal/nicorender
$env:NICO_MASK_PROBE_OUT = Join-Path (Get-Location) 'build/nico-mask-probe/fixtures'
$env:NICO_MASK_PROBE_FIXTURE = 'F1'
rtk proxy build/nico-mask-probe/fixtures.test.exe '-test.run=^TestNicoMaskProbe$' '-test.v' '-test.count=1'
```

F1は同じ白コメント群を3秒ごとに再投入する10秒素材。F0、DENSITY100も選択できる。実snapshotは`NICO_MASK_SNAPSHOT`にパスを設定するとREAL（6秒）になる。ブラウザーfixture生成はベンチマークとは直列実行する。

実browser mask probeで全フレームRGBAを保存する診断では、`NICO_MASK_PROBE_DUMP_ALL=1`を指定する。未指定時は代表6フレームだけを保存する。browser/CDP切断で完走しなかったrunや、CPU mask-probe試験器の不透明背景出力はnative raw parityの証拠へ昇格させない。

以下の`python`にはnumpy/Pillowが必要。Codex同梱Pythonでも実行できる。

```powershell
rtk proxy python scripts/experiments/nico-mask-probe/run.py bench --scene build/nico-mask-probe/fixtures/f1.mask.nmf1 --name f1 --repeats 5
rtk proxy python scripts/experiments/nico-mask-probe/run.py quality --scene build/nico-mask-probe/fixtures/f1.mask.nmf1 --name f1-quality
rtk proxy python scripts/experiments/nico-mask-probe/validate.py
```

品質runでは全フレームの有効RGBAをMD5へ投入し、同bitのC/Dで全画素とMP4の一致を検査する。ROIはA8/候補の描画範囲の和集合＋3pxで、1/2/4bitとの差を測る。詳細dumpを含むrunは性能平均へ混ぜない。

`--frames 3600`で同じ配置時系列を60秒ぶん反復できる。PTSは反復しても単調増加する。`--cancel-at N`はNフレームでdrainしてexit2、`--fail-at N`はエラーでexit1になる。どちらも試験出力のみを使うこと。エラー時の途中MP4を本番出力として扱わない。

## 根拠

FFmpegの[KEEP_REF仕様](https://ffmpeg.org/doxygen/8.0/group__lavfi__buffersrc.html)と[send/receive仕様](https://ffmpeg.org/doxygen/8.0/group__lavc__encdec.html)を参照し、使用SDK9.0.1の`buffersrc.h` / `frame.h` / `avcodec.h`で再確認。KEEP_REFの参照共有は実APIのnull filterで同一ポインターと参照寿命を検査する。公開APIの仕様だけを根拠に全経路ゼロコピーとは呼ばない。
