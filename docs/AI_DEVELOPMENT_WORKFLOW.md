# AI Development Workflow

ImagePadServer を複数の AI / 外部ツールで引き継ぎながら開発するための作業手順です。

## 最初に読むもの

1. `docs/AI_TEAM_HANDOFF.md`
2. `docs/AI_SESSION_LOG.md` の最新セッション
3. `README.md`
4. 変更対象に関係する `internal/*` の実装

## 標準フロー

1. 依頼内容と引き継ぎ内容を確認する。
2. 既存の設計とパッケージ境界を読んで、最小の安全な変更に絞る。
3. 実装する。
4. `go test ./...` を実行する。
5. Windows 配布が関係する場合は GUI ビルドも確認する。
6. 変更内容、テスト結果、残リスクを次の担当者に残す。

## 役割の目安

- Supervisor: 作業範囲を決め、優先順位と停止条件を管理する。
- Product Lead: VRChat / ImagePad の利用体験を守り、受け入れ条件を整理する。
- Go Backend Engineer: API、画像処理、HLS、設定、ライブラリ状態を実装する。
- Windows UX Engineer: トレイ、ネイティブウィンドウ、ビルド、外部プロセス挙動を確認する。
- QA Reviewer: セキュリティ、競合、回帰、テスト不足を確認する。

## ガードレール

- SteamVR 連携は凍結中。明示依頼がない限り復活させない。
- UPnP 自動ポート開放は安全のため無効のまま扱う。
- 管理画面は localhost または管理トークン付き QR / Cookie の導線を守る。
- 公開 URL 上のメディアは VRChat が読むためのものとして扱う。
- FFmpeg / yt-dlp / cloudflared は Windows でコンソールを出さない。
- UI と README の日本語は UTF-8 で保存する。

## よく使うコマンド

```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
go test ./...
```

```powershell
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -trimpath -ldflags "-H=windowsgui" -o dist\1.2.2\dev\dev1\win\imagepadserver-v1.2.2-dev1-windows-amd64.exe .\cmd\imagepadserver
```
