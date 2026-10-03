# VOICEVOX実行環境の自動準備

2026-10-03。利用者指定: このアプリ用にVOICEVOXをインストールし、FFmpeg同様に起動時に用意する。既存X映像化の実装依頼の追加範囲として実行する。自動起動しないという以前の初期範囲をこの指示で置き換える。

方式: 現在のHTTPクライアントに接続するため、CORE・辞書・モデルを含む公式ENGINE CPU版0.25.2を固定URL/SHA256で取得。VVPPは公式build workflowでZIPとして生成されるためGo標準archive/zipで展開する。アプリ専用toolsディレクトリへatomic導入し、専用loopbackポートで起動する。GPUや外部VOICEVOXの設定は変更しない。進捗/失敗/再試行をX入力欄下へ表示し、音声なしジョブはエンジン待ちを要求しない。

所有: startup/stateful install/process lifecycleはAGENTSに従い主担当のみ。独立read-onlyレビューをsubagentへ渡す。shared checkoutの無関係な変更を保護し、commit/push/release/既存配信停止はしない。

## タスクと依存

| id | depends_on | 内容と所有ファイル | 検証 | 状態 |
|---|---|---|---|---|
| T0 | [] | 固定公式asset/SHA/ZIP/CLI契約、計画レビュー。主担当+read-only reviewer | primary source、レビュー指摘を確認 | 完了 |
| T1 | [T0] | `internal/voicevoxruntime` installer。主担当 | SHA失敗/ZIP traversal/symlink/collision/容量上限、partial cleanup/crash lock回収/再利用/cancel | 完了 |
| T2 | [T1] | 同package owned process manager。主担当 | startup/ready/exit/error/stop/cancel/retry競合のRED→GREEN、race18件・競合10反復、native API/synthesis | 完了 |
| T3 | [T2] | `internal/server/voicevox_runtime.go`, `xpost_voice.go`, UI、`internal/app/app.go`。主担当 | startup wiring、準備中表示/再試行、voice一覧/試聴、外部同port維持、既存fixture回帰。xpost_videoは音声なしで待たない既存worker契約を維持 | 完了 |
| T4 | [T3] | 利用案内/仕様/検証、専用build。主担当 | 公式ENGINEの取得/再利用と/speakers /audio_query /synthesis、実server preview、12package回帰、race検査/競合反復、cross build/vet/Windows build、50121 listener終了 | 完了 |
| T5 | [T3] | 独立read-only最終review | 同port外部選択、crash staging、Close/Retry競合指摘を修正・再レビュー。実行検査は主担当 | 完了 |

Wave1=T0。Wave2=T1→T2→T3は強く結合しstatefulのため主担当が順次実行。Wave3=T4とT5を並行確認。最短経路を維持し、CORE FFIへの全面置換、VOICEVOX editor、モデルの個別DL/UI、ジョブ横断cacheは追加しない。

T0レビュー反映: async startup、phase/status/retry/cancel、ZIPのpath/symlink/case collision/展開容量/件数上限、空き容量事前確認、全license保持、receiptとrequired runtime files検査。専用127.0.0.1:50121で既存50021を触らず、port conflictは失敗として表示。app lifecycle cancelでowned treeを終了/Waitする。保存された外部engineは維持し、GUIの明示「アプリ内VOICEVOXを使う」で変更できる。新規の初期接続先はアプリ内engine。CPU版は1 synthesis threadで起動し、独立owned engine treeも20% hard capに置くが、export treeとの合算20%保証は実測前に主張しない。unsupported OSはrecoverable error。レビューのlocal backendが利用不可だったため既存reviewerへ切替済み。T0完了、T1へ進む。

参考: https://github.com/VOICEVOX/voicevox_engine/releases/tag/0.25.2 、固定版run.py、engine_manifest.json、build-engine.yml。ZIP全体のlicense/terms/resourcesを保持する。runtime取得はexeへの再配布同梱ではない。

完了記録: `docs/verification/xpost-video/implementation-ledger.md` の追加検証節を参照。`build/xpost-video/imagepadserver.exe` を更新した。実ブラウザ+実VOICEVOX+実X+VRChatの一体E2E、合算CPU20、他OS runtime実行、一般配布資格化はこの追加の合格条件と混同しない。
