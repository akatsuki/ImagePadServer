# 低ビット字形・AVFrame共有の初期検証（2026-09-17）

結論: CPU試作ではコメント付きの大幅な高速化は確認できなかった。ユーザーの追加方針に従い、AVFrame直接共有の試作を残し、画素が変わる1/2/4bit量子化は不採用とする。既存の可逆palette/Deflateは継続。通常アプリへの今回の試作の組み込みは行っていない。

## 速度

1920×1080、60fps、背景RGBA(32,48,64,255)、libx264 CRF26/veryfast、encoder 8threads・filter 2threads・1映像slice。同じ入力・出力設定で4条件を各5回、順序を変えて直列実行。Chrome字形生成と元動画decodeは計測外。NMF1読み込み・packing・初回展開/縁取り・全frame合成・色変換・encode・mux drainを含む。Cは制御用コピー経路であり、既存アプリそのものではない。

|素材|条件|中央値 秒|最小〜最大 秒|R8-Cに対する時間短縮|
|---|---|---:|---:|---:|
|F0 コメントなし10秒|R8-C|1.5197|1.4627〜1.6874|+0.00%|
|F0 コメントなし10秒|R2-C|1.5380|1.4993〜1.8174|-1.20%|
|F0 コメントなし10秒|R8-D|1.3978|1.3625〜1.4518|+8.03%|
|F0 コメントなし10秒|R2-D|1.4282|1.4046〜1.5885|+6.02%|
|F1 白コメ10秒|R8-C|19.6543|19.5156〜19.7644|+0.00%|
|F1 白コメ10秒|R2-C|19.5365|19.5016〜20.2044|+0.60%|
|F1 白コメ10秒|R8-D|19.7944|19.4670〜20.5254|-0.71%|
|F1 白コメ10秒|R2-D|19.4248|19.4207〜19.6415|+1.17%|
|REAL 実コメント6秒|R8-C|10.7579|10.6801〜10.8391|+0.00%|
|REAL 実コメント6秒|R2-C|10.7060|10.6567〜11.5682|+0.48%|
|REAL 実コメント6秒|R8-D|10.7660|10.6099〜11.0169|-0.07%|
|REAL 実コメント6秒|R2-D|10.6800|10.6013〜11.3879|+0.72%|

R8=8bit被覆率、R2=2bit被覆率、C=scratch→AVFrameコピー、D=AVFrameへ直接描画。併用の短縮はF1約1.17%、REAL約0.72%。直接共有単独はF1約0.71%、REAL約0.07%長く、反復間のばらつきに埋もれた。F0では約8.03%短縮し、コピー削減の効果は確認できた。

F1/REALの主スレッド経過時間の約9割はCPU合成。初回字形生成が9割という意味ではない。この新規CPU合成器の結果を、既存WARP経路や稼働アプリの性能へ読み替えない。

## 容量とコピー

|素材|A8字形|A2字形|A8キャッシュ|A2キャッシュ|Cのframe_handoffコピー|D|
|---|---:|---:|---:|---:|---:|---:|
|f1|99,498 B|25,029 B|298,494 B|224,025 B|4,976,640,000 B|0 B|
|real|631,287 B|158,414 B|1,893,861 B|1,420,988 B|2,985,984,000 B|0 B|

字形保持量は約75%減、packed＋展開A8＋縁取りのキャッシュは約25%減。ただし初回NMF1はA8入力であり、Chromeからの実転送削減は未検証。キャッシュ値は元Scene、命令、allocator余剰、AVFrame、encoderを含まない。実プロセスのpeak working set/commitは各result.jsonに別記。

frame_handoffのコピーは実memcpyカウンター。背景書込み、RGBA→YUV420P、encoder内部処理は残る。GPU転送・実DRAMトラフィック・encoder内部コピーはN/A。全経路ゼロコピーとは判定していない。

## 画質

F1 600frame、REAL 360frameの全有効RGBAを照合し、同bitのC/Dは全画素一致。MP4のSHA256も一致した。A1/A2/A4はA8との差分をROI（両描画範囲の和集合＋3px）で測定。下表は保存した代表6frameのRGB差（0〜255単位）。

|素材|候補|最大差|ROI平均絶対差（画素数で加重）|
|---|---|---:|---:|
|f1|A1|138|5.3174|
|f1|A2|39|2.1321|
|f1|A4|9|0.4761|
|real|A1|124|5.0113|
|real|A2|40|1.7430|
|real|A4|8|0.3950|

低ビット化はいずれも無劣化ではなく、今回の方針で不採用。新規のA8縁取り再生成も元Canvas2D strokeと一致しない。元RGBAをCPUで再描画した基準との比較で、CPU/WebGLの丸め差と縁取り変更による差を別に記録した（*-fixture-audit.json）。コピー削減を今後統合する場合は既存RGBA・縁取り・描画順を維持する。

F1は120コメント投入、画面内30〜40命令、3texture。REALは保存済み362コメントsnapshotの先頭6秒、画面内17〜60命令、15texture。両区間の対象textureは白塗り/黒縁でRGBA例外0。全コメントの色分布や多色・絵文字の実素材ゲートを通したという意味ではない。

## 実APIと出力の検査

- KEEP_REFのnull filterでポインター一致、下流参照保持中の非writable、解放後のwritable、EOF後の失敗送信で所有権維持を確認。
- 3枚のAVFrame poolを使用しmake_writable呼出し0。encoderの実EAGAINを発生させ、同一frame再送と120packet drainを確認。
- 60000/1001fpsの120frame、C/Dのcooperativeキャンセル、途中エラー注入を検査。
- parser/packing/alpha/outline/stride/不正ID/破損/投影のselftest成功。

正常完了・キャンセルdrainした82本の出力を全decodeし、PTS、frame数、全AUの1sliceを確認。意図的エラーの途中MP4はsafety.pyでexit1を確認し正常動画の集計から除外。

試験器作成中にpacket duration未設定で最終frameがMP4 edit listから落ちる問題を検出し、CFR packet duration=1を設定して再検査した。比較本測定は修正後のバイナリ。

## 保存物・再実行

- [試験器README](../../../scripts/experiments/nico-mask-probe/README.md)
- [環境とSDK hash](../../../build/nico-mask-probe/environment.json)
- [集計](../../../build/nico-mask-probe/results/summary.json)
- [F1生データ](../../../build/nico-mask-probe/results/f1.csv) / [REAL生データ](../../../build/nico-mask-probe/results/real.csv)
- [安全性](../../../build/nico-mask-probe/results/safety.json) / [出力検査](../../../build/nico-mask-probe/results/validation.json)
- [F1比較](../../../build/nico-mask-probe/results/f1-quality-roi.csv) / [REAL比較](../../../build/nico-mask-probe/results/real-quality-roi.csv)

CPU Ryzen 7 7800X3D、FFmpeg9.0.1 shared SDK、g++16.1.0。GPU RTX5070Ti/driver610.47を検出したが、本試験器の低bit GPU kernel/AVHWFramesContext連携は未実装のためGPU比較は未実施。ハードウェアが無いという意味ではない。

CURRENTとの同条件E2E比較、実GPU経路、4K、HDR、DENSITY100、継続新規字形、60秒warm試験、30分実時間の安定性、シーク/OOMは今回の初期検証範囲外。字形のfontHashはCSS記述のhashであり、実フォントファイルの選択証明ではない。font-candidates.jsonは候補ファイルのhash。性能runは直列、詳細品質runは性能値から除外し一部並列。

## 次に絞る箇所

現在のnative実装はtextureを保持しているが、WARP固定で、コメントごとにUpdateSubresource/Drawを実行し、各frameでCopyResource→Map→fwriteする。まず字形生成・texture登録・描画完了待ち・出力待ちを別計測する。描画側が支配的なら、描画順を保つ命令のまとめ処理、透明余白の縮小、固定層の再利用、実GPU経路を独立に比較する。キャッシュは既に存在するため、重複生成と失効理由を調べる。

優先試験は、同じRGBAを使うDrawInstanced等でのまとめ描画と、WARP/実GPUの比較。低bit化、フォント、縁取り、x264設定は変更しない。透明余白を縮小する場合はbilinear補間の余白と元の小数位置を保持する。固定層の再利用は描画順・丸めが同じになる範囲だけを対象とし、全画素比較で採否を決める。初回字形生成が主因と判明した場合だけ、重複生成の排除・先行生成・キャッシュ失効の改善へ切り替える。

公式根拠: [WARPの性質とバッチ化の指針](https://learn.microsoft.com/en-us/windows/win32/direct3darticles/directx-warp)、[DrawInstanced API](https://learn.microsoft.com/en-us/windows/win32/api/d3d11/nf-d3d11-id3d11devicecontext-drawinstanced)。実GPUがこのアプリ全体で速いという測定結果はまだ無い。

稼働中PID36316は2026-09-16 22:05:35作成のimagepadserver-perf.exe（SHA256 6c76ba71311782f5d91663728a6794c431067851652dc197d2fadf90674cc871）。今回の試験器・新しいnativeビルドとは別の実行物。起動中のアプリを停止/置換していない。まず計測対象のbackendと実行物を揃える必要がある。
