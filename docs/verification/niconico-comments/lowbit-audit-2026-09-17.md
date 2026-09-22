# ニコニココメント低ビット監査（2026-09-17）

入力: `C:\Users\masah\AppData\Local\Temp\imagepad-nico-perf-20260916\followup-real6s.scene`
SHA-256: `e17af09edc957b4992e9251be8530f3491f68444dd13dbcf8542c22e6bdb200a`

## 観測結果

NPS1 1920×1080、テクスチャ 13 件、画素 517,987。ユニークRGBAは 284、グレースケール（R=G=B）画素比は 100.000%。
非透明アルファは 222,123、完全不透明は 81,278。既存テクスチャには輪郭を含むため、fill mask 抽出・輪郭再生成の成功とは判定しない。

### A1/A2/A4/A8 アルファ量子化

A8を基準に q=floor(a*(2^b-1)/255+0.5)、復元 round(255*q/(2^b-1))。ROIは境界矩形ではなくアルファ>0画素。全テクスチャ集計: A1: mean 47.037, RMSE 66.368, max 127；A2: mean 10.873, RMSE 15.368, max 42；A4: mean 1.486, RMSE 2.956, max 8；A8: mean 0.000, RMSE 0.000, max 0。品質不変ポリシーではA1/A2/A4はアルファ値が変わるため却下、A8のみ一致。

### パレットと容量候補

RGBA生payload 2,071,948 B、Gray+Alpha生payload 1,035,974 B。zlib level 1 の個別payload合計は RGBA 180,800 B / Gray+Alpha 134,210 B、連結payload level 6 は RGBA 141,126 B / Gray+Alpha 111,896 B。これはオフライン圧縮候補で、ブラウザやE2Eの転送時間ではない。

幅 0,1,3,7,8,9,31,32,33、高さ0/1/3、bpp 1/2/4/8 のMSB packingを検証し、valid=108件、実復号とパレットRGBA再構成は全件一致。短い/長いinvalid lengthも 4 条件で実拒否判定を記録した。このpacking試験は自己生成fixtureのラウンドトリップであり、実ブラウザ/E2Eの証明ではない。

## 判定

有用: 実素材のRGBA分布、グレースケール適性、低ビット誤差、幅ごとのrow_bytes、パレットpackingの再現可能な基礎データ。
却下: 品質不変ポリシー下のA1/A2/A4。A8は同一画素だが容量削減を示す変更ではない。
未検証: fill mask抽出、outline再生成、AVFrame直接共有、ブラウザ・実機E2E性能。
