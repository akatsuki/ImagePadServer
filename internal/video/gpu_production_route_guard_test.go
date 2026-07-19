package video

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestGPUProductionRoutesDoNotReintroduceCPUComposition is a narrow migration
// guard. CPU ASS/showwaves composition remains available only through the
// explicitly named reference helpers; production single-track and playlist
// paths must feed the same canonical scene into the GPU sidecar.
func TestGPUProductionRoutesDoNotReintroduceCPUComposition(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(file)
	audioSource := readRouteSource(t, filepath.Join(root, "audio_visualizer.go"))
	radioSource := readRouteSource(t, filepath.Join(root, "radio_render.go"))

	for name, source := range map[string]string{
		"single-track GPU route": routeBody(sourceBetween(audioSource, "func runAudioVisualizerHLSGPU", "func writeGPUFrame")),
		"playlist GPU route":     routeBody(sourceBetween(radioSource, "func renderRadioTrackGPU", "func audioVisualizerMP4ArgsWithEncoder")),
	} {
		for _, banned := range []string{"showwaves", "showfreqs", "drawtext", "ass=", "subtitles"} {
			if strings.Contains(strings.ToLower(source), banned) {
				t.Errorf("%s contains CPU composition filter %q", name, banned)
			}
		}
		for _, bannedCall := range []string{
			"RenderVisualizerBaseCPUWithFallback",
			"RenderVisualizerBaseCPU",
			"RenderVisualizerReferenceFrameCPU",
			"RenderSpectrumMaskCPU",
			"RenderSpectrumCompositeTextureCPU",
			"RenderCanonicalASSOverlay",
			"BuildVisualizerASS",
			"StreamAudioWaveFrames",
			"rgbaToYUV420p",
		} {
			if strings.Contains(source, bannedCall) {
				t.Errorf("%s contains forbidden CPU full-frame call %q", name, bannedCall)
			}
		}
		if !strings.Contains(source, "CanonicalMusicScene") {
			t.Errorf("%s does not use CanonicalMusicScene", name)
		}
	}
}

// TestGPULoudnessShaderOwnsTheProductionLayer guards the mandatory native
// route: no opt-in or CPU-raster alternative may remain in production.
func TestGPULoudnessShaderOwnsTheProductionLayer(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source := readRouteSource(t, filepath.Join(filepath.Dir(file), "audio_visualizer.go"))
	body := routeBody(sourceBetween(source, "func runAudioVisualizerHLSGPU", "func writeGPUFrame"))
	for _, want := range []string{`scene.LoudnessTexture = nil`} {
		if !strings.Contains(body, want) {
			t.Errorf("GPU loudness route missing mandatory native ownership %q", want)
		}
	}
	for _, banned := range []string{`IMAGEPAD_GPU_LOUDNESS_SHADER`, `useGPULoudness`, `buildLoudnessLayer`} {
		if strings.Contains(body, banned) {
			t.Errorf("GPU loudness route retains legacy branch %q", banned)
		}
	}
}

func TestGPUDynamicShaderOwnsWaveformAndSpectrum(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source := readRouteSource(t, filepath.Join(filepath.Dir(file), "audio_visualizer.go"))
	body := routeBody(sourceBetween(source, "func runAudioVisualizerHLSGPU", "func writeGPUFrame"))
	for _, want := range []string{`StreamWaveformPCMHistoryFrames`, `scene.WaveformTexture = nil`, `scene.SpectrumTexture = nil`} {
		if !strings.Contains(body, want) {
			t.Errorf("mandatory dynamic GPU route missing %q", want)
		}
	}
	for _, banned := range []string{`IMAGEPAD_GPU_DYNAMIC_SHADER`, `useGPUDynamic`, `StreamAudioWaveFrames`, `RenderSpectrumMaskCPU`, `RenderSpectrumCompositeTextureCPU`} {
		if strings.Contains(body, banned) {
			t.Errorf("dynamic GPU route retains legacy branch %q", banned)
		}
	}
}

func TestGPUYUVRequiredRouteFailsClosed(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source := readRouteSource(t, filepath.Join(filepath.Dir(file), "audio_visualizer.go"))
	body := routeBody(sourceBetween(source, "func runAudioVisualizerHLSGPU", "func writeGPUFrame"))
	for _, want := range []string{`sidecar.RequireYUV420Output()`, `sidecar YUV420P output required`} {
		if !strings.Contains(body, want) {
			t.Errorf("GPU YUV-required route missing fail-closed guard %q", want)
		}
	}
	for _, banned := range []string{`IMAGEPAD_GPU_YUV_REQUIRED`, `yuvRequired`} {
		if strings.Contains(body, banned) {
			t.Errorf("GPU YUV route must be mandatory, found legacy opt-in %q", banned)
		}
	}
}

func TestGPUYUVRequiredRouteUsesNativeYUVFrame(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source := readRouteSource(t, filepath.Join(filepath.Dir(file), "audio_visualizer.go"))
	body := routeBody(sourceBetween(source, "func runAudioVisualizerHLSGPU", "func writeGPUFrame"))
	for _, want := range []string{`sidecar.RenderSceneYUV`, `yuvFrame.PackedBytes`} {
		if !strings.Contains(body, want) {
			t.Errorf("GPU YUV route missing native transport %q", want)
		}
	}
	for _, banned := range []string{`sidecar.RenderScene(ctx`, `rgbaToYUV420p`} {
		if strings.Contains(body, banned) {
			t.Errorf("GPU YUV route retains CPU fallback %q", banned)
		}
	}
}

func TestGPUTextShaderOwnsGlyphComposition(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source := readRouteSource(t, filepath.Join(filepath.Dir(file), "audio_visualizer.go"))
	body := routeBody(sourceBetween(source, "func runAudioVisualizerHLSGPU", "func writeGPUFrame"))
	for _, want := range []string{`scene.TextOverlay = nil`} {
		if !strings.Contains(body, want) {
			t.Errorf("GPU text route missing native ownership %q", want)
		}
	}
	for _, banned := range []string{`IMAGEPAD_GPU_TEXT_SHADER`, `useGPUText`, `scene.GlyphAtlas = nil`, `BuildVisualizerASS`, `postYUVFilter`} {
		if strings.Contains(body, banned) {
			t.Errorf("GPU text route retains legacy branch %q", banned)
		}
	}
}

func readRouteSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func sourceBetween(source, start, end string) string {
	i := strings.Index(source, start)
	if i < 0 {
		return ""
	}
	source = source[i:]
	if j := strings.Index(source, end); j >= 0 {
		return source[:j]
	}
	return source
}

func routeBody(source string) string {
	// Remove comments so the guard checks the executable route body rather than
	// the migration rationale mentioning the old filters.
	source = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(source, "")
	source = regexp.MustCompile(`//[^\r\n]*`).ReplaceAllString(source, "")
	return source
}
