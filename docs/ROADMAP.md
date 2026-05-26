# Roadmap

ImagePadServer の今後の開発計画です。コードの読み方や責務の索引は `docs/ROADMAP_INDEX.md` を参照してください。

## 実装済み

- HLS reliability
  - 動画ごとに ID 付き playlist / segment 名を使う。
  - playlist と最低 1 セグメントが生成されるまで HLS URL を公開しない。
  - 変換中は EVENT、完了後は VOD として playlist を確定する。
  - 関連コード: `internal/video/publisher.go`, `internal/server/server.go`

- History and resend
  - 画像/動画/OBS 録画を履歴へ積み、管理画面から再公開できる。
  - 履歴メディアは `/history/` 配下の管理 API 経由で扱う。
  - 変換済み HLS は履歴から復元できる。
  - 関連コード: `internal/library/store.go`, `internal/server/server.go`

- Thumbnails
  - 動画、OBS 録画、履歴、動画キューで使うサムネイルを生成・表示する。
  - 関連コード: `internal/server/server.go`, `internal/video/publisher.go`

- Favorites
  - 履歴アイテムをお気に入りとして永続化できる。
  - お気に入りは通常の一時メディア削除とは別ディレクトリで保持する。
  - 関連コード: `internal/library/store.go`, `internal/server/server.go`

- Processing queue
  - 動画ファイル、動画 URL、履歴アイテムを変換キューへ追加できる。
  - 変換中の状態、進捗、待機、完了、失敗を UI に表示する。
  - 新しいジョブを優先し、古い実行中ジョブを待機へ戻す制御がある。
  - 関連コード: `internal/video/publisher.go`, `internal/server/ui.go`

- Manual quality controls / Network check
  - `Auto` / `1080p` / `720p` / `360p` を選べる。
  - 初回と手動操作でアップロード帯域を測定し、Auto 判定へ反映する。
  - 関連コード: `internal/video/publisher.go`, `internal/settings/settings.go`, `internal/server/server.go`

- OBS RTMP
  - OBS から RTMP を受け、HLS 共有 URL と録画 VOD を生成できる。
  - Stream Key の生成・更新と OBS 用 UI がある。
  - 関連コード: `internal/obsrtmp/manager.go`, `internal/server/server.go`, `internal/server/ui.go`

## 優先度 A: 近いうちに直す

- Video mode choices
  - 動画 URL をライブ扱いで見るか、取得して HLS 再配信するかを選択できるようにする。
  - URL 再指定時に古いストリームが残らないことを回帰テスト化する。

- Queue controls
  - キュー内ジョブの明示的なキャンセル、再実行、削除ボタンを整える。
  - 現在は優先投入と状態表示が中心なので、手動操作を追加する。

## 優先度 B: 使い勝手を上げる

- おすそ分け
  - PC を持たない Quest / Android VRChat ユーザー向けに、サーバー主が一時的な投稿専用 URL を開けるようにする。
  - 参加者は画像投稿だけを行い、管理画面・履歴・設定にはアクセスできない設計にする。
  - 初期実装は画像のみ、承認制、セッション有効期限付きにする。
  - 詳細は `docs/OSHARE.md` に分ける。

- Share URL display
  - URL 表示を画像・動画・OBS でさらに整理し、コピー対象の見通しを良くする。
  - ローカル URL、公開 URL、HLS URL のラベルをユーザーが迷いにくい形へ寄せる。

- Quality tuning
  - Auto 判定と画質プリセットを実利用の回線条件に合わせて調整する。
  - 配信中の動的変更は解像度変更ではなくビットレート変更に限定する。

## 優先度 C: 品質と保守

- Security review
  - SSRF、管理トークン、ログ、公開メディア領域、ダウンロード済み外部ツールの検証を継続する。
  - おすそ分け追加時は、投稿専用 API と管理 API の分離を重点的に確認する。
  - メディア保存領域に実行ファイルが紛れ込まない運用を維持する。

- Documentation
  - README はユーザー向け、`docs/ROADMAP.md` は実装済み/今後計画、`docs/ROADMAP_INDEX.md` はコード索引として役割を分ける。
  - BOOTH 説明文とスクリーンショットの更新タイミングをリリースごとに確認する。

## 凍結中

- SteamVR integration
  - 現在は無期限凍結。
  - 資産としてコードとメモは残すが、通常起動や状態ブロックからは参照しない。
  - 再開する場合は、SteamVR overlay の正しい起動手順と描画方式を公式ドキュメントから再調査する。
