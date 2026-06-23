package video

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEncoderPriorityByPlatform(t *testing.T) {
	tests := []struct {
		goos string
		want []string
	}{
		{"windows", []string{"h264_nvenc", "h264_qsv", "h264_amf", "libx264"}},
		{"darwin", []string{"h264_videotoolbox", "libx264"}},
		{"linux", []string{"libx264"}},
	}
	for _, tc := range tests {
		if got := EncoderPriority(tc.goos); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("EncoderPriority(%q) = %#v, want %#v", tc.goos, got, tc.want)
		}
	}
}

func TestParseAdvertisedVideoEncoders(t *testing.T) {
	output := ` V..... h264_nvenc           NVIDIA NVENC H.264 encoder
 V..... h264_qsv             H.264 / AVC / MPEG-4 AVC
 A..... aac                  AAC encoder`
	got := parseAdvertisedVideoEncoders(output)
	for _, name := range []string{"h264_nvenc", "h264_qsv"} {
		if !got[name] {
			t.Errorf("advertised encoders missing %q: %#v", name, got)
		}
	}
	if got["aac"] {
		t.Fatal("audio encoder was parsed as video encoder")
	}
}

func TestSelectVideoEncoderProbesInPriorityOrderAndCaches(t *testing.T) {
	resetVideoEncoderCacheForTest()
	oldList := listAvailableEncoders
	oldProbe := probeEncoder
	defer func() {
		listAvailableEncoders = oldList
		probeEncoder = oldProbe
		resetVideoEncoderCacheForTest()
	}()

	listCalls := 0
	listAvailableEncoders = func(context.Context, string) (map[string]bool, error) {
		listCalls++
		return map[string]bool{"h264_nvenc": true, "h264_qsv": true, "h264_amf": true, "libx264": true}, nil
	}
	var probes []string
	probeEncoder = func(_ context.Context, _ string, profile VideoEncoderProfile) error {
		probes = append(probes, profile.Name)
		if profile.Name == "h264_nvenc" {
			return errors.New("driver unavailable")
		}
		return nil
	}

	first := selectVideoEncoderForOS(context.Background(), "fake-ffmpeg", "windows", EncoderStandard)
	second := selectVideoEncoderForOS(context.Background(), "fake-ffmpeg", "windows", EncoderLowLatency)
	if first.Name != "h264_qsv" || second.Name != "h264_qsv" {
		t.Fatalf("selected first=%q second=%q, want h264_qsv", first.Name, second.Name)
	}
	if first.Purpose != EncoderStandard || second.Purpose != EncoderLowLatency {
		t.Fatalf("purposes were not preserved: first=%q second=%q", first.Purpose, second.Purpose)
	}
	if !reflect.DeepEqual(probes, []string{"h264_nvenc", "h264_qsv"}) {
		t.Fatalf("probe order = %#v", probes)
	}
	if listCalls != 1 {
		t.Fatalf("list calls = %d, want cached single call", listCalls)
	}
}

func TestSelectVideoEncoderFallsBackToCPUWhenDiscoveryFails(t *testing.T) {
	resetVideoEncoderCacheForTest()
	oldList := listAvailableEncoders
	defer func() {
		listAvailableEncoders = oldList
		resetVideoEncoderCacheForTest()
	}()
	listAvailableEncoders = func(context.Context, string) (map[string]bool, error) {
		return nil, errors.New("ffmpeg failed")
	}
	got := selectVideoEncoderForOS(context.Background(), "broken-ffmpeg", "windows", EncoderStandard)
	if got.Name != "libx264" || got.Hardware {
		t.Fatalf("fallback profile = %#v, want CPU", got)
	}
}

func TestRunVideoEncodeFallsBackToCPUOnce(t *testing.T) {
	hardware := NewVideoEncoderProfile("h264_nvenc", EncoderStandard)
	var attempts []string
	cleanups := 0
	err := runVideoEncodeWithFallback(context.Background(), hardware, func() { cleanups++ }, func(profile VideoEncoderProfile) error {
		attempts = append(attempts, profile.Name)
		if profile.Hardware {
			return errors.New("GPU session unavailable")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attempts, []string{"h264_nvenc", "libx264"}) || cleanups != 1 {
		t.Fatalf("attempts=%#v cleanups=%d", attempts, cleanups)
	}
}

func TestRunVideoEncodeDoesNotFallbackAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := runVideoEncodeWithFallback(ctx, NewVideoEncoderProfile("h264_qsv", EncoderStandard), nil, func(VideoEncoderProfile) error {
		attempts++
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("err=%v attempts=%d, want canceled and one attempt", err, attempts)
	}
}

func TestVisualizerAndSoundCloudArgsUseInjectedEncoder(t *testing.T) {
	preset := QualityPreset{Height: 720, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k", CRF: 27}
	encoder := NewVideoEncoderProfile("h264_nvenc", EncoderStandard)
	visualizer := strings.Join(audioVisualizerFFmpegArgsWithEncoder("audio.m4a", "vis.ass", ".", "id", preset, nil, encoder, ""), " ")
	soundcloud := strings.Join(soundCloudHLSArgsWithEncoder("audio.m4a", "art.png", "id", preset, encoder), " ")
	for name, args := range map[string]string{"visualizer": visualizer, "soundcloud": soundcloud} {
		if !strings.Contains(args, "-c:v h264_nvenc") || strings.Contains(args, "-c:v libx264") {
			t.Errorf("%s args did not use injected encoder: %s", name, args)
		}
	}
}

func TestStillAndUploadedArgsUseInjectedEncoder(t *testing.T) {
	preset := QualityPreset{Height: 720, VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", AudioBitrate: "128k", CRF: 27}
	encoder := NewVideoEncoderProfile("h264_qsv", EncoderStandard)
	tests := map[string][]string{
		"still-mp4": stillMP4ArgsWithEncoder("image.png", "out.mp4", preset, encoder),
		"still-hls": stillHLSArgsWithEncoder("image.png", "id", preset, encoder),
		"uploaded":  uploadedHLSArgsWithEncoder("video.mp4", "id", preset, encoder),
	}
	for name, raw := range tests {
		args := strings.Join(raw, " ")
		if !strings.Contains(args, "-c:v h264_qsv") || strings.Contains(args, "-c:v libx264") {
			t.Errorf("%s args did not use injected encoder: %s", name, args)
		}
	}
}

func TestStaticContentEncodeOptions(t *testing.T) {
	cases := map[string][]string{
		"libx264":    {"-tune animation", "-sc_threshold 0", "-aq-mode 3", "-g 120", "-keyint_min 120"},
		"h264_nvenc": {"-g 120", "-keyint_min 120", "-rc-lookahead 20", "-no-scenecut 1", "-bf 3", "-aq-strength 12"},
		"h264_amf":   {"-g 120", "-keyint_min 120", "-bf 3"},
		"h264_qsv":   {"-g 120", "-keyint_min 120"},
	}
	for name, wants := range cases {
		joined := strings.Join(staticContentEncodeOptions(NewVideoEncoderProfile(name, EncoderStandard)), " ")
		for _, w := range wants {
			if !strings.Contains(joined, w) {
				t.Errorf("%s static options missing %q: %s", name, w, joined)
			}
		}
	}
	// GPU encoders must never receive the libx264-private flags.
	nv := strings.Join(staticContentEncodeOptions(NewVideoEncoderProfile("h264_nvenc", EncoderStandard)), " ")
	if strings.Contains(nv, "-sc_threshold") || strings.Contains(nv, "-tune animation") {
		t.Errorf("nvenc must not use libx264-only flags: %s", nv)
	}
}

func TestStandardHardwareUsesConstantQualityNotTargetBitrate(t *testing.T) {
	// Regression: GPU standard encodes must use constant quality (capped), not
	// a target bitrate, so highly compressible visualizer video stays small
	// instead of being padded toward the target bitrate (the "file explosion").
	preset := QualityPreset{VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", CRF: 27}
	for _, name := range []string{"h264_nvenc", "h264_qsv", "h264_amf"} {
		joined := strings.Join(NewVideoEncoderProfile(name, EncoderStandard).FFmpegArgs(preset, "medium"), " ")
		if strings.Contains(joined, "-b:v 2500k") {
			t.Errorf("%s standard uses target bitrate -b:v 2500k (causes file bloat): %s", name, joined)
		}
	}
}

func TestVideoEncoderProfileArguments(t *testing.T) {
	preset := QualityPreset{VideoBitrate: "2500k", MaxRate: "3000k", BufferSize: "5000k", CRF: 27}
	tests := []struct {
		name     string
		purpose  EncoderPurpose
		hardware bool
		contains []string
	}{
		{"libx264", EncoderStandard, false, []string{"-c:v libx264", "-preset medium", "-crf 27"}},
		{"libx264", EncoderLowLatency, false, []string{"-preset ultrafast", "-tune zerolatency"}},
		{"h264_nvenc", EncoderStandard, true, []string{"-c:v h264_nvenc", "-preset p6", "-rc vbr", "-cq 27", "-b:v 0", "-spatial_aq 1", "-bf 3"}},
		{"h264_nvenc", EncoderLowLatency, true, []string{"-preset p1", "-tune ull", "-b:v 2500k"}},
		{"h264_qsv", EncoderStandard, true, []string{"-c:v h264_qsv", "-preset medium", "-global_quality 27"}},
		{"h264_amf", EncoderStandard, true, []string{"-c:v h264_amf", "-quality quality", "-rc cqp", "-qp_i 27", "-qp_p 27", "-vbaq 1"}},
		{"h264_amf", EncoderLowLatency, true, []string{"-c:v h264_amf", "-quality speed", "-usage lowlatency"}},
		{"h264_videotoolbox", EncoderStandard, true, []string{"-c:v h264_videotoolbox", "-realtime 0"}},
	}
	for _, tc := range tests {
		t.Run(tc.name+"/"+string(tc.purpose), func(t *testing.T) {
			profile := NewVideoEncoderProfile(tc.name, tc.purpose)
			if profile.Hardware != tc.hardware {
				t.Fatalf("Hardware = %v, want %v", profile.Hardware, tc.hardware)
			}
			joined := strings.Join(profile.FFmpegArgs(preset, "medium"), " ")
			for _, want := range tc.contains {
				if !strings.Contains(joined, want) {
					t.Errorf("args %q missing %q", joined, want)
				}
			}
		})
	}
}
