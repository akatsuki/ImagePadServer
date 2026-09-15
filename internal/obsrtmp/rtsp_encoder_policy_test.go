package obsrtmp

import (
	"reflect"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

func TestRTSPEncoderDiagnosticPolicyChangesOnlyTuneForLibx264(t *testing.T) {
	manager := New("", "127.0.0.1", 1935, "key", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	}, Callbacks{})
	contract := manager.argumentContract("session", video.ResolveQuality("auto", 0), video.CPUVideoEncoder(video.EncoderLowLatency))
	base := manager.ffmpegRTSPArgsForContract(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs")
	policy := RTSPEncoderDiagnosticPolicy{Encoder: contract.VideoEncoderProfile, Tune: "animation"}
	got, err := manager.FFmpegRTSPArgsForDiagnostic(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs", policy)
	if err != nil {
		t.Fatalf("diagnostic args: %v", err)
	}
	if !reflect.DeepEqual(withoutTune(base), withoutTune(got)) {
		t.Fatalf("diagnostic policy changed options other than tune:\nbase=%v\n got=%v", base, got)
	}
	if tune := optionValue(got, "-tune"); tune != "animation" {
		t.Fatalf("tune = %q, want animation", tune)
	}
}

func TestRTSPEncoderDiagnosticPolicyChangesOnlyTuneForNVENC(t *testing.T) {
	manager := New("", "127.0.0.1", 1935, "key", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	}, Callbacks{})
	contract := manager.argumentContract("session", video.ResolveQuality("auto", 0), video.NewVideoEncoderProfile("h264_nvenc", video.EncoderLowLatency))
	base := manager.ffmpegRTSPArgsForContract(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs")
	policy := RTSPEncoderDiagnosticPolicy{Encoder: contract.VideoEncoderProfile, Tune: "hq"}
	got, err := manager.FFmpegRTSPArgsForDiagnostic(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs", policy)
	if err != nil {
		t.Fatalf("diagnostic args: %v", err)
	}
	if !reflect.DeepEqual(withoutTune(base), withoutTune(got)) {
		t.Fatalf("diagnostic policy changed options other than tune:\nbase=%v\n got=%v", base, got)
	}
	if tune := optionValue(got, "-tune"); tune != "hq" {
		t.Fatalf("tune = %q, want hq", tune)
	}
}

func TestRTSPEncoderDiagnosticPolicyRejectsUnknownOrCrossEncoderTune(t *testing.T) {
	manager := New("", "127.0.0.1", 1935, "key", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	}, Callbacks{})
	cpu := manager.argumentContract("cpu", video.ResolveQuality("auto", 0), video.CPUVideoEncoder(video.EncoderLowLatency))
	for _, tune := range []string{"unknown", "ull"} {
		if _, err := manager.FFmpegRTSPArgsForDiagnostic(cpu, "recording.mp4", "rtsp://127.0.0.1:8554/obs", RTSPEncoderDiagnosticPolicy{Encoder: cpu.VideoEncoderProfile, Tune: tune}); err == nil {
			t.Fatalf("tune %q was accepted for libx264", tune)
		}
	}
	nvenc := manager.argumentContract("nvenc", video.ResolveQuality("auto", 0), video.NewVideoEncoderProfile("h264_nvenc", video.EncoderLowLatency))
	if _, err := manager.FFmpegRTSPArgsForDiagnostic(nvenc, "recording.mp4", "rtsp://127.0.0.1:8554/obs", RTSPEncoderDiagnosticPolicy{Encoder: nvenc.VideoEncoderProfile, Tune: "zerolatency"}); err == nil {
		t.Fatal("libx264 tune was accepted for NVENC")
	}
}

func TestRTSPEncoderDiagnosticPolicyKeepsRecordingAndRTSPOutput(t *testing.T) {
	manager := New("", "127.0.0.1", 1935, "key", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	}, Callbacks{})
	contract := manager.argumentContract("session", video.ResolveQuality("auto", 0), video.CPUVideoEncoder(video.EncoderLowLatency))
	args, err := manager.FFmpegRTSPArgsForDiagnostic(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs", RTSPEncoderDiagnosticPolicy{Encoder: contract.VideoEncoderProfile, Tune: "zerolatency"})
	if err != nil {
		t.Fatalf("diagnostic args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-f rtsp", "-rtsp_transport tcp", "-pkt_size 1200", "-c copy", "-movflags +faststart", "recording.mp4"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %v", want, args)
		}
	}
}

func TestRTSPDiagnosticTuneIsOptInAtActiveRTSPPath(t *testing.T) {
	manager := New("", "127.0.0.1", 1935, "key", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	}, Callbacks{})
	contract := manager.argumentContract("session", video.ResolveQuality("auto", 0), video.CPUVideoEncoder(video.EncoderLowLatency))
	t.Setenv(rtspDiagnosticTuneEnv, "animation")
	got, err := manager.ffmpegRTSPArgsForActiveContract(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs")
	if err != nil {
		t.Fatalf("active diagnostic args: %v", err)
	}
	if tune := optionValue(got, "-tune"); tune != "animation" {
		t.Fatalf("active RTSP tune = %q, want animation", tune)
	}
	t.Setenv(rtspDiagnosticTuneEnv, "")
	base, err := manager.ffmpegRTSPArgsForActiveContract(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs")
	if err != nil {
		t.Fatalf("normal active args: %v", err)
	}
	expected := manager.ffmpegRTSPArgsForContract(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs")
	if !reflect.DeepEqual(base, expected) {
		t.Fatalf("normal active path changed the contract:\nbase=%v\nwant=%v", base, expected)
	}
}

func TestRTSPUsesFastSoftwarePresetWithoutChangingHLS(t *testing.T) {
	manager := New("", "127.0.0.1", 1935, "key", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	}, Callbacks{})
	contract := manager.argumentContract("session", video.ResolveQuality("auto", 0), video.CPUVideoEncoder(video.EncoderLowLatency))
	rtsp := manager.ffmpegRTSPArgsForContract(contract, "recording.mp4", "rtsp://127.0.0.1:8554/obs")
	if got := optionValue(rtsp, "-preset"); got != "fast" {
		t.Fatalf("RTSP software preset = %q, want fast: %v", got, rtsp)
	}
	hls := manager.ffmpegLHLSArgsForContract(contract, "recording.mp4", "http://127.0.0.1:8888/obs")
	if got := optionValue(hls, "-preset"); got != "ultrafast" {
		t.Fatalf("HLS software preset = %q, want ultrafast: %v", got, hls)
	}
}

func withoutTune(args []string) []string {
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-tune" && i+1 < len(args) {
			i++
			continue
		}
		result = append(result, args[i])
	}
	return result
}

func optionValue(args []string, option string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == option {
			return args[i+1]
		}
	}
	return ""
}
