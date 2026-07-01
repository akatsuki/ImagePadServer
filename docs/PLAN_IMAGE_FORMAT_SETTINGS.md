# 実装計画: 画像フォーマット・品質設定の拡張

**背景:** 現在の画像設定は自由入力（数値）で、選択肢が広すぎる上に WebP に非対応。
プリセット選択 UI に統一し、PNG最適化・WebP出力を追加することで回線品質を問わず使いやすくする。

関連計画: `PLAN_PNG_OPTIMIZE.md`（PNG最適化パイプラインの詳細）

---

## 変更一覧

| レイヤー | ファイル | 変更の種類 |
|---|---|---|
| UI | `internal/server/ui.go` | `<input type="number">` → `<select>` 4か所、JS で品質行の表示制御 |
| サーバー | `internal/server/server.go` | `optionsFromValues` の品質プリセット解釈、WebP 対応 |
| 画像処理 | `internal/imageproc/processor.go` | WebP エンコード追加、PNG 最適化フック |
| 画像処理 | `internal/imageproc/tools.go` | 新規: pngquant / oxipng / WebP エンコード用ツール取得 |
| 画像処理 | `internal/imageproc/png_optimize.go` | 新規: PNG最適化パイプライン（PLAN_PNG_OPTIMIZE.md 参照） |
| 画像処理 | `internal/imageproc/webp_encode.go` | 新規: WebP エンコード（ffmpeg 経由） |

---

## Phase 1: UI 変更（`ui.go` L989-992）

### Before
```html
<label><span>最大辺</span><input name="maxDimension" type="number" min="64" max="8192" value="2048"></label>
<label><span>形式</span><select name="format"><option value="jpeg">JPEG</option><option value="png">PNG</option></select></label>
<label><span>JPEG品質</span><input name="quality" type="number" min="40" max="95" value="88"></label>
<label><span>最大MB</span><input name="maxMB" type="number" min="1" max="120" value="30"></label>
```

### After
```html
<label><span>最大辺</span>
  <select name="maxDimension">
    <option value="2048" selected>2048px</option>
    <option value="1024">1024px</option>
    <option value="512">512px</option>
    <option value="256">256px</option>
    <option value="128">128px</option>
  </select>
</label>
<label><span>形式</span>
  <select name="format" id="formatSelect">
    <option value="png">非劣化 (PNG)</option>
    <option value="webp" selected>高品質 (WebP)</option>
    <option value="jpeg">高圧縮 (JPEG)</option>
  </select>
</label>
<label><span>品質</span>
  <select name="quality" id="qualitySelect">
    <!-- JS により format に応じて動的に生成 -->
    <!-- PNG 初期値: 非劣化(lossless), 最高, 高, 中, 低, 最低 -->
    <!-- JPEG/WebP 初期値: 最高, 高(default), 中, 低, 最低 -->
  </select>
</label>
<label><span>最大MB</span>
  <select name="maxMB">
    <option value="30" selected>30 MB</option>
    <option value="20">20 MB</option>
    <option value="10">10 MB</option>
    <option value="5">5 MB</option>
    <option value="1">1 MB</option>
  </select>
</label>
```

### JS: フォーマットに応じた品質選択肢の切り替え

PNG は 6 段階（非劣化がデフォルト）、JPEG/WebP は 5 段階。format 変更時に select の options を差し替える。

```javascript
const formatSelect = document.getElementById('formatSelect');
const qualitySelect = document.getElementById('qualitySelect');

const qualityOptions = {
  png: [
    { value: 'lossless', label: '非劣化', selected: true },
    { value: 'highest',  label: '最高' },
    { value: 'high',     label: '高' },
    { value: 'medium',   label: '中' },
    { value: 'low',      label: '低' },
    { value: 'lowest',   label: '最低' },
  ],
  webp: [
    { value: 'highest', label: '最高' },
    { value: 'high',    label: '高', selected: true },
    { value: 'medium',  label: '中' },
    { value: 'low',     label: '低' },
    { value: 'lowest',  label: '最低' },
  ],
  jpeg: [
    { value: 'highest', label: '最高' },
    { value: 'high',    label: '高', selected: true },
    { value: 'medium',  label: '中' },
    { value: 'low',     label: '低' },
    { value: 'lowest',  label: '最低' },
  ],
};

function updateQualityOptions() {
  const opts = qualityOptions[formatSelect.value] || qualityOptions.jpeg;
  qualitySelect.innerHTML = opts
    .map(o => `<option value="${o.value}"${o.selected ? ' selected' : ''}>${o.label}</option>`)
    .join('');
}
formatSelect.addEventListener('change', updateQualityOptions);
updateQualityOptions(); // 初期状態を適用
```

### ビデオプレーヤーモード時の `.controls` 非表示

現状、`.controls`（最大辺 / 形式 / 品質 / 最大MB）は OBS モード時のみ非表示になっている。
ビデオプレーヤー有効時にも非表示にする必要がある。

**現状の制御箇所（2か所）:**

```javascript
// setUploadMode（L1937-1939）
if (uploadControls) {
    uploadControls.hidden = obsMode;  // ← obsMode のみ
}

// applyVideoPlayer（L1678〜）
// → uploadControls への言及なし（これがバグ）
```

**修正方針:** 非表示条件を `obsMode || state.videoPlayerEnabled` に統一し、
`setUploadMode` と `applyVideoPlayer` の両方から同じ関数で制御する。

```javascript
function updateUploadControlsVisibility() {
    if (uploadControls) {
        uploadControls.hidden = obsMode || state.videoPlayerEnabled;
    }
}
```

呼び出し箇所:
- `setUploadMode()` 内で `uploadControls.hidden = obsMode` を `updateUploadControlsVisibility()` に置き換え
- `applyVideoPlayer()` 末尾に `updateUploadControlsVisibility()` を追加

**理由:** ビデオプレーヤー有効時は動画・音声・画像が混在してアップロードされるため、
画像固有のフォーマット設定（JPEG/PNG/WebP・品質・最大辺・最大MB）は意味をなさない。

---

### デフォルト変更の意図

| 項目 | 旧デフォルト | 新デフォルト | 理由 |
|---|---|---|---|
| 形式 | JPEG | WebP | 回線が遅い環境を優先。WebP は JPEG より高品質・小サイズ |
| 品質 | 88（数値） | 高（プリセット） | 意味のある選択肢に統一 |

---

## Phase 2: 品質プリセットのマッピング（`server.go`）

### `imageproc.Options` の追加フィールド

```go
type Options struct {
    MaxDimension  int
    JPEGQuality   int
    WebPQuality   int    // 追加: WebP の -quality 値
    PNGQuality    string // 追加: "lossless" | "highest" | "high" | "medium" | "low" | "lowest"
    MaxBytes      int64
    MaxInputBytes int64
    Format        string
}
```

`DefaultOptions()`:
- `JPEGQuality = 85`（"高"相当）
- `WebPQuality = 80`（"高"相当）
- `PNGQuality = "lossless"`（非劣化デフォルト）
- `Format = "webp"`

### マッピングテーブル

```go
var qualityPresetToJPEG = map[string]int{
    "highest": 95,
    "high":    85,
    "medium":  75,
    "low":     60,
    "lowest":  45,
}

var qualityPresetToWebP = map[string]int{
    "highest": 90,
    "high":    80,
    "medium":  70,
    "low":     55,
    "lowest":  40,
}

// PNG は pngquant の --quality=min-max 形式
var qualityPresetToPNGRange = map[string][2]int{
    // "lossless" → pngquant をスキップ（oxipng のみ）
    "highest": {90, 100},
    "high":    {75, 90},
    "medium":  {60, 75},
    "low":     {45, 60},
    "lowest":  {30, 45},
}
```

### `optionsFromValues` の変更

```go
if v := value("quality"); v != "" {
    if q, ok := qualityPresetToJPEG[v]; ok {
        opts.JPEGQuality = q
    } else if q, err := strconv.Atoi(v); err == nil {
        opts.JPEGQuality = q // 旧フォーマットの後方互換
    }
    if q, ok := qualityPresetToWebP[v]; ok {
        opts.WebPQuality = q
    }
    // PNG: "lossless" を含む全プリセットをそのまま渡す
    opts.PNGQuality = v
}
```

### PNG最適化パイプラインへの反映（`png_optimize.go`）

```go
func OptimizePNG(path string, quality string) (sizeBytes int64, err error) {
    if quality == "" || quality == "lossless" {
        // pngquant をスキップ、oxipng のみ実行
        return runOxipng(path)
    }
    // pngquant: --quality=min-max
    r := qualityPresetToPNGRange[quality] // e.g. {75, 90}
    if err := runPngquant(path, r[0], r[1]); err != nil {
        log.Printf("pngquant: %v (skipping lossy step)", err)
    }
    return runOxipng(path)
}
```

---

## Phase 3: WebP エンコード（`imageproc/webp_encode.go`）

### 方針

Go の `golang.org/x/image/webp` は**デコードのみ**で、エンコード非対応。
選択肢:

| 方法 | pros | cons |
|---|---|---|
| **ffmpeg（バンドル済み）** | 追加ツール不要 | `imageproc → video` 依存が深まる |
| cwebp バンドル | imageproc が自己完結 | 新規バイナリ追加 |
| 純 Go ライブラリ | 依存なし | 主要なエコシステムに存在しない |

→ **ffmpeg 経由を採用**（すでに依存しており、`decodeCameraRAW` の前例がある）。
  cwebp バンドルは別タスクで検討。

### 実装

アルファチャンネルは無効化し、透過部分は黒でフラット化する。
既存の `flatten()` 関数（JPEG パスで使用済み）がそのまま使える。

```go
// EncodeWebP flattens alpha to black, then encodes to lossy WebP via ffmpeg.
// quality should be in [0, 100]; 80 is a good default.
func EncodeWebP(src image.Image, outPath string, quality int) error
```

内部:
1. `flatten(src)` でアルファを黒背景に合成（既存関数を流用）
2. `image/png` で中間 PNG に書く（非劣化のまま ffmpeg に渡す）
3. 一時ファイルに書き出す
4. `ffmpeg -y -hide_banner -loglevel error -i <tmp.png> -quality <q> <outPath>` を実行
5. 一時ファイルを削除
6. 出力ファイルを検証（存在 + サイズ > 0）

---

## Phase 4: `processor.go` への統合

### `Options` 構造体の変更（L34-40）

```go
type Options struct {
    MaxDimension  int
    JPEGQuality   int
    WebPQuality   int   // 追加
    MaxBytes      int64
    MaxInputBytes int64
    Format        string // "jpeg" | "png" | "webp"
}
```

### `DefaultOptions()` の変更

```go
func DefaultOptions() Options {
    return Options{
        MaxDimension:  2048,
        JPEGQuality:   85,   // "高" プリセットに対応
        WebPQuality:   80,   // "高" プリセットに対応
        MaxInputBytes: maxImageBytes,
        MaxBytes:      30 << 20,
        Format:        "webp", // デフォルトを WebP に変更
    }
}
```

### `Process()` のフォーマット分岐（L100-123 周辺）

```go
switch opts.Format {
case "png":
    enc := png.Encoder{CompressionLevel: png.BestCompression}
    var buf bytes.Buffer
    if err := enc.Encode(&buf, resized); err != nil {
        return Result{}, err
    }
    data = buf.Bytes()
    ext = ".png"
    contentType = "image/png"

case "webp":
    outWebP := filepath.Join(outDir, "processed.webp")
    if err := EncodeWebP(resized, outWebP, opts.WebPQuality); err != nil {
        return Result{}, fmt.Errorf("webp encode: %w", err)
    }
    // WebP はファイル書き込み済みなので data パスを迂回
    // → Process() の return を分岐させるか、data = os.ReadFile(outWebP) で統一

default: // "jpeg"
    encoded, err := encodeJPEGWithinLimit(flatten(resized), opts.JPEGQuality, opts.MaxBytes)
    if err != nil {
        return Result{}, err
    }
    data = encoded
    ext = ".jpg"
    contentType = "image/jpeg"
}
```

> **実装注意:** WebP は ffmpeg がファイル直書きするため、現在の `data = []byte → os.Write` フローと合わせるには `os.ReadFile(outWebP)` で読み直すか、`Process()` の返却パスを分岐させる。シンプルさのため `os.ReadFile` で統一する。

### PNG 最適化フック（`PLAN_PNG_OPTIMIZE.md` との統合）

```go
case "png":
    // ... encode ...
    // 最適化はファイル書き込み後に実施
    if _, err := os.WriteFile(path, data, 0600); err != nil {
        return Result{}, err
    }
    if _, optErr := OptimizePNG(path); optErr != nil {
        log.Printf("png optimize: %v", optErr)
    }
    // data の再読み込みは不要（path が serve されるため）
    return Result{...}, nil
```

---

## Phase 5: テスト計画

| テスト | 確認内容 |
|---|---|
| `TestProcessWebP_basic` | WebP 出力が生成されること、ContentType が `image/webp` |
| `TestProcessWebP_quality` | 品質 40 vs 90 でファイルサイズに差があること |
| `TestProcessWebP_transparency` | 透過 PNG を入力した WebP 出力でアルファが黒背景にフラット化されること |
| `TestQualityPresetMapping` | `"highest"` → JPEG 95、WebP 90 のマッピング |
| `TestDefaultOptionsFormat` | デフォルト形式が `"webp"` であること |
| `TestOptionsFromValues_presets` | 全プリセット名が正しく数値に変換されること |
| 既存テスト群 | リグレッション確認（format="jpeg" / "png" が従来通り動くこと） |

---

## 実装順序

1. [ ] `imageproc.Options` に `WebPQuality` 追加、`DefaultOptions` をWebPデフォルトに
2. [ ] `imageproc/webp_encode.go` — `EncodeWebP` 実装
3. [ ] `processor.go` — フォーマット分岐に `"webp"` 追加
4. [ ] `server.go` — `qualityPresetToJPEG` / `qualityPresetToWebP` マップ追加、`optionsFromValues` 更新
5. [ ] `ui.go` — `<select>` UI に差し替え、JS 品質行制御
6. [ ] `imageproc/tools.go` + `png_optimize.go` — PNG最適化（`PLAN_PNG_OPTIMIZE.md` 参照）
7. [ ] テスト追加
8. [ ] `ValidateImageTools()` + server.go 起動シーケンス
9. [ ] dev build & commit

---

## 未決事項

- **WebP 透過**: アルファ無効化・黒背景フラット化で確定。`flatten()` を流用。
- **PNG "非劣化" の実態**: BestCompression + oxipng のみ。pngquant は一切実行しない。
- **MaxBytes と WebP**: WebP は ffmpeg がサイズを直接制御できないため、MaxBytes 超過チェックは `os.Stat` で後処理する（超過時はエラー）
- **品質行ラベル**: PNG 非表示・WebP/JPEG で「品質」と表示。WebP に「品質」が適用されることをユーザーに明示するか（現在の UI には説明がない）
- **後方互換**: 旧フォーマット（`quality=88` のような数値）はフォールバックで従来通り動作させる
- **WebP サポート確認**: VRChat / OBS での WebP 対応バージョンを調査・注記する（WebP は広くサポートされているが念のため）
