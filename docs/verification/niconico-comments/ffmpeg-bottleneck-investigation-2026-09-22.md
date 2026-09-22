# ニコニコ書き出し：FFmpeg負荷の切り分けと改善候補

2026-09-22。調査・診断試験の結果であり、製品のエンコーダー設定は変更していない。

## 結論

次の大幅改善候補は、現行の合成・色変換を保ったまま、最後のH.264圧縮をNVENCへ置き換えること。RTX 5070 Tiと手元のFFmpegで実エンコードでき、保存済みコメントフレームを使う診断では、CPU量・所要時間を大きく削減できた。

GPUを使わない候補は`superfast`、容量増加を抑える候補は`bframes=0`。いずれも符号化結果は変わる。`rc-lookahead=0`は今回ほぼ高速化せず、SSIMも悪化したため優先しない。既に本番相当で遅くなったthread数固定を再び推奨しない。

ただし、本番全体の高速化率・VRChatのframe time・文字部分の品質はまだ確定していない。特に容量調整後のNVENC試験はVRChat不在時の結果なので、採用判定には使わない。

## 前の説明の訂正

「FFmpeg以外はほぼ誤差」「3段階の経過時間が近いので負荷が均衡している」は、測定からは言えない。

- `native_ffmpeg`と`native_compositor`はStart〜Waitのプロセス寿命、`native_sprite_stream`はproducerの開始〜終了。パイプ待ち・下流からの逆圧・CPU予算待ちを含む。
- FFmpegが最後に終了するのは、上流EOF後の圧縮flushやmuxでも起きる。終了時刻の差0.3秒を、他段階の削減余地の上限にできない。
- 前回のPID別`ffmpeg.exe`合算には、書き出しだけでなくHLS作成・検証用decode・framemd5等の別FFmpegも混ざる。1秒サンプルは開始・終了直前を取りこぼす。これをx264単独CPUとは呼ばない。
- Chrome、compositor、workerにも秒単位のCPU消費があり、CPU予算を共有しているため、そこを削る効果も残る。

## 今回の条件と記録

証拠ディレクトリ：`build/nico-cpu20/ffmpeg-investigation-20260922/`。

- `probe.py`：再現用診断スクリプト。製品コードを変更せず、同じフィルターとエンコーダー引数を再構成。
- `summary.json`：環境が変わった前半と後半を分けた集計。品質検証runは速度集計に含めない。
- 各候補/反復にFFmpeg全argv、stderr、Windows Job report、CPU/実時間、出力を保存。
- FFmpeg 9.0.1 shared、実体SHA-256 `cf6b46df53d3672e86af7662358bbd2b21c90cc78c133f3f81f46e63acc387b3`。
- 元動画：1280×720、29.97fps、H.264、limited-range BT.709、AAC。出力：1920×1080、30fps、6秒、180 frame。
- 既存current/current parity用の60fps RGBA rawから偶数frameを選び、30fpsとしてpipe入力。全候補で同じ180 frameを使用。**ブラウザー生成・WARP合成を実行する本番E2Eではない**。
- feederとFFmpegを同じWindows Jobへ入れ、20% hard-cap設定、全性能runで`verified=true`・exit 0。CPU総量はJob、FFmpeg自身は`-benchmark`のuser+system秒で別記。
- 前半はVRChat PID 68036を開始時・途中に検出。総CPUの各run平均はおおむね95〜99%。連続VRChat frame-time計測はしていないため、併用受入れではない。
- 後半はVRChat不在。総CPU平均は約67〜77%。この条件を前半と混ぜない。
- 20%はJob設定値。短時間のCPU秒÷wall÷8による観測値は20%を超えるrunもあり、後半基準の中央値21.29%、NVENC CQ29は18.10%。厳密な連続20%以内を証明したとは扱わない。

追加rawファイルは作らず、既存rawを読み込んだ。診断出力は新規ディレクトリに保持した。

## CPU負荷の切り分け

前半各3回の中央値。各行は独立に走らせた試験で、差分は原因の推定であり厳密な排他的CPUプロファイルではない。

| 処理 | FFmpeg CPU秒 | Job CPU秒 | 起動を含むwall秒 |
|---|---:|---:|---:|
| 現行相当：decode・scale・RGBA合成・YUV変換・x264・AAC・MP4 | 7.844 | 8.703 | 5.442 |
| 同じ処理から音声を除外 | 7.219 | 8.047 | 5.127 |
| 同じdecode・合成・色変換まで、圧縮・音声なし | 2.157 | 2.953 | 2.045 |
| decode・scale・YUV出力のみ | 0.532 | 0.813 | 0.635 |
| 元動画decodeのみ | 0.328 | 0.688 | 0.596 |
| RGBA pipe入力のみ | 0.157 | 0.875 | 1.012 |

音声なしと合成までの差は約5.06 CPU秒。現行相当7.844 CPU秒の約65%に相当し、**映像圧縮が主要因であることを差分試験でも支持**する。音声を丸ごと外してもwall短縮は約5.8%。デコードだけのCPU量も小さく、音声copyやHW decodeを先に大改修する優先度は低い。

`-loglevel verbose`では、scaleが`1280x720 yuv420p -> 1920x1080 rgba`、overlayがRGBA同士、その後auto_scaleが`rgba -> yuv420p`になっていることを確認。背景映像のYUV→RGBA→YUV往復は実在する。一方、これを削ると色差・chroma補間・alpha合成の結果が変わるため、単なる冗長処理削除とは扱わない。

現行x264実効値には`threads=12`、`lookahead_threads=4`、`rc_lookahead=10`、`bframes=3`、`sliced_threads=0`、`slices=1`を確認した。thread数固定は既存の本番相当試験でdefaultより約5%遅かったため、未検証の改善として再提示しない。

## 候補の比較

前半各3回、現行相当5.442秒に対する比較。CPU削減率はfeeder込みJob CPUの中央値比。サイズは10進MB。

| 候補 | wall秒 | 時間短縮 | CPU量削減 | 容量 | 対基準容量 | 全画面SSIM |
|---|---:|---:|---:|---:|---:|---:|
| veryfast / CRF26（基準） | 5.442 | — | — | 6.392 MB | — | 0.971390 |
| superfast / CRF26 | 4.082 | 25.0% | 24.4% | 8.042 MB | +25.8% | 0.972505 |
| veryfast / bframes=0 | 4.720 | 13.3% | 14.5% | 6.534 MB | +2.2% | 0.971596 |
| veryfast / rc-lookahead=0 | 5.440 | 0.03% | 4.1% | 6.509 MB | +1.8% | 0.967748 |
| YUV420でoverlay合成 | 4.820 | 11.4% | 11.3% | 6.517 MB | +1.9% | 0.970694 |
| NVENC p4 / HQ / VBR-CQ26 | 2.593 | 52.3% | 56.9% | 8.748 MB | +36.8% | 0.980533 |

NVENCではCRFとCQの数値を同品質と仮定していない。まず実測し、次にCQを29へ調整して出力容量を近づけた。

後半の別バッチはVRChat不在、A/B交互順で各3回。**この表は本番採用やVRChat併用成功の証拠ではない**。

| 候補 | wall秒 | Job CPU秒 | 時間短縮 | CPU量削減 | 容量 | 全画面SSIM |
|---|---:|---:|---:|---:|---:|---:|
| veryfast / CRF26 | 4.386 | 7.609 | — | — | 6.392 MB | 0.971390 |
| NVENC p4 / HQ / VBR-CQ29 | 2.115 | 3.094 | 51.8% | 59.3% | 6.285 MB | 0.972126 |

CQ29の容量は基準より1.68%小さい。全画面SSIMは近いが、Y成分だけでは基準0.974031、CQ29 0.973734でわずかに低い。文字ROI、半透明文字、多色の縁取り、高密度区間の品質が同等とはまだ言えない。

後半NVENC runのGPU全体使用率はサンプル最大12%、encoder最大31%、VRAM最大7214 MiB。基準のVRAMは約6911 MiB。短いrunの疎サンプルであり、VRChat不在なのでゲーム影響の判定に流用しない。NVENCが専用回路であっても転送・VRAM・ドライバー処理の負荷は残る。

## 成果物の確認

基準と6候補の代表出力それぞれで以下を確認した。

- 圧縮前の基準合成結果と180 frameを比較したSSIM。
- 180 frameすべてのH.264 AUが1 slice。
- 180 frameのPTSが0〜179/30秒、IDR/keyframeが0秒と4秒。
- HLS 2 segment、各先頭keyframe、MP4映像decode、HLS映像・音声decode成功。

SSIMの最初の試行はフィルターの先読みで181 frameを評価したため棄却した。`quality-v2-*`では比較前の両入力を180 frameへtrimし、timebaseを1/30へ揃えて再計測。品質値はv2だけを採用する。文字ROIや音声同期の詳細、長尺・実機再生の受入れは未実施。

## 推奨する解決策

### 第一候補：合成経路を保ったNVENC出力

変更箇所は`internal/video/niconico_args.go`の映像encoder選択と、workerへ渡す明示的なencoder profile。入力デコード、RGBA合成、fps、色変換、AAC、最終MP4、HLS copy-remuxを維持して、比較する変更を圧縮部分へ限定する。

診断で動いた候補設定は次のとおり。既定変更の提案ではなく、次の本番相当試験の出発点である。

```text
-c:v h264_nvenc -preset p4 -tune hq -rc vbr -cq 29 -b:v 0
-bf 3 -slices 1 -forced-idr 1 -no-scenecut 1
-g 120 -force_key_frames expr:gte(t,n_forced*4) -pix_fmt yuv420p
```

実装第一段階ではNVENCを明示指定した場合だけ使用し、GPU/encoderが使用不能なら失敗させる。既定値はCPU x264のままにして、途中で方式を混ぜない。自動的なx264再生成は、NVENC失敗時の再レンダーとVRChat併用受入れを別途確認してから追加する。x264専用引数はNVENCに流用せず、両経路で実出力のsingle-sliceを検証する。

### 第一段階の実装済み範囲

- `NicoEncodeOptions.Encoder` とworker protocolの `encoder` を追加。空欄または `x264` は従来経路、`nvenc` は `h264_nvenc`を選択する。
- サーバー経路では `IMAGEPAD_NICO_ENCODER=nvenc` のときだけNVENCを要求する。環境変数未設定時の既定動作は変更しない。
- NVENC側は `p4 / hq / VBR-CQ / bf=3 / slices=1 / forced-idr / no-scenecut` を組み立て、x264専用の `-crf` と `-x264-params` は渡さない。
- 無効なencoder名はworker起動前に拒否する。NVENC実機の長尺・VRChat併用受入れと、失敗時の自動x264再生成は未完了。

採否はCPU20%の全workerで、同じ実素材・snapshotの短尺5ペア、155秒、高密度コメント、文字ROIの品質、音声同期、GPU/VRAM、VRChat p95/p99 frame timeを揃えて決める。今回の約52%はFFmpeg診断部分の短縮であり、書き出し全体が2倍になる保証ではない。

### CPUだけで改善する場合

`superfast`が今回最も速いCPU候補。容量約26%増が許容できる用途の明示profileとして比較する。容量増を小さくしたい場合は`bframes=0`（時間約13%短縮、容量約2%増）が次候補。同じCRFでも画像は変わるため、既定画質と同等とは自動判定しない。

### 後回しにするもの

`rc-lookahead=0`は効果不足、YUV420直合成は効果に対して色・alphaの回帰リスクが大きい。HW decode、音声copy、巨大queue、共有メモリー全面移行も、今回の測定では最優先ではない。圧縮を軽くした後にChrome/WARP/転送が新たな主因になれば、その時点のCPUと待ち時間で再評価する。

## 一次資料

- [FFmpeg benchmark / filter threads](https://ffmpeg.org/ffmpeg.html)：benchmarkは実時間・user/system CPUを示す。フィルターthreadとcodec threadは別設定。
- [FFmpeg overlay](https://ffmpeg.org/ffmpeg-filters.html#overlay-1)：formatとalphaの指定は合成結果に関わる。
- [FFmpeg libx264](https://ffmpeg.org/ffmpeg-codecs.html#libx264_002c-libx264rgb)：preset、rc-lookahead、thread関連の仕様。
- [NVIDIAのFFmpeg利用ガイド](https://docs.nvidia.com/video-technologies/video-codec-sdk/13.1/ffmpeg-with-nvidia-gpu/index.html)：NVENCのVBR-CQ設定。
- [NVENC Application Note](https://docs.nvidia.com/video-technologies/video-codec-sdk/13.1/nvenc-application-note/index.html)：グラフィックス/CUDAコアと別のハードウェアencoderであること。VRChat影響ゼロを意味しない。
