# AirPlay配信映像の全画面クロップ設計

作成日: 2026-09-16。状態: 実装前の設計。実機・性能検証は未実施。

## 目的と表示仕様

AirPlay受信中にブラウザの「画面を埋める」スイッチを操作し、RTSP・HLS・録画に出る映像を切り替える。
ブラウザのプレビューだけを拡大する機能ではない。

| スイッチ | 内部値 | 表示 |
|---|---|---|
| OFF（初期値） | `contain` | 縦横比を保って全体を収め、余白は黒帯 |
| ON | `cover` | 縦横比を保って配信画面を埋め、はみ出しを中央で切り抜く |

向きのフラグではなく、入力の有効画像領域・ピクセル縦横比と出力の比率を比較する。
縦長のiPhoneでは幅に合わせて上下を切り、横に長いiPhoneでは高さに合わせて左右を切る。
4:3のiPadを16:9へ出す場合は横向きでも上下を切る。回転時は新しい入力形式から再計算する。
設定を保存し、ブラウザ再読み込み・次回受信・画質変更・publisher回復後も引き継ぐ。
切り抜き位置の移動、ズーム倍率、顔追従、黒画素による黒帯自動検出は今回の範囲外。
送信アプリが映像の画素へ焼き込んだ黒帯や、Reddit等の外部画面描画内容自体は変更しない。

## 保持する条件

- 表示切り替えによるUxPlay・bridge・MediaMTXの再起動、RTSP再接続、録画分割を行わない。
- 配信の解像度・fps・SAR=1:1・音声経路・時刻基準を維持する。
- `superfast`、CRF、VBV、GOPなど既存エンコード設定をこの機能で変更しない。
- `sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0`を保持し、実出力も1フレーム1スライスとする。
- 表示操作と保持画像の再描画は新しい受信映像と数えず、無信号タイマーやmedia-ready判定を進めない。
- `CORRUPTED`・`DECODE_ONLY`・IDR待ち・候補publisherの実映像証拠を迂回しない。
- Windowsの検証済みsource-clock経路を最初の対象にする。旧RTP/FFmpeg/direct経路には未対応を明示する。
- Mac/Linuxは実機未検証と明示する。GoのCI成功からnative機能対応を推定しない。
- exeには実行用ランタイムと通知・取得案内を内蔵し、完全な対応ソースは同じリリースの別ZIPに置く。

## 現行コードから確認した根拠

基準HEAD: `29450e6d474566fa525d316504a4ae78fc58df40`（公開版`v1.8.0`）。作業ツリーには別作業の未コミット変更がある。
この機能は公開済みv1.8.0を変更せず、次版`v1.8.1`（または実装時点の次の未公開版）へ収録する。

| 箇所 | 現在の動作・計画への影響 |
|---|---|
| `native/airplay-gstreamer-bridge/source_clock_pipeline.c:1875` | デコード後に`videoscale add-borders=true`で固定サイズ化してからappsinkへ渡す。保持画像は既に縮小済み |
| 同`:525`、`:670` | デコード画像を固定周期schedulerへ渡し、最後の画像をHOLDする。元画像を保てば静止中も再描画できる |
| `native/airplay-gstreamer-bridge/source_clock_candidate.inc` | 回復候補には別のデコード保持・出力経路がある。通常経路だけの変更では固定サイズ契約を破る |
| `internal/airplay/source_clock_session.go:1132` | 既存の画質切り替えは旧publisherを停止して新しく起動する。表示切り替えには使わない |
| `internal/airplay/source_clock_event_log.go:144` | 通常のイベント通知はプロセス終了後に集約する。表示反映の確認には独立した現在状態が必要 |
| `native/airplay-gstreamer-bridge/source_clock_events.c:238` | Windows対応の原子的なスナップショット置換が既にある |
| `internal/server/ui_script_airplay.go` | 既存の画質UI・状態同期がある。表示モードの状態を独立して加える |

v1.8.0では`internal/server/ui.go`、`ui_script_domrefs.go`、`ui_script_uploadevents.go`にNiconicoコメント機能が追加されている。
AirPlayのUI追加はこれらの既存DOM参照・イベント・media intent判定を保持したまま行う。
また、v1.8.0の`release.yml`と`scripts/build-release.sh`にはNiconico compositorのビルド・payload埋め込みが追加されているため、次版のAirPlay配布変更でもその処理を維持する。

## 採用方式

```text
iPhone → UxPlay → 既存のAU検証・デコード
                         ↓
               元サイズのI420画像＋その画像のcaps
                         ↓
               既存schedulerによる選択／HOLD
                         ↓
             contain / coverの変換（ここを切り替える）
                         ↓
       既存の固定サイズappsrc → H.264 → RTSP・HLS・録画

ブラウザ → 認証済みAPI → Goの受信セッション管理
                         ↓
               publisher別の表示制御ファイル
                         ↓
                nativeで受理・描画・状態通知
```

元画像を`GstSample`としてcapsと一緒に保持する。選ばれた画像だけを`GstVideoConverter`で切り抜き・拡縮し、固定サイズI420へ変換する。
入力側の固定サイズ`videoscale`を外すことで二重のサイズ変更を避ける。
同じ画像・同じ表示revisionのHOLDは変換済み画像を再利用し、表示revisionが変わった場合だけ元画像から作り直す。
色空間・レンジ・stride・crop metadataを検証して扱い、画像データを幅×高さだけで解釈しない。

比較した方式:

1. **元画像を保持して出力直前に変換（採用）**: 静止中も切り替えられ、拡大前の画質を保てる。通常経路・候補経路・メモリ所有権の検査が必要。
2. デコード直後のcrop要素を動的変更: 変更量は小さいが、新しい入力が来ないHOLD中の再描画とcaps再交渉が課題。
3. 既存画質切り替えを流用: 配信処理が再起動するため、今回の連続配信要件を満たさない。

## 操作・反映・互換性

- UIは「画面を埋める」、説明は「縦横比を保ち、はみ出しを中央でカット」。初期OFF。
- 保存した希望値、nativeが受理した値、映像に適用した値を区別する。
- 実画像がまだない場合は「映像待ち」。保持画像がある場合は新しい入力がなくても切り替える。
- 反映完了は変換した画像を固定サイズappsrcへ正常に投入した時点。視聴側のバッファ遅延を含まない。
- `publisher-ready`の任意フィールド`videoViewProtocol: 1`で実際のbridgeの対応を確認する。古いbridgeでも受信は継続するがスイッチは無効にする。
- 表示revisionとpublisher generationを分離し、古い世代・古い操作の通知で新しい状態を上書きしない。
- 画質変更・回復が進行中なら新しい表示操作は競合として返す。次のpublisherには保存済みの希望値を初期設定する。
- 変換設定の失敗は前の表示を保持して表示機能のエラーを返す。配信全体の停止・受信再接続で補償しない。

## 検証と完了の定義

幾何計算、実画素、通常/HOLD/回復経路、操作競合、H.264実出力、配布exe、iPhone＋PC版VRChatの順に確認する。
nativeの描画反映目標は正常入力または保持画像がある状態で500ms以内。API待ちは最大2秒、未確定は202と状態照会で扱う。
HLS/VRChatへの見え方は既存の再生遅延を加味し、500msを視聴到達保証とはしない。
自動テスト合格、配布物合格、実機合格を別々に記録する。

## 技術資料

- [GstVideoConverter](https://gstreamer.freedesktop.org/documentation/video/videoconverter.html): source/destination矩形、黒帯塗り、同期フレーム変換。`set_config`失敗時にも部分変更が起こり得るため、変換器は別に作成・検証してから交換する。
- [GstVideoCropMeta](https://gstreamer.freedesktop.org/documentation/video/gstvideometa.html): デコード画像の有効領域。画素からの黒帯推測とは別に扱う。
- `docs/RTSP_H264_COMPATIBILITY_CONTRACT.md`
- `docs/AIRPLAY_DISTRIBUTION_LICENSES.md`

- 公開済み`v1.8.0`のAirPlay入力manifest・タグ・assetは変更せず、次版の新しいruntimeSetIDと2つの配布ZIPを生成する。

Context7と公式資料を参照済み。実装時は配布で固定したGStreamer 1.28.6のヘッダー・DLLで利用APIを照合する。
