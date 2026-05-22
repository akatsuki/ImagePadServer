# ImagePadServer

ImagePadServer は、PC やスマホから画像・動画をアップロードして、VRChat ワールド内の ImagePad 系ツールやビデオプレーヤーへ渡せる URL を発行するローカル補助アプリです。

画像だけを使う場合は ImagePad 向けの軽い URL を発行します。ビデオプレーヤーモードを有効にすると、静止画や動画を VRChat の動画プレーヤーで読み込みやすい HLS として配信できます。

## できること

- PC から画像をアップロードする
- スマホから QR コード経由で画像・動画をアップロードする
- 現在の画像を VRChat 向け URL として公開する
- Cloudflare Tunnel で HTTPS の公開 URL を作る
- ビデオプレーヤーモード時に HLS URL をコピー対象にする
- 動画ファイルを FFmpeg で変換しながら HLS 配信する
- 動画サイト URL を yt-dlp で取得して HLS として再配信する
- 画質を `Auto` / `1080p` / `720p` / `360p` から選ぶ
- 初回や手動操作でアップロード帯域を測定し、Auto 画質へ反映する

## まず使う

ビルド済み exe を起動します。

```powershell
.\dist\imagepadserver-v1.0.0.exe
```

開発用にソースから起動する場合:

```powershell
go run .\cmd\imagepadserver
```

起動するとブラウザ UI が開きます。スマホからアップロードしたい場合は、UI に表示される QR コードをスマホで読み取ってください。スマホは PC と同じ LAN / Wi-Fi に接続している必要があります。

## 通常の画像フロー

1. ImagePadServer を起動する
2. ブラウザ UI で画像をアップロードする
3. URL が表示されたらコピーする
4. VRChat の ImagePad 系ツールに URL を貼る

画像は VRChat で扱いやすいサイズ・形式へ変換されます。新しい画像をアップロードすると、現在公開中の画像が置き換わります。

## ビデオプレーヤーモード

状態パネルの `ビデオプレーヤー対応` をオンにすると、VRChat のビデオプレーヤー向け出力が有効になります。デフォルトはオフです。

オンのとき:

- タブ名が `画像` から `画像/動画` に変わる
- 画像だけでなく動画ファイルもアップロードできる
- コピーされる URL は HLS を優先する
- 動画ファイルは FFmpeg で HLS に変換される
- 動画サイト URL は yt-dlp で取得してから HLS として再配信される

HLS URL の例:

```text
https://xxxxx.trycloudflare.com/stream/{video-id}/current-{video-id}.m3u8
```

VRChat では、HLS やライブ系の読み込みは AVPro 系プレーヤーが前提です。Unity Video Player は単純な MP4 直リンク向けと考えてください。

## HLS の挙動

動画変換中は、FFmpeg が HLS セグメントを作りながら配信します。変換完了を待たずに VRChat 側が読み込みを開始できるように、変換中の playlist は EVENT として出力します。

変換が完了すると、playlist は VOD として確定され、末尾に `#EXT-X-ENDLIST` が追加されます。

古い動画が VRChat 側に残らないように、動画ごとに playlist と segment のファイル名を変えています。動画サイト URL を再指定した場合も、先に現在の公開状態をクリアしてから新しい動画の処理を始めます。

## 動画サイト URL

ビデオプレーヤーモードがオンの状態でリンク欄に動画サイト URL を貼ると、yt-dlp で動画を取得し、FFmpeg で HLS に変換して再配信します。

注意点:

- 認証が必要な動画、年齢制限、地域制限、メンバー限定動画は失敗することがあります
- yt-dlp 側がサイト仕様変更に追従するまで失敗することがあります
- 長い動画は変換完了まで時間がかかりますが、HLS は変換しながら出力されます

## 画質設定

UI から次の画質を選べます。

- `Auto`
- `1080p`
- `720p`
- `360p`

`Auto` はアップロード帯域だけを見て決めます。帯域チェックでは 40 MB をアップロードして測定します。

現在の目安:

- 12 Mbps 以上: 1080p
- 5 Mbps 以上: 720p
- 5 Mbps 未満: 360p

配信中に画質を変更した場合、解像度は途中変更せず、ビットレートだけを変更対象にします。次回以降の変換では選択した画質が最初から反映されます。

## FFmpeg / yt-dlp / cloudflared

FFmpeg は動画変換と HLS 生成に使います。yt-dlp は動画サイト URL の取得に使います。cloudflared は公開 HTTPS URL の生成に使います。

Windows では、必要なツールが見つからない場合にアプリのローカルフォルダへ自動ダウンロードします。

```text
%APPDATA%\ImagePadServer\bin
```

手動でパスを指定する場合:

```powershell
$env:IMAGEPAD_FFMPEG="C:\tools\ffmpeg\bin\ffmpeg.exe"
$env:IMAGEPAD_YTDLP="C:\tools\yt-dlp\yt-dlp.exe"
.\dist\imagepadserver-v1.0.0.exe
```

Windows では FFmpeg / yt-dlp / cloudflared を非表示で起動するため、変換中にコマンドプロンプトは表示されない想定です。

## 公開 URL とポート開放

初心者でも使いやすくするため、ImagePadServer は UPnP による自動ポート開放を使いません。代わりに Cloudflare Tunnel で HTTPS の公開 URL を作ります。

これにより、ルーターのポートフォワーディング設定を手動で行わなくても、VRChat から画像や HLS を取得できます。

注意:

- `trycloudflare.com` がネットワークや DNS フィルタでブロックされることがあります
- セキュリティソフトが `cloudflared.exe` を止めることがあります
- 公開 URL は VRChat がメディアを取得するために外部から読める必要があります

## セキュリティ

ImagePadServer は自分の PC 上で動かすローカルアプリです。管理画面とメディア公開は扱いが違います。

管理画面:

- localhost からのアクセスは許可
- スマホなど LAN 内からのアクセスは QR に含まれる管理トークンが必要
- Cloudflare Tunnel 経由の管理画面アクセスは拒否
- QR コードや管理トークンを公開しないでください

メディア公開:

- VRChat が取得するため、画像・動画の公開 URL は外部から読めます
- メディア保存先は `%APPDATA%\ImagePadServer\media`
- 保存先は可能な範囲で制限付き権限にします
- 実行ファイルとして使わせる目的の場所ではありません

## 今後のやること

- 動画をライブ扱いで見るか、変換して配信するかを選択できるようにする
- サーバーを閉じるまでの過去ログから再配信できる履歴機能を追加する
- 履歴や現在の動画にサムネイルを生成して表示する
- よく使う画像・動画をお気に入りとして残し、ワンクリックで再配信できるようにする
- 動画処理キューを追加し、変換中の追加投入・キャンセル・次の処理を制御できるようにする
- URL 表示を画像・動画で統合して、表示行数を減らす
- ビデオ配信の画質をネットワーク状況に応じて調整する
- 画質プリセットと Auto 判定をさらに調整する
- SteamVR 対応

## SteamVR 連携

SteamVR 連携は無期限凍結中です。

実装資産は将来の検証用としてリポジトリ内に残していますが、現在のアプリ起動経路、管理 API、状態パネルからは参照しません。

## 設定

主な環境変数:

```text
IMAGEPAD_HOST
IMAGEPAD_PORT
IMAGEPAD_ADVERTISE_HOST
IMAGEPAD_PREFER_TAILSCALE
IMAGEPAD_FFMPEG
IMAGEPAD_YTDLP
```

デフォルト:

```text
IMAGEPAD_HOST=0.0.0.0
IMAGEPAD_PORT=8080
```

ポートを変える例:

```powershell
$env:IMAGEPAD_PORT="8095"
.\dist\imagepadserver-v1.0.0.exe
```

## トラブルシュート

### VRChat で古い動画が再生され続ける

最新ビルドでは、動画ごとに HLS の URL とファイル名を変えています。それでも VRChat 側が古いストリームを持ち続ける場合は、次を試してください。

1. UI の `画像クリア` を押す
2. 数秒待つ
3. 新しい動画や動画 URL を指定する
4. コピーされた HLS URL を VRChat プレーヤーへ入れ直す

### `unable to load video` と表示される

よくある原因:

- HLS segment がまだ生成されていない
- Cloudflare Tunnel がブロックされている
- VRChat 側から `trycloudflare.com` に到達できない
- 古い URL を VRChat 側が保持している
- Unity Video Player で HLS を読もうとしている

AVPro 系プレーヤーで、次の形の URL になっているか確認してください。

```text
https://.../stream/{video-id}/current-{video-id}.m3u8
```

### `stream has ended` と表示される

プレーヤーが VOD 化済みの playlist をライブとして扱った場合などに起きることがあります。ImagePadServer は変換中は EVENT、変換完了後は VOD として出力します。

再読み込みしても改善しない場合は、動画を再指定して新しい HLS URL を発行してください。

### スマホから管理画面を開けない

確認すること:

- スマホが PC と同じ LAN / Wi-Fi にいる
- QR コードから開いている
- 手入力 URL ではなく、管理トークン付き URL を使っている
- Windows ファイアウォールがローカル LAN からの接続を許可している

### FFmpeg / yt-dlp の自動取得に失敗する

手動で配置して環境変数を指定してください。

```powershell
$env:IMAGEPAD_FFMPEG="C:\tools\ffmpeg\bin\ffmpeg.exe"
$env:IMAGEPAD_YTDLP="C:\tools\yt-dlp\yt-dlp.exe"
```

## 開発

テスト:

```powershell
go test ./...
```

Windows exe ビルド:

```powershell
$env:CGO_ENABLED="0"
$env:GOOS="windows"
$env:GOARCH="amd64"
go build -trimpath -ldflags "-H=windowsgui" -o dist\imagepadserver-windows-amd64.exe .\cmd\imagepadserver
```

## バージョン

- Version: `v1.0.0`
- Author: Akat / 赤月さん
- Copyright: Copyright (c) 2026 Akat / 赤月さん
- License: MIT License

## ライセンス

MIT License

依存ライブラリや同梱ツールの表記は [NOTICE.md](NOTICE.md) にまとめています。ブラウザ UI の `Open source notices` からも確認できます。
