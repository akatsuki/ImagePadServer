# ImagePadServer ユーザーガイド

このページは、ImagePadServer を実際に使う人向けの説明です。まず全体の使い方を確認し、必要な機能だけ読んでください。

## 基本の画像フロー

1. ImagePadServer を起動する
2. ブラウザ UI で画像をアップロードする
3. URL が表示されたらコピーする
4. VRChat の ImagePad 系ツールに URL を貼る

画像は VRChat で扱いやすいサイズ・形式へ変換されます。新しい画像をアップロードすると、現在公開中の画像が置き換わります。

## スマホからアップロードする

起動後の UI に QR コードが表示されます。スマホで読み取ると、同じ PC の ImagePadServer にアクセスできます。

スマホは PC と同じ LAN / Wi-Fi に接続してください。手入力 URL ではなく、QR コードに含まれる管理トークン付き URL を使う必要があります。

## ビデオプレーヤーモード

状態パネルの `ビデオプレーヤー対応` をオンにすると、VRChat のビデオプレーヤー向け出力が有効になります。デフォルトはオフです。

オンのとき:

- タブ名が `画像` から `画像/音声/動画` に変わる
- 画像だけでなく音声・動画ファイルもアップロードできる
- コピーされる URL は HLS を優先する
- 動画ファイルは FFmpeg で HLS に変換される
- 音声ファイルは FFmpeg で HLS に変換される
- 動画サイト URL、音声の直接 URL、SoundCloud トラック URL は yt-dlp で取得して HLS として再配信される

HLS URL の例:

```text
https://xxxxx.trycloudflare.com/stream/{video-id}/current-{video-id}.m3u8
```

VRChat では、HLS やライブ系の読み込みは AVPro 系プレーヤーが前提です。Unity Video Player は単純な MP4 直リンク向けと考えてください。

## 音声機能

ビデオプレーヤーモードがオンのとき、音声ファイルのアップロード、音声の直接 URL、SoundCloud トラック URL を扱えます。

音声ファイルは HLS として配信され、ビジュアライザー付きの映像に変換されます。対応形式はアプリに同梱の FFmpeg が認識するものすべてです。

アートワークの優先順位:

1. 音声ファイルに埋め込まれたアートワーク
2. SoundCloud などのプラットフォームから取得した画像
3. 暗色背景に音符マークとビジュアライザー

タイトルは ID3 タグ等のメタデータから取得します。メタデータがない場合は、アップロード時のファイル名または URL の末尾を使います。

## SoundCloud トラック

`soundcloud.com` / `www.soundcloud.com` / `m.soundcloud.com` / `on.soundcloud.com` の単一トラック URL をリンク欄に貼ると、音源を取得してサムネイル背景と波形ビジュアライザー付きの HLS 映像として配信します。

特徴:

- サムネイルがある場合は背景に使う
- サムネイルがない場合は暗色背景にする
- 波形は変換時に HLS 映像へ焼き込む
- 同じ音源・同じ設定なら、同じ位置では同じ波形画像になる
- SoundCloud 専用のキュー項目として管理される

注意点:

- 認証が必要なメディア、年齢制限、地域制限、削除済みトラックは失敗することがあります
- playlist / profile / likes URL は単一トラックとして解決できず失敗します
- yt-dlp 側がサイト仕様変更に追従するまで失敗することがあります
- 長いメディアは変換完了まで時間がかかりますが、HLS は変換しながら出力されます

## HLS の挙動

動画変換中は、FFmpeg が HLS セグメントを作りながら配信します。変換完了を待たずに VRChat 側が読み込みを開始できるように、変換中の playlist は EVENT として出力します。

変換が完了すると、playlist は VOD として確定され、末尾に `#EXT-X-ENDLIST` が追加されます。

古い動画が VRChat 側に残らないように、動画ごとに playlist と segment のファイル名を変えています。動画サイト URL を再指定した場合も、先に現在の公開状態をクリアしてから新しい動画の処理を始めます。

## OBS 遅延モード

OBS から RTMP で受けた映像は、設定 UI の `OBS Latency` で配信方式を選べます。

| モード | 方式 | 配信経路 | 備考 |
|---|---|---|---|
| 通常遅延（HLS） | 標準 MPEG-TS HLS | 既存の HLS 配信 | 最も互換性が高い |
| 低遅延（LHLS, 実験） | community LHLS | FFmpeg の DASH muxer をループバック専用 HTTP sink に流す | 実験的 |
| 超低遅延（LL-HLS, 実験） | Apple LL-HLS | MediaMTX サイドカー経由 | 実験的 |
| リアルタイム（RTSPT, PC専用） | RTSP over TCP | MediaMTX サイドカーの読み出し | ブラウザプレビュー非対応 |

LHLS と LL-HLS は実験的です。実際の遅延は経路・プレーヤー・回線に依存します。各モードは選択した方式のまま配信し、別方式への無言のフォールバックは行いません。

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

## ファイルサイズ制限

アップロードできるファイルの最大サイズは 4 GiB - 1 バイト（4294967295 バイト）です。これを超えるファイルは受け付けられません。

## 公開 URL と Cloudflare Tunnel

ImagePadServer は UPnP による自動ポート開放を使いません。代わりに Cloudflare Tunnel で HTTPS の公開 URL を作ります。

これにより、ルーターのポートフォワーディング設定を手動で行わなくても、VRChat から画像や HLS を取得できます。

注意:

- `trycloudflare.com` がネットワークや DNS フィルタでブロックされることがあります
- セキュリティソフトが `cloudflared` / `cloudflared.exe` を止めることがあります
- 公開 URL は VRChat がメディアを取得するために外部から読める必要があります

## ツールの自動取得

FFmpeg は動画変換と HLS 生成に使います。yt-dlp は動画サイト URL の取得に使います。cloudflared は公開 HTTPS URL の生成に使います。

Windows / macOS では、必要なツールが見つからない場合にアプリ専用のローカルフォルダへ自動ダウンロードします。PATH の変更やシステム領域へのインストールは行いません。Linux では PATH 上のツール、またはアプリのローカル `bin` フォルダを使います。

```text
%APPDATA%\ImagePadServer\bin
~/Library/Application Support/ImagePadServer/bin
~/.config/ImagePadServer/bin
```

macOS で事前に Homebrew 版を使いたい場合:

```sh
brew install ffmpeg webp yt-dlp cloudflared
```

Windows では FFmpeg / yt-dlp / cloudflared を非表示で起動するため、変換中にコマンドプロンプトは表示されない想定です。
