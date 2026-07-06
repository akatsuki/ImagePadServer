# ImagePadServer BOOTH 掲載用テキスト

## 商品名案

ImagePadServer - VRChat向け画像・動画・音楽URL配信ツール

## 短い説明

VRChatのIMAGE PADやIwaSyncなどの動画プレーヤーへ、PC・スマホから画像・動画・音楽をすばやく送るためのWindows向け補助アプリです。アップロードするとVRChatに貼り付けるためのURLを発行し、必要な変換や公開URLの用意までまとめて行います。

## 商品説明

ImagePadServerは、VRChatのIMAGE PADやIwaSyncなどの動画プレーヤーへ、PC・スマホから画像や動画・音楽をすばやく送るためのWindows向け補助アプリです。

PCで起動するとブラウザUIが開きます。画像をアップロードするとImagePad向けURLを発行し、動画や音楽をアップロードするとHLS URLとして配信できます。スマホ連携用のQRコードも表示されるため、PCと同じLAN/Wi-Fiにいるスマホから写真や動画を選んで送れます。

基本無料です。

金など払わんでいい。
払いたいものだけ払えばいい。

おかねほちい...（正直者）

## 主な機能

- PCブラウザから画像をアップロード
- スマホからQRコード経由で画像・動画をアップロード
- VRChatのImagePad向けURLを発行
- ビデオプレーヤーモードでHLS URLを発行
- FFmpegによる動画変換・HLS配信
- yt-dlp連携による動画サイトURLの取得・再配信
- 音楽モード: 音楽ファイルやSoundCloud URLをアップロードすると、オーディオビジュアライザー動画を自動生成して配信
- Cloudflare TunnelによるHTTPS公開URLの発行
- OBS配信のリアルタイム中継
- FFmpeg / yt-dlpを自動ダウンロード・同梱（手動インストール不要）
- 画質プリセット: Auto / 1080p / 720p / 360p

## 使い方

1. ImagePadServerを起動します。
2. ブラウザUIが開きます。
3. 画像または動画・音楽をアップロードします。
4. 表示されたURLをVRChatのImagePadまたは動画プレーヤーへ貼り付けます。

超簡単。スマホ連携。

1. スマホを出すじゃろ
2. QRコードを読むじゃろ
3. 写真や動画を選択
4. 公開ボタンを押せばリンクが勝手にコピーされるぞ！
5. 好きなワールドに貼り付けるのじゃ

## 公式サイト

https://akatsuki.github.io/ImagePadServer/

## 推奨環境

- OS: Windows 10 / 11（Mac/Linux版は公式サイトから）
- CPU: VRChatが快適に動作する環境より少し余裕を持たせて
- Memory: 24GB以上推奨（VRChatと食い合うぞ）
- Network: アップロード帯域30Mbps以上
- その他: PCとスマホを同じLAN / Wi-Fiに接続できる環境

わけわからないセキュリティ入れてるとたまに動かないぞ。

## VRChatで動画を再生する場合の注意

- VRChat内でHLSを再生する場合は、AVPro系の動画プレーヤー利用をおすすめします。
- Unity Video PlayerではHLSやライブ系URLがうまく再生できない場合があります。
- 公開インスタンスでCloudflare TunnelのURLを使う場合、ワールド側のVideo Player Allowed Domainsにドメイン追加が必要になる場合があります。
- Cloudflare Tunnelの一時URLは起動ごとに変わる場合があります。

## アップデート履歴

### 2026-07-06: v1.5.8

OBS配信のURL発行をRTSP用とHLS用に分割。RTSPモード中に表示だけHLS URLのまま残る問題を修正。

### 2026-07-06: v1.5.7

URL発行エンジンをモード別に整理。動画から静止画、静止画から動画へ切り替えた際に、古いHLS URLがコピー対象に残らないように修正。

### 2026-07-05: v1.5.6

ミュージックアップロードのシングルモードを復活。動画と同じ変換オプションから音楽モードのバックエンドへ接続できるように調整。設定画面の整理とUI表示の細部を修正。

### 2026-06-24: v1.4.6

アルバムアートワークの多形式対応（JPEG・WebP・BMP・GIF・TIFF）。プローブ処理の安定性改善。

### 2026-06-23: v1.4.5

動画・音楽ダウンロードの並列化で高速化。VODエンコード効率改善。

### 2026-06-22: v1.4.0

ウェブUIに終了ボタン追加。FFmpegの取得を高速化。

### 2026-06: v1.3.2

FFmpeg / ffprobe / yt-dlpを同梱。初回起動時に自動ダウンロードするため手動インストール不要に。

### 2026-06: v1.3.0

音楽モード追加。音楽ファイル・SoundCloud URLをアップロードするとスペクトラムアナライザーとアルバムアートを背景にしたビジュアライザー動画を自動生成して配信。ラウドネス正規化（-14 LUFS）対応。

### 2026-05-26: v1.2.0

OBS配信に対応。

### 2026-05-24: v1.1.0

動画プレーヤーモード、HLS配信、動画サイトURL対応、画質プリセット、Cloudflare Tunnel連携などに対応。

## 同梱・ライセンス

Author: Akat / 赤月さん  
License: MIT License

依存ライブラリや同梱ツールのライセンス表記は、アプリ内のOpen source noticesおよび同梱NOTICEをご確認ください。

## サポートについて

不具合報告時は、以下の情報があると確認しやすいです。

- 使用しているVRChatワールド・動画プレーヤー名
- ImagePad向けか、ビデオプレーヤー向けか
- 表示されたエラー文
- アップロードしたファイル形式
- 動画サイトURLを使った場合は、そのサービス名

## スクリーンショット掲載順

1. `booth/screenshots/01-hero-icon.png`
   アイコン中心の訴求画像。BOOTHの1枚目推奨。
2. `booth/screenshots/00-cover.png`
   機能概要の説明画像。
3. `booth/screenshots/01-main-imagepad-mode.png`
   ImagePad向け通常モード。
4. `booth/screenshots/02-video-player-mode.png`
   ビデオプレーヤーモード。
5. `booth/screenshots/03-link-upload.png`
   画像・動画URL入力画面。
6. `booth/screenshots/04-mobile-view.png`
   スマホ幅のアップロード画面。
