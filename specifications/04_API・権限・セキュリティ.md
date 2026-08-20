# 04. API・権限・セキュリティ

## 4.1 権限モデル

### 管理API

`internal/server/server.go` の `s.admin(...)` で保護される。次を満たす必要がある。

- loopbackからのアクセスは、ローカル管理ホスト宛てに限り許可。
- private/LAN、link-local、Tailscale CGNAT等からのアクセスは、許可された管理ホストと有効な管理トークンが必要。
- Cloudflare Tunnel等の公開ホストから管理GUI・管理APIへ入る経路は拒否する。
- トークンはクエリ `token`、`X-ImagePad-Token`、または管理Cookieから取得し、定数時間比較する。
- QRはLAN利用のためのトークン付き導線であり、QR・トークンを外部へ公開しない。

### 公開メディア

`/image/current*`, `/video/current.mp4`, `/stream/*`, `/pub/*` 等は、VRChat・外部プレーヤーが取得できる必要がある。`publicReadAllowed` はプライベートネットワークだけでなく、公開トンネルからのメディア取得を許可する。一方、公開メディア経路を管理APIの認証代替にしてはならない。

### ペアリング済みデバイス

BrowserRelayStreamer用のペアリングはLAN内要求に限り、短い有効期限のPINとnonceを使う。確認試行は上限付きで、成功後に `obs-relay` スコープのデバイス資格情報を発行する。デバイス資格情報はOBSリレー設定取得に限定し、管理トークン・一般管理API・公開範囲変更権限を与えない。

## 4.2 実装済みHTTPエンドポイント

以下は `Server.Register` に登録されている現行経路である。ほぼ全ての `/api/*` は管理権限が必要で、ペアリング要求・確認とOBS relay-configは専用認証で処理される。

### 管理・状態・設定

| メソッド/経路 | 用途 |
|---|---|
| GET `/` | 管理GUI |
| GET `/healthz` | ヘルスチェック。正常時 `ok` |
| GET `/api/state` | GUI状態 |
| GET `/api/events` | 状態イベント |
| POST `/api/clear` | 現在メディアのクリア |
| POST `/api/quit` | アプリ終了要求 |
| POST `/api/tunnel/reconnect` | Tunnel再接続 |
| GET/POST `/api/about`, `/api/update-check` | バージョン・更新確認 |
| GET `/api/ffmpeg` | FFmpeg状態 |
| POST `/api/video-player` | ビデオプレーヤーモード |
| POST `/api/music-mode` | 音楽モード |
| GET/POST `/api/video-quality` | 画質・プレイリスト方式設定 |
| POST `/api/encoder-mode` | エンコーダー設定 |
| POST `/api/network-check` | ネットワーク測定 |
| GET `/qr/phone.png` | スマホ接続QR |

### 取り込み・履歴

| メソッド/経路 | 用途 |
|---|---|
| POST `/api/upload` | ファイル取り込み |
| POST `/api/upload-queue` | ファイルの変換キュー投入 |
| POST `/api/upload-url` | URL取り込み |
| POST `/api/upload-url-queue` | URLの変換キュー投入 |
| POST `/api/browser-media-candidates` | ブラウザメディア候補解析 |
| GET `/api/history` | 履歴取得 |
| POST `/api/history/favorite` | お気に入り切替 |
| POST `/api/history/queue` | 履歴の変換キュー投入 |
| POST `/api/history/select` | 履歴を現在対象へ選択・復元 |
| POST `/api/history/publish` | 履歴公開状態切替 |
| POST `/api/copy-url` | GUIのコピー対象URL更新 |
| GET `/history/...` | 管理権限付き履歴ファイル取得 |

### ペアリング・yt-dlp補助機能

| メソッド/経路 | 用途 |
|---|---|
| POST `/api/pairing/request` | BrowserRelayStreamerのペアリング開始。LAN内要求に限定 |
| POST `/api/pairing/confirm` | PIN/nonceを確認し、限定スコープのデバイス資格情報を発行 |
| GET/POST `/api/ytdlp/cookies` | yt-dlp Cookieの保存状態確認・削除等 |
| POST `/api/ytdlp/login` | ブラウザ経由のyt-dlpログインCookie取得 |
| POST `/api/ytdlp/channel` | yt-dlpチャンネル・取得可否診断 |

上記のyt-dlp経路は管理権限で保護し、Cookieの実値をJSON応答やログへ含めない。ペアリング経路は管理トークンを発行せず、成功後の資格情報を `obs-relay` スコープへ限定する。


### OBS・音楽

| メソッド/経路 | 用途 |
|---|---|
| POST `/api/obs/start`, `/api/obs/end` | OBS受信開始・終了 |
| GET/POST `/api/obs/key` | OBSキー状態・更新 |
| GET/POST `/api/obs/latency` | OBS方式設定 |
| GET/POST `/api/obs/relay-config` | ペアリング済みリレー用設定 |
| GET/POST `/api/music/playlist` | プレイリスト状態 |
| POST `/api/music/playlist/add`, `/remove`, `/reorder` | 項目操作 |
| POST `/api/music/playlist/start`, `/play`, `/pause`, `/seek`, `/next`, `/stop` | 再生操作 |
| POST `/api/music/playlist/options` | ループ・シャッフル等 |
| GET `/api/music/playlist/artwork` | アートワーク |
| GET/POST `/api/music/playlists`, `/load`, `/delete` | 保存済みリスト |
| POST `/api/music/playlist/gpu-evaluation/start` | GPU評価経路 |

### 公開メディア

| 経路 | 用途 |
|---|---|
| `/image/current`, `/image/current.png`, `/image/current.jpg` | 現在画像 |
| `/video/current.mp4` | 現在MP4 |
| `/stream/current.m3u8` | 互換・旧形式のHLS |
| `/stream/{id}/{playlist-or-segment}` | ID付きHLS |
| `/radio/{...}` | 音楽ラジオHLS |
| `/pub/{...}` | 公開項目 |
| `/assets/fonts/`, `/favicon.ico`, `/app-icon.png` | UI静的資産 |

## 4.3 API応答の不変条件

- 成功した取り込みは、状態API、履歴API、必要な公開URLのIDが同じメディアを指す。
- 失敗した外部取得・変換は、公開中の別メディアを不用意に破壊しない。ただしURL再指定時に旧公開状態をクリアしてから処理する経路があるため、UIにはその状態を明示する。
- playlist URLを返した時点で、同一IDのplaylistと必要なセグメントが取得可能である。
- 管理プレビューのURLは同一オリジンへ正規化でき、現在ページの管理トークンが必要な場合は引き継ぐ。
- 履歴リンクのコピー対象は内部識別子単体ではなく、公開ベースURLと `/pub/{内部ID}` からなる完全なHTTP(S) URLとする。トンネル接続中はトンネルURL、未接続時はLAN URLをベースにし、管理トークンを付加してはならない。
- 公開URL、ローカル管理URL、HLS URLのラベルとコピー対象を混同しない。

## 4.4 セキュリティ要件

- 管理トークン、OBSキー、ペアリング秘密値、yt-dlp Cookieをログ・仕様書・エラー表示へ出力しない。
- 外部URL取り込みはサイズ制限、許可するURL処理、応答のContent-Length、ストリーム超過を検査する。
- ファイル名、メディアID、HLSセグメント名はパストラバーサルを許さず、安全な名前へ正規化する。
- 外部ツール作業ディレクトリから実行ファイルを無条件に公開メディア領域へ置かない。
- UDP discoveryは発見であり認証ではない。
- 公開メディアが読めることと、管理画面が公開されることは別である。
- ペアリングPINからOBSキーを直接導出しない。

## 4.5 API仕様書群との扱い

`docs/BROWSER_RELAY_STREAMER_API.md` は外部連携の設計・契約資料として有効だが、例示バージョンは現行アプリ版と一致しない場合がある。本章の `Server.Register`、`pairing.go`、認証実装、受け入れテストを現行動作の根拠とする。API例に認証情報を実値で記録してはならない。
