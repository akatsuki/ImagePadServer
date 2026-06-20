# Visualizer FFT Resolution + LUFS Loudness + Absolute Spectrum Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the hyperactive low-frequency bar (bar0) by raising the analysis FFT to 8192, normalize music-mode tracks to -14 LUFS, and render the spectrum bars on an absolute (fixed-window) scale against that normalized signal.

**Architecture:** Three independent changes. (1) Raise `fftWindowSize` from 2048 to 8192 so the lowest bands are resolved instead of collapsing onto the single leakage-heavy bin 1. (2) In music mode only, transcode the downloaded source to a loudness-normalized intermediate (two-pass EBU R128 loudnorm, target -14 LUFS) once, and feed that file to *both* analysis and render so the bars and the audio reflect the same signal. (3) Replace the per-track 95th-percentile relative normalization in `normalizeSpectrumTrack` with a fixed absolute dB window, calibrated against -14 LUFS content.

**Tech Stack:** Go, gonum `dsp/fourier`, ffmpeg (`loudnorm` filter, `print_format=json`), Go testing.

**Out of scope (discussed, deferred):** A-weighting / spectral tilt, hill-shape weighting, and a bar-count / resolution upgrade (24 → 32 bars ≈ 1/3 octave, which must be paired with a 40 Hz low bound so the narrowest bars still cover ≥1 FFT bin, plus drawing-geometry rework — "future if possible" priority). These build on top of this work and are separate plans.

---

### Task 1: Raise the analysis FFT window to 8192 and tighten the band range to 30 Hz–16 kHz

**Files:**
- Modify: `internal/video/audio_analysis.go:27`
- Modify: `internal/video/spectrum_math.go:8-61` (doc comment + band edges)
- Test: `internal/video/spectrum_math_test.go`

The 2048-point FFT at 48 kHz gives 23.4 Hz bins, so bar0 and a real 60 Hz tone both collapse onto bin 1. Raising to 8192 (5.86 Hz bins) resolves the low bands. At the same time, tighten the band range from 20 Hz–20 kHz to 30 Hz–16 kHz so no bar sits in a near-dead zone (sub-30 Hz or >16 kHz). Both are spectrum-only changes that belong together so calibration (Task 7) is done once on the final layout.

- [ ] **Step 1: Write the failing test**

Drive `fractionalLogBandEnergies` end-to-end with a synthetic Hann-windowed 60 Hz sine at the production window size. The dominant band must NOT be bar0, and the 60 Hz fundamental must dwarf the sub-bass bar0.

Add to `internal/video/spectrum_math_test.go`:

```go
func TestLowToneLandsAboveBar0(t *testing.T) {
	// 60 Hz sine, Hann-windowed, at the production FFT size.
	n := fftWindowSize
	pcm := make([]float64, n)
	for i := range pcm {
		w := 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(n-1)))
		pcm[i] = math.Sin(2*math.Pi*60*float64(i)/float64(sampleRate)) * w
	}
	fft := fourier.NewFFT(n)
	coeff := fft.Coefficients(nil, pcm)
	bands := fractionalLogBandEnergies(coeff, sampleRate)

	// Find the dominant band; it must be a low-mid band, not the lowest bar.
	maxIdx := 0
	for b := 1; b < 24; b++ {
		if bands[b] > bands[maxIdx] {
			maxIdx = b
		}
	}
	if maxIdx == 0 {
		t.Fatalf("bar0 should not be the dominant band for a 60 Hz tone")
	}
	if bands[maxIdx] <= bands[0]*4 {
		t.Fatalf("60 Hz tone did not dominate its band: bar0=%.4g dominant[%d]=%.4g", bands[0], maxIdx, bands[maxIdx])
	}
}
```

Add imports `"math"` and `"gonum.org/v1/gonum/dsp/fourier"` to the test file if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestLowToneLandsAboveBar0 -v`
Expected: FAIL — with `fftWindowSize = 2048`, bin 1 (11.7–35.2 Hz) leakage inflates bar0 so the 4x margin is not met.

- [ ] **Step 3a: Raise the window size**

In `internal/video/audio_analysis.go`, change the constant:

```go
	fftWindowSize   = 8192
```

Leave `frameAdvance = 1600` unchanged — it still drives the 30 fps frame cadence (48000/1600 = 30); only frequency resolution improves.

- [ ] **Step 3b: Tighten the band range to 30 Hz–16 kHz**

In `internal/video/spectrum_math.go`, add range constants and use them for the band edges in `fractionalLogBandEnergies`. Replace:

```go
		loFreq := 20.0 * math.Pow(1000.0, float64(b)/24.0)
		hiFreq := 20.0 * math.Pow(1000.0, float64(b+1)/24.0)
```

with:

```go
		loFreq := spectrumFMin * math.Pow(spectrumFMax/spectrumFMin, float64(b)/24.0)
		hiFreq := spectrumFMin * math.Pow(spectrumFMax/spectrumFMin, float64(b+1)/24.0)
```

and declare the constants near the top of `spectrum_math.go`:

```go
const (
	// spectrumFMin / spectrumFMax bound the 24 log-spaced visualizer bands.
	// 30 Hz–16 kHz keeps every bar inside the range where real music has
	// energy: below 30 Hz is sub-bass rumble, above 16 kHz is inaudible air.
	spectrumFMin = 30.0
	spectrumFMax = 16000.0
)
```

Leave `mapLogBands` (the decorative 64-band fingerprint) at its existing 20 Hz–20 kHz mapping — it is unrelated to the bars.

Update the doc comment on `fractionalLogBandEnergies` so it states the size and range in use:

```go
// fractionalLogBandEnergies maps FFT coefficients into 24 logarithmic bands
// spanning spectrumFMin..spectrumFMax (30 Hz–16 kHz) using fractional bin
// overlap.
...
// sampleRate must match the sample rate used to produce coeff.
// The analyzer uses an 8192-point FFT at 48 kHz (coeff length 4097),
// giving ~5.86 Hz bin spacing so the lowest bands are resolved.
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run TestLowToneLandsAboveBar0 -v`
Expected: PASS

- [ ] **Step 5: Run the full video package to surface golden/threshold drift**

Run: `go test ./internal/video`
Expected: spectrum/fingerprint/mood golden tests may fail because magnitudes (~4x) and centroid/lowRatio averages shift. For each failure: confirm the new behavior is correct (better low-frequency resolution) and update the golden expectation, OR widen a threshold that was incidentally tuned to 2048. Do NOT update a golden without first confirming the new value is sensible. Record each updated golden in the commit message.

- [ ] **Step 6: Commit**

```bash
git add internal/video/audio_analysis.go internal/video/spectrum_math.go internal/video/spectrum_math_test.go
git commit -m "fix: raise spectrum FFT to 8192 so low bands resolve instead of collapsing onto bin 1"
```

---

### Task 2: Parse the full loudnorm measurement as JSON

**Files:**
- Modify: `internal/video/audio_analysis.go:669-708` (near `extractLUFS`)
- Test: `internal/video/audio_analysis_test.go`

Two-pass loudnorm needs the measured input values from pass 1 to do an accurate linear normalization in pass 2. `loudnorm=...:print_format=json` emits a JSON block; parse it into a struct.

- [ ] **Step 1: Write the failing test**

Add to `internal/video/audio_analysis_test.go`:

```go
func TestParseLoudnormJSON(t *testing.T) {
	out := `[Parsed_loudnorm_0 @ 0x55] 
{
	"input_i" : "-18.40",
	"input_tp" : "-2.10",
	"input_lra" : "7.30",
	"input_thresh" : "-28.60",
	"target_offset" : "0.50"
}
`
	m, err := parseLoudnormJSON(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m.InputI != -18.40 || m.InputTP != -2.10 || m.InputLRA != 7.30 ||
		m.InputThresh != -28.60 || m.TargetOffset != 0.50 {
		t.Fatalf("unexpected measurement: %+v", m)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestParseLoudnormJSON -v`
Expected: FAIL — `parseLoudnormJSON` and `LoudnormMeasurement` undefined.

- [ ] **Step 3: Implement the parser**

Add to `internal/video/audio_analysis.go`:

```go
// LoudnormMeasurement holds the pass-1 values emitted by ffmpeg's loudnorm
// filter with print_format=json. They feed an accurate linear pass-2.
type LoudnormMeasurement struct {
	InputI       float64
	InputTP      float64
	InputLRA     float64
	InputThresh  float64
	TargetOffset float64
}

// parseLoudnormJSON extracts the loudnorm measurement object from ffmpeg
// stderr. The filter prints a single JSON object after a bracketed header.
func parseLoudnormJSON(s string) (LoudnormMeasurement, error) {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return LoudnormMeasurement{}, fmt.Errorf("no loudnorm JSON object found")
	}
	var raw struct {
		InputI       string `json:"input_i"`
		InputTP      string `json:"input_tp"`
		InputLRA     string `json:"input_lra"`
		InputThresh  string `json:"input_thresh"`
		TargetOffset string `json:"target_offset"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &raw); err != nil {
		return LoudnormMeasurement{}, fmt.Errorf("parse loudnorm JSON: %w", err)
	}
	parse := func(v string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64); return f }
	return LoudnormMeasurement{
		InputI:       parse(raw.InputI),
		InputTP:      parse(raw.InputTP),
		InputLRA:     parse(raw.InputLRA),
		InputThresh:  parse(raw.InputThresh),
		TargetOffset: parse(raw.TargetOffset),
	}, nil
}
```

Add `"encoding/json"` to the import block in `audio_analysis.go` if not already present (it is present in `music_download.go`, not necessarily here — verify and add).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run TestParseLoudnormJSON -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/video/audio_analysis.go internal/video/audio_analysis_test.go
git commit -m "feat: parse ffmpeg loudnorm JSON measurement for two-pass normalization"
```

---

### Task 3: Build the two-pass loudnorm filter string

**Files:**
- Modify: `internal/video/audio_analysis.go`
- Test: `internal/video/audio_analysis_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestLoudnormFilterString(t *testing.T) {
	m := LoudnormMeasurement{InputI: -18.4, InputTP: -2.1, InputLRA: 7.3, InputThresh: -28.6, TargetOffset: 0.5}
	got := loudnormFilter(m, -14.0)
	want := "loudnorm=I=-14.0:TP=-1.0:LRA=11.0:" +
		"measured_I=-18.4:measured_TP=-2.1:measured_LRA=7.3:measured_thresh=-28.6:" +
		"offset=0.5:linear=true:print_format=summary"
	if got != want {
		t.Fatalf("loudnorm filter mismatch:\n got %q\nwant %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestLoudnormFilterString -v`
Expected: FAIL — `loudnormFilter` undefined.

- [ ] **Step 3: Implement**

```go
// loudnormFilter builds an accurate (linear) two-pass loudnorm filter string
// targeting targetLUFS, using the pass-1 measurement. TP -1.0 dBTP and LRA 11
// match common streaming presets.
func loudnormFilter(m LoudnormMeasurement, targetLUFS float64) string {
	return fmt.Sprintf(
		"loudnorm=I=%.1f:TP=-1.0:LRA=11.0:"+
			"measured_I=%g:measured_TP=%g:measured_LRA=%g:measured_thresh=%g:"+
			"offset=%g:linear=true:print_format=summary",
		targetLUFS, m.InputI, m.InputTP, m.InputLRA, m.InputThresh, m.TargetOffset,
	)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run TestLoudnormFilterString -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/video/audio_analysis.go internal/video/audio_analysis_test.go
git commit -m "feat: build accurate two-pass loudnorm filter string"
```

---

### Task 4: Normalize a music source to a -14 LUFS intermediate file

**Files:**
- Modify: `internal/video/music_download.go`
- Test: `internal/video/music_download_test.go`

This is the single normalization point. Pass 1 measures; pass 2 writes a FLAC intermediate (lossless, avoids lossy-on-lossy before the final AAC encode). Both analysis and render later consume this path.

- [ ] **Step 1: Write the failing test (argument capture)**

Follow the existing arg-capture pattern in `music_download_test.go` (it already captures yt-dlp args). Add a test that captures the pass-2 ffmpeg args produced by `normalizeLoudnessArgs` (a pure helper, no process execution):

```go
func TestNormalizeLoudnessArgs(t *testing.T) {
	m := LoudnormMeasurement{InputI: -20, InputTP: -1.5, InputLRA: 6, InputThresh: -30, TargetOffset: 0.2}
	args := normalizeLoudnessArgs("in.webm", "out.flac", m, -14.0)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-i in.webm", "-af loudnorm=I=-14.0", "-c:a flac", "out.flac"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in args: %s", want, joined)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestNormalizeLoudnessArgs -v`
Expected: FAIL — `normalizeLoudnessArgs` undefined.

- [ ] **Step 3: Implement the helper and the runner**

In `internal/video/music_download.go`:

```go
// normalizeLoudnessArgs builds the pass-2 ffmpeg arguments that apply an
// accurate loudnorm to src and write a lossless FLAC intermediate at dst.
func normalizeLoudnessArgs(src, dst string, m LoudnormMeasurement, targetLUFS float64) []string {
	return []string{
		"-v", "error",
		"-i", src,
		"-af", loudnormFilter(m, targetLUFS),
		"-ar", "48000",
		"-ac", "2",
		"-c:a", "flac",
		"-y", dst,
	}
}

// NormalizeMusicLoudness produces a -14 LUFS FLAC next to src and returns its
// path. Pass 1 measures via extractLoudnormMeasurement; pass 2 applies it.
func NormalizeMusicLoudness(ctx context.Context, ffmpeg, src string) (string, error) {
	m, err := extractLoudnormMeasurement(ctx, ffmpeg, src)
	if err != nil {
		return "", fmt.Errorf("measure loudness: %w", err)
	}
	dst := strings.TrimSuffix(src, filepath.Ext(src)) + ".norm.flac"
	cmd := exec.CommandContext(ctx, ffmpeg, normalizeLoudnessArgs(src, dst, m, -14.0)...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("apply loudnorm: %w\n%s", err, stderr.String())
	}
	return dst, nil
}
```

Add `extractLoudnormMeasurement` in `audio_analysis.go` next to `extractLUFS` (pass 1, JSON):

```go
// extractLoudnormMeasurement runs loudnorm pass 1 and returns the measured
// values needed for an accurate pass 2.
func extractLoudnormMeasurement(ctx context.Context, ffmpeg, sourcePath string) (LoudnormMeasurement, error) {
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-v", "info",
		"-i", sourcePath,
		"-af", "loudnorm=I=-14:TP=-1.0:LRA=11.0:print_format=json",
		"-f", "null", "-",
	)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return LoudnormMeasurement{}, fmt.Errorf("loudnorm pass 1: %w\n%s", err, stderr.String())
	}
	return parseLoudnormJSON(stderr.String())
}
```

Add imports to `music_download.go`: `"bytes"`, `"context"`, `"os/exec"` (verify which are missing; `context` is already imported).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run TestNormalizeLoudnessArgs -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/video/music_download.go internal/video/audio_analysis.go internal/video/music_download_test.go
git commit -m "feat: normalize music sources to a -14 LUFS FLAC intermediate"
```

---

### Task 5: Route the normalized intermediate into acquisition (music mode only)

**Files:**
- Modify: `internal/video/music_download.go:94-101` (`DownloadMusic` return)
- Modify: `internal/server/audio_upload.go` (the music acquisition helper that calls `DownloadMusic`)
- Test: `internal/server/music_mode_test.go`

The normalized file must become the `SourcePath` used downstream, but only in music mode. Normalize inside the server-side music acquisition helper (it already has the ffmpeg path and runs probing), so `DownloadMusic` stays a pure download.

- [ ] **Step 1: Write the failing test**

In `internal/server/music_mode_test.go`, add a test asserting the acquisition helper calls a normalization hook and substitutes the returned path as the analyzed/rendered source. Use the existing injection seam (the music downloader is already injectable per Task 3 of the music-mode plan); add a parallel injectable `normalizeLoudness func(ctx, ffmpeg, src) (string, error)` and assert the substituted path flows to the publish/queue call. Mirror the existing routing-test structure in this file.

(Write the assertion against whatever publish/queue spy the existing tests use; do not invent a new harness.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server -run MusicMode -v`
Expected: FAIL — no normalization seam; source path is the raw download.

- [ ] **Step 3: Implement**

In the server music acquisition helper, after a successful `DownloadMusic`, call the injected normalizer (default `video.NormalizeMusicLoudness`) and replace `acquired.SourcePath` with the result before invoking the existing publish/queue visualizer functions. On normalization error, log and fall back to the un-normalized source (do not fail the whole publish).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server -run MusicMode -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/audio_upload.go internal/server/music_mode_test.go internal/video/music_download.go
git commit -m "feat: feed the -14 LUFS intermediate to the visualizer in music mode"
```

---

### Task 6: Replace relative normalization with an absolute fixed dB window

**Files:**
- Modify: `internal/video/spectrum_math.go:140-237` (`normalizeSpectrumTrack`)
- Test: `internal/video/spectrum_math_test.go`

With loudness anchored at -14 LUFS, the per-track 95th-percentile reference is no longer needed and is exactly what makes every bar swing full-scale. Replace it with a fixed mapping so bar height reflects absolute energy.

- [ ] **Step 1: Write the failing test**

```go
func TestAbsoluteNormalizationIsNotGainInvariant(t *testing.T) {
	// A single quiet frame must map LOWER than the same frame amplified.
	quiet := [][24]float64{{}}
	loud := [][24]float64{{}}
	for b := 0; b < 24; b++ {
		quiet[0][b] = 0.01
		loud[0][b] = 1.0 // +40 dB
	}
	q := normalizeSpectrumTrack(quiet)
	l := normalizeSpectrumTrack(loud)
	if !(l[0][0] > q[0][0]+0.3) {
		t.Fatalf("absolute mapping must rank loud above quiet: quiet=%.3f loud=%.3f", q[0][0], l[0][0])
	}
}
```

This directly contradicts the old "constant-gain invariant" contract — that is the intended behavior change.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestAbsoluteNormalizationIsNotGainInvariant -v`
Expected: FAIL — current code normalizes each track to its own p95, so both map identically.

- [ ] **Step 3: Implement the absolute mapping**

Replace the body of `normalizeSpectrumTrack` with a fixed-window mapping and remove the percentile/floor-median statistics. Use named constants (calibrated in Task 7):

```go
const (
	// spectrumRefDB is the band magnitude (in dB, 20*log10|coeff|) that maps to
	// a full-height bar for -14 LUFS content at the 8192-point FFT scale.
	// Calibrated in Task 7.
	spectrumRefDB = 0.0 // PLACEHOLDER — set in Task 7
	// spectrumRangeDB is the dynamic range below the reference that maps to 0.
	spectrumRangeDB = 60.0
)

// normalizeSpectrumTrack maps raw per-frame band magnitudes to [0,1] using a
// fixed absolute window. Because music-mode audio is loudness-normalized to a
// known anchor, a fixed reference replaces the old per-track percentile so bar
// height reflects absolute energy and bars no longer all swing full-scale.
func normalizeSpectrumTrack(raw [][24]float64) [][24]float64 {
	if len(raw) == 0 {
		return raw
	}
	lo := spectrumRefDB - spectrumRangeDB
	result := make([][24]float64, len(raw))
	for fi, frame := range raw {
		for bi, v := range frame {
			if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				result[fi][bi] = 0
				continue
			}
			db := 20.0 * math.Log10(v)
			norm := (db - lo) / spectrumRangeDB
			if norm < 0 {
				norm = 0
			} else if norm > 1 {
				norm = 1
			}
			result[fi][bi] = norm
		}
	}
	return result
}
```

Remove now-unused `sort` import from `spectrum_math.go` if nothing else uses it.

- [ ] **Step 4: Update/replace the old invariance tests**

Find tests in `spectrum_math_test.go` (and `visualizer_*_test.go`) that assert constant-gain invariance or p95 behavior of `normalizeSpectrumTrack`. Delete or rewrite them to assert the absolute mapping (they encode the contract we deliberately changed). Run `go test ./internal/video -run Spectrum -v` and fix each failure by confirming the new value is correct before editing the expectation.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/video -run TestAbsoluteNormalizationIsNotGainInvariant -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/video/spectrum_math.go internal/video/spectrum_math_test.go
git commit -m "feat: render spectrum bars on an absolute fixed dB window"
```

---

### Task 7: Calibrate spectrumRefDB against -14 LUFS content

**Files:**
- Modify: `internal/video/spectrum_math.go` (the `spectrumRefDB` constant)
- Test: `internal/video/spectrum_math_test.go` (calibration guard)

The placeholder reference must be set to a value where loud passages of a -14 LUFS track fill ~90–100% and quiet passages sit low. Derive it empirically, then lock it with a guard test so future FFT/window changes can't silently break the calibration.

- [ ] **Step 1: Measure the reference on a real normalized track**

Pick a representative track. Run:

```bash
# 1) normalize, then dump the analysis. Use a tiny throwaway main or an
#    existing debug test that prints the 95th-percentile band dB across frames.
```

Write a temporary test `TestCalibrationProbe` (skipped in CI via `t.Skip` unless an env var is set) that normalizes a local sample, runs `AnalyzeAudio`, collects all pre-normalization band magnitudes, converts to dB, and logs the 95th percentile. Set `spectrumRefDB` to that logged value.

- [ ] **Step 2: Lock it with a guard test**

```go
func TestSpectrumRefDBCalibrated(t *testing.T) {
	if spectrumRefDB == 0.0 {
		t.Fatal("spectrumRefDB is still the placeholder; calibrate against -14 LUFS content (Task 7)")
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/video -run 'TestSpectrumRefDBCalibrated|Spectrum' -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/video/spectrum_math.go internal/video/spectrum_math_test.go
git commit -m "chore: calibrate absolute spectrum reference for -14 LUFS content"
```

---

### Task 8: Full suite + Windows build

**Files:** none (verification only)

- [ ] **Step 1: Run the full test suite**

Run: `go test ./...`
Expected: PASS. Address any remaining golden drift from the FFT and normalization changes, confirming each new value before updating.

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 3: Manual smoke (music mode)**

Publish one music-mode URL, confirm: (a) bar0 no longer pins to the top, (b) the output plays at a consistent loudness versus a second track, (c) loud sections fill the bars and quiet intros sit low.

- [ ] **Step 4: Commit any test updates**

```bash
git add -A
git commit -m "test: update goldens for 8192 FFT and absolute spectrum"
```

---

### Self-review

- **Spec coverage:** FFT 8192 (Task 1); -14 LUFS loudnorm via two-pass measure+apply (Tasks 2–4); normalized source feeds both analysis and render through one intermediate file (Tasks 4–5); absolute fixed-window bars on the normalized signal (Tasks 6–7); verification (Task 8). The user's three asks — FFT8192, loudnorm, absolute bars on the normalized signal — are all assigned.
- **Placeholder scan:** `spectrumRefDB = 0.0` is an explicit, guarded placeholder resolved in Task 7 with a failing guard test; no other placeholders.
- **Type consistency:** `LoudnormMeasurement` fields (`InputI/InputTP/InputLRA/InputThresh/TargetOffset`) are used identically in Tasks 2, 3, 4. `loudnormFilter(m, targetLUFS)`, `normalizeLoudnessArgs(src, dst, m, targetLUFS)`, `NormalizeMusicLoudness(ctx, ffmpeg, src)`, `extractLoudnormMeasurement(ctx, ffmpeg, src)` signatures are consistent across tasks. `normalizeSpectrumTrack([][24]float64) [][24]float64` signature is unchanged (body rewritten).
- **Risk:** Changing `fftWindowSize` also shifts `spectrumFeatures` (mood palette) and `Fingerprint64` (decorative fallback art only — not persisted/compared). Both are acceptable; goldens updated in Tasks 1/8.
