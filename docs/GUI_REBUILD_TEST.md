# GUI Rebuild Test

このブランチは、ImagePadServer のGUI再構築を本線から分離して試すための作業場です。

## 作業場所

- Branch: `codex/gui-rebuild-uupm-test`
- Worktree: `.worktrees/gui-rebuild-uupm-test`
- Design system: `design-system/imagepadserver-app-gui-rebuild/MASTER.md`

## UI UX Pro Max の初期方針

UI UX Pro Max で生成した design system を最初の基準にします。ただし、ImagePadServer は実用ツールなので、派手さよりも次を優先します。

- VRChatで使うURL発行・コピー・履歴・動画変換の操作が迷わないこと
- スマホLANアップロードでも読みやすく、タップしやすいこと
- 既存のアプリ内サービス、API、ボタンID、URLコピー導線を削らないこと
- 既存のリリース番号や配布導線を壊さないこと

## 初期対象

まずはアプリ本体のローカル管理UIだけを対象にします。公式サイト側の変更はこのテストから外します。

- `internal/server/ui.go`

初回のCSS中心変更だけでは不十分だったため、次の段階としてHTMLの大枠をアプリ運用コンソールとして再配置します。
ただし既存のID、API、JavaScriptの動作線は維持します。

## ゼロベース配置

- 上段: 状態バー。外部公開、ImagePad URL、更新、ビデオプレーヤー対応、ミュージックモードを集約する
- 中央左: 作業ステージ。画像/リンク/OBSの入力、変換設定、公開/キュー投入を一つの流れにする
- 中央右: 公開プレビュー。現在公開中メディア、共有URL、更新、クリア、VRChat Video、画質/速度チェックを集約する
- 下段: ライブラリ。履歴、お気に入り、動画変換を素材棚として横幅いっぱいで扱う
- 補助ドック: スマホQR、アプリ情報、設定、終了を公開操作より低い優先度で配置する

## 残すサービス

- スマホ接続QRとLANアップロード
- 公開URL、外部公開状態、アップデート確認
- ビデオプレーヤー対応とミュージックモード
- 画像/リンク/OBSアップロード
- 変換設定、公開、動画変換キュー
- 現在公開中メディアのプレビュー、共有URL、クリア
- 履歴、お気に入り、動画変換タブ
- OBS RTMP/RTSP関連の接続、キー、レイテンシ設定
- 設定、終了、トースト、モーダル類

## 検証

GUI変更前の baseline:

- `go mod download`: pass
- `go test ./internal/app ./internal/config ./internal/imageproc ./internal/library ./internal/network ./internal/settings ./internal/upnp ./internal/ytdlpauth ./internal/ytdlplab`: pass
- `go test ./internal/video -timeout 60s`: pass
- `go test ./internal/obsrtmp -timeout 60s`: pass
- `go test ./...`: 120秒でタイムアウト
- `go test ./internal/server -timeout 60s`: 外側の90秒制限でタイムアウト

GUI作業後は、少なくとも上記の通過済みコマンドを再実行し、`internal/server` は必要に応じてUI関連テスト名に絞って確認します。

## Guardrails

- 本線 `main` では作業しない
- リリース番号や配布物はこのテストでは変更しない
- UI再構築とサーバー挙動変更を同じ差分に混ぜない
- 既存ユーザーが使う主要導線は、見た目を変えても意味とURLを維持する
