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
- UPnP成功時はグローバルIPのImagePad用URLを主表示
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
- ImagePad用外部URL: `http://<global-ip>:8080/image/current?v=...`
- ImagePad用ローカルURL: `http://192.168.x.x:8080/image/current?v=...`

UPnPに成功し、ルーターからグローバルIPを取得できた場合は、外部URLを主表示します。

UPnPに失敗した場合、外部URLは表示されません。その場合でも同一LAN内のスマホ操作やローカル画像配信は利用できます。

## VRChat側の注意

VRChat の `VRCImageDownloader` には制限があります。

- 画像は最大 `2048 x 2048`
- 画像読み込みはワールド全体で一定間隔に制限される
- 許可されていないURL/ドメインは、ユーザー側で `Allow Untrusted URLs` が必要になる場合がある

ImagePadServer はローカルPC上で動くため、VRChat側では未信頼URLとして扱われる可能性があります。

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
