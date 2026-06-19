# Analysis and Rendering Tickets

These tickets build the visual output without touching queue or server integration.

## Locked rendering architecture

- AV-204 decodes audio to 48 kHz stereo float PCM and returns deterministic 30 fps analysis frames.
- AV-301 and AV-303 produce static RGBA/PNG assets.
- AV-302 writes a deterministic ASS subtitle file for title, artist, album, and per-second time text. libass/FreeType provides antialiasing and clipping.
- AV-401 streams complete non-text/non-waveform RGBA frames to FFmpeg over stdin as `rawvideo`; FFmpeg reads the original audio as the second input, reuses the existing `showwaves` waveform behavior, applies the ASS subtitles, and encodes HLS.
- Do not write an uncompressed full-track rawvideo file to disk.

### AV-301: Render deterministic fallback artwork

**Dependencies:** AV-104, AV-204. **Parallel wave:** 4.

**Files:**
- Create: `internal/video/fallback_artwork.go`
- Create: `internal/video/fallback_artwork_test.go`
- Create: `internal/video/testdata/golden/fallback-720.png`

Required API:

```go
type Palette struct { Start, End color.RGBA }
func PaletteForFeatures(features AudioFeatures) Palette
func RenderFallbackArtwork(ctx context.Context, ffmpeg string, fonts FontSet, features AudioFeatures, foreground color.RGBA, size int) (*image.RGBA, error)
```

- [ ] Add boundary tests in the exact first-match order: high-energy, bass-focused, bright, calm, default.
- [ ] Add a deterministic pixel/golden test for a 288 px tile.

```go
func TestRenderFallbackArtworkIsDeterministic(t *testing.T) {
	f := AudioFeatures{BPM: 132, IntegratedLUFS: -10}
	a, err := RenderFallbackArtwork(context.Background(), testFFmpeg(t), testFonts(t), f, color.RGBA{255,255,255,224}, 288)
	if err != nil { t.Fatal(err) }
	b, err := RenderFallbackArtwork(context.Background(), testFFmpeg(t), testFonts(t), f, color.RGBA{255,255,255,224}, 288)
	if err != nil { t.Fatal(err) }
	if !bytes.Equal(pngBytes(t, a), pngBytes(t, b)) { t.Fatal("render changed") }
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil { t.Fatal(err) }
	return out.Bytes()
}

func testFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := ffmpegPath()
	if err != nil { t.Skipf("ffmpeg unavailable: %v", err) }
	return path
}

func testFonts(t *testing.T) FontSet {
	t.Helper()
	fonts, err := VisualizerFonts()
	if err != nil { t.Skipf("fonts unavailable: %v", err) }
	return fonts
}
```

- [ ] Run focused tests; expected RED.
- [ ] Draw the diagonal palette gradient and 64 round-capped radial lines in Go exactly as spec section 14.2.
- [ ] Render `♪` to a transparent PNG with FFmpeg `drawtext` using the 600-weight Noto instance at 168 canonical pixels, scan its alpha bounds, and composite that visual bounding box at tile center `(144,144)`.
- [ ] Run focused, golden, and package tests.
- [ ] Commit `feat: render audio fallback artwork`.

### AV-302: Generate canonical layout and ASS text tracks

**Dependencies:** AV-100, AV-102, AV-104. **Parallel wave:** 4.

**Files:**
- Create: `internal/video/visualizer_layout.go`
- Create: `internal/video/visualizer_layout_test.go`
- Create: `internal/video/visualizer_ass.go`
- Create: `internal/video/visualizer_ass_test.go`

Required API:

```go
type Rect struct { X, Y, W, H int }
type VisualizerLayout struct { Artwork, Title, Artist, Album, Spectrum, Loudness, Progress, Time Rect }
type TextMetrics struct { Width, Height int }
func LayoutForSize(width, height int) (VisualizerLayout, error)
func ScrollOffset(elapsed, textWidth, viewportWidth float64) float64
func FormatMediaTime(seconds int) string
func MeasureTextWithFFmpeg(ctx context.Context, ffmpeg, fontPath, text string, fontSize int) (TextMetrics, error)
func BuildVisualizerASS(metadata AudioMetadata, duration float64, layout VisualizerLayout, fonts FontSet, metrics map[string]TextMetrics) string
```

The metrics map keys are exactly `title`, `artist`, and `album`; omit the album key only when album text is empty.

- [ ] Add tests for exact 720p coordinates and uniform 1080p/360p scaling.
- [ ] Add scroll cases at `2.999`, `3.0`, `4.0`, end-of-scroll, and reset.

```go
func TestScrollOffset(t *testing.T) {
	if got := ScrollOffset(2.999, 900, 752); got != 0 { t.Fatalf("pause=%v", got) }
	if got := ScrollOffset(4.0, 900, 752); got != -40 { t.Fatalf("scroll=%v", got) }
	cycle := 3.0 + (900.0-752.0)/40.0
	if got := ScrollOffset(cycle, 900, 752); got != 0 { t.Fatalf("reset=%v", got) }
}
```

- [ ] Run focused tests; expected RED.
- [ ] Measure text by rendering each string once with FFmpeg `drawtext` onto a transparent oversized PNG using the exact static font file and font size, then scan nontransparent alpha bounds. Do not estimate width from rune count.
- [ ] Generate ASS styles using explicit Regular 400, Medium 500, and SemiBold 600 font files.
- [ ] Generate `\clip`, `\pos`, and `\move` events per field/cycle; do not shrink, wrap, or ellipsize.
- [ ] Generate one time event per second using the same integer second that drives the progress marker.
- [ ] Run package tests and commit `feat: define visualizer layout and text tracks`.

### AV-303: Prepare artwork and blurred background

**Dependencies:** AV-201, AV-301. **Parallel wave:** 4.

**Files:**
- Create: `internal/video/visualizer_background.go`
- Create: `internal/video/visualizer_background_test.go`
- Create: `internal/video/testdata/golden/artwork-dark-720.png`
- Create: `internal/video/testdata/golden/artwork-light-720.png`

Required API:

```go
type ForegroundMode struct { Color color.RGBA; Overlay color.RGBA }
func SelectForegroundMode(background image.Image, metadataRect, graphRect image.Rectangle) ForegroundMode
func PrepareVisualizerBase(ctx context.Context, ffmpeg, artworkPath string, fallback *image.RGBA, layout VisualizerLayout, outPath string) (ForegroundMode, error)
```

- [ ] Add tests for center crop, 24 px artwork radius, no reflection, shadow offset, dark/light foreground selection, and contrast ratio >= 4.5.
- [ ] Run focused tests; expected RED.
- [ ] Use FFmpeg `scale`, `crop`, and `gblur=sigma=64` for the full-frame source; use Go alpha masks for rounded foreground artwork and shadow.
- [ ] Increase overlay opacity in 5-point steps up to 60% until both measured regions pass.
- [ ] Run golden and package tests.
- [ ] Commit `feat: prepare visualizer artwork background`.

### AV-401: Stream the shared audio visualizer into HLS

**Dependencies:** AV-204, AV-302, AV-303. **Parallel:** prohibited.

**Files:**
- Create: `internal/video/audio_visualizer.go`
- Create: `internal/video/audio_visualizer_test.go`
- Create: `internal/video/audio_hls_integration_test.go`
- Modify: `internal/video/soundcloud.go` only to delete the obsolete fallback-art and SoundCloud-only renderer after replacement tests are GREEN.
- Modify: `internal/video/soundcloud_test.go` only to delete obsolete renderer expectations.

Required API:

```go
func RunAudioVisualizerHLS(ctx context.Context, outDir, ffmpeg string, input AudioRenderInput, id string, preset QualityPreset) error
func WriteVisualizerRGBAFrames(ctx context.Context, dst io.Writer, input AudioRenderInput, width, height int) error
func AudioVisualizerFFmpegArgs(audioPath, assPath, fontDir, id string, preset QualityPreset) []string
```

- [ ] Add argument tests that require raw RGBA stdin at 30 fps, source audio as input 1, `showwaves=s=752x168:rate=30:mode=line` overlaid at `(432,320)` in front of bars, ASS subtitles with explicit `fontsdir`, H.264/yuv420p, AAC 48 kHz stereo, HLS EVENT, 2-second segments, and final even 16:9 crop.
- [ ] Add frame tests for 24 bars, transparent bar bottoms, waveform foreground order, 1000-point graph, four guide lines, progress marker position, and no text in raw frames.
- [ ] Run focused tests; expected RED.
- [ ] Implement frame generation from `AudioAnalysis.Frames`; never recompute FFT in the renderer.
- [ ] Launch FFmpeg with `pipe:0` for RGBA frames and the source audio path as the second input. Write frames in timestamp order and close stdin exactly once.
- [ ] Split the audio input: preserve one branch for AAC encoding and feed the other into the existing `showwaves` behavior. Give the waveform the global foreground color at 55% alpha and overlay it before ASS text.
- [ ] Apply ASS after the waveform overlay, then encode. Propagate FFmpeg exit errors and cancellation.
- [ ] Add a short synthetic runtime test guarded by `testing.Short()` and tool availability.
- [ ] Probe its result and assert 1280x720, H.264, yuv420p, 30 fps, AAC, 48000 Hz, stereo, and successful full decode.
- [ ] Run `rtk go test ./internal/video -run 'Test(AudioVisualizer|AudioHLS)' -count=1 -v` and package tests.
- [ ] Commit `feat: render shared audio visualizer HLS`.

## Rendering failure rules

- Missing font: explicit error, no OS fallback.
- Missing loudness envelope: fail render, no flat substitute.
- Invalid duration: fail before starting FFmpeg.
- Broken artwork: move to next artwork candidate or fallback tile.
- FFmpeg without `subtitles`/libass: explicit toolchain capability error from AV-100.
- Broken pipe after context cancellation: report cancellation, not generic encode failure.
