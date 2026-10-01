# ニコニココメント・タイムラインGPU合成設計

日付: 2026-09-24 / 状態: T0–T6の実装と主要package検証は完了。T7のRTX 5070 Ti/Vulkan 360-frame GPU位置readbackと共有透明領域exactnessはPASSだが、1280×720/30fpsの360-frame画素比較はmax差2・許容差1超過352pxでFAIL。GPU `textureSample`線形補間案はframe62で9px超過となり、4-tap手動補間より悪化して破棄。T8のMP4-only direct NVENC/ring3 paired runはコメントなし中央値1.087秒、timeline中央値3.445秒、paired差2.358秒で4秒基準を満たす。一方、MP4+HLS teeを出すreal workerはNVENC/ring3中央値4.215秒、x264/ring2 4.720秒で基準未達。benchmark wrapperの古い3秒thresholdを4秒へ修正し、raw direct runも4秒で再集計。direct stage mediansはcapture 1.675秒、runtime prepare 0.505秒、helper pipe 1.720秒で、stageは並列のため加算しない。T9のWindows RTX 5070 Ti/Vulkan release helper・明示path・embedded runtime・tamper/cleanup試験はPASS。WGPU失敗時は通知付きCPU描画fallback、NVENC失敗時はx264を選べる。WGPU経路はdefault-offを維持。実コメントMP4/HLS、追加解像度/FPS、AMD/Intel/Apple Silicon/Linux実機は未検証。詳細は[検証結果](../../verification/niconico-comments/timeline-acceptance.md)と[実行計画](../plans/2026-09-23-niconico-comment-timeline-wgpu.md)を参照。

実装と検証の手順は[実行計画書](../plans/2026-09-23-niconico-comment-timeline-wgpu.md)を参照。

## 0. 精査による修正

| 元の設計の問題 | 修正した決定 |
|---|---|
| 連続マイクロ秒で位置を計算すると、既存の10ms単位の切り捨てと異なる | 出力PTSは有理数、コメント評価時刻は既存と同じ整数vpos（1/100秒） |
| 位置・速度の1/256固定小数点化に根拠がなく、速度誤差が蓄積する | JS由来の係数をfloat64で保持し、GPUのfloat32との差を検査。位置の逐次加算は禁止 |
| テクスチャごとに並べ替えると半透明の重なり順が変わる | owner/index/同一コメント内の順序を保持。連続する同一資産の描画のみまとめる |
| RGBAのalpha方式・上下方向・色変換が未定義 | 現行WebGL/NPS3と同じpremultiplied RGBA8・top-left・Unormを明記 |
| 音楽用サイドカーの再利用範囲が広すぎる | コメント専用の小さなWGPU実行ファイルを追加。既存Nicoの子プロセス/FFmpegパイプ管理を再利用 |
| 動画本体のGPU合成まで行うように読める | 初期実装は透明コメント面をGPU生成し、動画へのoverlayは既存FFmpegが行う |
| 未対応効果・メモリ上限・途中失敗時の再試行が曖昧 | 対応表、ハード上限、ジョブ全体を最大1回再実行する条件を定義 |
| 3秒台の性能受入れ範囲と既存計測の終了地点が一致しない | MP4変換完了、HLS準備完了、検査時間を別の時計で記録し、5回中央値が4秒未満の構成を性能受入れとする |
| コメント画像の再利用がフレーム単位の仕事も不要にすると誤読されうる | コメント資産/位置計算を再利用する一方、FFmpeg/x264へ渡す全画面RGBAは出力フレームごとに合成・読戻しすることを明記 |

上記は設計上の欠落の修正であり、新経路の実装・速度・画質を確認したという意味ではない。

## 1. 目的

コメント付き動画の変換時間を短くする。現在のネイティブNPS3経路は、ブラウザーで出力フレームごとにコメントを描画し、各時刻のWebGLスプライト描画列を送っている。コメント画像はキャッシュされるが、180フレームの動画ならアニメーション評価と描画コマンド生成も180回行う。

新経路では、コメント画像を一度取得し、コメントごとの表示区間・位置・速度・順序を時刻付きのイベントとして渡す。WGPUがコメント資産を再利用し、出力フレーム時刻ごとに位置を計算して透明RGBA面へ描く。したがってブラウザー側の文字画像生成・全コメント評価・描画コマンド生成はフレームごとに行わないが、全画面RGBAのGPU合成とFFmpegへの読戻しは出力フレームごとに残る。6秒1080p30ではRGBA8だけで約1.493GBを渡す。FFmpegへはこれまでどおり完成したoverlay frameを渡すため、x264を含む既存のエンコーダー経路を維持できる。

## 2. 確認済みの前提

- 既存のNPS3は、テクスチャと**フレームごとの**描画コマンドを運ぶプロトコルである。
- `niconicomments`のWebGL描画と画像キャッシュは、現在の見た目を保つ基準実装である。コメント文字・フォント・改行を別のシェーダー実装で描き直さない。
- 6秒・1080p・30fps・362件の調査用スナップショットでは、移動コメント294件、上コメント39件、下コメント29件だった。6秒間に見えた移動コメントは72件で、13個のキャッシュ画像を共有していた。調査対象で確認した移動位置は既存ブラウザー描画式と一致した。ただし、これは一つのスナップショットでの幾何検証であり、全コメント機能の互換性や速度を証明しない。
- この調査の画像資産は合計約2.0 MBのRGBA、圧縮済みで約113 KBだった。支配的なコストが常にテキスト画像生成だとは限らないため、段階ごとの計測を続ける。
- 既存のGo/WGPU側にはサイドカー起動、フレーム転送、キャンセル、GPU失敗処理の部品がある。コメントの新規シーン形式は音楽用データ形式と分離し、共通化できる転送・ライフサイクルだけを再利用する。

## 3. 成功条件

1. 1フレームごとのブラウザー描画・コマンド生成をコメントのタイムラインイベント評価に置き換える。
2. コメント付き6秒動画を、コメントなしの同一入力と同じ出力条件で変換し、描画・転送・FFmpeg各段階の時間を比較できる。
3. ローカル入力動画と固定コメントスナップショットが準備済みの状態から、ブラウザー/サイドカー起動・能力検査、コメント合成、FFmpegの動画デコード・合成・H.264/AACエンコード・MP4確定までを`convert_wall`として計測する。1080p30では同一条件を5回以上計測し、5回中央値が4.000秒未満の構成があれば性能受入れとする。全サンプル・最大値・nearest-rank p95を併記し、最大値が4秒を超えても隠さない。HLS準備までの`ready_wall`、試験用の全フレームdecode/NAL検査の`verification_wall`、ネット取得時間も別に記録する。テスト検査時間を変換時間へ混ぜず、変換に必要な初期化を計測外へ移さない。
4. 720p/1080p、30/60fpsを同じタイムライン規則で扱う。性能受入れ前に対象環境・encoder・CPU試験条件・複数回の中央値とばらつきを報告する。x264 fallbackと各GPU encoderは独立した条件として評価する。
5. CPU20%相当の制約は性能試験でゲーム実行中の余力を模擬する用途にだけ使う。製品の通常実行にCPU使用率上限を設けない。
6. AMD/Intel/NVIDIAのGPUベンダーに依存しない合成経路を用意し、x264をエンコーダーとして選べる。Apple SiliconではWGPUのMetalとx264がビルド・実機検証できた場合に対応を表明する。アーキテクチャ上の移植性だけで製品対応済みとは扱わない。

## 4. 採用する構成

### 4.1 コメントイベントと画像の抽出

既存の固定版`niconicomments`を文字画像・コメント属性・レイアウトの参照実装として使う。ブラウザー側アダプターは初期化後にコメント要素の画像と、同ライブラリが決めた配置・アニメーション情報を一度だけ収集する。移動コメントの衝突回避によるY位置、上/下コメントの固定位置、重なり順、反転移動、透明度、色、サイズを描画エンジンと同じ規則で得る。

新規実装でコメント配置規則を推測・複製しない。`getCommentPos`/`sortTimelineComment`で既存の配置を確定し、対象時間外の先行コメントも衝突計算から除かない。`timeline`の実際の所属区間から可視候補区間を得る。各要素の`draw`を基準時刻で呼び、既存スプライト捕捉器で画像・矩形・投影行列・alphaを取得する。通常描画で使う`drawCanvas`を出力フレーム数ぶん呼ばない。画像のタイル分割も同一コメント内の複数primitiveとして保存する。

初期対応を次のように限定し、対応範囲はレポートに記録する。サポート判定は生mail文字列だけでなく、パース後の要素、設定、NicoScript状態、捕捉した描画primitiveを確認する。

| 入力 | 初期経路 |
|---|---|
| 静的なHTML5コメントのnaka/ue/shita、色、サイズ、改行、固定alpha、通常の投稿者コメント | タイムライン候補 |
| 同じ画像の重複、画像のタイル分割、画面外から入るコメント、動画開始前から表示中のコメント | タイムライン候補。元の配置・順序を保持 |
| FlashCommentとして実体化したFlash、逆/禁止等のNicoScript時間変化、ボタン、背景/枠、追加plugin、debug描画、commentLimit | ジョブ全体を既存経路へ戻す。初期実装で部分的に省略しない |
| 未知のクラス/primitive、非有限値、非連続の所属区間、未解決配置、資源上限超過 | 明確な未対応理由を返す |

固定版bundleは既定でFlashComment用の`commentPlugins` factoryを1件登録する。この登録は標準HTML5コメントに適用されないため、登録件数だけを理由に全ジョブを拒否しない。既定のfactoryがbundle固定版と一致し、追加の`plugins`/comment factoryがなく、実コメント要素が全てHTML5Commentの場合だけ候補にする。FlashCommentとして実体化した要素や追加/未知のfactory・plugin instanceは未対応として扱う。

既存バンドルは上流0.4.1からのローカル変更を含むため、VERSION.txtの上流ハッシュだけで識別しない。実際に使用するbundle.jsのSHA-256とアダプター契約を固定する。未知のBundlePath指定を新経路で推測実行しない。

### 4.2 NCT1タイムライン形式

既存NPS3との誤読を避け、新しいバージョン付き`NCT1`ストリームとして定義する。内容は次の二種類に分ける。

- **画像資産**: 安定ID、幅、高さ、ハッシュ、premultiplied RGBA8画素。ブラウザー→Goは既存の可逆圧縮を利用可能。NCT1の初版はGoで検査済みの展開画素を運び、Rust側へ別の解凍実装を増やさない。
- **表示イベント**: 資産ID、開始/終了vpos（半開区間）、基準vpos、基準矩形、投影行列、alpha、横移動のfloat64係数、owner/index/primitiveの順序キー。

コメント評価時刻は`floor(frameIndex * FPSDen * 100 / FPSNum)`。これは既存の`CommentTimeMs`とJSの`floor(ms/10)`に一致させる。負のコメント開始時刻も保持する。GPUでは現在vposと基準vposの整数差をとってからfloat32へ変換し、長い動画の絶対時刻をfloat32化しない。Goのfloat64参照計算とGPUの差を検証し、任意の1/256固定小数点への再量子化は行わない。

初版の上限は出力3840×2160、1,000,000フレーム、FPS分子/分母は各1,000,000以下かつ60fps以下、1画像16384×16384以下かつ128MiB以下、全画像256MiB以下、10,000画像、100,000描画イベントとする。画像は実アダプターの上限も満たすこと。全時刻をsigned int32 vposで表せること、表示区間内の基準時刻との差がfloat32で整数を正確に表せる範囲内であることも検査する。乗算・加算のoverflow、NaN/Inf、ID重複/未定義参照、終端後の余分なデータは拒否する。

全資産を保持する初版の上限を超えたジョブは既存経路へ戻す。長尺の資産ページングは初版へ混ぜず、フォールバック率とメモリ測定に基づく次の改善候補として記録する。

### 4.3 WGPU合成

`gpu/nico-compositord`にコメント専用の小さなRust実行ファイルを作る。音楽側と同じ固定版wgpu 0.20.1を基準にし、音楽のtrack/epoch状態やNVENC依存を持ち込まない。既存Nicoの`runNicoNativePipeTimedInDir`を用い、stdin=NCT1、stdout=透明RGBA面、stderr=状態という直接パイプを維持する。

画像資産を一度登録し、イベントをowner/index/primitive順で固定する。1秒区間ごとの可視候補をもとにrender bundleを作り、最大2区間を保持する。各フレームはvposを更新して該当bundleを実行する。境界内の真の可視区間はシェーダーで判定する。まとめるのは**連続して並ぶ同一資産**のみで、A→B→AをA→A→Bへ並べ替えない。画像atlas導入は初版では不要とする。

描画/読戻しは`Rgba8Unorm`、premultiplied alpha、`One / OneMinusSrcAlpha`、元の投影行列、線形filterとclampを保つ。WebGLの上下方向を明示的に正規化する。sRGBへの自動変換やalpha方式の変更をこの最適化へ混ぜない。動画本体のデコード・scale/pad・overlay・YUV変換は既存FFmpegで行う。

OSごとのAPIはWGPUバックエンドに委ねる。WindowsではDX12を基準にVulkanも診断候補とし、macOSはMetal、LinuxはVulkanを候補にする。CPU/software adapterをハードウェアGPU成功として報告しない。初期の読戻しリングは3スロット、完成フレームキューは2枚上限とする。256byte行アラインメントのpaddingを除去してFFmpegへ送る。map解除前のGPU再利用、出力完了前の上書き、1フレームごとの全GPU待機を避ける。リング1/2/3の候補は同じ画質条件で比較する。

診断用のGPU backend指定とリング数はGoのRenderOptions、worker Request、自己検査、実行CLIへ同じ値を渡す。空値はauto/3に正規化し、不正な値は起動前に拒否する。実変換プロセス自身が記録したbackend/adapter/ring数と要求を照合する。mapped bufferのviewはwriter内で取得・破棄し、unmapとslot返却が終わるまでGPUへ再利用させない。

GPU合成後にx264を使う場合は完成RGBAフレームをCPUへ読み戻す。GPU合成だけでエンコード用CPUフレームが不要になるとは扱わず、読み戻し帯域とRGBA/YUV変換を計測する。将来のGPUエンコード選択は合成バックエンドから独立させる。

### 4.4 フォールバックとエンコード

- 既存のブラウザーRGBA/NPS3経路を、見た目の参照・安全なフォールバックとして残す。
- `Backend=timeline`はWGPUを強制試行するが、呼出元キャンセル以外の失敗では元コメントを保ったCPU描画経路へ一度fallbackし、worker進捗と完了通知で切替を知らせる。`Backend=auto`かつ`TimelineEnabled=true`のときだけ新経路を先に試し、通常の`auto`ではdefault-offを維持する。実際のbackend・encoder・adapter・失敗理由・試行時間を診断情報に記録し、OS固有のpathや詳細なデバイスエラーをUIへ露出しない。
- 未対応コメントや初期化失敗は可能な限りFFmpeg起動前に判定する。途中失敗では当該試行の子プロセスを終了し、専用stagingを破棄して先頭から再実行する。既存MP4/HLSは新試行の両方が成功するまで保護する。cancel時は再試行しない。途中出力への継ぎ足し、部分フレームの再利用、成功を装うコメント欠落は禁止する。
- `x264`と任意のハードウェアエンコーダーは独立して選ぶ。設定画面/APIの共通エンコーダー選択をNico workerまで伝え、GPUは現行workerで利用するNVENC、CPUはlibx264へ対応付ける。x264が利用可能ならAMD/Intel/NVIDIA/Apple Siliconでも同じ合成済みフレームをエンコードできるため、GPUが使えない環境やCPUを希望する利用者はCPUを明示選択できる。NVENC固有の初期化/実行エラーではCPU/libx264で一度再試行する。WGPUコメント合成の失敗はencoderエラーとは区別し、CPU描画へfallbackしてから選択中encoderで続行する。呼出元キャンセルはどちらのfallbackも行わない。`IMAGEPAD_NICO_ENCODER`は診断用の明示overrideとして残し、NVENCからx264への自動切替を無効にする。
- エンコーダーのpreset、thread、画質、色形式は本設計の対象にせず既定値を変えない。x264は`sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0`を維持する。RTSP向けH.264では全アクセスユニットが厳密に1 VCLスライスという既存契約を維持する。

timeline試行の期限は、能力検査10秒＋出力drain2秒、ブラウザー抽出60秒、パイプ開始または完了frame数増加から無進捗30秒とする。監視間隔は250ms以下、中断検出後の所有プロセス終了は5秒以内。最終frame後のFFmpeg終了待ちも監視する。同じ進捗値やログでは時計を更新しない。呼出元のより短いdeadlineを優先し、長尺で正常に進む処理へ3秒の強制停止をかけない。具体的な検査は実行計画3.2/T4/T5/T7に従う。

## 5. 見た目と互換性の境界

コメント画像は既存`niconicomments`描画器から取得し、WGPUで文字組みやフォント選択をやり直さない。出力フレーム時刻、コメントの出現/消失境界、画像資産、配置、速度、合成順、透明度をブラウザー参照出力と照合する。

次の項目を代表 fixture に含める: 日本語・改行・同一画像の重複利用、ue/shita/naka、大小文字、色、透明度、逆スクロール、衝突する移動コメント、高密度スナップショット、投稿者属性や装飾を持つコメント、NicoScript由来の状態。既存ライブラリが現在適用していない動作を新実装で追加しない。比較不能な効果や仕様は新経路を拒否して既存経路へ戻す。

### 合成結果の受入れ

- 入出力フレーム数、PTS、コメント可視区間、イベントごとのX/Yと重なり順をfixtureで一致検証する。
- RGBA比較は同じブラウザーのWebGL readPixelsによるpremultiplied参照画像に対して行い、PNGのstraight-alpha画素と混ぜない。イベント境界、表示順、画像ハッシュは完全一致。コメント外は完全一致、各画素チャンネル差は1以内、GPU位置と元のfloat64位置との差は1/256px以内を初期合格条件とする。これは未検証の受入れ条件であり、合格の保証ではない。差分画像・最大差・超過画素数を全フレーム分記録し、全画面平均やサンプル数枚だけで合格にしない。失敗時に閾値を自動で緩めない。
- H.264出力を検証し、1スライス契約が適用される配信経路では実際のNALを数える。設定文字列だけでは合格にしない。
- 中断、GPUデバイス喪失、破損ストリーム、FFmpeg失敗、キャンセル後の子プロセスと一時ファイルの終了を検証する。

## 6. 性能評価

固定した6秒入力・固定コメントスナップショットで、コメント処理を一切起動しない基準、従来browser、従来native、タイムラインGPUを同じFFmpeg条件で比較する。コメントなし基準もデコード・scale/pad・fps・画質・音声・MP4条件を揃える。各候補は順番を交互にして5回以上、毎回新しいブラウザー/サイドカー/FFmpegプロセスで測る。ビルド時間は除き、能力検査やshader生成を含む`convert_wall`を使う。段階時間は並行して重なるため単純合算しない。

1080p30・362件 fixtureを性能受入れの基準にし、720p30、720p60、1080p60、高密度fixtureは回帰・拡大試験とする。CPU20%相当の試験条件と上限なしの条件を両方測る。各条件で合成CPU時間、FFmpeg CPU時間、プロセス別CPU、GPU、ピーク常駐メモリ、出力サイズ、画質、中央値とばらつきを報告する。ネット取得を含む全体時間も別の実運用指標として記録する。

5回中央値が4秒未満にならない条件は性能未達として記録する。データを切り詰めたり品質や互換性を下げて合格にしない。未達値、支配段階、CPU制約とGPU読戻しの影響を示す。

CPU20と上限なし、x264とNVENCなどは別セルとして判定する。当該セルの5回以上の中央値が4秒未満なら性能目標を受け入れ、最大値・nearest-rank p95を併記する。x264で基準未達でもフォールバックの正しさは別に判定し、NVENCの結果をx264や別GPUの速度として扱わない。0件・長尺・高密度・未対応機能入りの結果と新経路の適用率も示す。

## 7. 配備と切り替え

新経路は明示指定で検証できる状態から開始する。以下が全て通るまで`auto`の既定選択にせず、従来のブラウザー/NPS3経路を通常動作として保つ。

サーバーworkerの製品設定は`IMAGEPAD_NICO_RENDERER`、`IMAGEPAD_NICO_TIMELINE_ENABLED`、`IMAGEPAD_NICO_TIMELINE_COMPOSITOR`、`IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS`、`IMAGEPAD_NICO_TIMELINE_GPU_BACKEND`からRequestへ明示的に渡す。設定がない通常起動ではtimelineはOFF、readback ringはruntime既定の3、GPU backendはautoとする。`IMAGEPAD_NICO_RENDERER=timeline`はWGPUを強制試行するが、呼出元キャンセル以外の失敗では元コメントを保った従来CPU描画へ一度戻して切替を通知する。`auto`と`IMAGEPAD_NICO_TIMELINE_ENABLED=true`の組も新経路を先に試し、同じCPU描画fallbackを行う。encoder選択と合成fallbackは独立し、NVENC固有失敗時だけ別契約でx264を一度試す。性能harnessの`NICO_TIMELINE_*`変数は別契約とし、製品workerは読まない。

1. ブラウザー参照と全代表fixtureの動き・描画順・RGBA差分が合格。
2. GPU別/OS別の成功または未対応結果を明示し、GPU失敗時にコメントを保持したフォールバックが合格。
3. x264と利用可能なハードウェアエンコーダーそれぞれでMP4検査が合格。x264のスレッド/スライス契約を維持。
4. キャンセル、失敗、時間、メモリの上限が合格。
5. 少なくとも1つの対象構成で5回中央値4秒未満を再現し、条件と全サンプルを記録。x264 fallbackと他GPU/OSの性能は別途明示する。

通常の動画変換、音楽レンダラー、OBS、AirPlay、入力コメント取得、共有状態は変更対象にしない。RTSP配布物・ランタイム・H.264契約を変更しない。

## 8. 対象外

- 文字グリフをWGPUで直接描画し、ブラウザーのフォント・改行・文字組みを置換すること。
- NPS3や現行ブラウザー経路の削除。
- FFmpegのx264 preset、thread数、画質、音声条件の変更。
- GPUエンコーダーを全ベンダーで必須化すること。
- CPU使用率上限を製品の通常実行へ適用すること。
- Apple Siliconをパッケージ・実機検証前に製品対応済みと宣言すること。

## 9. 根拠となる現行コードと資料

- `internal/niconico/timeline.go`: 出力FPSとコメント時刻。
- `internal/nicorender/assets/bundle.js`: `processMovableComment`、`getPosX`、`TIMELINE_COMMENT_SORT`、`BaseComment.draw`、WebGLのalpha/blend。
- `internal/nicorender/assets/sprites.js` / `sprite_codec.go`: 既存の画像/primitive捕捉と上限。
- `internal/video/niconico_native.go` / `niconico_pipeline.go` / `niconico_args.go`: 直接パイプ、フォールバック、FFmpeg overlayとencoder条件。
- `internal/nicoexportworker/protocol.go` / `worker.go`: 実ジョブへのbackend伝播。描画器だけの試験でこの層の成功を代用しない。
- `internal/server/niconico_worker.go`: 現行の実行CPU設定は100%。20%は試験runnerで適用する。
- `gpu/playlist-compositord/Cargo.lock`: 現行wgpuは0.20.1。Context7の最新API例をそのまま持ち込まず、[0.20.1のBuffer](https://docs.rs/wgpu/0.20.1/wgpu/struct.Buffer.html)、[ImageDataLayout](https://docs.rs/wgpu/0.20.1/wgpu/struct.ImageDataLayout.html)、[Device::poll](https://docs.rs/wgpu/0.20.1/wgpu/struct.Device.html#method.poll)を基準にする。
