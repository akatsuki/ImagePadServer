# ImagePadServer

ImagePadServer は、VRChat の ImagePad 系ギミックへ画像URLを渡しやすくするためのローカルWebサーバーです。

PCやスマホのブラウザから画像をアップロードすると、サーバー側で VRChat の画像読み込み向けにリサイズ・変換し、ImagePad に貼り付けるためのURLを発行します。

## 現在できること

- 起動するとローカルWebサーバーを開始
- ブラウザUIを自動で開く
- PC/スマホから画像をアップロード
- スマホ接続用QRコードをPC表示時に表示
- スマホ表示時はQRコードを自動で非表示
- 画像を最大 `2048 x 2048` に収まるよう変換
- JPEG/PNGへ変換
- JPEGは指定サイズ以内に収まるよう品質を調整
- 現在選択中の画像を `/image/current` で配信
- UPnPでTCPポート開放を試行
- Cloudflare Tunnel接続時は公開HTTPSのImagePad用URLを主表示
- ローカルURLは必要な時だけ表示

## 動作環境

- Windows
- macOS
- Linux

現時点ではGo製のWebサーバーとして動作します。`.exe` / `.app` などの配布パッケージ化は今後の作業です。

## 使い方

```sh
go run ./cmd/imagepadserver
```

起動後、ブラウザで管理画面が開きます。

スマホから操作する場合は、PC画面に表示されるQRコードを読み取ってください。スマホはPCと同じLAN/Wi-Fiに接続されている必要があります。

## ビルド

単体バイナリ:

```sh
go build -o imagepadserver ./cmd/imagepadserver
```

複数OS向け:

```sh
./scripts/build-release.sh
```

生成物は `dist/` に出力されます。

`darwin/arm64` のビルドには Go 1.16 以降が必要です。

## 設定

環境変数で待受ホストとポートを変更できます。

```sh
IMAGEPAD_HOST=0.0.0.0 IMAGEPAD_PORT=8095 go run ./cmd/imagepadserver
```

デフォルト:

- `IMAGEPAD_HOST=0.0.0.0`
- `IMAGEPAD_PORT=8080`

## URLの考え方

ImagePadServer は用途別にURLを分けます。

- スマホ操作用URL: `http://192.168.x.x:8080/`
- ImagePad用公開URL: `https://xxxxx.trycloudflare.com/image/current.jpg?v=...`
- ImagePad用ローカルURL（トンネル未接続時）: `http://192.168.x.x:8080/image/current.jpg?v=...`
- 画面プレビュー用URL: `http://192.168.x.x:8080/image/current.jpg?v=...`

Cloudflare Tunnel に接続できた場合は、外部の人にも見える公開HTTPS URLを主表示します。Windowsで `cloudflared` が見つからない場合は、初回起動時に `%APPDATA%\ImagePadServer\bin\cloudflared.exe` へ自動取得します。

Note: `trycloudflare.com` domains may be blocked by some antivirus products,
corporate security tools, DNS filters, or network policies. If the generated
ImagePad URL opens on one device but not another, check whether
`*.trycloudflare.com` or `cloudflared.exe` is being blocked.

UPnPに失敗した場合、外部URLは表示されません。その場合でも同一LAN内のスマホ操作やローカル画像配信は利用できます。

## VRChat側の注意

VRChat の `VRCImageDownloader` には制限があります。

- 画像は最大 `2048 x 2048`
- 画像読み込みはワールド全体で一定間隔に制限される
- 許可されていないURL/ドメインは、ユーザー側で `Allow Untrusted URLs` が必要になる場合がある

VRChat のログに `Insecure connection not allowed` が出る場合、`http://` URL が拒否されています。外部共有には Cloudflare Tunnel の公開HTTPS URLを使ってください。トンネル未接続時のローカルHTTPS URLは自分のPC向けのフォールバックです。

## セキュリティ注意

UPnPが成功すると、指定ポートがインターネット側から到達可能になる場合があります。

このアプリは画像アップロードと画像配信に用途を絞っていますが、公開ネットワーク上で動かす場合は以下に注意してください。

- 信頼できる環境でのみ起動する
- 不要になったらアプリを終了する
- ルーターのUPnP設定を理解したうえで使う
- 知らない人へ管理画面URLを共有しない

## 開発

テスト:

```sh
go test ./...
```

整形:

```sh
gofmt -w cmd internal
```

## 今後の予定

- Windows向け `.exe` 配布
- macOS向け `.app` 配布
- Linux向けAppImage等の配布
- Windows SteamVR オーバーレイ連携
- ImagePad向けURLコピー体験の改善
- UPnP失敗時の診断表示改善

Windows/SteamVR 実機で作業する場合は [docs/WINDOWS_STEAMVR_HANDOFF.md](docs/WINDOWS_STEAMVR_HANDOFF.md) を参照してください。

## ライセンス

MIT License

## Version / Copyright / Open Source Notices

- Version: `v1.0.0-rc.1`
- Author: Akat / 赤月さん
- Copyright: Copyright (c) 2026 Akat / 赤月さん
- License: MIT License

Open source notices are listed in `NOTICE.md` and are also shown in the browser UI under "Open source notices".
