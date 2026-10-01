# ニコニコ動画コメント再現の先行実装調査

調査日: 2026-09-16。目的: URL取り込み時に選択した動画を、流れるコメント込みで事前エンコードする設計の根拠を整理する。

実装計画: [ニコニコ動画・コメント付きエンコード実装計画](../plans/2026-09-16-niconico-comments.md)

## 1. 調査から得た結論

1. **描画は `@xpadev-net/niconicomments` を固定版で採用する案を推奨する。** レーンだけを自作する方式では、文字幅、改行リサイズ、Flash/HTML5差、コメントアート（CA）の再現範囲が狭くなる。
2. **コメント取得と映像取得を分離する。** 映像は既存のyt-dlp、コメントはスレッド構造を残す取得アダプターとする。yt-dlpのニコニコ字幕JSONは取得の参考になるが、現行コードではスレッドを平坦化する。
3. **NNDDからは動作仕様と回帰ケースを学び、古い実装全体は移植しない。** 現行構成の直接的な参考は、niconicomments-convertとRe:NNDDの焼き込み処理。
4. **動画時刻からコメントフレームを生成する。** 実時間のタイマー、画面キャプチャ、利用者のブラウザータブの再生状態に処理を依存させない。
5. **一度だけ映像を再エンコードする。** コメント付きMP4を完成させ、そのMP4をHLSへremuxして既存配信に渡す。

以上は本プロジェクト向けの設計判断であり、先行実装の性能・再現性をそのまま保証するものではない。

## 2. 調査の証拠区分

| 区分 | 今回の到達点 |
|---|---|
| ソース確認 | ImagePadServerの現行作業ツリー、下記OSSのREADME・実ソース・ライセンスを確認 |
| 外部接続確認・更新 | 追加検証でCookieなしの`watch/sm9`と、`actionTrackId`を付けた`api/watch/v3_guest/sm9`がHTTP **200**。動画ページの`nvComment`を使ったthreads取得もHTTP/JSONともに **200**。実装したGoクライアントのライブテストも成功 |
| 実取得結果 | `sm9`でmain **850件**、easy **166件**、owner **0件**、合計 **1,016件**。全取得コメントに文字列の本文と数値の`vposMs`を確認 |
| 未実施 | 別動画・認証必須動画の取得、実動画ダウンロード、描画・エンコード速度測定、ブラウザーE2E、VRChat実機確認 |
| 初回406の扱い | 初回は406で止まったが、取得不能の根拠にはならない。追加検証では`actionTrackId`なしが400 `INVALID_PARAMETER`、ありは200。初回406と今回400の原因が同一とは断定しない |

外部接続確認ではユーザーの保存Cookieやアカウントを使用していない。本文・userId・threadKey・生API応答はファイルやログに保存せず、status・件数・型の確認結果のみ記録した。取得集合はその時点でAPIが返した範囲であり、動画の全期間の全コメントを意味しない。既定描画対象はmain＋ownerなので、この確認例では850件となる。

再現手順: `X-Frontend-Id: 6`、`X-Frontend-Version: 0`等を付け、guest watch APIには`actionTrackId=AAAAAAAAAA_<Unix時刻ms>`をクエリ指定する。動画ページの`data-server-response`にも`data.response.comment.nvComment`が存在した。その`server/params/threadKey`から、`additionals:{}`とともに`POST /v1/threads`へ要求する。threads要求は`Content-Type: text/plain;charset=UTF-8`、ニコニコのOrigin/Referer、`X-Client-Os-Type: others`を使用した。要求形状は[yt-dlpの一次ソース](https://github.com/yt-dlp/yt-dlp/blob/master/yt_dlp/extractor/niconico.py)を再確認した。

実装した`internal/niconico.Client`に対して同じ条件でライブテストを実行し、非空スナップショット、main/easy/ownerのスレッド構造、各コメントIDを確認した。本文そのものやthreadKeyは試験ログへ出していない。

## 3. NNDD

確認対象: [SSW-SCIENTIFIC/NNDD](https://github.com/SSW-SCIENTIFIC/NNDD)、固定コミット `84f0f11efaca4fed5faf9fae461cc3963ca0adfb`。
READMEでは保守終了を表明している。旧Adobe AIR / ActionScriptとXML形式が前提であり、現在の取得APIの仕様書としては扱わない。

| 実ソース | 観察したこと | 今回に取り込む知見 |
|---|---|---|
| [Comments.as](https://github.com/SSW-SCIENTIFIC/NNDD/blob/84f0f11efaca4fed5faf9fae461cc3963ca0adfb/src/org/mineap/nndd/player/comment/Comments.as) `getComment` / `resetEnableShowFlag` | XMLのvposを10倍してミリ秒に換算。一度表示したコメントをフラグで消費 | 旧vposとvposMsの単位を混同しない。事前エンコードでは消費タイマーではなくフレーム時刻を使用 |
| [CommentManager.as](https://github.com/SSW-SCIENTIFIC/NNDD/blob/84f0f11efaca4fed5faf9fae461cc3963ca0adfb/src/org/mineap/nndd/player/comment/CommentManager.as) `moveComment` / `searchNextNNDDText` | 移動間隔・本文長による速度補正、固定プールと前走者の右端による空き判定 | 長短コメントが同時に流れるケース、密集時の配置を比較テストにする |
| 同 `setComment` / `removeComment` | 上下固定、サイズ・色の分岐。画面外・表示期限で消去。ジャンプ等を伴う命令も処理 | 描画属性と再生操作を分離。動画を書き出す処理にはジャンプや外部アクセスを持ち込まない |
| [NNDDComment.as](https://github.com/SSW-SCIENTIFIC/NNDD/blob/84f0f11efaca4fed5faf9fae461cc3963ca0adfb/src/org/mineap/nndd/model/NNDDComment.as) | 本文だけでなくmail、vpos、user_id、thread、投稿日時を保持 | 入力正規化時に再現に必要な属性を落とさない |

`CommentManager.as` の通常コメントは12段×5枠、上下固定は各12枠。これはNNDD側の方式であり、現行公式プレイヤーの普遍的なレーン数として採用しない。`moveComment`の呼び出し間隔依存も移植しない。

ライセンスは[LICENSE](https://github.com/SSW-SCIENTIFIC/NNDD/blob/84f0f11efaca4fed5faf9fae461cc3963ca0adfb/LICENSE)と旧作者分の[LICENSE0](https://github.com/SSW-SCIENTIFIC/NNDD/blob/84f0f11efaca4fed5faf9fae461cc3963ca0adfb/LICENSE0)でMITを確認。ただしリポジトリ内には別ライセンスの構成要素もあり、全ファイルを一括してMITと判断しない。今回の推奨案ではNNDDコードを組み込まない。

## 4. niconicomments

確認対象: [xpadev-net/niconicomments](https://github.com/xpadev-net/niconicomments)、固定コミット `d3eb388197b9e40c6e9c592e83a37ecc6ab39fcd`。このコミットのpackage.jsonは **0.4.1**、ライセンスはMIT。

- [公開API資料](https://xpadev-net.github.io/niconicomments/)には、v1入力、`mode`、`keepCA`、`scale`、`drawCanvas(vpos)`がある。描画時刻は秒×100。既定の`keepCA=false`は保持し、CAを別レイヤーにする補正を公式標準表示と混同しない。
- [v1パーサー](https://github.com/xpadev-net/niconicomments/blob/d3eb388197b9e40c6e9c592e83a37ecc6ab39fcd/src/input/v1.ts)は`thread.fork == "owner"`を投稿者判定に使い、`vposMs / 10`を切り捨てて内部時刻へ変換する。`postedAt`が不正ならコメントを除外する。`isPremium`、`commands`、`userId`も使用する。
- [HTML5Comment](https://github.com/xpadev-net/niconicomments/blob/d3eb388197b9e40c6e9c592e83a37ecc6ab39fcd/src/comments/HTML5Comment.ts)は文字計測、改行による縮小、固定コメントの横幅調整を実装している。単純な「文字数×固定幅」への置換は避ける。
- [rendererインターフェース](https://xpadev-net.github.io/niconicomments/type/interfaces/_types_renderer.IRenderer.html)があり、Canvas・CSS・WebGL系の実装を分離している。今回はブラウザーのCanvas2Dを選び、別の文字描画エンジンとの互換性検証を増やさない。
- [fonts.ts](https://github.com/xpadev-net/niconicomments/blob/d3eb388197b9e40c6e9c592e83a37ecc6ab39fcd/src/definition/fonts.ts)のフォント選択と実機フォントの差が再現性に影響する。日本語が表示できることと、CAの幅が一致することは別の受入れ条件にする。

採用時はnpmの可変エイリアスやCDN実行時取得を使わず、配布物のバージョン・整合性ハッシュ・対応コミットを固定する。README、Webドキュメント、SECURITY.mdの版表記には差があったため、実装・型定義・テストも同じ固定版を参照する。

## 5. niconicomments-convert

確認対象: [xpadev-net/niconicomments-convert](https://github.com/xpadev-net/niconicomments-convert)、固定コミット `31048c6722b169a91a9ea4326262e7e780730074`、MIT。

[renderer.ts](https://github.com/xpadev-net/niconicomments-convert/blob/31048c6722b169a91a9ea4326262e7e780730074/src/renderer/renderer.ts)は、コメントをCanvasに描画し、フレームIDを付けたPNGを変換側へ送る。描画とエンコードを並行させ、未処理フレームが増えた場合に待機する設計は参考になる。

ただし参照版には例外を透明フレームへ置換する処理や、内部`timeline`への依存がある。今回の設計では、**描画失敗と正しい透明フレームを区別し、フレーム欠損を成功扱いしない**。公開APIで最初から最後まで描画し、空区間の最適化は正確性を確認した後に限定する。Electronアプリ全体は導入しない。

## 6. Re:NNDDとNicoCommentDL

確認対象: [Re:NNDD](https://github.com/abeshinzo78/Re-NNDD)、固定コミット `640b933c5edb82e7635d954a6f62071e99ef1df3`、MIT。NNDDの精神的後継を目指す別実装。

- [browser.ts](https://github.com/abeshinzo78/Re-NNDD/blob/640b933c5edb82e7635d954a6f62071e99ef1df3/src/lib/burnin/browser.ts): Canvas2DのコメントフレームをRustへ送り、FFmpegに入力する。書き出し中・終了処理中のキャンセルを処理する。
- [comments.ts](https://github.com/abeshinzo78/Re-NNDD/blob/640b933c5edb82e7635d954a6f62071e99ef1df3/src/lib/burnin/comments.ts): 投稿日時の形式、フォント、v1データ形をプレイヤーと書き出しで揃える。一方、参照版では`isPremium:false`等の補完があるので、そのまま移植せず元データを維持する。
- [comment.rs](https://github.com/abeshinzo78/Re-NNDD/blob/640b933c5edb82e7635d954a6f62071e99ef1df3/src-tauri/src/api/comment.rs): `POST {server}/v1/threads`とmain/owner/easyの区別を確認。HTTP成功だけでなくレスポンス内ステータスも検査する必要がある。
- [burnin検証手順](https://github.com/abeshinzo78/Re-NNDD/blob/640b933c5edb82e7635d954a6f62071e99ef1df3/scripts/burnin-verify/README.md): 焼き込み前後のフレーム比較を独立した検証工程にしている点を参考にする。

[NicoCommentDL](https://github.com/abeshinzo78/NicoCommentDL)はniconicommentsとWebCodecsを組み合わせたブラウザー拡張。有限キューによる流量制御、フレーム番号からの時刻生成、アルファ処理の検証対象を学べる。ただしImagePadServerにはFFmpeg基盤があるため、独自HLS取得・WebCodecsエンコード・MP4 mux実装は追加しない。READMEの性能や互換性の主張を本プロジェクトの測定値にはしない。

## 7. ASS字幕へ変換する案

- [Danmaku2ASS](https://github.com/m13253/danmaku2ass): ニコニコ等のXML/JSONをASSへ変換する先行実装。GPL-3.0。
- [NicoDanmaku2ASS](https://github.com/fireattack/nicodanmaku2ass): ニコニコとAA表示を重視した派生。GPL-3.0。
- [FFmpeg ass/subtitlesフィルター](https://ffmpeg.org/ffmpeg-filters.html#ass): libassによる映像への字幕合成。`fontsdir`、文字形状・フォント・座標系の検証が必要。

ASSの移動・固定表示は基本コメントを軽い依存で実現する代案になる。ただしCanvas側の改行リサイズ、フォント置換、スクリプト、CAまで等価になるとは確認できていない。今回は比較用の候補に留め、描画不具合時に自動でASSへ切り替えない。GPLコードをMITの自作コードとして取り込まない。

## 8. yt-dlpと取得データの落とし穴

参照: [niconico.py](https://github.com/yt-dlp/yt-dlp/blob/bbc809a1161d3bfca51fa36f59dda35556ee85a0/yt_dlp/extractor/niconico.py)、コミット `bbc809a1161d3bfca51fa36f59dda35556ee85a0`。

`_get_subtitles`はwatch情報の`comment.nvComment`からserver、params、threadKeyを読み、threads APIを呼び出す。出力名は字幕言語`comments`、形式`json`。対応するCLI指定は`--write-subs --sub-langs comments --sub-format json`であり、一般的な`--write-comments`と区別する。

参照版は`data.threads[*].comments[*]`をフラット配列へ変換する。スレッドのID/forkをその出力から正しく復元できるとは限らず、投稿者をmainと推測して補う設計にはしない。動画取得だけを既存yt-dlpに任せ、スレッド保持型の独立取得口を設ける判断はこの実装に基づく。

コメント総数の統計値と、APIが実際に返す表示対象集合は同一とは限らない。過去の全コメント、公式アカウントのNG設定、公式視聴画面と同じコメント選別まで取得できたとは表示しない。

## 9. ImagePadServerへの適用で重要な現状

調査時HEAD: `4a7d02df6a5c148c8182c0d3b7d3fb867960fe63`。既存の未コミット変更を含む作業ツリーを読んだ。グラフ索引には古い行番号があったため、次は実ファイルで確認した箇所。

| 現行箇所 | 確認事項 | 計画への反映 |
|---|---|---|
| `internal/server/server.go:919` / `:1124` | URLの即時共有とキュー追加が別ハンドラー | 両方で同一のコメントオプションを受理 |
| `internal/video/soundcloud.go:77` | `IsPageMediaURL`にニコニコ判定がない | yt-dlp失敗後にHTMLを動画として再取得する誤ったfallbackを防ぐ |
| `internal/video/publisher.go:952` | `yt-dlp-source.*`という共通一時名・掃除処理 | ニコニコ準備ジョブには専用ディレクトリを使用 |
| `internal/server/server.go:1480` / `:2299` | 入力動画を履歴・currentに設定後、HLS変換を開始 | コメント付きは完成前にcurrentを置き換えない専用経路が必要 |
| `internal/video/publisher.go:795` | 通常動画はHLSへ直接エンコード | コメント付きではMP4完成→HLS remuxの専用ジョブを追加 |
| `internal/video/publisher.go:591` | `GeneratedFiles`はIDと無関係な`current.mp4`も拾う | 新ジョブでは型付き成果物一覧を渡し、共通globから収集しない |
| `internal/library/store.go:756` | `MarkConverted`は既存履歴IDとHLS成果物を前提にする | MP4・HLS・コメントメタデータをまとめて完成登録するAPIが必要 |
| `internal/server/browser_media_probe.go:95` | 専用プロファイルの非表示Edge/Chrome起動とCDP接続例がある | 起動方法を参考にし、描画用プロセスは別所有・別ポートで管理 |
| `internal/video/font.go:17` | Noto Sans JP 3ウェイトが組み込まれている | 日本語fallbackに再利用。公式フォントとの寸法一致は別検査 |

今回のURL動画配信はHLS経路である。OBS/AirPlayのRTSP経路を、この機能に必須の既存VOD経路として扱わない。

## 10. 再現テストに落とす項目

通常スクロール、上下固定、同時刻の長短文、サイズ・色、改行、全角/半角空白、罫線、結合文字、絵文字、旧Flashコメント、HTML5コメント、投稿者コメント、密集、画面内に残っているコメント、区間途中からの参照描画、動画先頭/末尾、4:3/16:9/縦動画を含める。

評価は「同じ固定版ライブラリを直接描画した基準フレームとの一致」と「権利上利用できる実サンプルでの公式表示との差」を分ける。前者だけで公式完全再現とは呼ばない。ライブラリのサンプル動画・コメントJSONは、ソースコードのMIT許諾だけを理由に本製品へ再配布しない。

## 11. 実装初版の動作確認

`@xpadev-net/niconicomments` 0.4.1のbundle（SHA-256 `D62F58AE0BD045EB86C2E116EEFAEC34E46E9B53C2F710656EFAE9E79252FEE7`）をheadless Chromiumで読み込み、v1 threadをPNG/RGBAへ変換した。RGBAは2フレームのbounded pipeを介してFFmpegへ渡し、CPU libx264/AACのMP4を作成後、HLSは`-c copy`でVOD化する実装とした。これは合成経路の成立確認であり、30秒以上の連続描画、VFR、遅延音声、実機プレイヤーの互換性を保証するものではない。
