# 低ビット字形＋AVFrame直接共有の試験計画

ユーザーの2026-09-17「まずテストをしてみて高速かが確認されるかチェックしよう」に基づく。前回のDeflate/OS pipeの試験と分離する。通常アプリ、配布物、稼働サーバーは変更しない。試験専用libav開発環境をbuild内へ置く。commit/push/releaseを行わない。

## 比較契約

CPU 1920x1080/60fpsでR8-C/R2-C/R8-D/R2-Dを実装。Cは合成scratchからAVFrameへの明示コピー、Dは書込可能なAVFrameへ直接合成しlibavfilterへ参照付きで渡す。同じlibavcodec/libx264、CRF26/veryfast、1スライス、同じ入力/PTS/背景/縁取りを使う。必要な色変換は共通。Cは制御基準であって現行アプリそのものではない。

A8字形からA1/A2/A4を作り、bit列で保持、同じ最大値膨張の縁取りを合成側で生成する。初期比較は初回A8展開＋縁取りキャッシュ（E）を固定し、P直接読出しは追加軸として別記する。量子化で差があっても速度試験は実行し、画質と速度を別判定する。

主素材は既存niconicommentsの白コメfixtureと実snapshot由来の単色コメント。ブラウザーのstrokeを抑制してfillのアルファを取得し、元の位置/時刻/描画順を保存する。元RGBA版は別の品質参照として保持。分離できない色/装飾は無理に単色化しない。合成方法が違うA8と現行の同一性を仮定しない。

試験ファイルNMF1はlittle endian。header uint32[7]={magic0x31464d4e,width,height,fpsNum,fpsDen,textureCount,frameCount}。各texture uint32[8]={id,width,height,kind,fillRGBA,outlineRGBA,outlineRadiusPx,payloadBytes} + payload。kind1=rawA8 w*h、kind0=元RGBA w*h*4。色はRが低位byte。各frame uint32 commandCount + NPS互換100byte command {id,rect[4]float,proj[16]float,color[4]float}。frame時刻=floor(index*fpsDen*1000/fpsNum)。

T0レビュー反映: 出力最大3840x2160、texture寸法16384以下/各128MiB/合計512MiB、texture1万以下、frame10万以下/command全体1000万以下、ファイル1GiB以下。乗算前に検査し、未知kind/不一致payload/末尾余剰を拒否する。AVFrameのPTSはindexそのもの、time_base={fpsDen,fpsNum}とし、ms値をPTSに代用しない。パケットはav_rescale_qでmux time_baseへ変換する。

共有API契約はKEEP_REF付きbuffersrc。poolが持つAVFrameと下流の参照を分離し、av_frame_is_writableがtrueのslotのみ再利用。null filterで実ポインター・保持ref・解放後のwritabilityを検査する。色変換先やencoder内部コピーは別に計上/未計測とし、zero-copyとは呼ばない。pool上限到達時はdrainを試み、進捗なしならエラー終了する。

初期速度試験は背景RGBA(32,48,64,255)を共通にし、映像デコードを含めない。既存経路CURRENTとの比較は同じ背景動画を使うが、glyph方式・縁取りも異なるため4条件の主比較と混ぜない。

最低4条件は各5回、順序反転、10秒fixture。F0なし/高密度、A1/A4、warm/steadyを追加して影響を切り分ける。60秒/30分試験は最初の成立後、時間と実用判断に必要な範囲で延長。GPUは実デバイス/API互換性を調査し、同じ4条件が成立するなら別計測。成立しなければ具体的理由と未検証範囲を残す。

## タスク

### T0: 計画レビュー
- **depends_on**: []
- **location**: 本計画（読み取りのみ）
- **description**: 比較交絡、所有権・保持参照・EOS・画像差の判定を独立レビュー。
- **validation**: 指摘をメインが確認してAPIを固定。
- **status**: 完了。サイズ/PTS/参照寿命の指摘を採用。

### T1: 試験用SDKとAVFrame/encoder
- **depends_on**: [T0]
- **location**: scripts/experiments/nico-mask-probe/main.cpp, build.ps1, build/nico-mask-probe
- **description**: メイン担当。公式配布先の固定shared SDKをhash検証し、同一プロセスのAVFrame参照共有とlibavfilter/libx264を実装。プールはav_frame_is_writableで返却を確認し保持中を上書きしない。EAGAIN再送とdrain、コピー/alloc/参照/PTSのカウンターを実装。
- **validation**: 実APIを呼ぶsmoke、同bitのC/Dの全画素/出力一致、保持参照を残す寿命テスト、EOF/失敗/キャンセル。
- **status**: 試験器完成。SDK hash/参照寿命/EAGAIN再送/drain成立。最終品質・障害検査はT4。

### T2: 字形・描画命令fixture
- **depends_on**: [T0]
- **location**: internal/nicorender/mask_probe_test.go と scripts/experiments/nico-mask-probe/capture.js のみ
- **description**: 専任担当。nico_mask_probeタグ限定で固定A8/NMF1と元RGBA品質参照を生成。白コメF1（同時30件、固定＋移動、10秒/60fps）、実snapshot6秒/60fps、F0/密度100を用意。source/font/fixtureのhashと描画条件を記録。
- **validation**: payload/dim/ID/時刻、非空mask、ブラウザー生成完了。大きい実ブラウザー試験はメインが直列実行。
- **status**: F1/REALを実Chromeで生成済み。WebGL captureへの修正と実行はメインが担当。元browser RGBAと同一commandsの基準NMF1も保存。

### T3: 低ビットCPU合成
- **depends_on**: [T0]
- **location**: scripts/experiments/nico-mask-probe/mask_renderer.h と専用selftest.cpp のみ
- **description**: 専任担当。NMF1読み込み、bpp1/2/4/8のMSB packing、同じA8展開＋縁取り、NPS矩形/射影のCPU bilinear合成。色/描画順/clip/小数位置を保持。RGBA例外を維持。独立selftestを用意。
- **validation**: width0/1/3/7/8/9/31/32/33、stride、全alpha、破損、定義済み矩形・射影・重なり。同bitで出力決定的。
- **status**: 実装・fresh selftest PASS。試験器の背景/outline条件誤りをメインが修正。性能計測前に合成器を固定。

### T4: 比較・品質・報告
- **depends_on**: [T1,T2,T3]
- **location**: scripts/experiments/nico-mask-probe/run.py、docs/verification/niconico-comments/mask-avframe-2026-09-17.md、build/nico-mask-probe
- **description**: メイン担当。4条件と追加軸を直列測定。frame_handoffのコピー実byte、合成/展開/encoder待ち/全体時間、画素差、全デコード/PTS/1スライス、環境hashを保存し、高速かを判定する。
- **validation**: 試作単体と既存経路比較を混同しない。未計測はN/A。画質不合格でも速度値は残す。通常アプリへ昇格させない。
- **status**: 初期検証完了。F0/F1/REALの4条件各5回、F1/REALの全frame C/D画素一致、A1/A2/A4差分、参照寿命/EAGAIN/キャンセル/途中エラー、82出力のdecode/PTS/1sliceを検査。結果はdocs/verification/niconico-comments/mask-avframe-2026-09-17.md。GPU/CURRENT/高密度/長時間の拡張試験は未実施として明記。

追加方針: ユーザー指示によりコピー共有の試作を保持し、劣化を伴う低bit量子化は不採用。実験コードと結果は再現用に保存。既存の可逆palette/Deflateは継続。次は実行中backendの確認と字形生成/合成/待ちの分離計測から、描画命令のまとめ処理と実GPU経路を独立比較する。

T0後、T1/T2/T3を独立実装。ベンチマークはT4で直列。試験器作成は実行可能なselftest・故障注入・実出力比較を受入れ証拠とし、性能仮説自体を単体テストの期待値にしない。ユーザーの明示的な試験指示を本計画の実行承認として扱う。
