# 実装計画: PNG最適化パイプライン（pngquant + oxipng）

**背景:** PNG出力が `png.Encode` 一発で終わっており、回線速度が遅いユーザーには配信コストが高い。
pngquant（有損失パレット化）→ oxipng（無損失deflate最適化）の二段構えで 70〜85% 削減を目指す。

---

## 設計方針

| 項目 | 方針 |
|---|---|
| 配置 | `internal/imageproc/` に閉じる（`video` パッケージへの依存を増やさない） |
| 新規ファイル | `tools.go`（取得・パス解決）、`png_optimize.go`（最適化ロジック） |
| 失敗時の挙動 | ツール未インストール・最適化失敗はエラーにせず元ファイルのまま提供（ベストエフォート） |
| ダウンロード | 初回PNG出力時にオンデマンドで取得（`ValidateInstalledTools` にも追加） |
| 環境変数 | `IMAGEPAD_PNGQUANT`, `IMAGEPAD_OXIPNG`（テスト・上書き用） |

---

## ファイル構成と変更点

```
internal/imageproc/
  processor.go           ← PNG出力後に OptimizePNG() を呼ぶ（5行追加）
  tools.go               ← 新規: pngquant/oxipng のパス解決・ダウンロード
  png_optimize.go        ← 新規: OptimizePNG(path string) error
```

---

## Phase 1: `tools.go` — ツール取得

### ダウンロードソース

| ツール | プラットフォーム | URL | 形式 |
|---|---|---|---|
| pngquant | Windows | `https://github.com/kornelski/pngquant/releases/download/3.0.3/pngquant-windows.zip` | zip（pngquant.exe） |
| pngquant | macOS | PATH のみ（Homebrew: `brew install pngquant`） | — |
| oxipng | Windows | `https://github.com/shssoichiro/oxipng/releases/download/v9.1.3/oxipng-9.1.3-x86_64-pc-windows-msvc.zip` | zip（oxipng.exe） |
| oxipng | macOS arm64 | `https://github.com/shssoichiro/oxipng/releases/download/v9.1.3/oxipng-9.1.3-aarch64-apple-darwin.tar.gz` | tar.gz |
| oxipng | macOS x64 | `https://github.com/shssoichiro/oxipng/releases/download/v9.1.3/oxipng-9.1.3-x86_64-apple-darwin.tar.gz` | tar.gz |

> **pngquant macOS:** 公式バイナリなし。macOS では PATH から探し、なければスキップ（oxipng のみ実行）。
> エラーではなく「可能な最適化を実施する」にとどめる。

### パス解決ロジック（既存の ffmpeg パターンに準拠）

```go
// EnsurePngquant returns the pngquant path; downloads on first use (Windows only).
// On macOS, falls back to PATH. Returns ("", nil) when unavailable — caller treats
// missing pngquant as "skip lossy step", not an error.
func EnsurePngquant() (string, error)

// EnsureOxipng returns the oxipng path; downloads on first use (Windows/macOS).
// Returns ("", nil) when unavailable.
func EnsureOxipng() (string, error)
```

優先順位:
1. `IMAGEPAD_PNGQUANT` / `IMAGEPAD_OXIPNG` 環境変数
2. バンドル済みバイナリ（`settings.Dir()/bin/<version>/pngquant[.exe]`）
3. PATH（macOS の Homebrew 向け）
4. 未インストール → `("", nil)` を返す

### 環境変数（追加）

| 変数 | 用途 |
|---|---|
| `IMAGEPAD_PNGQUANT` | pngquant バイナリパスの上書き |
| `IMAGEPAD_OXIPNG` | oxipng バイナリパスの上書き |
| `IMAGEPAD_PNGQUANT_SHA256` | ダウンロード時チェックサム（CI/テスト用） |
| `IMAGEPAD_OXIPNG_SHA256` | 同上 |

---

## Phase 2: `png_optimize.go` — 最適化ロジック

```go
// OptimizePNG runs pngquant (lossy) → oxipng (lossless) on path in-place.
// If a tool is unavailable or fails, the step is silently skipped.
// Returns the final file size after all available optimizations.
func OptimizePNG(path string) (sizeBytes int64, err error)
```

### パイプライン詳細

```
[1] pngquant（有損失・省略可能）
    コマンド: pngquant --quality=65-80 --speed 3 --strip --force --output <tmp> -- <path>
    --quality=65-80 : 最低65を下回るなら量子化しない（品質保証）
    --speed 3       : 速度/品質のバランス（1=最高品質・最低速、10=最低品質・最高速）
    --strip         : メタデータ削除
    --force         : 上書き許可
    成功時: <tmp> を <path> に置換
    失敗時: 元ファイルを保持してそのまま oxipng ステップへ

[2] oxipng（無損失・省略可能）
    コマンド: oxipng --opt 3 --strip safe <path>
    --opt 3   : 最適化レベル（0〜6、3はバランス型）
    --strip safe : 安全なメタデータのみ削除
    インプレース最適化（出力が入力より小さい場合のみ置換）
    失敗時: 元ファイルを保持
```

### サイズ比較ガード

```go
// pngquant が極端に小さいファイルを出した場合（>50% 削減の場合のみ採用）は
// 品質チェックを通す——今回は省略し、pngquant の --quality フラグを信頼する。
// oxipng はサイズが増えた場合は自動的に元ファイルを保持する（デフォルト動作）。
```

---

## Phase 3: `processor.go` への統合

変更箇所: L108〜L113（PNG エンコード後）

**Before:**
```go
if opts.Format == "png" {
    var buf bytes.Buffer
    if err := png.Encode(&buf, resized); err != nil {
        return Result{}, err
    }
    data = buf.Bytes()
}
```

**After:**
```go
if opts.Format == "png" {
    enc := png.Encoder{CompressionLevel: png.BestCompression}
    var buf bytes.Buffer
    if err := enc.Encode(&buf, resized); err != nil {
        return Result{}, err
    }
    data = buf.Bytes()
}
```

そして、L125（`file.Write(data)` 後）に追記:

```go
if opts.Format == "png" {
    if _, optErr := OptimizePNG(path); optErr != nil {
        // best-effort: log but don't fail the upload
        log.Printf("png optimize: %v", optErr)
    }
}
```

> `BestCompression` への変更は pngquant 前段の圧縮率を改善し、oxipng の効果も高める。

---

## Phase 4: 起動時バリデーションに追加

`video.ValidateInstalledTools()` に相当する処理を `imageproc` に追加:

```go
// ValidateImageTools proactively downloads pngquant and oxipng so they are
// ready before the first PNG upload arrives.
func ValidateImageTools() {
    if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
        return
    }
    _, _ = EnsurePngquant()
    _, _ = EnsureOxipng()
}
```

`server.go` の起動シーケンスで `video.ValidateInstalledTools()` と並列呼び出し。

---

## テスト計画

| テスト | 内容 |
|---|---|
| `TestOptimizePNG_basic` | 実際の PNG を入力して最適化後のサイズが元より小さいことを確認 |
| `TestOptimizePNG_no_pngquant` | `IMAGEPAD_PNGQUANT=""` で pngquant なし時に oxipng のみ動作することを確認 |
| `TestOptimizePNG_no_tools` | 両方なし時にエラーではなく元サイズが返ることを確認 |
| `TestEnsurePngquant_env` | `IMAGEPAD_PNGQUANT` 環境変数が優先されることを確認 |
| `TestEnsureOxipng_env` | 同上 |
| 既存 `TestProcessPNGOverMaxBytes` | 最適化後のサイズも MaxBytes 以内であることを確認（リグレッション） |

---

## 期待される削減率

| 入力 | pngquant + oxipng | pngquant のみ | oxipng のみ |
|---|---|---|---|
| スクリーンショット（フラット色が多い） | 85〜95% | 75〜85% | 10〜20% |
| 写真系 PNG | 60〜75% | 55〜70% | 5〜15% |
| ロゴ・シンプルグラフィック | 80〜90% | 70〜80% | 10〜25% |

---

## 実装順序

1. [ ] `tools.go` — EnsurePngquant / EnsureOxipng（パス解決 + Windows ダウンロード）
2. [ ] `png_optimize.go` — OptimizePNG（pngquant → oxipng パイプライン）
3. [ ] `processor.go` — BestCompression 変更 + OptimizePNG 呼び出し
4. [ ] テスト追加
5. [ ] `ValidateImageTools()` + server.go 起動シーケンス
6. [ ] 環境変数をドキュメントに追記
7. [ ] dev build & commit

---

## 未決事項

- pngquant のバージョンを今後も最新追従するか、ffmpeg と同様にピン留めにするか
  → 初期はピン留め（3.0.3）、yt-dlp 方式（latest）は採用しない（理由: API 変更リスクが低いため）
- `--quality=65-80` の閾値はユーザー設定可能にするか
  → 初期は固定。設定 UI は後回し。
- oxipng の最適化レベル `--opt 3` vs `--opt max`
  → 初期は `3`。`max` は処理時間が大幅増（10〜50倍）のため採用しない。
