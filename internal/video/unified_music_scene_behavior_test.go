package video

import (
	"strings"
	"testing"
)

// T0 freezes the dimensions that T1/T3 must preserve while extracting the
// canonical scene. This test intentionally exercises contracts, not routing.
func TestUnifiedMusicSceneCanonicalGeometry(t *testing.T) {
	for _, tc := range []struct {
		height, width, waveW, waveH, waveX, waveY int
	}{
		{360, 640, 376, 84, 216, 160},
		{720, 1280, 752, 168, 432, 320},
		{1080, 1920, 1128, 252, 648, 480},
	} {
		p := QualityPreset{Height: tc.height, VideoBitrate: "1m", MaxRate: "1m", BufferSize: "2m", AudioBitrate: "96k", RadioLatency: "rtsp-ultra"}
		r := NewRadioRenderRecipe(p, CPUVideoEncoder(EncoderLowLatency), "", 10)
		canvas := r.AssetRenderRecipeContract().Canvas
		wave := r.AssetRenderRecipeContract().Waveform
		if canvas.Width != tc.width || canvas.Height != tc.height || canvas.FrameRate != 30 {
			t.Fatalf("height %d canvas=%+v", tc.height, canvas)
		}
		if wave.Width != tc.waveW || wave.Height != tc.waveH || wave.X != tc.waveX || wave.Y != tc.waveY {
			t.Fatalf("height %d waveform=%+v", tc.height, wave)
		}
	}
}

func TestUnifiedMusicSceneMuxTailsRemainDistinct(t *testing.T) {
	p := QualityPreset{Height: 720, VideoBitrate: "1m", MaxRate: "1m", BufferSize: "2m", AudioBitrate: "96k", RadioLatency: "rtsp-ultra"}
	hls := audioVisualizerFFmpegArgsWithEncoder("audio.wav", "track.ass", "fonts", "id", p, nil, CPUVideoEncoder(EncoderStandard), "")
	hlsJoined := strings.Join(hls, " ")
	for _, want := range []string{"-f hls", "-hls_time 4", "-hls_playlist_type event"} {
		if !strings.Contains(hlsJoined, want) {
			t.Errorf("HLS tail missing %q: %s", want, hlsJoined)
		}
	}
	r := NewRadioRenderRecipe(p, CPUVideoEncoder(EncoderLowLatency), "", 10)
	r.SourcePath, r.OutputPath = "audio.wav", "out.mp4"
	mp4 := strings.Join(r.FFmpegArgs(nil), " ")
	for _, want := range []string{"-movflags +faststart", "-f mp4", "-y out.mp4"} {
		if !strings.Contains(mp4, want) {
			t.Errorf("MP4 tail missing %q: %s", want, mp4)
		}
	}
}

func TestUnifiedMusicSceneGpuFrameStrideContract(t *testing.T) {
	f := GpuFrame{Schema: GPUContractVersion, Width: 640, Height: 360, RowStride: 2560, Format: PixelRGBA8, ColorSpace: ColorSRGB, Ownership: "OwnedByTransport", Payload: make([]byte, 2560*360)}
	if err := f.Validate(); err != nil {
		t.Fatalf("canonical RGBA frame rejected: %v", err)
	}
	if err := (GpuFrame{Schema: GPUContractVersion, Width: 640, Height: 360, RowStride: 2564, Payload: make([]byte, 2564*360), Ownership: "OwnedByTransport"}).Validate(); err == nil {
		t.Fatal("unaligned row stride accepted")
	}
}

func TestUnifiedMusicSceneGpuErrorSentinelsRemainStable(t *testing.T) {
	if ErrGPURequired.Error() != "gpu_required" || ErrGPURendererUnavailable.Error() != "gpu_renderer_unavailable" {
		t.Fatal("GPU error sentinels changed")
	}
	if err := RequireGPUFrame(GpuFrame{}); err == nil {
		t.Fatal("invalid GPU frame was accepted")
	}
}
