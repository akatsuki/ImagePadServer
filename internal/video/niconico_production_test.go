package video

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
)

func TestNicoNativeStatusMalformed(t *testing.T) {
	for _, input := range []string{
		"NICO_PROGRESS x 3\n", "NICO_PROGRESS 1 x\n", "NICO_PROGRESS 1 2\n",
		"NICO_PROGRESS 1 3\nNICO_PROGRESS 1 3\n", "NICO_DONE 3\n",
		"NICO_PROGRESS 3 3\nNICO_DONE 3\nNICO_DONE 3\n",
		"NICO_PROGRESS 3 3\nNICO_DONE 3\nNICO_PROGRESS 3 3\n",
		"NICO_PROGRESS 3 3\nNICO_DONE 3", strings.Repeat("x", 4097),
	} {
		s := nicoNativeStatus{expected: 3}
		_, err := s.Write([]byte(input))
		if err == nil {
			err = s.complete()
		}
		if err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	s := nicoNativeStatus{expected: 3}
	for _, part := range []string{"NICO_PRO", "GRESS 1 3\nNICO_PROGRESS 3 3\nNI", "CO_DONE 3\n"} {
		if _, err := s.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.complete(); err != nil {
		t.Fatal(err)
	}
}

func TestNicoProductionSelection(t *testing.T) {
	o := nicorender.RenderOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1}
	e := NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 26}
	for _, backend := range []string{"unknown", "native", "auto"} {
		t.Run(backend, func(t *testing.T) {
			r := o
			r.Backend = backend
			r.CompositorPath = filepath.Join(t.TempDir(), "absent.exe")
			r.BrowserPath = r.CompositorPath
			_, rr, err := EncodeNicoCommentedWithRenderer(context.Background(), "missing-ffmpeg", "missing-source", filepath.Join(t.TempDir(), "out.mp4"), niconico.Snapshot{}, r, e)
			if err == nil {
				t.Fatal("expected error")
			}
			if backend == "auto" && (rr.Backend != "browser" || rr.FallbackReason == "") {
				t.Fatalf("missing fallback report: %+v", rr)
			}
		})
	}
	o.Width++
	if _, _, err := EncodeNicoCommentedWithRenderer(context.Background(), "", "", "", niconico.Snapshot{}, o, e); err == nil || !strings.Contains(err.Error(), "geometry/timeline") {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestNicoNativeFailurePreservesOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := "previous completed output"
	if err := os.WriteFile(output, []byte(sentinel), 0600); err != nil {
		t.Fatal(err)
	}
	r := nicorender.RenderOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1}
	e := NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 26}
	report, _, err := encodeNicoNative(context.Background(), "missing-native", "missing-ffmpeg", source, output, niconico.Snapshot{}, r, e)
	if err == nil {
		t.Fatal("expected process failure")
	}
	if len(report.StageTimings) == 0 || report.StageTimings[len(report.StageTimings)-1].Name != "native_render_encode" {
		t.Fatalf("failure timing was not preserved: %+v", report.StageTimings)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != sentinel {
		t.Fatalf("completed output modified: %v %q", err, data)
	}
	pending, err := filepath.Glob(filepath.Join(dir, ".niconico-*.mp4"))
	if err != nil || len(pending) != 0 {
		t.Fatalf("partial outputs retained: %v %v", pending, err)
	}
}

func TestNicoAutoFallbackAfterNativeRuntimeFailureRegeneratesBrowser(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}

	oldPrepare := prepareNicoNativeCompositor
	oldNative := encodeNicoNativeForPipeline
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		prepareNicoNativeCompositor = oldPrepare
		encodeNicoNativeForPipeline = oldNative
		encodeNicoBrowserForPipeline = oldBrowser
	})
	prepareNicoNativeCompositor = func(context.Context, string) (string, func(), error) {
		return "native-compositor", func() {}, nil
	}
	encodeNicoNativeForPipeline = func(_ context.Context, _, _, _, outputPath string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		if err := os.WriteFile(outputPath, []byte("partial-native"), 0600); err != nil {
			t.Fatal(err)
		}
		return NicoEncodeReport{}, nicorender.RenderReport{}, errors.New("native runtime failure")
	}
	encodeNicoBrowserForPipeline = func(_ context.Context, _, _, outputPath string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
			t.Fatalf("browser fallback saw native partial output: %v", err)
		}
		if err := os.WriteFile(outputPath, []byte("browser-complete"), 0600); err != nil {
			t.Fatal(err)
		}
		return NicoEncodeReport{OutputPath: outputPath}, nicorender.RenderReport{Backend: "browser"}, nil
	}

	_, report, err := EncodeNicoCommentedWithRenderer(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		Backend: "auto", Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 26})
	if err != nil {
		t.Fatal(err)
	}
	if report.Backend != "browser" || !strings.Contains(report.FallbackReason, "native runtime failure") {
		t.Fatalf("fallback report = %+v", report)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "browser-complete" {
		t.Fatalf("fallback output = %v %q", err, data)
	}
}

func TestNicoAutoFallbackFailurePreservesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	const previous = "previous-completed-output"
	if err := os.WriteFile(output, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}

	oldPrepare := prepareNicoNativeCompositor
	oldNative := encodeNicoNativeForPipeline
	oldBrowser := encodeNicoBrowserForPipeline
	t.Cleanup(func() {
		prepareNicoNativeCompositor = oldPrepare
		encodeNicoNativeForPipeline = oldNative
		encodeNicoBrowserForPipeline = oldBrowser
	})
	prepareNicoNativeCompositor = func(context.Context, string) (string, func(), error) {
		return "native-compositor", func() {}, nil
	}
	encodeNicoNativeForPipeline = func(_ context.Context, _, _, _, _ string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		return NicoEncodeReport{}, nicorender.RenderReport{}, errors.New("native runtime failure")
	}
	encodeNicoBrowserForPipeline = func(_ context.Context, _, _, outputPath string, _ niconico.Snapshot, _ nicorender.RenderOptions, _ NicoEncodeOptions) (NicoEncodeReport, nicorender.RenderReport, error) {
		if err := os.WriteFile(outputPath, []byte("partial-browser"), 0600); err != nil {
			t.Fatal(err)
		}
		return NicoEncodeReport{}, nicorender.RenderReport{}, errors.New("browser regeneration failure")
	}

	_, _, err := EncodeNicoCommentedWithRenderer(context.Background(), "ffmpeg", source, output, niconico.Snapshot{}, nicorender.RenderOptions{
		Backend: "auto", Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1,
	}, NicoEncodeOptions{Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 26})
	if err == nil || !strings.Contains(err.Error(), "browser regeneration failure") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil || string(data) != previous {
		t.Fatalf("previous output was not preserved: %v %q", readErr, data)
	}
}

func TestPromoteNicoFallbackOutputDiagnosesBackupCleanupFailure(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, ".niconico-fallback.mp4")
	output := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(source, []byte("new-output"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("previous-output"), 0600); err != nil {
		t.Fatal(err)
	}

	oldRemove := removeNicoFallbackBackup
	oldDiagnostic := reportNicoFallbackCleanup
	t.Cleanup(func() {
		removeNicoFallbackBackup = oldRemove
		reportNicoFallbackCleanup = oldDiagnostic
	})
	removeCalls := 0
	removeNicoFallbackBackup = func(path string) error {
		removeCalls++
		if removeCalls == 2 {
			return errors.New("simulated backup cleanup failure")
		}
		return os.Remove(path)
	}
	diagnosed := false
	reportNicoFallbackCleanup = func(string, ...any) { diagnosed = true }

	if err := promoteNicoFallbackOutput(source, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "new-output" {
		t.Fatalf("promoted output = %v %q", err, data)
	}
	if !diagnosed {
		t.Fatal("backup cleanup failure was not diagnosed")
	}
	if removeCalls != 2 {
		t.Fatalf("backup remove calls = %d, want 2", removeCalls)
	}
}

func TestNicoProductionBackendAcceptanceAllowsExplicitAutoFallbackOnly(t *testing.T) {
	cases := []struct {
		name          string
		requested     string
		actual        string
		allowFallback bool
		want          bool
	}{
		{name: "native success", requested: "auto", actual: "native-warp", want: true},
		{name: "diagnostic auto fallback", requested: "auto", actual: "browser", allowFallback: true, want: true},
		{name: "fallback disabled", requested: "auto", actual: "browser", want: false},
		{name: "native never falls back", requested: "native", actual: "browser", allowFallback: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := acceptsNicoProductionBackend(tc.requested, tc.actual, tc.allowFallback); got != tc.want {
				t.Fatalf("acceptsNicoProductionBackend(%q, %q, %v) = %v, want %v", tc.requested, tc.actual, tc.allowFallback, got, tc.want)
			}
		})
	}
}

func acceptsNicoProductionBackend(requested, actual string, allowFallback bool) bool {
	if requested != "auto" && requested != "native" {
		return true
	}
	if actual == "native-warp" {
		return true
	}
	return requested == "auto" && allowFallback && actual == "browser"
}

// Opt-in: exercises the public API, real Chrome, embedded helper, unchanged
// FFmpeg settings, progress, complete decode, HLS and every actual H.264 slice.
func TestNicoProductionPipeline(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_PRODUCTION_TEST") != "1" {
		t.Skip("opt-in real pipeline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	data, err := os.ReadFile(os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot niconico.Snapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	duration := int64(6000)
	if v := os.Getenv("IMAGEPAD_NICO_PERF_DURATION_MS"); v != "" {
		duration, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	backend := os.Getenv("IMAGEPAD_NICO_PRODUCTION_BACKEND")
	if backend == "" {
		backend = "auto"
	}
	o := nicorender.RenderOptions{Backend: backend, Width: 1920, Height: 1080, DurationMs: duration, FPSNum: 30, FPSDen: 1, Transport: "binary", BatchFrames: 30, ReuseUnchanged: true, SparseFrames: true}
	o.SpriteCompression = os.Getenv("IMAGEPAD_NICO_TEST_COMPRESSION")
	o.NativeCopyOutput = os.Getenv("IMAGEPAD_NICO_TEST_COPY_OUTPUT") == "1"
	var stageTimings []NicoStageTiming
	enc := NicoEncodeOptions{Width: o.Width, Height: o.Height, DurationMs: duration, FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k", StageObserver: func(stage NicoStageTiming) {
		stageTimings = append(stageTimings, stage)
	}}
	clock, _ := niconico.NewFrameClock(30, 1)
	expected := clock.FrameCountForDurationMs(duration)
	last := int64(0)
	progressError := ""
	o.Progress = func(n, total int64) {
		if n <= last || n > expected || total != expected {
			progressError = "invalid progress"
		}
		last = n
	}
	dir := t.TempDir()
	if dest := os.Getenv("IMAGEPAD_NICO_PRODUCTION_ARTIFACTS"); dest != "" {
		dir = dest
		if err = os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(dir, "out.mp4")
	// Exercise replacement of an existing destination on the real platform.
	if err = os.WriteFile(output, []byte("old output"), 0600); err != nil {
		t.Fatal(err)
	}
	ffmpeg := os.Getenv("IMAGEPAD_NICO_PERF_FFMPEG")
	start := time.Now()
	er, rr, err := EncodeNicoCommentedWithRenderer(ctx, ffmpeg, os.Getenv("IMAGEPAD_NICO_PERF_SOURCE"), output, snapshot, o, enc)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	allowFallback := os.Getenv("IMAGEPAD_NICO_ALLOW_NATIVE_FALLBACK") == "1"
	if !acceptsNicoProductionBackend(backend, rr.Backend, allowFallback) {
		t.Fatalf("native not selected: %+v", rr)
	}
	if allowFallback && backend == "auto" && rr.Backend == "browser" && rr.FallbackReason == "" {
		t.Fatalf("diagnostic fallback was accepted without a fallback reason: %+v", rr)
	}
	if last != expected || progressError != "" || rr.FrameCount != expected || er.FrameCount != expected {
		t.Fatalf("progress=%d expected=%d error=%s reports=%+v %+v", last, expected, progressError, rr, er)
	}
	hls, err := CreateNicoHLS(ctx, ffmpeg, output, filepath.Join(dir, "hls"))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{output, hls.PlaylistPath} {
		if b, e := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", input, "-f", "null", "-").CombinedOutput(); e != nil {
			t.Fatalf("decode %s: %v %s", input, e, b)
		}
	}
	md5, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", output, "-map", "0:v:0", "-f", "framemd5", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	decoded := 0
	for _, line := range strings.Split(string(md5), "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
			decoded++
		}
	}
	if decoded == 0 || (duration == 6000 && decoded != 180) {
		t.Fatalf("decoded=%d", decoded)
	}
	stream, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", output, "-map", "0:v:0", "-c:v", "copy", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	au, slices := 0, 0
	for i := 0; i+4 < len(stream); i++ {
		start := 0
		if stream[i] == 0 && stream[i+1] == 0 && stream[i+2] == 1 {
			start = i + 3
		} else if stream[i] == 0 && stream[i+1] == 0 && stream[i+2] == 0 && stream[i+3] == 1 {
			start = i + 4
		}
		if start == 0 {
			continue
		}
		kind := stream[start] & 31
		if kind == 9 {
			if au > 0 && slices != 1 {
				t.Fatalf("AU %d slices=%d", au, slices)
			}
			au++
			slices = 0
		} else if kind == 1 || kind == 5 {
			slices++
		}
		i = start
	}
	if au != decoded || slices != 1 {
		t.Fatalf("AU=%d decoded=%d final slices=%d", au, decoded, slices)
	}
	if err = os.WriteFile(filepath.Join(dir, "frames.md5"), md5, 0600); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"backend": rr.Backend, "compression": o.SpriteCompression, "copy_output": o.NativeCopyOutput, "duration_ms": duration, "encode_seconds": elapsed.Seconds(), "generated_frames": expected, "decoded_frames": decoded, "single_slice_aus": au, "last_progress": last, "stage_timings": stageTimings, "encoder_report": er, "report": rr}
	summary, _ := json.MarshalIndent(record, "", "  ")
	if err = os.WriteFile(filepath.Join(dir, "result.json"), summary, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(summary))
}
