package video

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func testRadioRecipe(encoder VideoEncoderProfile) RadioRenderRecipe {
	preset := QualityPreset{
		Height:       720,
		VideoBitrate: "2400k",
		MaxRate:      "2800k",
		BufferSize:   "5600k",
		AudioBitrate: "160k",
		RadioLatency: "rtsp-ultra",
	}
	return NewRadioRenderRecipe(preset, encoder, "loudnorm=I=-14:LRA=11:TP=-1.5", 180)
}

func TestRadioRenderRecipeCanonicalJSONAndFingerprintAreDeterministic(t *testing.T) {
	recipe := testRadioRecipe(CPUVideoEncoder(EncoderLowLatency))
	first := recipe.CanonicalEncodingJSON()
	second := recipe.CanonicalEncodingJSON()
	if string(first) != string(second) {
		t.Fatalf("canonical JSON changed: %s != %s", first, second)
	}
	var decoded RadioEncodingContract
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("canonical JSON is invalid: %v", err)
	}
	if got := recipe.EncodingFingerprint(); len(got) != 64 {
		t.Fatalf("fingerprint = %q, want SHA-256 hex", got)
	}
}

func TestRadioRenderRecipeFingerprintCoversEveryReceiverVisibleField(t *testing.T) {
	base := testRadioRecipe(CPUVideoEncoder(EncoderLowLatency)).NormalizedEncodingContract()
	mutations := map[string]func(*RadioEncodingContract){
		"video codec":       func(c *RadioEncodingContract) { c.Video.Codec = "hevc" },
		"width":             func(c *RadioEncodingContract) { c.Video.Width++ },
		"height":            func(c *RadioEncodingContract) { c.Video.Height++ },
		"fps numerator":     func(c *RadioEncodingContract) { c.Video.FrameRate.Numerator++ },
		"fps denominator":   func(c *RadioEncodingContract) { c.Video.FrameRate.Denominator++ },
		"pixel format":      func(c *RadioEncodingContract) { c.Video.PixelFormat = "yuv422p" },
		"gop":               func(c *RadioEncodingContract) { c.Video.GOPFrames++ },
		"minimum keyframe":  func(c *RadioEncodingContract) { c.Video.MinKeyframeFrames++ },
		"forced cadence":    func(c *RadioEncodingContract) { c.Video.ForcedKeyframeSeconds += .5 },
		"b frames":          func(c *RadioEncodingContract) { c.Video.BFrames++ },
		"rate control":      func(c *RadioEncodingContract) { c.Video.RateControl.Mode = "cbr" },
		"video bitrate":     func(c *RadioEncodingContract) { c.Video.RateControl.Bitrate = "2500k" },
		"max rate":          func(c *RadioEncodingContract) { c.Video.RateControl.MaxRate = "3000k" },
		"buffer size":       func(c *RadioEncodingContract) { c.Video.RateControl.BufferSize = "6000k" },
		"audio codec":       func(c *RadioEncodingContract) { c.Audio.Codec = "opus" },
		"audio bitrate":     func(c *RadioEncodingContract) { c.Audio.Bitrate = "192k" },
		"audio sample rate": func(c *RadioEncodingContract) { c.Audio.SampleRate++ },
		"audio channels":    func(c *RadioEncodingContract) { c.Audio.Channels++ },
		"private parameter": func(c *RadioEncodingContract) { c.PrivateParameters[0].Value = "false" },
	}
	baseFingerprint := base.EncodingFingerprint()
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := base.Clone()
			mutate(&candidate)
			if got := candidate.EncodingFingerprint(); got == baseFingerprint {
				t.Fatalf("mutation did not change fingerprint: %s", got)
			}
		})
	}
}

func TestRadioRecipeTypedSemanticsDriveArgsAndFingerprints(t *testing.T) {
	base := testRadioRecipe(CPUVideoEncoder(EncoderLowLatency))
	base.SourcePath, base.ASSPath, base.FontDir, base.OutputPath = "in.mp3", "overlay.ass", "fonts", "out.mp4"
	baseArgs := strings.Join(base.FFmpegArgs(nil), "\x00")
	baseStream, baseRender := base.StreamFingerprint(), base.AssetRenderFingerprint()
	tests := []struct {
		name                         string
		mutate                       func(*RadioRenderRecipe)
		streamChanges, renderChanges bool
	}{
		{"height", func(r *RadioRenderRecipe) { r.Preset.Height = 1080 }, true, true},
		{"video bitrate", func(r *RadioRenderRecipe) { r.Preset.VideoBitrate = "2500k" }, true, false},
		{"audio bitrate", func(r *RadioRenderRecipe) { r.Preset.AudioBitrate = "192k" }, true, false},
		{"legacy cadence", func(r *RadioRenderRecipe) { r.DeliveryProfile = "rtsp-low" }, true, false},
		{"audio filter", func(r *RadioRenderRecipe) { r.AudioFilter = "volume=0.8" }, false, true},
		{"fade rule", func(r *RadioRenderRecipe) { r.EdgeFadeSeconds = 1.1 }, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			tc.mutate(&candidate)
			if got := strings.Join(candidate.FFmpegArgs(nil), "\x00"); got == baseArgs {
				t.Fatal("semantic mutation did not change emitted args")
			}
			if changed := candidate.StreamFingerprint() != baseStream; changed != tc.streamChanges {
				t.Fatalf("stream changed=%v, want %v", changed, tc.streamChanges)
			}
			if changed := candidate.AssetRenderFingerprint() != baseRender; changed != tc.renderChanges {
				t.Fatalf("render changed=%v, want %v", changed, tc.renderChanges)
			}
		})
	}
}

func TestRadioRecipeDoesNotFingerprintOrEmitUnusedCRF(t *testing.T) {
	base := testRadioRecipe(CPUVideoEncoder(EncoderLowLatency))
	base.SourcePath, base.ASSPath, base.FontDir, base.OutputPath = "in", "ass", "fonts", "out"
	candidate := base
	candidate.Preset.CRF = base.Preset.CRF + 9
	if strings.Join(candidate.FFmpegArgs(nil), "\x00") != strings.Join(base.FFmpegArgs(nil), "\x00") {
		t.Fatal("unused CRF changed radio arguments")
	}
	if candidate.StreamFingerprint() != base.StreamFingerprint() || candidate.AssetRenderFingerprint() != base.AssetRenderFingerprint() {
		t.Fatal("unused CRF changed fingerprint")
	}
}

func TestRadioRenderContractIncludesActualLayoutWaveformASSAndFadeRules(t *testing.T) {
	recipe := testRadioRecipe(CPUVideoEncoder(EncoderLowLatency))
	contract := recipe.AssetRenderRecipeContract()
	if contract.Waveform.Width == 0 || contract.Waveform.Height == 0 || contract.Waveform.Rate != 30 || contract.Waveform.Mode != "line" {
		t.Fatalf("waveform contract = %+v", contract.Waveform)
	}
	if contract.Overlay.X == 0 || contract.Overlay.Y == 0 || contract.ASS.Renderer == "" || contract.Fades.EdgeSeconds != radioEdgeFadeSeconds {
		t.Fatalf("render contract = %+v", contract)
	}
	short := recipe
	short.DurationSeconds = .5
	if short.AssetRenderFingerprint() != recipe.AssetRenderFingerprint() {
		t.Fatal("content duration changed reusable render-rule fingerprint")
	}
	if short.AssetRenderContentValues().FadeEnabled || !recipe.AssetRenderContentValues().FadeEnabled {
		t.Fatal("content-specific fade state not separated")
	}
}

func TestRadioRenderRecipeFingerprintExcludesExecutionDetails(t *testing.T) {
	base := testRadioRecipe(CPUVideoEncoder(EncoderLowLatency))
	want := base.EncodingFingerprint()
	mutations := []func(*RadioRenderRecipe){
		func(r *RadioRenderRecipe) { r.SourcePath = `C:\\input.mp3` },
		func(r *RadioRenderRecipe) { r.ASSPath = `C:\\temp\\overlay.ass` },
		func(r *RadioRenderRecipe) { r.FontDir = `D:\\fonts` },
		func(r *RadioRenderRecipe) { r.OutputPath = `D:\\output.mp4` },
		func(r *RadioRenderRecipe) { r.LogLevel = "debug" },
		func(r *RadioRenderRecipe) { r.ProgressEnabled = true },
		func(r *RadioRenderRecipe) { r.ThreadCount = 3 },
		func(r *RadioRenderRecipe) { r.OutputTimestampOffset = 12.5 },
		func(r *RadioRenderRecipe) { r.MuxURL = "rtsp://example.invalid/live" },
		func(r *RadioRenderRecipe) { r.AppVersion = "v99" },
	}
	for i, mutate := range mutations {
		candidate := base
		mutate(&candidate)
		if got := candidate.EncodingFingerprint(); got != want {
			t.Fatalf("excluded mutation %d changed fingerprint: %s != %s", i, got, want)
		}
	}
}

func TestRadioRenderRecipeCPUAndGPUEquivalentContractsMatch(t *testing.T) {
	cpu := testRadioRecipe(CPUVideoEncoder(EncoderLowLatency))
	gpuProfile := NewVideoEncoderProfile("h264_nvenc", EncoderLowLatency)
	gpu := testRadioRecipe(gpuProfile)
	if cpu.EncodingFingerprint() != gpu.EncodingFingerprint() {
		t.Fatalf("equivalent CPU/GPU contracts differ:\n%s\n%s", cpu.CanonicalEncodingJSON(), gpu.CanonicalEncodingJSON())
	}
}

func TestLegacyRadioRecipeGOPMatrixIsPreserved(t *testing.T) {
	wants := map[string]int{
		"hls-high":      120,
		"hls":           30,
		"rtsp-low":      60,
		"rtsp-ultra":    30,
		"rtsp-realtime": 15,
	}
	for profile, want := range wants {
		recipe := testRadioRecipe(CPUVideoEncoder(EncoderLowLatency))
		recipe.DeliveryProfile = profile
		if got := recipe.NormalizedEncodingContract().Video.GOPFrames; got != want {
			t.Fatalf("profile %s GOP = %d, want %d", profile, got, want)
		}
	}
}

func TestParseRadioAssetSpecFromFFprobeJSON(t *testing.T) {
	data := []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":1280,"height":720,"pix_fmt":"yuv420p","r_frame_rate":"30/1","has_b_frames":0,"bit_rate":"2400000"},{"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2,"bit_rate":"160000"}],"frames":[{"media_type":"video","key_frame":1,"best_effort_timestamp_time":"0.0"},{"media_type":"video","key_frame":1,"best_effort_timestamp_time":"1.0"}]}`)
	spec, err := ParseRadioAssetSpecJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Video.Codec != "h264" || spec.Video.Width != 1280 || spec.Video.FrameRate.Numerator != 30 {
		t.Fatalf("video spec = %+v", spec.Video)
	}
	if spec.Audio.Codec != "aac" || spec.Audio.SampleRate != 48000 || spec.Audio.Channels != 2 {
		t.Fatalf("audio spec = %+v", spec.Audio)
	}
}

func TestRadioAssetGOPProofRejectsInsufficientMalformedAndVariableCadence(t *testing.T) {
	base := `{"streams":[{"codec_type":"video","codec_name":"h264","width":1280,"height":720,"pix_fmt":"yuv420p","r_frame_rate":"30/1","has_b_frames":0},{"codec_type":"audio","codec_name":"aac","sample_rate":"48000","channels":2}],"frames":%s}`
	cases := map[string]string{
		"no keyframes":        `[]`,
		"single keyframe":     `[{"media_type":"video","key_frame":1,"best_effort_timestamp_time":"0"}]`,
		"malformed timestamp": `[{"media_type":"video","key_frame":1,"best_effort_timestamp_time":"bad"},{"media_type":"video","key_frame":1,"best_effort_timestamp_time":"1"}]`,
		"variable cadence":    `[{"media_type":"video","key_frame":1,"best_effort_timestamp_time":"0"},{"media_type":"video","key_frame":1,"best_effort_timestamp_time":"1"},{"media_type":"video","key_frame":1,"best_effort_timestamp_time":"2.2"}]`,
	}
	for name, frames := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRadioAssetSpecJSON([]byte(fmt.Sprintf(base, frames))); err == nil {
				t.Fatal("unproven GOP accepted")
			}
		})
	}
}
