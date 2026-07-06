# ImagePadServer トラブルシュート

まず ImagePadServer を最新版に更新してください。古い URL や古い HLS playlist が VRChat 側に残っている場合は、動画や URL を指定し直すだけで改善することがあります。

## VRChat で古い動画が再生され続ける

ImagePadServer は動画ごとに HLS の URL とファイル名を変えています。それでも VRChat 側が古いストリームを持ち続ける場合は、次を試してください。

1. UI の `画像クリア` を押す
2. 数秒待つ
3. 新しい動画や動画 URL を指定する
4. コピーされた HLS URL を VRChat プレーヤーへ入れ直す

## `unable to load video` と表示される

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

## `stream has ended` と表示される

プレーヤーが VOD 化済みの playlist をライブとして扱った場合などに起きることがあります。ImagePadServer は変換中は EVENT、変換完了後は VOD として出力します。

再読み込みしても改善しない場合は、動画を再指定して新しい HLS URL を発行してください。

## スマホから管理画面を開けない

確認すること:

- スマホが PC と同じ LAN / Wi-Fi にいる
- QR コードから開いている
- 手入力 URL ではなく、管理トークン付き URL を使っている
- Windows ファイアウォールがローカル LAN からの接続を許可している

## FFmpeg / yt-dlp / cloudflared の自動取得に失敗する

手動で配置して環境変数を指定してください。

```powershell
$env:IMAGEPAD_FFMPEG="C:\tools\ffmpeg\bin\ffmpeg.exe"
$env:IMAGEPAD_YTDLP="C:\tools\yt-dlp\yt-dlp.exe"
$env:IMAGEPAD_CLOUDFLARED="C:\tools\cloudflared\cloudflared.exe"
.\imagepadserver-v1.5.8-windows-amd64.exe
```

macOS で Homebrew 版を使う場合:

```sh
brew install ffmpeg webp yt-dlp cloudflared
```

## 直接 URL や動画サイト URL の取得に失敗する

動画サイトや SoundCloud は、サイト側の仕様変更や地域制限で失敗することがあります。

確認すること:

- 認証が必要な URL ではないか
- 年齢制限、地域制限、削除済みメディアではないか
- playlist / profile / likes URL など、単一メディアではない URL を指定していないか
- yt-dlp が古くなっていないか

URL がメディアページの場合は yt-dlp のエラーが表示されます。直接ファイル URL の場合は、SSRF 対策のためローカル・プライベートネットワーク宛の URL は拒否されます。

## Cloudflare Tunnel の URL にアクセスできない

確認すること:

- PC がインターネットに接続している
- `trycloudflare.com` がネットワークや DNS フィルタでブロックされていない
- セキュリティソフトが `cloudflared` / `cloudflared.exe` を止めていない
- VRChat 側から公開 URL に到達できる

管理画面は Cloudflare Tunnel 経由では開けません。Tunnel は VRChat がメディアを取得するための公開 URL として使います。

## 設定を変えて起動したい

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
.\imagepadserver-v1.5.8-windows-amd64.exe
```
