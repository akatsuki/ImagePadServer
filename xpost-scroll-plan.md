# X投稿の読み上げ連動スクロール

2026-10-03。会話で提示した固定枠・本文のみスクロール・短文静止・引用内スクロールを利用者が「任せる」で承認。実装・検証まで進める。共有変更を保護し、commit/push/releaseや稼働中アプリの停止は行わない。

## 採用設計

- 元投稿1画面と引用1画面を用意し、本文の長さによる改ページをやめる。外枠・作者・日時・文字サイズは固定。引用がある場合も元投稿から引用読み上げへの移行で枠/引用ボックスの位置を保持する。
- 本文は段落・リンクの色を保って固定フォントで一度だけ描画する。最大1024px高のタイルへ分け、長文がGPU texture寸法を超えないようにする。本文viewportに交差するタイルだけをwgpuのUVで切り出す。
- VOICEVOXの文/句ごとのqueryをまとめて1回のsynthesisへ渡す。各句の音素時間、無音、話速、engineの端数丸めと実WAV長から時刻cueを作る。語単位の強制アラインメントではなく句単位の追従。読み上げ中の位置をviewport下部より上へ保ち、必要なときだけ滑らかに動かす。
- 文字数のみで全体のスクロール速度を決めない。URL/IDは従来どおり読まない。短文とスクロール停止中は静止frame再利用を保持する。引用の親本文は読み終わった表示を維持する。
- CoverFlow退場用に読み終わりのカード/パネルを一度だけ焼き付ける。横メディアの全画面・縦1件の2画面・ライト/ダークを保持。動画エンコーダ/終端EOF/検証は変更しない。

## 固定するデータ契約

`xpostmodel.SpeechCue`: `StartRune, EndRune int`（SpeechTextのUnicode rune境界）、`StartSeconds, EndSeconds float64`（最終WAV基準）。`Speech.Cues []SpeechCue`。既存`Synthesize`互換を保持し、新規`SynthesizeTimed`をexportから使う。

`ScrollTile`: `Path string`, `Top, Height int`。`ScrollLine`: `StartRune, EndRune int`, `Top, Bottom float64`。`ScrollView`: `X,Y,Width,Height,ContentHeight int`, `Tiles []ScrollTile`, `Lines []ScrollLine`。`Page.CardScroll, PanelScroll *ScrollView`と`CardEndPath, PanelEndPath string`を追加。位置は元の1920x1080/960x1080画像基準で、codec/timelineで出力寸法へ変換する。

`xpostvideo.Draw.UV`, `xpostgpu.Layer.UV`: `*[4]float32`（u0,v0,u1,v1、nilは全画像）。Rust Layerはdefault付き`Option<[f32;4]>`。UVが有限で0..1内・始点<終点であることをGo/Rust両側で検査する。

## 依存付き実行計画

### T1: データ契約と計画レビュー
- **depends_on**: []
- **location**: `internal/xpostmodel/model.go`, 本計画
- **description**: 共通契約を追加し、短いread-onlyレビューの指摘を主担当が判断する。
- **validation**: 既存modelと直接利用packageのコンパイル、計画の境界条件確認。
- **ownership**: main
- **status**: complete
- **log**: 共通model追加、短いread-onlyレビュー4件を境界契約へ反映。Speechにsliceが増えたため既存のゼロ値検査をDeepEqualへ変更し、全フィールドを検査する意味を保持。

### T2: VOICEVOXの時間cue
- **depends_on**: [T1]
- **location**: `internal/xposttts/client.go`, 新規timing補助/テスト
- **description**: 句を自然な区切りでqueryし、accent_phrasesを1回のsynthesisへまとめ、実音声時刻cueを返す。空本文/空モーラ、pause設定、疑問形、話速、cancel、API failureを扱う。Synthesizeの旧利用契約を保持する。
- **validation**: RED→GREENで不均等な音素長/話速/無音を検査。本文を分割しても欠落・重複しない。実VOICEVOXはmainが検証する。
- **ownership**: voice worker（起動/インストール/保存設定/実engine操作はmain）
- **status**: complete
- **log**: SynthesizeTimedを新設。句queryを1合成へ結合し、実WAV基準cueへ変換。音素長/休止/話速/疑問形/18:00を検査。null/過大時間を拒否。37件通過、実VOICEVOXの6 cueを生成。

### T3: GPUのUV切り出し
- **depends_on**: [T1]
- **location**: `internal/xpostgpu/*`, `gpu/xpost-compositord/src/protocol.rs`, `scene.rs`とテスト
- **description**: Optional UV範囲を頂点へ適用する。nilの従来表示と合成/更新/配色を保持する。renderer本体のreadbackは変更しない。
- **validation**: Go/Rust RED→GREEN、不正範囲拒否、UVなし互換、実wgpuの色付き帯画像で正しい切り出しpixelを確認（nativeはmain）。
- **ownership**: GPU worker
- **status**: complete
- **log**: Go/Rust optional UVを追加。Go/Rust RED→GREENと実wgpuの帯pixel切り出しを通過。既存full UV/両テーマ背景を保持。

### T4: 固定枠・本文タイル・読み上げ位置マップ
- **depends_on**: [T1]
- **location**: `internal/xpostimage/video.go`, 新規scroll renderer/テスト
- **description**: 1作者1画面へ切替。作者/枠/引用headerを固定し、本文のみタイル化する。URL/mentionを含む表示位置とSpeechTextのrune位置を対応させ、全本文とリンク表示を保持。元投稿と引用の枠座標を揃える。静止画用rendererは保持する。
- **validation**: 同じ外枠、短文静止、長文/引用の本文保持、emoji/entityのUTF-16検査維持、タイル高さ/メモリ上限、両テーマPNGを確認。
- **ownership**: main
- **status**: complete
- **log**: 元投稿1画面/引用1画面、固定枠・固定フォント、本文1024px tileとSpeech rune対応を実装。長文が複数ページになるREDを再現後GREEN。親終了と引用開始はpixel-identical、本文/リンク/emoji保持と両テーマ固定geometryを確認。

### T5: タイムライン/codec/export接続
- **depends_on**: [T2,T3,T4]
- **location**: `internal/xpostvideo/*`, `internal/xpostcodec/render.go`, `internal/xpostexport/run.go`
- **description**: 音声cueから表示行とスクロールoffsetを求める。frameごとの文字描画なし。止まる区間の静止cache、最終表示のCoverFlow/portrait panel連続性を保持し、UV付きbody tilesを登録する。
- **validation**: 不均等cueで読んでいる行が見える、スクロール単調/境界内、短文・静止cache・CoverFlow continuity、旧plan互換、native encode/フレーム/clock/slice検査。
- **ownership**: main
- **status**: complete
- **log**: cue時刻から必要な行だけsmoothstepで移動、pause/static cache・終端card/panelを維持。独立レビューの欠落cue/終了画像/viewport外/過大sample clockをRED→GREENで修正。640x1080 panel scaleとsingle portrait保持も検査。実GPU/codecへUVとtileを接続。

### T6: 実投稿/fixtureのnative検証と開発build
- **depends_on**: [T5]
- **location**: `docs/verification/xpost-video/`, 開発build・opt-in native tests
- **description**: 問題の投稿2106219507274731847で実取得/実VOICEVOX/1080p生成。引用と非常に長い本文fixture、短文、ライト/ダーク、縦/横を確認。CPU20 workerを使い、合算VRChat成功とは区別する。
- **validation**: Go関連回帰、Node取得回帰、Rust全テスト、wgpu pixel、完成MP4全decode/frame数/clock/1-slice/AAC/HLS、見た目、開発runtime SHA。ユーザーへ動作するartifactを渡す。
- **ownership**: main
- **status**: complete
- **log**: 関連Go716/Rust10/Node12件通過、実GPUの切り出し/背景/旧pixelを確認。引用縦2画面と短文の両テーマ4本、問題の実投稿1080p/30fps/1675framesを生成。全decode/clock/1-slice/AAC/HLSを通過。最終開発exe SHAはscrolling.json。実GUI公開・VRChat・他OSは未確認、稼働中アプリと保存設定を変更せず。

Wave 1: T1。Wave 2: T2/T3を並列、mainはT4。Wave 3: T5。Wave 4: T6。workerへcommit禁止・他者編集を戻さないこと・ファイル所有範囲を明記する。

## レビューで明確化した境界

- ScrollLineのrune位置は常にそのPage.SpeechTextの境界。表示だけのURL/mention/空行は音声cursorを進めない。表示本文の音声化と本来のSpeechTextの正規化結果を照合し、対応しない文字は黙って飛ばさずエラーにする。cue中の位置はその実音声区間内で補間し、対応行を求める。末尾や無音は最終状態へclampする。
- `Page.Quoted`がScrollViewの所有本文を指定する。元投稿Pageは親の本文、引用Pageは引用ボックス内の本文をactive viewportにする。他方の本文はその時点の静止画像に含める。引用Pageの開始画像は「親の最終offset + 引用の初期offset」で、親Pageの終了画像と同じgeometry/内容。引用Pageの終了画像は親・引用とも最終offsetを含む。
- タイル幅は固定viewport幅（最大1352px）、1タイル高は最大1024px。両view合算RGBA上限128MiB/Page、全scroll上限256MiB、native既存総512MiB/assets4096の検査を維持。上限を超える本文は切り捨てず明示エラー。タイルはジョブのowned outDir内だけに作り、既存ジョブcleanupへ従う。
- 新しいscroll Pageはメディア表示中に4秒周期でページを切り替えず、読み終わった最後のパネルを保持する。CoverFlow/slideは終了画像を利用する。metadataのない旧Pageは旧timeline互換を維持する。

参照: 実ENGINEのOpenAPIと固定0.25.2のtts_engine.py（音素フレーム93.75Hz、pause/speed、疑問形）。wgpuは既存0.20.1のtexture/vertex経路を使い、依存の更新はしない。
