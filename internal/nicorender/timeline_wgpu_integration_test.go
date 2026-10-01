package nicorender

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const timelineIntegrationRandomSeed uint32 = 0x4e435431

type timelineIntegrationReferenceManifest struct {
	SchemaVersion  int               `json:"schemaVersion"`
	VideoID        string            `json:"videoId"`
	CommentCount   int               `json:"commentCount"`
	SnapshotSHA    string            `json:"snapshotSha256"`
	SourceSHA      string            `json:"sourceSha256"`
	BundleSHA      string            `json:"bundleSha256"`
	TestRandomSeed uint32            `json:"testRandomSeed"`
	Reference      TimelineReference `json:"reference"`
}

func applyTimelineIntegrationRenderOverrides(options RenderOptions, getenv func(string) string) (RenderOptions, bool, bool, error) {
	parse := func(name string, min, max int64) (int64, bool, error) {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			return 0, false, nil
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < min || value > max {
			return 0, true, fmt.Errorf("%s=%q must be an integer in %d..%d", name, raw, min, max)
		}
		return value, true, nil
	}

	anyOverride := false
	if value, set, err := parse("NICO_TIMELINE_REFERENCE_WIDTH", 1, 3840); err != nil {
		return RenderOptions{}, false, false, err
	} else if set {
		options.Width = int(value)
		anyOverride = true
	}
	if value, set, err := parse("NICO_TIMELINE_REFERENCE_HEIGHT", 1, 2160); err != nil {
		return RenderOptions{}, false, false, err
	} else if set {
		options.Height = int(value)
		anyOverride = true
	}
	fpsNumChanged := false
	if value, set, err := parse("NICO_TIMELINE_REFERENCE_FPS_NUM", 1, 1_000_000); err != nil {
		return RenderOptions{}, false, false, err
	} else if set {
		options.FPSNum = value
		anyOverride = true
		fpsNumChanged = true
	}
	fpsDenChanged := false
	if value, set, err := parse("NICO_TIMELINE_REFERENCE_FPS_DEN", 1, 1_000_000); err != nil {
		return RenderOptions{}, false, false, err
	} else if set {
		options.FPSDen = value
		anyOverride = true
		fpsDenChanged = true
	}
	if options.FPSNum > 60*options.FPSDen {
		return RenderOptions{}, false, false, fmt.Errorf("reference frame rate %d/%d exceeds 60 fps", options.FPSNum, options.FPSDen)
	}
	return options, anyOverride, fpsNumChanged || fpsDenChanged, nil
}

func TestTimelineIntegrationRenderOverrides(t *testing.T) {
	base := RenderOptions{Width: 1920, Height: 1080, DurationMs: 12000, FPSNum: 30, FPSDen: 1}
	for _, tc := range []struct {
		name           string
		values         map[string]string
		want           RenderOptions
		wantOverride   bool
		wantRateChange bool
	}{
		{
			name: "720p 30fps",
			values: map[string]string{
				"NICO_TIMELINE_REFERENCE_WIDTH":  "1280",
				"NICO_TIMELINE_REFERENCE_HEIGHT": "720",
			},
			want:         RenderOptions{Width: 1280, Height: 720, DurationMs: 12000, FPSNum: 30, FPSDen: 1},
			wantOverride: true,
		},
		{
			name: "720p 60fps",
			values: map[string]string{
				"NICO_TIMELINE_REFERENCE_WIDTH":   "1280",
				"NICO_TIMELINE_REFERENCE_HEIGHT":  "720",
				"NICO_TIMELINE_REFERENCE_FPS_NUM": "60",
			},
			want:           RenderOptions{Width: 1280, Height: 720, DurationMs: 12000, FPSNum: 60, FPSDen: 1},
			wantOverride:   true,
			wantRateChange: true,
		},
		{
			name:           "1080p 60fps",
			values:         map[string]string{"NICO_TIMELINE_REFERENCE_FPS_NUM": "60"},
			want:           RenderOptions{Width: 1920, Height: 1080, DurationMs: 12000, FPSNum: 60, FPSDen: 1},
			wantOverride:   true,
			wantRateChange: true,
		},
		{
			name: "fractional 60000/1001",
			values: map[string]string{
				"NICO_TIMELINE_REFERENCE_FPS_NUM": "60000",
				"NICO_TIMELINE_REFERENCE_FPS_DEN": "1001",
			},
			want:           RenderOptions{Width: 1920, Height: 1080, DurationMs: 12000, FPSNum: 60000, FPSDen: 1001},
			wantOverride:   true,
			wantRateChange: true,
		},
		{
			name: "4:3",
			values: map[string]string{
				"NICO_TIMELINE_REFERENCE_WIDTH":  "960",
				"NICO_TIMELINE_REFERENCE_HEIGHT": "720",
			},
			want:         RenderOptions{Width: 960, Height: 720, DurationMs: 12000, FPSNum: 30, FPSDen: 1},
			wantOverride: true,
		},
		{
			name: "square",
			values: map[string]string{
				"NICO_TIMELINE_REFERENCE_WIDTH":  "720",
				"NICO_TIMELINE_REFERENCE_HEIGHT": "720",
			},
			want:         RenderOptions{Width: 720, Height: 720, DurationMs: 12000, FPSNum: 30, FPSDen: 1},
			wantOverride: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, overridden, rateChanged, err := applyTimelineIntegrationRenderOverrides(base, func(name string) string {
				return tc.values[name]
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.Width != tc.want.Width || got.Height != tc.want.Height || got.FPSNum != tc.want.FPSNum || got.FPSDen != tc.want.FPSDen || overridden != tc.wantOverride || rateChanged != tc.wantRateChange {
				t.Fatalf("options=%+v overridden=%t rateChanged=%t, want %+v overridden=%t rateChanged=%t", got, overridden, rateChanged, tc.want, tc.wantOverride, tc.wantRateChange)
			}
		})
	}
}

func TestTimelineIntegrationRenderOverridesRejectInvalidValues(t *testing.T) {
	base := RenderOptions{Width: 1920, Height: 1080, DurationMs: 12000, FPSNum: 30, FPSDen: 1}
	for _, tc := range []struct {
		name   string
		values map[string]string
	}{
		{name: "zero width", values: map[string]string{"NICO_TIMELINE_REFERENCE_WIDTH": "0"}},
		{name: "width over limit", values: map[string]string{"NICO_TIMELINE_REFERENCE_WIDTH": "3841"}},
		{name: "height over limit", values: map[string]string{"NICO_TIMELINE_REFERENCE_HEIGHT": "2161"}},
		{name: "bad width", values: map[string]string{"NICO_TIMELINE_REFERENCE_WIDTH": "wide"}},
		{name: "zero fps numerator", values: map[string]string{"NICO_TIMELINE_REFERENCE_FPS_NUM": "0"}},
		{name: "zero fps denominator", values: map[string]string{"NICO_TIMELINE_REFERENCE_FPS_DEN": "0"}},
		{name: "fps over 60", values: map[string]string{"NICO_TIMELINE_REFERENCE_FPS_NUM": "60001", "NICO_TIMELINE_REFERENCE_FPS_DEN": "1000"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := applyTimelineIntegrationRenderOverrides(base, func(name string) string {
				return tc.values[name]
			}); err == nil {
				t.Fatal("invalid timeline reference override was accepted")
			}
		})
	}
}

func TestNicoTimelineIntegrationBrowserWGPU(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_INTEGRATION") != "1" {
		t.Skip("set NICO_TIMELINE_INTEGRATION=1 to capture browser reference and run the WGPU compositor")
	}

	artifactDir := requiredTimelineEnv(t, "NICO_TIMELINE_ARTIFACTS")
	assertOutsideTimelineRepo(t, artifactDir, "artifact directory")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatal(err)
	}
	helperPath := requiredTimelineEnv(t, "IMAGEPAD_NICO_TIMELINE_COMPOSITOR")
	if info, err := os.Stat(helperPath); err != nil || info.IsDir() {
		t.Fatalf("WGPU compositor is unavailable: %s (%v)", helperPath, err)
	}
	python := strings.TrimSpace(os.Getenv("NICO_TIMELINE_PYTHON"))
	if python == "" {
		python = "python"
	}
	browserPath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER"))
	if browserPath == "" {
		var err error
		browserPath, err = findBrowser()
		if err != nil {
			t.Fatalf("NICO_TIMELINE_INTEGRATION=1 requires Chromium: %v", err)
		}
	}

	catalog := readTimelineFixtureCatalog(t)
	snapshot := loadTimelineFixture(t, "sm9-362")
	if snapshot.VideoID != catalog.RealInput.VideoID || snapshot.CommentCount != catalog.RealInput.CommentCount {
		t.Fatalf("real snapshot identity=%s/%d, want %s/%d", snapshot.VideoID, snapshot.CommentCount, catalog.RealInput.VideoID, catalog.RealInput.CommentCount)
	}
	options := RenderOptions{
		Width:       catalog.RealInput.Capture.Width,
		Height:      catalog.RealInput.Capture.Height,
		DurationMs:  catalog.RealInput.Capture.DurationsMs[len(catalog.RealInput.Capture.DurationsMs)-1],
		FPSNum:      catalog.RealInput.Capture.FPSNum,
		FPSDen:      catalog.RealInput.Capture.FPSDen,
		BrowserPath: browserPath,
		Transport:   "binary",
		BatchFrames: 30,
	}
	if bundlePath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_REFERENCE_BUNDLE")); bundlePath != "" {
		options.BundlePath = bundlePath
	}
	options, renderOverridesApplied, frameRateOverridden, err := applyTimelineIntegrationRenderOverrides(options, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if rawBatchFrames := strings.TrimSpace(os.Getenv("NICO_TIMELINE_REFERENCE_BATCH_FRAMES")); rawBatchFrames != "" {
		batchFrames, err := strconv.Atoi(rawBatchFrames)
		if err != nil || batchFrames < 1 || batchFrames > 120 {
			t.Fatalf("NICO_TIMELINE_REFERENCE_BATCH_FRAMES=%q must be in 1..120", rawBatchFrames)
		}
		options.BatchFrames = batchFrames
	}
	durationOverridden := false
	if rawDurationMs := strings.TrimSpace(os.Getenv("NICO_TIMELINE_REFERENCE_DURATION_MS")); rawDurationMs != "" {
		durationMs, err := strconv.ParseInt(rawDurationMs, 10, 64)
		if err != nil || durationMs <= 0 || durationMs > options.DurationMs {
			t.Fatalf("NICO_TIMELINE_REFERENCE_DURATION_MS=%q must be in 1..%d", rawDurationMs, options.DurationMs)
		}
		options.DurationMs = durationMs
		durationOverridden = true
	}
	layoutProbeOnly := os.Getenv("NICO_TIMELINE_LAYOUT_PROBE_ONLY") == "1"
	if layoutProbeOnly && !durationOverridden {
		options.DurationMs = 33
		probePath := filepath.Join(artifactDir, "browser-layout-probe.json")
		if err := os.Setenv("NICO_TIMELINE_LAYOUT_PROBE_PATH", probePath); err != nil {
			t.Fatalf("set layout probe output path: %v", err)
		}
		defer os.Unsetenv("NICO_TIMELINE_LAYOUT_PROBE_PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	testRandomSeed := timelineIntegrationRandomSeed
	var reference TimelineReference
	referencePixelPath := ""
	referenceManifestPath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_REFERENCE_MANIFEST"))
	if referenceManifestPath != "" {
		if renderOverridesApplied || durationOverridden {
			t.Fatal("NICO_TIMELINE_REFERENCE_MANIFEST cannot be combined with render-option overrides")
		}
		if layoutProbeOnly {
			t.Fatal("NICO_TIMELINE_REFERENCE_MANIFEST cannot be combined with NICO_TIMELINE_LAYOUT_PROBE_ONLY")
		}
		assertOutsideTimelineRepo(t, referenceManifestPath, "reference manifest")
		manifestBytes, err := os.ReadFile(referenceManifestPath)
		if err != nil {
			t.Fatalf("read browser reference manifest: %v", err)
		}
		var manifest timelineIntegrationReferenceManifest
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			t.Fatalf("decode browser reference manifest: %v", err)
		}
		if manifest.SchemaVersion != 1 || manifest.VideoID != snapshot.VideoID || manifest.CommentCount != snapshot.CommentCount || manifest.SnapshotSHA != catalog.RealInput.SnapshotSHA256 || manifest.SourceSHA != catalog.RealInput.SourceSHA256 || manifest.BundleSHA != catalog.BundleSHA256 || manifest.TestRandomSeed != timelineIntegrationRandomSeed {
			t.Fatalf("browser reference manifest identity does not match the current fixture and seeded renderer: %+v", manifest)
		}
		reference = manifest.Reference
		if reference.PixelStream == "" || filepath.Base(reference.PixelStream) != reference.PixelStream {
			t.Fatalf("browser reference pixel stream must be a file name, got %q", reference.PixelStream)
		}
		referencePixelPath = filepath.Join(filepath.Dir(referenceManifestPath), reference.PixelStream)
		if info, err := os.Stat(referencePixelPath); err != nil || info.IsDir() {
			t.Fatalf("browser reference pixels are unavailable: %s (%v)", referencePixelPath, err)
		}
	} else {
		reference = captureTimelineReferenceWithRandomSeed(ctx, t, snapshot, options, &testRandomSeed)
	}
	stem := fmt.Sprintf("sm9-%dx%d-%dfps-%df", reference.Width, reference.Height, reference.FPSNum/reference.FPSDen, reference.FrameCount)
	if !layoutProbeOnly && !durationOverridden && !frameRateOverridden && reference.FrameCount != int64(catalog.RealInput.Capture.Frames[len(catalog.RealInput.Capture.Frames)-1]) {
		t.Fatalf("browser reference frames=%d, want %d", reference.FrameCount, catalog.RealInput.Capture.Frames[len(catalog.RealInput.Capture.Frames)-1])
	}
	if reference.PixelStreamSHA == "" || reference.NonZeroAlpha == 0 || reference.PartialAlpha == 0 {
		t.Fatalf("browser reference is incomplete: %+v", reference)
	}
	if referenceManifestPath == "" {
		referenceManifest := timelineIntegrationReferenceManifest{
			SchemaVersion:  1,
			VideoID:        snapshot.VideoID,
			CommentCount:   snapshot.CommentCount,
			SnapshotSHA:    catalog.RealInput.SnapshotSHA256,
			SourceSHA:      catalog.RealInput.SourceSHA256,
			BundleSHA:      catalog.BundleSHA256,
			TestRandomSeed: timelineIntegrationRandomSeed,
			Reference:      reference,
		}
		referenceManifestBytes, err := json.MarshalIndent(referenceManifest, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		referenceManifestPath = filepath.Join(artifactDir, stem+"-browser-reference.json")
		if err := writeTimelineArtifactExclusive(referenceManifestPath, referenceManifestBytes); err != nil {
			t.Fatalf("write browser draw-order reference: %v", err)
		}
		referencePixelPath = filepath.Join(artifactDir, reference.PixelStream)
	}
	if layoutProbeOnly {
		t.Logf("captured browser layout probe frames=%d artifacts=%s", reference.FrameCount, artifactDir)
		return
	}
	if os.Getenv("NICO_TIMELINE_REFERENCE_ONLY") == "1" {
		t.Logf("captured browser-only reference frames=%d pixel_stream_sha256=%s artifacts=%s", reference.FrameCount, reference.PixelStreamSHA, artifactDir)
		return
	}

	scene, captureReport, err := captureCommentTimelineSeededForTest(ctx, snapshot, options, timelineIntegrationRandomSeed)
	if err != nil {
		t.Fatalf("capture supported comments for NCT1: %v (report=%+v)", err, captureReport)
	}
	if int64(scene.Header.FrameCount) != reference.FrameCount || scene.Header.Width != uint32(reference.Width) || scene.Header.Height != uint32(reference.Height) || scene.Header.FPSNum != uint32(reference.FPSNum) || scene.Header.FPSDen != uint32(reference.FPSDen) {
		t.Fatalf("NCT1 output clock/geometry differs from browser reference: header=%+v reference=%+v", scene.Header, reference)
	}
	drawOrderReport := compareTimelineReferenceDrawOrder(scene, reference)
	drawOrderBytes, err := json.MarshalIndent(drawOrderReport, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTimelineArtifactExclusive(filepath.Join(artifactDir, stem+"-draw-order-comparison.json"), drawOrderBytes); err != nil {
		t.Fatalf("write draw-order comparison: %v", err)
	}
	positionReport := compareTimelineReferencePositions(scene, reference, 1.0/256.0)
	positionBytes, err := json.MarshalIndent(positionReport, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTimelineArtifactExclusive(filepath.Join(artifactDir, stem+"-position-comparison.json"), positionBytes); err != nil {
		t.Fatalf("write position comparison: %v", err)
	}

	scenePath := filepath.Join(artifactDir, stem+".nct")
	var sceneBytes bytes.Buffer
	if err := WriteCommentTimeline(&sceneBytes, scene); err != nil {
		t.Fatalf("encode NCT1 scene: %v", err)
	}
	if err := writeTimelineArtifactExclusive(scenePath, sceneBytes.Bytes()); err != nil {
		t.Fatalf("write NCT1 scene artifact: %v", err)
	}
	if os.Getenv("NICO_TIMELINE_CAPTURE_ONLY") == "1" {
		if drawOrderReport.MismatchFrames != 0 || positionReport.MissingBrowserPosition != 0 || positionReport.OverToleranceX != 0 || positionReport.OverToleranceY != 0 {
			t.Fatalf("capture parity failed: draw_order_mismatch_frames=%d position_samples=%d max_position_error_x=%.9f max_position_error_y=%.9f missing_browser_position=%d position_over_tolerance_x=%d position_over_tolerance_y=%d", drawOrderReport.MismatchFrames, positionReport.Samples, positionReport.MaxAbsErrorX, positionReport.MaxAbsErrorY, positionReport.MissingBrowserPosition, positionReport.OverToleranceX, positionReport.OverToleranceY)
		}
		t.Logf("captured and compared NCT1 scene draws=%d positions=%d artifacts=%s", len(scene.Draws), positionReport.Samples, artifactDir)
		return
	}
	input, err := os.Open(scenePath)
	if err != nil {
		t.Fatal(err)
	}
	candidatePath := filepath.Join(artifactDir, stem+"-wgpu.rgba")
	candidate, err := os.OpenFile(candidatePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = input.Close()
		t.Fatalf("create WGPU RGBA artifact: %v", err)
	}
	backend := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_TIMELINE_GPU_BACKEND"))
	if backend == "" {
		backend = "auto"
	}
	slots := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS"))
	if slots == "" || slots == "0" {
		slots = "3"
	}
	slotCount, err := strconv.Atoi(slots)
	if err != nil || slotCount < 1 || slotCount > 3 {
		_ = input.Close()
		_ = candidate.Close()
		t.Fatalf("invalid WGPU readback slots %q", slots)
	}
	reportPath := filepath.Join(artifactDir, stem+"-wgpu-runtime.json")
	stderrPath := filepath.Join(artifactDir, stem+"-wgpu-stderr.log")
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = input.Close()
		_ = candidate.Close()
		t.Fatalf("create WGPU stderr artifact: %v", err)
	}
	cmd := exec.CommandContext(ctx, helperPath,
		"--stdin", "--backend", backend, "--readback-slots", strconv.Itoa(slotCount), "--report", reportPath)
	cmd.Stdin = input
	cmd.Stdout = candidate
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	for _, file := range []interface{ Close() error }{input, candidate, stderr} {
		if closeErr := file.Close(); closeErr != nil && runErr == nil {
			runErr = closeErr
		}
	}
	if runErr != nil {
		log, _ := os.ReadFile(stderrPath)
		t.Fatalf("run WGPU compositor: %v; stderr=%s", runErr, strings.TrimSpace(string(log)))
	}
	expectedRGBABytes := int64(reference.Width) * int64(reference.Height) * 4 * reference.FrameCount
	if info, err := os.Stat(candidatePath); err != nil || info.Size() != expectedRGBABytes {
		t.Fatalf("WGPU RGBA length=%v, want %d bytes: %v", info, expectedRGBABytes, err)
	}
	var runtimeReport struct {
		Schema          int     `json:"schema"`
		Protocol        string  `json:"protocol"`
		Renderer        string  `json:"renderer"`
		Backend         string  `json:"backend"`
		AdapterName     string  `json:"adapterName"`
		AdapterType     string  `json:"adapterType"`
		ReadbackSlots   int     `json:"readbackSlots"`
		CompletedFrames int     `json:"completedFrames"`
		Error           *string `json:"error"`
	}
	runtimeData, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(runtimeData, &runtimeReport); err != nil {
		t.Fatalf("decode WGPU runtime report: %v", err)
	}
	if runtimeReport.Schema != 1 || runtimeReport.Protocol != "NCT1" || runtimeReport.Renderer != "wgpu" || runtimeReport.Backend == "" || runtimeReport.AdapterName == "" || runtimeReport.AdapterType == "" || runtimeReport.ReadbackSlots != slotCount || runtimeReport.CompletedFrames != int(scene.Header.FrameCount) || runtimeReport.Error != nil {
		t.Fatalf("WGPU runtime report is incomplete or unsuccessful: %+v", runtimeReport)
	}

	compareScript := filepath.Join("..", "..", "scripts", "experiments", "nico-timeline", "compare.py")
	diffDir := filepath.Join(artifactDir, stem+"-wgpu-diffs")
	comparisonPath := filepath.Join(artifactDir, stem+"-wgpu-comparison.json")
	referencePath := referencePixelPath
	compareStderr := &strings.Builder{}
	compareCmd := exec.CommandContext(ctx, python, compareScript,
		referencePath, candidatePath,
		"--width", strconv.Itoa(reference.Width), "--height", strconv.Itoa(reference.Height), "--frames", strconv.FormatInt(reference.FrameCount, 10),
		"--tolerance", "2", "--require-transparent-exterior-exact", "--diff-dir", diffDir, "--report", comparisonPath)
	compareCmd.Stdout = nil
	compareCmd.Stderr = compareStderr
	compareErr := compareCmd.Run()
	if compareErr != nil {
		if _, isExit := compareErr.(*exec.ExitError); !isExit {
			t.Fatalf("run browser/WGPU comparison: %v; stderr=%s; artifacts=%s", compareErr, strings.TrimSpace(compareStderr.String()), artifactDir)
		}
	}
	comparison, err := os.ReadFile(comparisonPath)
	if err != nil {
		t.Fatal(err)
	}
	var comparisonReport struct {
		Frames                             int    `json:"frames"`
		MaxChannelDiff                     int    `json:"max_channel_diff"`
		PixelsOverTolerance                uint64 `json:"pixels_over_tolerance"`
		DifferentPixels                    uint64 `json:"different_pixels"`
		TransparentExteriorPixels          uint64 `json:"transparent_exterior_pixels"`
		TransparentExteriorDifferentPixels uint64 `json:"transparent_exterior_different_pixels"`
		TransparentExteriorExact           bool   `json:"transparent_exterior_exact"`
		DifferentFrames                    int    `json:"different_frames"`
		ReferenceRGBASHA256                string `json:"reference_rgba_sha256"`
		CandidateRGBASHA256                string `json:"candidate_rgba_sha256"`
	}
	if err := json.Unmarshal(comparison, &comparisonReport); err != nil {
		t.Fatalf("decode full-frame comparison report: %v", err)
	}
	totalPixels := uint64(reference.Width) * uint64(reference.Height) * uint64(reference.FrameCount)
	matchPercent := 0.0
	if comparisonReport.DifferentPixels <= totalPixels && totalPixels > 0 {
		matchPercent = 100 * float64(totalPixels-comparisonReport.DifferentPixels) / float64(totalPixels)
	}
	if comparisonReport.Frames != int(reference.FrameCount) || comparisonReport.ReferenceRGBASHA256 != reference.PixelStreamSHA || comparisonReport.DifferentPixels > totalPixels || matchPercent < 98 || comparisonReport.MaxChannelDiff > 2 || comparisonReport.PixelsOverTolerance != 0 || !comparisonReport.TransparentExteriorExact || comparisonReport.TransparentExteriorDifferentPixels != 0 || drawOrderReport.MismatchFrames != 0 || positionReport.MissingBrowserPosition != 0 || positionReport.OverToleranceX != 0 || positionReport.OverToleranceY != 0 || compareErr != nil {
		t.Fatalf("browser/WGPU parity failed: pixel_frames=%d different_frames=%d exact_pixel_match=%.5f%% max_channel_diff=%d pixels_over_2=%d transparent_exterior_pixels=%d transparent_exterior_different=%d transparent_exterior_exact=%t draw_order_mismatch_frames=%d position_samples=%d max_position_error_x=%.9f max_position_error_y=%.9f position_over_tolerance_x=%d position_over_tolerance_y=%d; artifacts=%s", comparisonReport.Frames, comparisonReport.DifferentFrames, matchPercent, comparisonReport.MaxChannelDiff, comparisonReport.PixelsOverTolerance, comparisonReport.TransparentExteriorPixels, comparisonReport.TransparentExteriorDifferentPixels, comparisonReport.TransparentExteriorExact, drawOrderReport.MismatchFrames, positionReport.Samples, positionReport.MaxAbsErrorX, positionReport.MaxAbsErrorY, positionReport.OverToleranceX, positionReport.OverToleranceY, artifactDir)
	}
	t.Logf("full-frame browser/WGPU parity passed: frames=%d byte-different_frames=%d exact_pixel_match=%.5f%% max_channel_diff=%d backend=%s adapter=%s ring=%d rgba_sha256=%s", comparisonReport.Frames, comparisonReport.DifferentFrames, matchPercent, comparisonReport.MaxChannelDiff, runtimeReport.Backend, runtimeReport.AdapterName, runtimeReport.ReadbackSlots, comparisonReport.CandidateRGBASHA256)
}
