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
		if !strings.Contains(source, "CanonicalMusicScene") {
			t.Errorf("%s does not use CanonicalMusicScene", name)
		}
	}
}

// TestGPULoudnessShaderOwnsTheProductionLayer guards the migration seam: the
// opt-in GPU loudness route must not eagerly rasterize/upload the CPU graph,
// and must leave the scene texture nil so the sidecar's dynamics shader is
// enabled by the absence of a canonical texture.
func TestGPULoudnessShaderOwnsTheProductionLayer(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source := readRouteSource(t, filepath.Join(filepath.Dir(file), "audio_visualizer.go"))
	body := routeBody(sourceBetween(source, "func runAudioVisualizerHLSGPU", "func writeGPUFrame"))
	for _, want := range []string{
		`IMAGEPAD_GPU_LOUDNESS_SHADER`,
		`if !useGPULoudness`,
		`scene.LoudnessTexture = nil`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GPU loudness route missing migration guard %q", want)
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
	for _, want := range []string{
		`IMAGEPAD_GPU_DYNAMIC_SHADER`,
		`if !useGPUDynamic`,
		`useGPUWaveform = true`,
		`useSpectrumCanonical = false`,
		`wave = make([]byte, waveW*waveH*4)`,
		`scene.WaveformTexture = nil`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dynamic GPU route missing %q", want)
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
	for _, want := range []string{
		`IMAGEPAD_GPU_YUV_REQUIRED`,
		`sidecar.RequireYUV420Output()`,
		`sidecar YUV420P output required`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GPU YUV-required route missing fail-closed guard %q", want)
		}
	}
	if !strings.Contains(body, `== "1"`) {
		t.Error("GPU YUV-required guard must remain opt-in")
	}
}

func TestGPUTextShaderOwnsGlyphComposition(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	source := readRouteSource(t, filepath.Join(filepath.Dir(file), "audio_visualizer.go"))
	body := routeBody(sourceBetween(source, "func runAudioVisualizerHLSGPU", "func writeGPUFrame"))
	for _, want := range []string{
		`IMAGEPAD_GPU_TEXT_SHADER`,
		`if !useGPUText`,
		`scene.GlyphAtlas = nil`,
		`else {`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GPU text route missing migration guard %q", want)
		}
	}
	if strings.Contains(body, `postYUVFilter {`) && !strings.Contains(body, `postYUVFilter && !useGPUText`) {
		t.Error("GPU text route must not apply the post-YUV ASS filter")
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
