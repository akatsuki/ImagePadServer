# ImagePadServer

ImagePadServer は、PC やスマホから画像・音声・動画をアップロードし、VRChat の ImagePad 系ツールやビデオプレーヤーで読み込める URL を発行するローカル補助アプリです。

画像だけなら ImagePad 向けの軽い URL を発行します。ビデオプレーヤーモードを有効にすると、静止画・音声・動画を HLS として配信できます。

## 配布サイト

公式サイト:

https://akatsuki.github.io/ImagePadServer/

最新版の GitHub Release:

https://github.com/akatsuki/ImagePadServer/releases/tag/v1.5.7

## まず使う

Windows では GitHub Release から `imagepadserver-v1.5.7-windows-amd64.zip` をダウンロードして展開し、exe を起動します。

```powershell
.\imagepadserver-v1.5.7-windows-amd64.exe
```

起動するとブラウザ UI が開きます。スマホからアップロードする場合は、UI に表示される QR コードをスマホで読み取ってください。スマホは PC と同じ LAN / Wi-Fi に接続している必要があります。

macOS / Linux では Go を入れたうえで、ソースから起動できます。

```sh
go run ./cmd/imagepadserver
```

## よく使う流れ

画像を VRChat の ImagePad 系ツールへ渡す場合:

1. ImagePadServer を起動する
2. ブラウザ UI で画像をアップロードする
3. 表示された URL をコピーする
4. VRChat の ImagePad 系ツールに URL を貼る

音声・動画・SoundCloud・OBS 配信を扱う場合は、状態パネルの `ビデオプレーヤー対応` をオンにします。コピーされる URL は HLS を優先します。

HLS URL の例:

```text
https://xxxxx.trycloudflare.com/stream/{video-id}/current-{video-id}.m3u8
```

VRChat で HLS やライブ系 URL を読む場合は AVPro 系プレーヤーが前提です。Unity Video Player は単純な MP4 直リンク向けと考えてください。

## 主な機能

- PC またはスマホから画像・音声・動画をアップロード
- Cloudflare Tunnel で HTTPS の公開 URL を発行
- 画像を VRChat で扱いやすいサイズ・形式へ変換
- 動画ファイル、音声ファイル、動画サイト URL、SoundCloud トラック URL を HLS として再配信
- 音声を波形・スペクトラム付きのビジュアライザー動画として配信
- 取り込み進捗、履歴、お気に入り、変換キュー、サムネイルを表示
- OBS から RTMP で送った映像を HLS / 低遅延系モードで共有
- 画質を `Auto` / `1080p` / `720p` / `360p` から選択
- 最大 4 GiB - 1 バイト（4294967295 バイト）のアップロードに対応

## 詳しいドキュメント

使い方:

- [ユーザーガイド](docs/USER_GUIDE.md)
- [トラブルシュート](docs/TROUBLESHOOTING.md)

開発・設計:

- [開発ガイド](docs/DEVELOPMENT.md)
- [ロードマップ索引](docs/ROADMAP_INDEX.md)
- [アーキテクチャ](docs/ARCHITECTURE.md)

## セキュリティの考え方

ImagePadServer は自分の PC 上で動かすローカルアプリです。管理画面とメディア公開は扱いが違います。

管理画面:

- localhost からのアクセスは許可
- スマホなど LAN 内からのアクセスは QR に含まれる管理トークンが必要
- Cloudflare Tunnel 経由の管理画面アクセスは拒否
- QR コードや管理トークンを公開しないでください

メディア公開:

- VRChat が取得するため、画像・音声・動画の公開 URL は外部から読めます
- 起動時と終了時にメディアワークスペースを初期化します
- メディア保存先はアプリ用データフォルダ配下です

## バージョン

- Version: `v1.5.7`
- Author: Akat / 赤月さん
- Copyright: Copyright (c) 2026 Akat / 赤月さん
- License: MIT License

## ライセンス

MIT License

依存ライブラリや同梱ツールの表記は [NOTICE.md](NOTICE.md) にまとめています。ブラウザ UI の `Open source notices` からも確認できます。
