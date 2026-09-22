# ネイティブコメント画像描画の比較実験

2026-09-17。ユーザーの「ためしてみて」に基づく実験。稼働中アプリ・本番実装は変更せず、実験コードと結果を保存する。画質設定・元の RGBA テクスチャ・描画順・時刻を維持する。

## 比較と依存関係

### T1: 設計確認
- **depends_on**: []
- **location**: この計画、native/nico-compositor/main.cpp
- **description**: 既存の WARP 合成器を基準に、順序維持の同一テクスチャ連続区間のみ DrawInstanced、透明余白のクロップ、ハードウェア D3D11 を別々に比較する。アトラス変更は今回含めない。
- **validation**: 独立レビュー。既存バイナリと実験 baseline の全フレーム一致。
- **status**: 完了
- **log**: mask_renderer の独立レビューを反映。PMA と透明 RGB、各 instance の行列・色、discard 時の Map 維持、adapter/driver、上下向き、malformed stream を検査対象とした。

### T2: ネイティブ実験実装
- **depends_on**: [T1]
- **location**: scripts/experiments/nico-native-probe/main.cpp, build.ps1
- **description**: 本番ソースの隔離コピーに --hardware、--instanced、--discard-output と CPU 区間計測を追加。同一 ID の連続コマンドのみまとめ、順序を保持。入力待ち、テクスチャ準備、CPU 描画投入、Map 待ち、出力待ちを区別。GPU 時間は timestamp/disjoint を任意計測できる場合のみ記録し、CPU 投入時間を GPU 実行時間と呼ばない。
- **validation**: 同一 RGBA 入力に対し全フレーム画素比較、描画コール数減少、無効ストリーム検査。変更なしの baseline と同じコンパイラ。
- **status**: 完了
- **log**: 独立コピーと同条件MSVCビルド。明示base indexでinstance offset不具合を修正し、REAL / F1 / stressでまとめ描画の全画素一致を確認。GPU timestampは未実装と明示、CPU投入・Map待ちを測定。

### T3: 固定入力・計測ハーネス
- **depends_on**: [T1]
- **location**: scripts/experiments/nico-native-probe/probe.py、必要時 internal/nicorender/native_profile_test.go
- **description**: F1/REAL の original RGBA NMF1 を NPS3 に変換。透明領域は最低 1 texel の補間用余白を残し、元の位置・大きさに対応した座標へ変更。サブピクセル補間の差はゼロでない場合報告し採用しない。ブラウザの文字生成・テクスチャ取り出しは別計測する。
- **validation**: FPS・フレーム数・コマンド順保存、境界と半透明混在、元画像と変更画像の全フレーム差。クロップは安全性確認用の実験で劣化を許可しない。
- **status**: 完了
- **log**: 元RGBAから変換。cropはREAL最大7/F1最大1の差で不採用。ブラウザは6秒/154.955秒を各5回測定。nil transportとバッファ再利用を現在のproduction手順に一致させた。

### T4: 実行・判定
- **depends_on**: [T2, T3]
- **location**: build/nico-native-probe、docs/verification/niconico-comments/native-render-2026-09-17.md
- **description**: baseline / instanced / crop / hardware を一変数ずつ比較。ウォームアップ後、交互順で各 5 回。合成単独と同じ libx264 CRF26 veryfast 設定でのエンコードを別評価。CPU / GPU / 入出力待ちを区別し、効果がある画素一致案だけ組合せ評価する。
- **validation**: 実行終了コード、全フレーム画素比較、MP4 decode・フレーム数・PTS、単一映像スライス。解像度1080p、60fps。実動画6秒のコメントを用いるがライブアプリ性能と混同しない。
- **status**: 完了
- **log**: 合成単独・エンコード込み各5回、追加のraw受渡し/overlayまでを各3回で比較。F1のcropは画素不一致かつREALエンコードでも利点がないため追加エンコードを省略。実GPUはWARPと最大2差のため本番昇格・組合せ性能試験は保留（REAL/stressでGPU内のまとめ有無は画素一致確認）。36件の不正入力拒否と42本のdecode/PTS/frame数/1 slice per AUが成功。別GPU負荷の変動を全実行に記録。結果は docs/verification/niconico-comments/native-render-2026-09-17.md。本番・稼働アプリ・Git履歴は変更なし。

## 役割と記録

T1 レビューは読取専用サブエージェント、T2 は GPU 実装担当、T3/T4 と最終判断はメイン担当。T2 と T3 のみ並行可。コミット・push・アプリ再起動は行わない。性能仮説のため reason_not_testable: 固定閾値の単体テストではなく再現可能な実測・画素比較で検証する。既存の lossy A1/A2/A4 マスクを基準画像に使わない。
