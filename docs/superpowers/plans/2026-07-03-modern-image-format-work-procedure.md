# Modern Image Format Recovery Work Procedure

この工程書は `2026-07-03-modern-image-format-implementation-plan.md` を実行するための進行管理用です。実装対象は WebP 出力、AVIF/HEIC/HEIF/JPEG XL 入力、画像設定プリセット、PNG 最適化です。`.txt` / `.docx` / `.doc` のプリレンダリングは後続機能として扱います。

## Ground Rules

- 作業前に `rtk git status --short` で既存変更を確認する。
- ユーザーや別作業者の変更は戻さない。
- テストを先に書き、失敗理由を確認してから実装する。
- 1 コミットは 1 つの意味のある単位にする。
- FFmpeg の対応状況に依存する AVIF/HEIC/JXL は、実ファイルで確認するまで完了扱いにしない。
- PNG 最適化ツールがない場合でもアップロードは失敗させない。

## Phase 0: Workspace Audit

- [ ] Run:

```powershell
rtk git status --short
```

- [ ] Record dirty files that are unrelated to this work.
- [ ] Confirm the worklist exists:

```powershell
rtk proxy pwsh -NoProfile -Command "Test-Path docs\superpowers\plans\2026-07-03-image-format-v1.5-recovery-worklist.md"
```

- [ ] Confirm Go works:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc ./internal/server
```

Exit criterion: baseline failures are understood, or baseline is green.

## Phase 1: RED Tests

- [x] Add tests for:
  - WebP default options.
  - WebP output.
  - AVIF/HEIC/HEIF/JXL input fixtures or fixture-generation path.
  - quality preset parsing.
  - upload accept list and preset controls.

- [x] Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc ./internal/server
```

- [x] Confirm failures are due to missing implementation.

Status note: RED confirmed with missing `WebPQuality` / `PNGQuality` implementation errors.

Commit after RED tests:

```powershell
rtk git add internal\imageproc\processor_test.go internal\server\server_test.go internal\server\ui_media_test.go
rtk git commit -m "test: cover modern image format recovery"
```

## Phase 2: Core Image Options

- [x] Add `WebPQuality` and `PNGQuality`.
- [x] Change defaults to WebP/high/lossless.
- [x] Normalize formats to `jpeg`, `png`, `webp`.
- [x] Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc
```

Commit:

```powershell
rtk git add internal\imageproc\processor.go internal\imageproc\processor_test.go
rtk git commit -m "feat: add image output option presets"
```

## Phase 3: Modern Input Decode

- [x] Create `internal/imageproc/modern_decode.go`.
- [x] Route `.avif`, `.heic`, `.heif`, `.jxl` through FFmpeg still-frame decode after standard decoders fail.
- [x] Keep camera RAW routing separate.
- [x] Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc
```

- [ ] For each unsupported decoder, capture the exact FFmpeg error and decide:
  - use an alternative Go decoder,
  - add a pinned helper binary,
  - or leave the format blocked with a clear reason and do not claim support.

Commit when all selected input formats pass:

```powershell
rtk git add internal\imageproc\modern_decode.go internal\imageproc\processor.go internal\imageproc\processor_test.go
rtk git commit -m "feat: decode modern compressed image inputs"
```

## Phase 4: WebP Output

- [x] Create `internal/imageproc/webp_encode.go`.
- [x] Add WebP output branch in `Process`.
- [x] Enforce `MaxBytes` after FFmpeg writes the file.
- [x] Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc
```

Commit:

```powershell
rtk git add internal\imageproc\webp_encode.go internal\imageproc\processor.go internal\imageproc\processor_test.go
rtk git commit -m "feat: output processed images as webp"
```

## Phase 5: PNG Optimization

- [x] Create `internal/imageproc/tools.go`.
- [x] Create `internal/imageproc/png_optimize.go`.
- [x] Add best-effort optimizer hook after PNG output.
- [x] Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc
```

Commit:

```powershell
rtk git add internal\imageproc\tools.go internal\imageproc\png_optimize.go internal\imageproc\processor.go internal\imageproc\processor_test.go
rtk git commit -m "feat: optimize png output best effort"
```

## Phase 6: Server Presets and UI

- [x] Update `optionsFromValues`.
- [x] Replace number inputs with select controls.
- [x] Add dynamic quality options.
- [x] Add modern input MIME/extensions to `accept`.
- [x] Add `updateUploadControlsVisibility()`.
- [x] Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/server
```

Commit:

```powershell
rtk git add internal\server\server.go internal\server\server_test.go internal\server\ui.go internal\server\ui_media_test.go
rtk git commit -m "feat: add image preset upload controls"
```

## Phase 7: Startup Validation and Docs

- [x] Add non-blocking `imageproc.ValidateImageTools()` startup call.
- [ ] Update `docs/ROADMAP.md`.
- [ ] Update release notes for the next dev build if this branch cuts one.
- [ ] Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./...
```

Status note: implementation was verified with:

```powershell
$env:IMAGEPAD_FFMPEG = (Get-Command ffmpeg).Source
rtk proxy pwsh -NoProfile -Command ".tools\go-sdk\go\bin\go.exe test -timeout 180s ./internal/imageproc ./internal/server ./internal/app"
```

Commit:

```powershell
rtk git add internal\server\server.go docs\ROADMAP.md
rtk git commit -m "docs: record modern image format recovery"
```

## Phase 8: Manual Verification Matrix

Run the app and verify:

| Case | Expected |
|---|---|
| JPEG upload, default settings | WebP output is published |
| PNG with transparency | WebP output is generated with expected flattened background |
| AVIF upload | Selected output type is published |
| HEIC/HEIF upload | Selected output type is published |
| JPEG XL upload | Selected output type is published |
| PNG lossless | PNG output remains valid |
| PNG low/lowest | Smaller output or graceful unchanged output |
| OBS mode | image controls hidden |
| video player mode | image controls hidden |
| legacy `quality=88&format=jpeg` | JPEG path still works |

Do not mark complete until generated WebP is displayed in the actual target viewer, VRChat or OBS.

## Phase 9: Dev Build Gate

- [ ] Confirm full tests pass:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./...
```

- [ ] Bump the next valid dev version if a build is requested.
- [ ] Regenerate Windows resources when version files change.
- [ ] Build using bundled Go.
- [ ] Record:
  - artifact path
  - app version shown in UI/about
  - SHA-256
  - smoke-test result

## Stop Conditions

Stop and report before continuing if:

- FFmpeg cannot decode HEIC/HEIF or JXL and no acceptable fallback is selected.
- WebP output cannot be displayed by the target viewer.
- Existing unrelated dirty files conflict with required edits.
- Full `go test ./...` fails outside touched areas and the failure is not understood.

## Completion Criteria

The work is complete only when:

- Tests pass with `.tools\go-sdk\go\bin\go.exe test ./...`.
- Manual upload matrix passes.
- Docs reflect supported and deferred formats.
- Any dev build, if cut, launches and performs JPEG -> WebP successfully.
