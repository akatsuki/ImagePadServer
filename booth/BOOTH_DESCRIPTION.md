# ImagePadServer BOOTH 掲載用テキスト

## 商品名案

ImagePadServer - VRChat向け画像・動画URL配信ツール

## 短い説明

VRChatのImagePadや動画プレーヤーへ、PC・スマホから画像や動画をすばやく送るためのWindows向け補助アプリです。ブラウザ画面からアップロードすると、VRChatで読み込むためのURLを発行・コピーできます。

## 商品説明

ImagePadServerは、VRChatワールド内のImagePad系ツールやビデオプレーヤーへ、画像・動画を手軽に渡すためのローカル補助アプリです。

PCでアプリを起動するとブラウザUIが開きます。画像をアップロードするとImagePad向けURLを発行し、ビデオプレーヤーモードを有効にすると、静止画や動画をVRChatの動画プレーヤーで扱いやすいHLS URLとして配信できます。

スマホ接続用のQRコードも表示されるため、PCと同じLAN/Wi-Fiにいるスマホから画像や動画を選んで送信できます。

## 主な機能

- PCブラウザから画像をアップロード
- スマホからQRコード経由で画像・動画をアップロード
- ImagePad向けURLを発行してコピー
- ビデオプレーヤーモードでHLS URLを発行
- FFmpegによる動画変換・HLS配信
- yt-dlp連携による動画サイトURLの取得・再配信
- Cloudflare TunnelによるHTTPS公開URLの発行
- 画質プリセット: Auto / 1080p / 720p / 360p
- 初回起動時・手動操作によるアップロード帯域チェック

## 使い方

1. ImagePadServerを起動します。
2. ブラウザUIが開きます。
3. 画像または動画をアップロードします。
4. 表示されたURLをVRChatのImagePadまたは動画プレーヤーへ貼り付けます。

スマホから使う場合は、画面左側のQRコードを読み取ってアップロード画面を開きます。

## ビデオプレーヤーモードについて

状態パネルの「ビデオプレーヤー対応」をオンにすると、コピー対象URLがHLS優先になります。

動画ファイルをアップロードした場合は、FFmpegで変換しながらHLSとして配信します。動画サイトURLを指定した場合は、yt-dlpで取得してからHLSへ変換・再配信します。

VRChat内でHLSを再生する場合は、AVPro系の動画プレーヤー利用をおすすめします。Unity Video PlayerではHLSやライブ系URLがうまく再生できない場合があります。

## 動作環境

- Windows 10 / 11
- VRChat
- ImagePad系ツール、またはVRChatワールド内の動画プレーヤー
- PCとスマホを同じLAN/Wi-Fiで使う場合は、同一ネットワーク接続

FFmpeg、yt-dlp、cloudflaredは、必要に応じてアプリのローカルフォルダへ自動配置されます。

## 注意事項

- VRChat側で `Allow Untrusted URLs` が必要になる場合があります。
- 公開URLはVRChatが画像・動画を取得するために外部から読める必要があります。
- Cloudflare TunnelのURLは、ネットワークやDNSフィルタによってブロックされる場合があります。
- 動画サイト側の仕様変更により、yt-dlpによる取得が失敗する場合があります。
- 年齢制限、地域制限、ログイン必須、メンバー限定などの動画は取得できない場合があります。
- SteamVR連携は現在、無期限凍結中です。

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

