# Modern Image Format Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add v1.5 image-format recovery support: default WebP output, AVIF/HEIC/HEIF/JPEG XL input, preset image settings, PNG optimization, and release verification.

**Architecture:** Keep conversion behavior in `internal/imageproc`; keep form parsing and UI behavior in `internal/server`. Modern compressed input is decoded to an in-memory image before the existing resize/output path, output selection remains JPEG/PNG/WebP, and unavailable optional optimizers never block uploads.

**Tech Stack:** Go 1.25, `internal/imageproc`, FFmpeg, optional pngquant/oxipng, `internal/server/ui.go`, bundled Go SDK at `.tools\go-sdk\go\bin\go.exe`.

---

## Scope

Included:

- WebP as the default output format.
- AVIF, HEIC, HEIF, and JPEG XL as input-only formats.
- JPEG/PNG/WebP preset quality parsing.
- PNG `lossless` and lossy optimization presets.
- UI preset controls for image settings.
- Runtime and manual verification.

Excluded:

- Text/Word document prerendering.
- PDF rendering.
- Animated/multi-frame output preservation.
- WebP/AVIF/JXL as animated output.
- Final `v1.5.0` release unless all release checks pass.

## File Map

Create:

- `internal/imageproc/webp_encode.go`  
  Encodes a flattened image to WebP through FFmpeg.

- `internal/imageproc/modern_decode.go`  
  Decodes AVIF/HEIC/HEIF/JPEG XL still images through FFmpeg when standard Go decoders fail.

- `internal/imageproc/tools.go`  
  Resolves pngquant and oxipng paths, plus proactive image-tool validation.

- `internal/imageproc/png_optimize.go`  
  Runs PNG optimization best-effort after PNG output is written.

Modify:

- `internal/imageproc/processor.go`  
  Adds WebP/PNG options, modern input fallback, WebP output, and PNG optimization hook.

- `internal/imageproc/processor_test.go`  
  Adds tests for WebP defaults/output, modern input routing, and PNG optimizer behavior.

- `internal/server/server.go`  
  Adds preset quality maps and updates `optionsFromValues`.

- `internal/server/server_test.go`  
  Adds tests for preset parsing and legacy numeric quality compatibility.

- `internal/server/ui.go`  
  Replaces free-form image setting inputs with selects, adds modern input extensions, and hides image-only controls in video/OBS modes.

- `internal/server/ui_media_test.go`  
  Adds tests for upload accept list and preset controls.

- `docs/ROADMAP.md` and release notes  
  Records the finished v1.5 recovery behavior.

---

### Task 1: RED Tests for Defaults, Presets, and Supported Inputs

**Files:**
- Modify: `internal/imageproc/processor_test.go`
- Modify: `internal/server/server_test.go`
- Modify: `internal/server/ui_media_test.go`

- [ ] **Step 1: Add imageproc default/output tests**

Add focused tests:

```go
func TestDefaultOptionsUsesWebP(t *testing.T) {
	opts := DefaultOptions()
	if opts.Format != "webp" {
		t.Fatalf("Format = %q, want webp", opts.Format)
	}
	if opts.WebPQuality != 80 {
		t.Fatalf("WebPQuality = %d, want 80", opts.WebPQuality)
	}
	if opts.PNGQuality != "lossless" {
		t.Fatalf("PNGQuality = %q, want lossless", opts.PNGQuality)
	}
}

func TestProcessWebPOutput(t *testing.T) {
	dir := t.TempDir()
	opts := DefaultOptions()
	opts.Format = "webp"
	result, err := Process(bytes.NewReader(testPNG(t)), "input.png", dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.ContentType != "image/webp" {
		t.Fatalf("ContentType = %q, want image/webp", result.ContentType)
	}
	if result.PublicName != "current.webp" {
		t.Fatalf("PublicName = %q, want current.webp", result.PublicName)
	}
	if info, err := os.Stat(result.Path); err != nil || info.Size() == 0 {
		t.Fatalf("webp output missing or empty: info=%v err=%v", info, err)
	}
}
```

- [ ] **Step 2: Add modern input routing tests**

Use tiny fixture files under `internal/imageproc/testdata/` when available. If real fixtures are not committed, generate them with FFmpeg in the test setup and skip only when FFmpeg cannot encode that fixture format on the developer machine.

```go
func TestModernImageExtensionsAreNotCameraRAW(t *testing.T) {
	for _, name := range []string{"sample.avif", "sample.heic", "sample.heif", "sample.jxl"} {
		if IsCameraRAWName(name) {
			t.Fatalf("%s classified as camera RAW", name)
		}
	}
}
```

- [ ] **Step 3: Add server preset parsing tests**

```go
func TestOptionsFromValuesQualityPresets(t *testing.T) {
	form := url.Values{
		"format":  {"webp"},
		"quality": {"high"},
		"maxMB":   {"20"},
	}
	opts := optionsFromValues(form.Get)
	if opts.Format != "webp" || opts.WebPQuality != 80 {
		t.Fatalf("opts = %+v, want webp high", opts)
	}
}

func TestOptionsFromValuesLegacyJPEGQuality(t *testing.T) {
	form := url.Values{"format": {"jpeg"}, "quality": {"88"}}
	opts := optionsFromValues(form.Get)
	if opts.Format != "jpeg" || opts.JPEGQuality != 88 {
		t.Fatalf("opts = %+v, want jpeg quality 88", opts)
	}
}
```

- [ ] **Step 4: Add UI tests**

Assert `ui.go` contains:

- `image/avif`
- `image/heic`
- `image/heif`
- `image/jxl`
- `.avif`
- `.heic`
- `.heif`
- `.jxl`
- `id="formatSelect"`
- `id="qualitySelect"`

- [ ] **Step 5: Run RED tests**

Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc ./internal/server
```

Expected: FAIL because WebP options/output, modern decode, and preset UI do not exist yet.

### Task 2: Extend `imageproc.Options` and Format Normalization

**Files:**
- Modify: `internal/imageproc/processor.go`
- Test: `internal/imageproc/processor_test.go`

- [ ] **Step 1: Add fields**

Update `Options`:

```go
type Options struct {
	MaxDimension  int
	Format        string
	JPEGQuality   int
	WebPQuality   int
	PNGQuality    string
	MaxInputBytes int64
	MaxBytes      int64
}
```

- [ ] **Step 2: Update defaults**

```go
func DefaultOptions() Options {
	return Options{
		MaxDimension:  2048,
		Format:        "webp",
		JPEGQuality:   85,
		WebPQuality:   80,
		PNGQuality:    "lossless",
		MaxInputBytes: maxImageBytes,
		MaxBytes:      30 << 20,
	}
}
```

- [ ] **Step 3: Normalize options in `Process`**

Use:

```go
opts.Format = strings.ToLower(opts.Format)
switch opts.Format {
case "jpeg", "jpg":
	opts.Format = "jpeg"
case "png", "webp":
default:
	opts.Format = "jpeg"
}
if opts.JPEGQuality <= 0 || opts.JPEGQuality > 100 {
	opts.JPEGQuality = 85
}
if opts.WebPQuality <= 0 || opts.WebPQuality > 100 {
	opts.WebPQuality = 80
}
if opts.PNGQuality == "" {
	opts.PNGQuality = "lossless"
}
```

- [ ] **Step 4: Run tests**

Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc
```

Expected: default option tests pass; WebP output tests still fail until Task 4.

### Task 3: Add Modern Compressed Input Decode

**Files:**
- Create: `internal/imageproc/modern_decode.go`
- Modify: `internal/imageproc/processor.go`
- Test: `internal/imageproc/processor_test.go`

- [ ] **Step 1: Add extension detection**

```go
func isModernCompressedImageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".avif", ".heic", ".heif", ".jxl":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 2: Add FFmpeg still-image decoder**

```go
func decodeModernCompressedImage(input []byte, name, outDir string, maxDimension int) (image.Image, error) {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return nil, fmt.Errorf("FFmpeg is required to decode %s: %w", filepath.Ext(name), err)
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(name))
	in, err := os.CreateTemp(outDir, "modern-source-*"+ext)
	if err != nil {
		return nil, err
	}
	inPath := in.Name()
	defer os.Remove(inPath)
	if _, err := in.Write(input); err != nil {
		_ = in.Close()
		return nil, err
	}
	if err := in.Close(); err != nil {
		return nil, err
	}
	out, err := os.CreateTemp(outDir, "modern-decoded-*.png")
	if err != nil {
		return nil, err
	}
	outPath := out.Name()
	_ = out.Close()
	defer os.Remove(outPath)
	args := []string{"-y", "-hide_banner", "-loglevel", "error", "-i", inPath, "-frames:v", "1"}
	if maxDimension > 0 {
		args = append(args, "-vf", rawScaleFilter(maxDimension))
	}
	args = append(args, outPath)
	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	output, err := video.CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, trimCommandOutput(output))
	}
	file, err := os.Open(outPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return png.Decode(file)
}
```

- [ ] **Step 3: Wire into `decodeImage`**

Keep order:

```go
img, format, err := image.Decode(bytes.NewReader(input))
if err == nil {
	return img, format, nil
}
if isModernCompressedImageName(name) {
	modernImg, modernErr := decodeModernCompressedImage(input, name, outDir, maxDimension)
	if modernErr != nil {
		return nil, "", fmt.Errorf("%w; modern image fallback failed: %v", err, modernErr)
	}
	return modernImg, "modern", nil
}
if !IsCameraRAWName(name) {
	return nil, "", err
}
```

- [ ] **Step 4: Add capability notes to tests**

Modern format fixture tests must fail clearly when decode is absent. When FFmpeg lacks a decoder for a specific format, record the exact FFmpeg error and keep the task open until a pinned helper path is selected.

- [ ] **Step 5: Run tests**

Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc
```

Expected: modern input tests pass where FFmpeg supports decoding; unsupported FFmpeg decoders produce a clear action item.

### Task 4: Add WebP Output Encoding

**Files:**
- Create: `internal/imageproc/webp_encode.go`
- Modify: `internal/imageproc/processor.go`
- Test: `internal/imageproc/processor_test.go`

- [ ] **Step 1: Add encoder**

```go
func EncodeWebP(src image.Image, outPath string, quality int) error {
	if quality <= 0 || quality > 100 {
		quality = 80
	}
	dir := filepath.Dir(outPath)
	tmp, err := os.CreateTemp(dir, "webp-source-*.png")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := png.Encode(tmp, flatten(src)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return err
	}
	cmd := exec.Command(ffmpeg, "-y", "-hide_banner", "-loglevel", "error", "-i", tmpPath, "-quality", strconv.Itoa(quality), outPath)
	hideWindow(cmd)
	output, err := video.CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimCommandOutput(output))
	}
	info, err := os.Stat(outPath)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return fmt.Errorf("webp output is empty")
	}
	return nil
}
```

- [ ] **Step 2: Add WebP branch in `Process`**

Use `.webp`, `image/webp`, and `current.webp`. Read the resulting file size with `os.Stat` and enforce `opts.MaxBytes`.

- [ ] **Step 3: Run tests**

Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc
```

Expected: WebP output tests pass.

### Task 5: Add PNG Optimization

**Files:**
- Create: `internal/imageproc/tools.go`
- Create: `internal/imageproc/png_optimize.go`
- Modify: `internal/imageproc/processor.go`
- Test: `internal/imageproc/processor_test.go`

- [ ] **Step 1: Add tool resolution API**

```go
func EnsurePngquant() (string, error)
func EnsureOxipng() (string, error)
func ValidateImageTools()
```

Resolution order:

1. `IMAGEPAD_PNGQUANT` / `IMAGEPAD_OXIPNG`
2. ImagePad managed bin directory
3. `exec.LookPath`
4. `("", nil)` when unavailable

- [ ] **Step 2: Add optimizer**

```go
func OptimizePNG(path string, quality string) (int64, error) {
	if quality == "" || quality == "lossless" {
		_ = runOxipngIfAvailable(path)
		return fileSize(path)
	}
	_ = runPngquantIfAvailable(path, quality)
	_ = runOxipngIfAvailable(path)
	return fileSize(path)
}
```

Quality map:

```go
var pngQualityRanges = map[string][2]int{
	"highest": {90, 100},
	"high":    {75, 90},
	"medium":  {60, 75},
	"low":     {45, 60},
	"lowest":  {30, 45},
}
```

- [ ] **Step 3: Hook after PNG write**

Use `png.Encoder{CompressionLevel: png.BestCompression}` and run `OptimizePNG(path, opts.PNGQuality)` after `os.WriteFile`.

- [ ] **Step 4: Run tests**

Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/imageproc
```

Expected: PNG tests pass even with no pngquant/oxipng installed.

### Task 6: Add Server Quality Presets

**Files:**
- Modify: `internal/server/server.go`
- Test: `internal/server/server_test.go`

- [ ] **Step 1: Add maps**

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
```

- [ ] **Step 2: Update `optionsFromValues`**

```go
if v := value("quality"); v != "" {
	if q, ok := qualityPresetToJPEG[v]; ok {
		opts.JPEGQuality = q
	} else if q, err := strconv.Atoi(v); err == nil {
		opts.JPEGQuality = q
	}
	if q, ok := qualityPresetToWebP[v]; ok {
		opts.WebPQuality = q
	}
	opts.PNGQuality = v
}
```

- [ ] **Step 3: Run tests**

Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/server
```

Expected: preset and legacy numeric quality tests pass.

### Task 7: Update Upload UI

**Files:**
- Modify: `internal/server/ui.go`
- Test: `internal/server/ui_media_test.go`

- [ ] **Step 1: Add accept extensions**

Add MIME/extension entries to the upload `accept` attribute:

```text
image/avif,image/heic,image/heif,image/jxl,.avif,.heic,.heif,.jxl
```

- [ ] **Step 2: Replace image setting inputs**

Use selects:

- max dimension: `2048`, `1024`, `512`, `256`, `128`
- format: `png`, `webp`, `jpeg`
- quality: dynamic `qualitySelect`
- max MB: `30`, `20`, `10`, `5`, `1`

- [ ] **Step 3: Add dynamic quality JS**

```javascript
const qualityOptions = {
  png: [
    { value: 'lossless', label: '非劣化', selected: true },
    { value: 'highest', label: '最高' },
    { value: 'high', label: '高' },
    { value: 'medium', label: '中' },
    { value: 'low', label: '低' },
    { value: 'lowest', label: '最低' },
  ],
  webp: [
    { value: 'highest', label: '最高' },
    { value: 'high', label: '高', selected: true },
    { value: 'medium', label: '中' },
    { value: 'low', label: '低' },
    { value: 'lowest', label: '最低' },
  ],
  jpeg: [
    { value: 'highest', label: '最高' },
    { value: 'high', label: '高', selected: true },
    { value: 'medium', label: '中' },
    { value: 'low', label: '低' },
    { value: 'lowest', label: '最低' },
  ],
};
```

- [ ] **Step 4: Fix controls visibility**

Add:

```javascript
function updateUploadControlsVisibility() {
  if (uploadControls) {
    uploadControls.hidden = obsMode || state.videoPlayerEnabled;
  }
}
```

Call from `setUploadMode()` and `applyVideoPlayer()`.

- [ ] **Step 5: Run tests**

Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./internal/server
```

Expected: UI string tests pass.

### Task 8: Startup Validation and Documentation

**Files:**
- Modify: `internal/server/server.go`
- Modify: `docs/ROADMAP.md`
- Modify: release notes for the next dev build

- [ ] **Step 1: Start image-tool validation**

Call `imageproc.ValidateImageTools()` near existing video tool validation. It must not fail startup.

- [ ] **Step 2: Update docs**

Document:

- default output is WebP
- supported inputs: JPEG, PNG, GIF, WebP, BMP, TIFF, SVG, RAW, AVIF, HEIC/HEIF, JPEG XL
- unsupported/deferred inputs: TXT/DOCX/DOC, PDF, ICO/ICNS
- PNG optimization is best-effort

- [ ] **Step 3: Run full tests**

Run:

```powershell
rtk test .tools\go-sdk\go\bin\go.exe test ./...
```

Expected: PASS.

### Task 9: Manual Runtime Verification

**Files:**
- No source changes unless defects are found.

- [ ] **Step 1: Start the app**

Use the repo's normal local command or build artifact.

- [ ] **Step 2: Upload matrix**

Verify:

- JPEG input -> WebP output
- PNG transparent input -> WebP output with expected flattened background
- AVIF input -> selected output
- HEIC/HEIF input -> selected output
- JPEG XL input -> selected output
- PNG lossless -> valid PNG
- PNG low/lowest -> optimized or gracefully unchanged
- legacy `quality=88&format=jpeg` -> JPEG output

- [ ] **Step 3: UI matrix**

Verify:

- image mode shows controls
- OBS mode hides image controls
- video player mode hides image controls
- quality options change when format changes

- [ ] **Step 4: VRChat/OBS smoke**

Confirm the generated WebP output displays in the actual target viewer before marking the feature complete.

### Task 10: Dev Build Gate

**Files:**
- Modify only after all implementation checks pass:
  - `internal/about/about.go`
  - `winres/winres.json`
  - release notes under `dist/` if required

- [ ] **Step 1: Choose next valid dev version**

Use the repo's existing version policy. Do not cut final `v1.5.0` unless all v1.5 checks pass.

- [ ] **Step 2: Build**

Use bundled Go:

```powershell
rtk proxy pwsh -NoProfile -Command ".tools\go-sdk\go\bin\go.exe build -trimpath -ldflags '-H=windowsgui' -o dist\manual\imagepadserver-modern-formats.exe ."
```

- [ ] **Step 3: Smoke**

Launch the artifact, perform JPEG -> WebP upload, record artifact path and checksum.

## Self-Review

- Spec coverage: WebP output, AVIF/HEIC/HEIF/JXL input, presets, PNG optimization, UI visibility, documentation, and release verification are covered.
- Placeholder scan: no implementation step depends on unspecified code ownership.
- Type consistency: `WebPQuality`, `PNGQuality`, `EncodeWebP`, `OptimizePNG`, and `ValidateImageTools` names are consistent across tasks.
