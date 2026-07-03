# Image Format v1.5 Recovery Worklist

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recover the media input work that was planned for v1.5 but is still missing: WebP output, modern compressed image input, preset-based image settings, PNG optimization, and release verification.

**Architecture:** Keep image conversion behavior inside `internal/imageproc`, keep request/UI preset parsing inside `internal/server`, and avoid broad server refactors. WebP uses the already available FFmpeg path, and PNG optimization uses optional image tools and remains best-effort.

**Tech Stack:** Go, `imageproc`, FFmpeg, pngquant, oxipng, server-rendered HTML/JS in `internal/server/ui.go`.

---

## Current Gap

The v1.5 roadmap split this work into `v1.4.12` and `v1.4.13`, but the current main checkout still has:

- `internal/imageproc/processor.go`: only `jpeg` and `png` output; default is `jpeg`.
- `internal/imageproc/processor.go`: input decoding covers common Go-registered formats plus SVG/RAW, but AVIF, HEIC/HEIF, and JPEG XL are not registered or handled by the FFmpeg image fallback.
- `internal/imageproc/`: no `webp_encode.go`, `png_optimize.go`, or image tool resolver file.
- `internal/server/server.go`: `optionsFromValues` only parses numeric JPEG quality.
- `internal/server/ui.go`: upload controls still use free-form number inputs and only hide for OBS mode.

## Input Format Audit

Current input support in the main checkout:

| Format | Current Status | Notes |
|---|---|---|
| JPEG / PNG / GIF | Supported | Registered through the Go standard image packages. Animated GIF is decoded as a still image for this pipeline. |
| WebP | Supported as input | Registered through `golang.org/x/image/webp`; output WebP is still missing and handled elsewhere in this plan. |
| BMP / TIFF | Supported | Registered through `golang.org/x/image/bmp` and `golang.org/x/image/tiff`. |
| SVG | Supported | Rasterized through the existing `oksvg` path. |
| Camera RAW | Supported for listed camera extensions | Uses the existing FFmpeg fallback path. |
| AVIF | Missing; add in this recovery | Major modern web format. Add input-only support now; output remains controlled by selected output format. |
| HEIC / HEIF | Missing; add in this recovery | Important for iPhone/iPad photo import. Treat as input-only and verify against the bundled FFmpeg/dependency path because support depends on decoder availability. |
| JPEG XL (`.jxl`) | Missing; add in this recovery | Modern compressed image format. Treat as input-only and require real fixture verification because ecosystem support is still uneven. |
| ICO / ICNS | Missing; low priority | Common as icons, not a primary ImagePad sharing format. Do not include in this recovery unless user reports real uploads. |

## Deferred Document Input Audit

Document inputs are not image formats, and they are not required for the immediate v1.5 image-format recovery. Keep this as a later feature. When implemented, keep document rendering out of `imageproc`; use a separate document-to-image prerender path and then pass the rendered image through the normal image output pipeline.

| Format | Planned Status | Notes |
|---|---|---|
| Plain text (`.txt`) | Deferred | Render UTF-8 and common Japanese Windows text files to a clean page image. Use a fixed page layout and readable font fallback. |
| Word document (`.docx`) | Deferred | Primary Word target. Prefer a deterministic local renderer path and render the first page initially. |
| Legacy Word (`.doc`) | Best-effort | Support only if the chosen renderer can convert it. If unavailable, return a clear unsupported-format error. |
| Rich Text (`.rtf`) | Optional follow-up | Similar user intent to Word, but not requested now. Do not include unless it falls out naturally from the renderer. |
| PDF | Separate feature | Useful, but not part of this recovery unless explicitly requested; it needs page selection and PDF-specific rendering rules. |

## Work Order

### Task 1: Lock Existing Behavior With Failing Tests

**Files:**
- Modify: `internal/imageproc/processor_test.go`
- Modify: `internal/server/server_test.go`
- Modify: `internal/server/ui_media_test.go`

- [ ] Add a test that `imageproc.DefaultOptions().Format` is `webp`.
- [ ] Add a test that an AVIF file or generated AVIF fixture can be processed as input and converted to the selected output format.
- [ ] Add a test that a HEIC or HEIF file fixture can be processed as input and converted to the selected output format.
- [ ] Add a test that a JPEG XL `.jxl` file fixture can be processed as input and converted to the selected output format.
- [ ] Add a test that `IsCameraRAWName("sample.avif")`, `IsCameraRAWName("sample.heic")`, `IsCameraRAWName("sample.heif")`, and `IsCameraRAWName("sample.jxl")` remain false; these should use the generic image decode/fallback path, not the camera RAW path.
- [ ] Add a test that `Process(..., Format: "webp")` returns `ContentType: image/webp`, `PublicName: current.webp`, and writes a non-empty file.
- [ ] Add a test that `Process(..., Format: "png", PNGQuality: "lossless")` still returns `image/png` even when optimization tools are unavailable.
- [ ] Add server tests for preset parsing:
  - `quality=high&format=jpeg` maps to JPEG 85.
  - `quality=high&format=webp` maps to WebP 80.
  - `quality=lossless&format=png` sets PNG lossless behavior.
  - legacy `quality=88` remains accepted for JPEG.
- [ ] Add UI string tests that confirm the upload form contains preset `<select>` controls for max dimension, format, quality, and max MB.

**Verification:**
- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc ./internal/server`
- [ ] Confirm the new tests fail before implementation for the expected missing WebP/preset behavior.
- [ ] Confirm AVIF/HEIC/HEIF/JXL tests fail because input is not yet decoded, not because fixtures are invalid.

### Task 2: Extend `imageproc.Options` and Defaults

**Files:**
- Modify: `internal/imageproc/processor.go`

- [ ] Add `WebPQuality int` to `Options`.
- [ ] Add `PNGQuality string` to `Options`.
- [ ] Change `DefaultOptions()` to:
  - `Format: "webp"`
  - `JPEGQuality: 85`
  - `WebPQuality: 80`
  - `PNGQuality: "lossless"`
  - `MaxDimension: 2048`
  - `MaxBytes: 30 << 20`
- [ ] Normalize formats so only `jpeg`, `png`, and `webp` are accepted; unknown values fall back to `jpeg` for compatibility.
- [ ] Clamp `WebPQuality` to a sane range, using `80` as fallback.

**Verification:**
- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc`

### Task 2A: Add Modern Compressed Image Input Support

**Files:**
- Modify: `internal/imageproc/processor.go`
- Modify: `internal/server/ui.go`
- Modify: `docs/superpowers/plans/2026-07-03-image-format-v1.5-recovery-worklist.md` only if the audit changes during implementation

- [ ] Add AVIF, HEIC/HEIF, and JPEG XL to the accepted upload types in the file picker:
  - MIME: `image/avif`
  - Extension: `.avif`
  - MIME: `image/heic`, `image/heif`
  - Extensions: `.heic`, `.heif`
  - MIME: `image/jxl`
  - Extension: `.jxl`
- [ ] Add these formats to link placeholder/examples only if the UI has format-specific examples nearby; do not add explanatory UI text just for these formats.
- [ ] Prefer a generic FFmpeg still-image fallback for `.avif`, `.heic`, `.heif`, and `.jxl` first, because ImagePadServer already relies on FFmpeg for RAW and WebP work.
- [ ] Before relying on FFmpeg, add a small capability check or test fixture run that proves the repo's bundled/current FFmpeg can decode each format.
- [ ] If the bundled/current FFmpeg cannot decode one of these formats, choose one explicit fallback for that format:
  - add a small maintained Go decoder if available and compatible with Go 1.25,
  - or add a pinned helper binary in the same style as other managed tools,
  - or mark that format as unavailable at runtime with a clear error while keeping the plan item open until a decoder path is selected.
- [ ] Keep AVIF, HEIC/HEIF, and JPEG XL as input-only. The output remains JPEG/PNG/WebP based on the existing image settings.
- [ ] Preserve the existing `decodeImage` order:
  - SVG detection first.
  - Standard registered decoders next.
  - Modern compressed image fallback after standard decoders.
  - Camera RAW fallback only for camera RAW extensions.
- [ ] Ensure conversion respects `MaxInputBytes`, EXIF/orientation behavior where available, resizing, and selected output format.
- [ ] Keep animated/multi-frame variants explicitly out of scope for this recovery; decode the first still frame only.

**Verification:**
- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc ./internal/server`
- [ ] Upload a real `.avif` image and confirm it publishes successfully as the selected output type.
- [ ] Upload a real `.heic` or `.heif` image and confirm it publishes successfully as the selected output type.
- [ ] Upload a real `.jxl` image and confirm it publishes successfully as the selected output type.
- [ ] Upload a non-matching file renamed to `.avif`, `.heic`, `.heif`, and `.jxl`; confirm each fails with the normal invalid image error.

### Deferred Task: Add Text and Word Document Prerender Input Support

**Files:**
- Create: `internal/documentproc/renderer.go`
- Create: `internal/documentproc/renderer_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/ui.go`
- Modify: `internal/server/ui_media_test.go`

> Deferred: Do not include this task in the immediate WebP / AVIF / HEIC / JPEG XL recovery. Keep it as a later feature plan.

- [ ] Create a `documentproc` package that converts supported document inputs into a temporary PNG page image.
- [ ] Keep the package boundary explicit:
  - Input: source bytes, original filename, output directory, render options.
  - Output: path to rendered PNG, width, height, and a short render note for logs.
  - No server state or HTTP request dependencies inside `documentproc`.
- [ ] Add `.txt` rendering:
  - Detect UTF-8 first.
  - Fall back to Windows Japanese text decoding only if a supported decoder is already available or added intentionally.
  - Render onto a white page with dark text.
  - Preserve line breaks.
  - Wrap long lines to page width.
  - Support writing direction as a render option.
  - Use a landscape image for vertical Japanese writing.
  - Use a portrait image for horizontal writing.
  - If direction is not explicitly selected, default `.txt` to horizontal writing and portrait image unless a later UI choice overrides it.
  - Cap rendered text length to avoid huge memory use.
- [ ] Add `.docx` rendering:
  - Prefer converting DOCX to PDF or image via LibreOffice if available.
  - If LibreOffice is not available, use a small Go DOCX text extraction fallback that renders plain text only.
  - Treat layout-faithful DOCX rendering as best-effort unless the selected renderer proves stable in tests.
  - When the renderer exposes page orientation or writing mode, preserve it.
  - When using the plain-text fallback, apply the same rule as `.txt`: vertical writing renders to landscape, horizontal writing renders to portrait.
- [ ] Add `.doc` handling:
  - Use LibreOffice when available.
  - If unavailable, return a clear unsupported-format error.
- [ ] Render the first page only for the initial recovery.
- [ ] After prerendering, pass the rendered PNG through the same image output path so max dimension, output format, quality preset, and max MB still apply.
- [ ] Add these accepted upload types:
  - MIME: `text/plain`
  - Extension: `.txt`
  - MIME: `application/vnd.openxmlformats-officedocument.wordprocessingml.document`
  - Extension: `.docx`
  - MIME: `application/msword`
  - Extension: `.doc`
- [ ] Keep drag-and-drop labels generic enough to include documents without adding long explanatory UI copy.
- [ ] Add document render controls only if they fit the existing upload settings surface without clutter:
  - writing direction: horizontal / vertical
  - page image direction derived from writing direction, not separately configurable in the first pass
- [ ] Add clear errors for encrypted/password-protected Word files, unsupported legacy `.doc`, and renderer startup failures.

**Verification:**
- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./internal/documentproc ./internal/server`
- [ ] Upload a UTF-8 `.txt` file and confirm it publishes as the selected output type.
- [ ] Upload a horizontal-writing `.txt` file and confirm the rendered image is portrait.
- [ ] Upload a vertical-writing `.txt` file and confirm the rendered image is landscape.
- [ ] Upload a Japanese `.txt` file and confirm readable output or a clear encoding error, depending on the chosen decoder path.
- [ ] Upload a `.docx` file and confirm the first page publishes as the selected output type.
- [ ] Upload a `.doc` file and confirm either successful conversion or a clear unsupported-format error.

### Task 3: Add WebP Encoding

**Files:**
- Create: `internal/imageproc/webp_encode.go`
- Modify: `internal/imageproc/processor.go`

- [ ] Implement `EncodeWebP(src image.Image, outPath string, quality int) error`.
- [ ] Reuse `flatten(src)` before encoding; keep alpha handling consistent with JPEG output.
- [ ] Write a temporary PNG in the output directory.
- [ ] Use FFmpeg to encode WebP from the temporary PNG.
- [ ] Use hidden-window command setup on Windows via existing `hideWindow`.
- [ ] Validate the WebP output exists and has size greater than zero.
- [ ] In `Process`, add the `webp` branch:
  - extension `.webp`
  - content type `image/webp`
  - public name `current.webp`
  - max byte check after FFmpeg output.

**Verification:**
- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc`
- [ ] Manually upload a JPEG or PNG and confirm the published current media is WebP.

### Task 4: Add Quality Preset Parsing

**Files:**
- Modify: `internal/server/server.go`

- [ ] Add `qualityPresetToJPEG`.
- [ ] Add `qualityPresetToWebP`.
- [ ] Add PNG preset handling with values:
  - `lossless`
  - `highest`
  - `high`
  - `medium`
  - `low`
  - `lowest`
- [ ] Update `optionsFromValues` so one `quality` form value populates JPEG, WebP, and PNG fields as appropriate.
- [ ] Preserve legacy numeric JPEG quality support.
- [ ] Keep `maxDimension` and `maxMB` parsing compatible with existing form submissions.

**Verification:**
- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./internal/server`

### Task 5: Replace Image Settings UI With Presets

**Files:**
- Modify: `internal/server/ui.go`

- [ ] Replace max dimension number input with a select:
  - `2048`
  - `1024`
  - `512`
  - `256`
  - `128`
- [ ] Replace format select with:
  - `png`: non-lossy PNG
  - `webp`: high-quality WebP, selected by default
  - `jpeg`: high-compression JPEG
- [ ] Replace quality number input with a dynamic select.
- [ ] Replace max MB number input with:
  - `30`
  - `20`
  - `10`
  - `5`
  - `1`
- [ ] Add JS quality option sets:
  - PNG: `lossless`, `highest`, `high`, `medium`, `low`, `lowest`
  - WebP/JPEG: `highest`, `high`, `medium`, `low`, `lowest`
- [ ] On format change, rebuild the quality select without losing a valid current selection where possible.

**Verification:**
- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./internal/server`
- [ ] Launch the app and confirm the upload controls show presets, not free-form numeric fields.

### Task 6: Fix Upload Control Visibility for Video Player Mode

**Files:**
- Modify: `internal/server/ui.go`

- [ ] Add `updateUploadControlsVisibility()`.
- [ ] Replace `uploadControls.hidden = obsMode` with `updateUploadControlsVisibility()`.
- [ ] Call the same function from `applyVideoPlayer()` after `state.videoPlayerEnabled` changes are applied.
- [ ] Hide image-only controls when `obsMode || state.videoPlayerEnabled`.

**Verification:**
- [ ] Launch the app.
- [ ] Confirm image controls are visible in normal image mode.
- [ ] Confirm image controls are hidden in OBS mode.
- [ ] Confirm image controls are hidden when video player mode is enabled.

### Task 7: Add PNG Optimization Tool Resolution

**Files:**
- Create: `internal/imageproc/tools.go`

- [ ] Add `EnsurePngquant() (string, error)`.
- [ ] Add `EnsureOxipng() (string, error)`.
- [ ] Support environment overrides:
  - `IMAGEPAD_PNGQUANT`
  - `IMAGEPAD_OXIPNG`
  - `IMAGEPAD_PNGQUANT_SHA256`
  - `IMAGEPAD_OXIPNG_SHA256`
- [ ] Resolve tools in this order:
  - environment override
  - app-managed bin directory
  - PATH
  - unavailable as `("", nil)`
- [ ] Keep unavailable tools non-fatal.

**Verification:**
- [ ] Run focused imageproc tests with env override paths.
- [ ] Confirm missing tools do not fail PNG processing.

### Task 8: Add PNG Optimization Pipeline

**Files:**
- Create: `internal/imageproc/png_optimize.go`
- Modify: `internal/imageproc/processor.go`

- [ ] Implement `OptimizePNG(path string, quality string) (int64, error)`.
- [ ] For `quality == "lossless"` or empty, skip pngquant and run oxipng only.
- [ ] For other presets, run pngquant with the matching quality range, then oxipng.
- [ ] Use best-effort behavior: log and keep the original file if a tool is missing or optimization fails.
- [ ] Change PNG encoding in `Process` to `png.Encoder{CompressionLevel: png.BestCompression}`.
- [ ] Run `OptimizePNG` after the PNG file is written.
- [ ] Re-check final file size against `MaxBytes`.

**Verification:**
- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc`
- [ ] Upload PNG with `lossless` and with lossy presets; compare output sizes.

### Task 9: Add Startup Validation for Image Tools

**Files:**
- Modify: `internal/imageproc/tools.go`
- Modify: `internal/server/server.go` or the actual startup owner if tool validation is launched elsewhere

- [ ] Add `ValidateImageTools()`.
- [ ] Call it at startup in the same style as video tool validation.
- [ ] Keep validation asynchronous or non-blocking if current video tool validation is non-blocking.
- [ ] Log tool availability without failing app startup.

**Verification:**
- [ ] Start the app with no pngquant/oxipng installed.
- [ ] Confirm startup continues.
- [ ] Confirm logs show image tool validation attempts or skips.

### Task 10: Documentation and Release Notes

**Files:**
- Modify: `docs/ROADMAP.md`
- Modify: `README.md` if user-facing upload defaults are documented there
- Modify: release notes for the next dev build

- [ ] Move image format work from planned/missing into the current active release checklist.
- [ ] Document default image output as WebP.
- [ ] Document AVIF, HEIC/HEIF, and JPEG XL as supported input formats, not as output formats.
- [ ] Document the format audit decision:
  - AVIF: included in this recovery.
  - HEIC/HEIF: included in this recovery for iPhone/iPad photo import.
  - JPEG XL: included in this recovery, with decoder capability verification required.
  - TXT/DOCX/DOC: deferred; later prerendered document input, routed separately from image decoding.
  - ICO/ICNS: low priority.
- [ ] Document PNG lossless vs lossy preset behavior.
- [ ] Document optional environment variables for pngquant and oxipng.
- [ ] Note that unavailable PNG optimization tools do not block uploads.

**Verification:**
- [ ] Re-read docs with UTF-8 in PowerShell before editing if text looks corrupted.

### Task 11: End-to-End Verification

**Files:**
- No source changes unless defects are found.

- [ ] Run `rtk test .tools\go-sdk\go\bin\go.exe test ./...`
- [ ] Upload JPEG -> default WebP output.
- [ ] Upload AVIF -> selected output type is produced successfully.
- [ ] Upload HEIC/HEIF -> selected output type is produced successfully.
- [ ] Upload JPEG XL -> selected output type is produced successfully.
- [ ] Upload transparent PNG -> WebP output has expected flattened background.
- [ ] Upload PNG lossless -> PNG output remains valid.
- [ ] Upload PNG low/lowest -> PNG output is smaller or gracefully unchanged if tools are missing.
- [ ] Upload with legacy numeric quality value -> JPEG path still works.
- [ ] Verify VRChat or OBS can display the generated WebP output before calling the feature complete.
- [ ] Verify UI behavior with video player mode on/off and OBS mode on/off.

### Task 12: Cut Recovery Dev Build

**Files:**
- Modify only the repo's normal version/release files after implementation passes:
  - `internal/about/about.go`
  - `winres/winres.json`
  - release notes under `dist/` if this repo's release flow requires them

- [ ] Bump to the next repo-valid dev version, not a final `v1.5.0` tag unless all v1.5 release checks pass.
- [ ] Regenerate Windows resources if required by the repo release flow.
- [ ] Build Windows artifact with the repo's established Go toolchain.
- [ ] Record checksum and smoke-test launch.

**Verification:**
- [ ] Confirm version shown by the app matches the artifact.
- [ ] Confirm the dev artifact can perform the default WebP upload path.
