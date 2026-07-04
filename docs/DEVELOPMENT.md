# ImagePadServer 開発ガイド

このページは、ImagePadServer を開発・検証・リリースする人向けのメモです。ユーザー向けの操作説明は [USER_GUIDE.md](USER_GUIDE.md) を参照してください。

## 基本コマンド

テスト:

```sh
go test ./...
```

ローカル起動:

```sh
go run ./cmd/imagepadserver
```

クロスプラットフォーム確認:

```sh
scripts/build-release.sh
```

Windows PowerShell で exe を直接ビルドする例:

```powershell
$env:CGO_ENABLED="0"
$env:GOOS="windows"
$env:GOARCH="amd64"
go build -trimpath -ldflags "-H=windowsgui" -o dist\1.5.2\release\win\imagepadserver-v1.5.2-windows-amd64.exe .\cmd\imagepadserver
```

## 配布物の出力先

`scripts/build-release.sh` は安定版と dev 版で出力先を分けます。

安定版:

```text
dist/<version>/release/<platform>/
```

dev 版:

```text
dist/<version>/dev/<devN>/<platform>/
```

例:

- `v1.5.2` は `dist/1.5.2/release/win/`
- `v1.5.2-dev1` は `dist/1.5.2/dev/dev1/win/`

## GitHub Actions リリース

`v*` タグを push すると GitHub Actions の Release workflow が動き、GitHub Release を発行します。

安定版タグ:

```text
v1.5.2
```

dev 版タグ:

```text
v1.5.2-dev1
```

dev タグは `dev-release` の prerelease として公開されます。

## バージョン更新箇所

リリース時は、次の表記が同じバージョンになるように確認してください。

- `internal/about/about.go`
- `winres/winres.json`
- `cmd/imagepadserver/rsrc_windows_amd64.syso`
- `README.md`

Windows リソースを更新した場合は `go-winres` で `.syso` を再生成します。

## Release workflow の前提ツール

Release workflow は macOS runner で実行します。WebP 生成のため、Homebrew の `ffmpeg` と `webp` をインストールします。

```sh
brew install ffmpeg webp
```

通常の FFmpeg に WebP encoder がない環境でも、アプリは `cwebp` があれば WebP 生成へフォールバックします。

## README と docs の役割

README は入口ページです。詳しい説明は次の docs に分けています。

- `docs/USER_GUIDE.md`: ユーザー向けの使い方
- `docs/TROUBLESHOOTING.md`: 問題別の確認手順
- `docs/DEVELOPMENT.md`: 開発・ビルド・リリース手順

README に長い説明を追加する場合は、まず既存 docs のどこに置くべきかを確認してください。

## 関連資料

- [ROADMAP_INDEX.md](ROADMAP_INDEX.md)
- [ARCHITECTURE.md](ARCHITECTURE.md)
- [OBS_LATENCY_PROTOCOL_DECISION.md](OBS_LATENCY_PROTOCOL_DECISION.md)
- [OBS_AVPRO_FEASIBILITY.md](OBS_AVPRO_FEASIBILITY.md)
