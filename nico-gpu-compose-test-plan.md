# 元動画合成と色変換のGPU移行試験

2026-09-17。「テストしてみて」に基づく隔離実験。既存試験・本番・稼働アプリ・Git履歴は変更しない。

追記：ユーザーの「外部委託をせずにあなただけでサクッと終わらせて」に従い委託を停止。GPUコードもメインが作成した。今回はREAL6秒の全360frame画素比較と、warmup後3回の速度比較に絞る。F1と5回測定は未実施として扱う。

## 設計

元動画のCPUデコードとbicubic拡大は共通化する。候補は、拡大済みRGBAをGPUへ送り、元のRGBAコメント平面とのPMA合成を行い、BT.709 limitedのYUV420pへcompute shaderで変換する。完成フレームだけをlibx264へ渡す。画質設定はCRF26/veryfast/threads8、H.264の1フレーム1スライスを保持する。

GPUで元動画をデコード・拡大する実験ではない。元動画側RGBA入力用の追加パイプも含めて計時する。GPU→CPU読み戻しは4→1.5 byte/pixelになるが、プロセス間総転送量が同じだけ減るとは主張しない。

### T1: 契約・レビュー
- **depends_on**: []
- **location**: 本計画、既存 native probe、docs/RTSP_H264_COMPATIBILITY_CONTRACT.md
- **description**: NPS3シーンファイル＋stdinの背景RGBAを入力、stdoutはI420または検査用RGBA。実素材はBT709/tv、1280x720/30000/1001で確認済み。出力1920x1080/60、CPU基準も出力行列・rangeを明示。色変換の差は別に検査する。
- **validation**: 独立レビュー。FPS、順序、PMA、行列・range、chroma filterの違いを記録。
- **status**: 完了。独立レビュー反映：背景生成は同一filter/画素形式、PMA合成とYUV差を分離、色行列はfilterで指定、PTSはframe index/fpsで検査する。最終背景は不透明を前提とする。

### T2: GPU合成器
- **depends_on**: [T1]
- **location**: scripts/experiments/nico-gpu-compose/main.cpp, build.ps1
- **description**: 前回D3D11コードの隔離コピー。元のコメント平面を先に描画し、背景RGBAとの合成を別passで行う。GPUでI420に変換し、3つのstaging bufferを交互にMapし、そのまま書き出す。8x2pixel単位でDWORDへpackして書込競合を防ぐ。widthは8の倍数、heightは偶数に限定。RGB→709 limited、chromaは2x2平均。--rgbaで最終合成RGBAを検査できる。GPU名、処理時間、読込・読戻しバイト数を記録。
- **validation**: MSVC build、黒白・原色fixture、短い入力・不正NPS3拒否、入出力frame数。同一RGBに対するCPU色変換との画素差を報告する。
- **status**: 完了。メインが作成・ビルド、黒白原色と独立CPU計算を検証。

### T3: 比較ハーネス
- **depends_on**: [T1]
- **location**: scripts/experiments/nico-gpu-compose/run.py, verify.py
- **description**: 従来WARPコメント＋CPU合成、GPUコメント＋CPU合成、GPU全体合成＋CPU色変換、GPU全体合成＋GPU色変換を同条件で比較。各processの終了、stderr、hash、argv、GPU負荷を保存。source→RGBAの処理も計時に含める。NPS3とframe clockは前回fixtureを再利用。
- **validation**: REAL6秒360frames、F1高密度10秒600frames。品質は全frame YUV差・合成RGBA差・色バー、保存画像の確認。初回warmup後、順序を変えて5回。高密度は効果のある候補を絞って測る。
- **status**: 完了。ユーザー指定でREAL6秒・3回へ縮小。360frameのRGBA/YUV全画素比較。

### T4: 試験・判定
- **depends_on**: [T2,T3]
- **location**: build/nico-gpu-compose、docs/verification/niconico-comments/gpu-compose-2026-09-17.md
- **description**: 実速度と品質を別評価。raw-copy対AVFrameの前回結果を今回の高速化と混ぜない。元動画拡大・合成・出力色変換を別々に検査できる記録を残す。
- **validation**: MP4 decode / frame数 / PTS / one slice per AU。鮮明な色境界とコメント部分も確認。完全一致しなければ差を明示し、本番には昇格しない。
- **status**: 完了（縮小スコープ）。16本のMP4 decode/PTS/frame数/1sliceを確認。全体GPU案は不採用。docs/verification/niconico-comments/gpu-compose-2026-09-17.md に記録。

メインがT1/T3/T4、GPU担当がT2。T2とT3のみ並行。性能実験なので固定閾値の単体テストは置かず（reason_not_testable: 性能の成否を仮定できない）、実測と全画素比較を受入証拠にする。ユーザーの既存指示に従いcommit/push・再起動は行わない。
