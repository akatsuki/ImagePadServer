package nicorender

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

type collectingSink struct {
	frames [][]byte
}

func TestBrowserLaunchArgsKeepSafeDefaultAndAllowDiagnosticModes(t *testing.T) {
	defaultArgs := browserLaunchArgs(1234, "profile", "new", "disabled")
	if !containsArg(defaultArgs, "--headless=new") || !containsArg(defaultArgs, "--disable-gpu") {
		t.Fatalf("default browser args = %#v", defaultArgs)
	}
	enabledArgs := browserLaunchArgs(1234, "profile", "old", "enabled")
	if !containsArg(enabledArgs, "--headless=old") || containsArg(enabledArgs, "--disable-gpu") {
		t.Fatalf("enabled diagnostic browser args = %#v", enabledArgs)
	}
	swiftShaderArgs := browserLaunchArgs(1234, "profile", "new", "swiftshader")
	if !containsArg(swiftShaderArgs, "--use-angle=swiftshader") || !containsArg(swiftShaderArgs, "--use-gl=angle") {
		t.Fatalf("swiftshader diagnostic browser args = %#v", swiftShaderArgs)
	}
	swiftShaderInProcessArgs := browserLaunchArgs(1234, "profile", "new", "swiftshader-inprocess")
	if !containsArg(swiftShaderInProcessArgs, "--use-angle=swiftshader") || !containsArg(swiftShaderInProcessArgs, "--in-process-gpu") {
		t.Fatalf("swiftshader-inprocess diagnostic browser args = %#v", swiftShaderInProcessArgs)
	}
	inProcessArgs := browserLaunchArgs(1234, "profile", "new", "inprocess")
	if !containsArg(inProcessArgs, "--in-process-gpu") || containsArg(inProcessArgs, "--disable-gpu") {
		t.Fatalf("in-process diagnostic browser args = %#v", inProcessArgs)
	}
	softwareArgs := browserLaunchArgs(1234, "profile", "new", "software")
	if !containsArg(softwareArgs, "--disable-gpu") || !containsArg(softwareArgs, "--disable-gpu-compositing") || !containsArg(softwareArgs, "--disable-features=UseSkiaRenderer") {
		t.Fatalf("software diagnostic browser args = %#v", softwareArgs)
	}
	softwareInProcessArgs := browserLaunchArgs(1234, "profile", "new", "software-inprocess")
	if !containsArg(softwareInProcessArgs, "--in-process-gpu") || !containsArg(softwareInProcessArgs, "--disable-gpu-compositing") || !containsArg(softwareInProcessArgs, "--disable-features=UseSkiaRenderer,VizDisplayCompositor") {
		t.Fatalf("software-inprocess diagnostic browser args = %#v", softwareInProcessArgs)
	}
	singleProcessArgs := browserLaunchArgs(1234, "profile", "new", "single-process")
	if !containsArg(singleProcessArgs, "--single-process") || !containsArg(singleProcessArgs, "--disable-gpu") {
		t.Fatalf("single-process diagnostic browser args = %#v", singleProcessArgs)
	}
	noDawnCacheArgs := browserLaunchArgs(1234, "profile", "new", "disabled-no-dawn-cache")
	if !containsArg(noDawnCacheArgs, "--disable-gpu") || !containsArg(noDawnCacheArgs, "--disable-features=SkiaGraphiteUsePersistentCache") {
		t.Fatalf("no-dawn-cache diagnostic browser args = %#v", noDawnCacheArgs)
	}
	noGpuSandboxArgs := browserLaunchArgs(1234, "profile", "new", "disabled-no-gpu-sandbox")
	if !containsArg(noGpuSandboxArgs, "--disable-gpu") || !containsArg(noGpuSandboxArgs, "--disable-gpu-sandbox") {
		t.Fatalf("no-gpu-sandbox diagnostic browser args = %#v", noGpuSandboxArgs)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func (s *collectingSink) WriteRGBA(_ context.Context, _ uint64, pixels []byte) error {
	s.frames = append(s.frames, append([]byte(nil), pixels...))
	return nil
}

func TestRenderRequiresBrowserWhenFramesAreRequested(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_RENDER_TEST") == "1" {
		t.Skip("integration test selects the configured browser")
	}
	old := findBrowser
	findBrowser = func() (string, error) { return "", errors.New("missing browser") }
	defer func() { findBrowser = old }()
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{VideoID: "sm9", Threads: []niconico.Thread{{Fork: "main", Comments: []niconico.Comment{{ID: "c1", VposMs: 0, Body: "test"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Render(context.Background(), snapshot, RenderOptions{Width: 64, Height: 36, DurationMs: 1, FPSNum: 30, FPSDen: 1}, &collectingSink{})
	if err == nil || !strings.Contains(err.Error(), ErrUnavailable.Error()) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestRenderRejectsUnknownTransport(t *testing.T) {
	_, err := Render(context.Background(), niconico.Snapshot{}, RenderOptions{
		Width: 64, Height: 36, DurationMs: 1, FPSNum: 30, FPSDen: 1, Transport: "jpeg",
	}, &collectingSink{})
	if err == nil || !strings.Contains(err.Error(), "unsupported render transport") {
		t.Fatalf("error = %v", err)
	}
}

func TestWriteRendererPageUsesOutputAspectForCommentCoordinates(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
	}{
		{name: "4:3", width: 960, height: 720},
		{name: "1:1", width: 720, height: 720},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := writeRendererPage(tc.width, tc.height, "file:///tmp/niconicomments.js", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(page)
			data, err := os.ReadFile(page)
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("config:{canvasWidth:%d,canvasHeight:%d}", tc.width, tc.height)
			if !strings.Contains(string(data), want) {
				t.Fatalf("renderer page does not set aspect-specific comment config: want %q", want)
			}
		})
	}
}

func TestWriteRendererPageRandomSeedPreludeIsOptInAndPrecedesBundle(t *testing.T) {
	seed := uint32(0x4e435431)
	seededPage, err := writeRendererPageWithRandomSeed(640, 360, "file:///tmp/niconicomments.js", nil, nil, &seed)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(seededPage)
	seeded, err := os.ReadFile(seededPage)
	if err != nil {
		t.Fatal(err)
	}
	seedMarker := fmt.Sprintf("let state=%d>>>0;Math.random=", seed)
	seedIndex := strings.Index(string(seeded), seedMarker)
	bundleIndex := strings.Index(string(seeded), `<script src="`)
	if seedIndex < 0 || bundleIndex < 0 || seedIndex >= bundleIndex {
		t.Fatalf("seeded page must install its random stream before the bundle: seed=%d bundle=%d", seedIndex, bundleIndex)
	}

	defaultPage, err := writeRendererPage(640, 360, "file:///tmp/niconicomments.js", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(defaultPage)
	defaultHTML, err := os.ReadFile(defaultPage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(defaultHTML), "Math.random=") {
		t.Fatal("production renderer page must retain the native Math.random implementation")
	}
}

func TestRenderSingleFrameWithHeadlessBrowser(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_RENDER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_RENDER_TEST=1 for the real browser test")
	}
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{
		VideoID:       "sm9",
		SelectedForks: []string{"main"},
		Threads: []niconico.Thread{{
			ID:   "thread",
			Fork: "main",
			Comments: []niconico.Comment{
				{ID: "c1", VposMs: 1000, Body: "render test\nline", Commands: []string{"red"}, PostedAt: "2026-01-01T00:00:00Z"},
				{ID: "c2", VposMs: 1000, Body: "top", Commands: []string{"ue", "big", "blue"}, PostedAt: "2026-01-01T00:00:00Z"},
				{ID: "c3", VposMs: 1000, Body: "bottom", Commands: []string{"shita", "small", "green"}, PostedAt: "2026-01-01T00:00:00Z"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := Render(ctx, snapshot, RenderOptions{Width: 640, Height: 360, DurationMs: 1001, FPSNum: 1, FPSDen: 1}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if report.FrameCount != 2 || len(sink.frames) != 2 || len(sink.frames[0]) != 640*360*4 {
		t.Fatalf("report=%#v frames=%d bytes=%d", report, len(sink.frames), len(sink.frames[0]))
	}
	alpha := 0
	for _, frame := range sink.frames {
		for i := 3; i < len(frame); i += 4 {
			if frame[i] != 0 {
				alpha++
			}
		}
	}
	if alpha == 0 {
		t.Fatal("rendered frame is fully transparent")
	}
}

func TestRenderBinaryTransportWithHeadlessBrowser(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_RENDER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_RENDER_TEST=1 for the real browser test")
	}
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{
		VideoID:       "sm9",
		SelectedForks: []string{"main"},
		Threads:       []niconico.Thread{{ID: "thread", Fork: "main"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sink := &collectingSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := Render(ctx, snapshot, RenderOptions{
		Width: 320, Height: 180, DurationMs: 1001, FPSNum: 1, FPSDen: 1,
		Transport: "binary", ReuseUnchanged: true,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if report.FrameCount != 2 || len(sink.frames) != 2 || len(sink.frames[0]) != 320*180*4 {
		t.Fatalf("report=%#v frames=%d bytes=%d", report, len(sink.frames), len(sink.frames[0]))
	}
	if !bytes.Equal(sink.frames[0], sink.frames[1]) {
		t.Fatal("repeat frame does not match previous full frame")
	}
}

func TestRenderBinaryTransportMatchesPNGReference(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_RENDER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_RENDER_TEST=1 for the real browser test")
	}
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{
		VideoID:       "sm9",
		SelectedForks: []string{"main"},
		Threads: []niconico.Thread{{
			ID: "thread", Fork: "main", Comments: []niconico.Comment{
				{ID: "c1", VposMs: 1000, Body: "render test", Commands: []string{"red"}, PostedAt: "2026-01-01T00:00:00Z"},
				{ID: "c2", VposMs: 1000, Body: "top", Commands: []string{"ue", "big", "blue"}, PostedAt: "2026-01-01T00:00:00Z"},
				{ID: "c3", VposMs: 1000, Body: "bottom", Commands: []string{"shita", "small", "green"}, PostedAt: "2026-01-01T00:00:00Z"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pngSink, binarySink := &collectingSink{}, &collectingSink{}
	base := RenderOptions{Width: 320, Height: 180, DurationMs: 1001, FPSNum: 1, FPSDen: 1}
	if _, err := Render(ctx, snapshot, base, pngSink); err != nil {
		t.Fatal(err)
	}
	base.Transport, base.ReuseUnchanged = "binary", true
	if _, err := Render(ctx, snapshot, base, binarySink); err != nil {
		t.Fatal(err)
	}
	if len(pngSink.frames) != len(binarySink.frames) {
		t.Fatalf("frame count png=%d binary=%d", len(pngSink.frames), len(binarySink.frames))
	}
	for frame := range pngSink.frames {
		if len(pngSink.frames[frame]) != len(binarySink.frames[frame]) {
			t.Fatalf("frame %d size png=%d binary=%d", frame, len(pngSink.frames[frame]), len(binarySink.frames[frame]))
		}
		maxDelta := byte(0)
		for i, want := range pngSink.frames[frame] {
			got := binarySink.frames[frame][i]
			delta := want - got
			if got > want {
				delta = got - want
			}
			if delta > maxDelta {
				maxDelta = delta
			}
		}
		if maxDelta > 1 {
			t.Fatalf("frame %d max RGBA delta=%d", frame, maxDelta)
		}
	}
}
