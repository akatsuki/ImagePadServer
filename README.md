# ImagePadServer

ImagePadServer は、PC やスマホから画像/音声/動画をアップロードして、VRChat ワールド内の ImagePad 系ツールやビデオプレーヤーへ渡せる URL を発行するローカル補助アプリです。

画像だけを使う場合は ImagePad 向けの軽い URL を発行します。ビデオプレーヤーモードを有効にすると、静止画/音声/動画を VRChat の動画プレーヤーで読み込みやすい HLS として配信できます。

## 配布サイト

GitHub Pages の配布サイトは次のURLです。

https://akatsuki.github.io/ImagePadServer/

## できること

- PC から画像/音声をアップロードする
- スマホから QR コード経由で画像/音声/動画をアップロードする
- 現在の画像を VRChat 向け URL として公開する
- Cloudflare Tunnel で HTTPS の公開 URL を作る
- ビデオプレーヤーモード時に HLS URL をコピー対象にする
- 動画ファイルを FFmpeg で変換しながら HLS 配信する
- 音声ファイルを FFmpeg で変換しながら HLS 配信する（対応形式は同梱の FFmpeg が認識するものすべて）
- 動画サイト URL、音声の直接 URL、SoundCloud トラック URL を yt-dlp で取得して HLS として再配信する
- 履歴から画像/音声/動画を再公開する
- よく使う画像/音声/動画をお気に入りに残す
- 画像/音声/動画の変換キューとサムネイルを表示する
- OBS から RTMP で送った映像を HLS として共有する
- 画質を `Auto` / `1080p` / `720p` / `360p` から選ぶ
- 初回や手動操作でアップロード帯域を測定し、Auto 画質へ反映する
- アップロードできるファイルサイズは最大 4 GiB - 1 バイト（4294967295 バイト）

- 詳しいコード構造とロードマップは [docs/ROADMAP_INDEX.md](docs/ROADMAP_INDEX.md) を参照してください。

## まず使う

Windows ではビルド済み exe を起動します。

```powershell
.\dist\imagepadserver-v1.1.1.exe
```

macOS / Linux では Go を入れたうえで、ソースから起動できます。

```sh
go run ./cmd/imagepadserver
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

- タブ名が `画像` から `画像/音声/動画` に変わる
- 画像だけでなく音声/動画ファイルもアップロードできる
- コピーされる URL は HLS を優先する
- 動画ファイルは FFmpeg で HLS に変換される
- 音声ファイルは FFmpeg で HLS に変換される（対応形式は同梱の FFmpeg が認識するものすべて）
- 動画サイト URL、音声の直接 URL、SoundCloud トラック URL は yt-dlp で取得して HLS として再配信される

HLS URL の例:

```text
https://xxxxx.trycloudflare.com/stream/{video-id}/current-{video-id}.m3u8
```

VRChat では、HLS やライブ系の読み込みは AVPro 系プレーヤーが前提です。Unity Video Player は単純な MP4 直リンク向けと考えてください。

## HLS の挙動

動画変換中は、FFmpeg が HLS セグメントを作りながら配信します。変換完了を待たずに VRChat 側が読み込みを開始できるように、変換中の playlist は EVENT として出力します。

変換が完了すると、playlist は VOD として確定され、末尾に `#EXT-X-ENDLIST` が追加されます。

古い動画が VRChat 側に残らないように、動画ごとに playlist と segment のファイル名を変えています。動画サイト URL を再指定した場合も、先に現在の公開状態をクリアしてから新しい動画の処理を始めます。

SoundCloud トラックの HLS は、音声が AAC stereo 48kHz、映像が H.264 / yuv420p で出力されます。ビジュアライザーはスペクトラムバーと波形を表示し、タイトル/アーティスト/アルバム情報を映像にオーバーレイします。波形は FFmpeg の `showwaves` フィルターが音源ファイルを解析して 30fps の映像フレームとして焼き込みます。

## 動画サイト URL と SoundCloud

ビデオプレーヤーモードがオンの状態でリンク欄に URL を貼ると、yt-dlp でメディアを取得し、FFmpeg で HLS に変換して再配信します。

### SoundCloud トラック

`soundcloud.com` / `www.soundcloud.com` / `m.soundcloud.com` / `on.soundcloud.com` の単一トラック URL をリンク欄に貼ると、音源を取得してサムネイル背景＋波形ビジュアライザー付きの HLS 映像として配信します。

- サムネイル（トラック画像）が取得できた場合は、その画像を背景に音声同期の波形がアニメーションします
- サムネイルがない場合は、暗色背景に音符マークと波形が表示されます
- 波形は音源変換時に HLS 映像へ焼き込まれており、再生時にブラウザやクライアント側の Canvas / WebAudio 描画は行いません
- 同じ音源・同じ設定なら、再生開始時刻に関係なく同じ位置では同じ波形画像になります
- SoundCloud 専用のキュー項目として管理され、Publish / Queue / キャンセルは通常動画と同様に操作できます

注意点（動画サイト URL 共通）:

- 認証が必要なメディア、年齢制限、地域制限、削除済みトラックは失敗することがあります
- playlist / profile / likes URL は単一トラックとして解決できず失敗します
- yt-dlp 側がサイト仕様変更に追従するまで失敗することがあります
- 長いメディアは変換完了まで時間がかかりますが、HLS は変換しながら出力されます

## 音声機能

ビデオプレーヤーモードがオンのとき、音声ファイルのアップロード、音声の直接 URL、SoundCloud トラック URL を扱えます。

### 音声ファイルのアップロード

ブラウザ UI から音声ファイルをアップロードできます。対応形式はアプリに同梱の FFmpeg が認識するものすべてです（特定の拡張子は列挙しません）。アップロードされた音声は HLS として配信され、ビジュアライザー付きの映像に変換されます。

### 音声の直接 URL

音声ファイルへの直接 URL をリンク欄に貼ると、yt-dlp で取得して HLS に変換し、ビジュアライザー付き映像として再配信します。

### アートワークの優先順位

音声ファイルに埋め込まれたアートワークが最優先で表示されます。埋め込みアートワークがない場合は SoundCloud 等のプラットフォームから取得した画像が使われます。どちらも取得できない場合は、暗色背景に音符マークとビジュアライザーが表示されます。

### メタデータのフォールバック

ID3 タグ等のメタデータからタイトルが取得できた場合はそれを表示します。メタデータがない場合は、アップロード時のファイル名（拡張子を除く）または URL の末尾をタイトルとして表示します。

### ファイルサイズ制限

アップロードできるファイルの最大サイズは 4 GiB - 1 バイト（4294967295 バイト）です。これを超えるファイルは受け付けられません。

### キューと履歴

音声ファイルのアップロードや URL 指定は、画像や動画と同様にキューで管理され、履歴に残ります。キューから変換状況の確認、履歴から再公開が可能です。

### ビジュアライザー

配信される HLS 映像には、スペクトラムバーと波形のビジュアライザーが含まれます。画面上部にはタイトル、アーティスト、アルバム情報がオーバーレイ表示されます。これらの情報はメタデータから取得されます。

### 検証コマンド

AV-602 の完了後、次のコマンドで README.md の整合性を確認できます。

```sh
rtk grep "2 GB" README.md
rtk git diff --check
```

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

Windows / macOS では、必要なツールが見つからない場合にアプリ専用のローカルフォルダへ自動ダウンロードします。PATH の変更やシステム領域へのインストールは行いません。Linux では PATH 上のツール、またはアプリのローカル `bin` フォルダを使います。

```text
%APPDATA%\ImagePadServer\bin
~/Library/Application Support/ImagePadServer/bin
~/.config/ImagePadServer/bin
```

macOS で事前に Homebrew 版を使いたい場合:

```sh
brew install ffmpeg yt-dlp cloudflared
```

手動でパスを指定する場合:

```powershell
$env:IMAGEPAD_FFMPEG="C:\tools\ffmpeg\bin\ffmpeg.exe"
$env:IMAGEPAD_YTDLP="C:\tools\yt-dlp\yt-dlp.exe"
.\dist\imagepadserver-v1.1.1.exe
```

Windows では FFmpeg / yt-dlp / cloudflared を非表示で起動するため、変換中にコマンドプロンプトは表示されない想定です。

## 公開 URL とポート開放

初心者でも使いやすくするため、ImagePadServer は UPnP による自動ポート開放を使いません。代わりに Cloudflare Tunnel で HTTPS の公開 URL を作ります。

これにより、ルーターのポートフォワーディング設定を手動で行わなくても、VRChat から画像や HLS を取得できます。

注意:

- `trycloudflare.com` がネットワークや DNS フィルタでブロックされることがあります
- セキュリティソフトが `cloudflared` / `cloudflared.exe` を止めることがあります
- 公開 URL は VRChat がメディアを取得するために外部から読める必要があります

## セキュリティ

ImagePadServer は自分の PC 上で動かすローカルアプリです。管理画面とメディア公開は扱いが違います。

管理画面:

- localhost からのアクセスは許可
- スマホなど LAN 内からのアクセスは QR に含まれる管理トークンが必要
- Cloudflare Tunnel 経由の管理画面アクセスは拒否
- QR コードや管理トークンを公開しないでください

メディア公開:

- VRChat が取得するため、画像/音声/動画の公開 URL は外部から読めます
- メディア保存先は Windows では `%APPDATA%\ImagePadServer\media`、macOS では `~/Library/Application Support/ImagePadServer/media`、Linux では `~/.config/ImagePadServer/media`
- 起動時と終了時にメディアワークスペースを初期化する（前回の画像/音声/動画・HLS は次回起動まで残らない）
- 保存先は可能な範囲で制限付き権限にします
- 実行ファイルとして使わせる目的の場所ではありません

## 今後のやること

- 動画をライブ扱いで見るか、変換して配信するかを選択できるようにする
- 動画処理キューのキャンセル・再実行・削除操作を整える
- PC を持たない Quest / Android VRChat ユーザー向けに、サーバー主が一時的な投稿専用 URL を開ける「おすそ分け」機能を追加する
- URL 表示を画像/音声/動画で統合して、表示行数を減らす
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
.\dist\imagepadserver-v1.1.1.exe
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

```sh
go test ./...
```

クロスプラットフォーム確認:

```sh
scripts/build-release.sh
```

Windows PowerShell で exe ビルド:

```powershell
$env:CGO_ENABLED="0"
$env:GOOS="windows"
$env:GOARCH="amd64"
go build -trimpath -ldflags "-H=windowsgui" -o dist\1.2.2\dev\dev1\win\imagepadserver-v1.2.2-dev1-windows-amd64.exe .\cmd\imagepadserver
```

`scripts/build-release.sh` writes builds under `dist/<version>/release/<platform>/` for stable versions and `dist/<version>/dev/<devN>/<platform>/` for dev versions.
For example, `v1.2.2` goes to `dist/1.2.2/release/win/`, while `v1.2.2-dev1` goes to `dist/1.2.2/dev/dev1/win/`.

GitHub Actions also publishes a GitHub Release automatically when you push a `v*` tag.
Dev tags such as `v1.2.2-dev1` are published as `dev-release` prereleases.

## バージョン

- Version: `v1.1.1`
- Author: Akat / 赤月さん
- Copyright: Copyright (c) 2026 Akat / 赤月さん
- License: MIT License

## ライセンス

MIT License

依存ライブラリや同梱ツールの表記は [NOTICE.md](NOTICE.md) にまとめています。ブラウザ UI の `Open source notices` からも確認できます。
