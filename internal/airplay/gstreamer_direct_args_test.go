package airplay

import (
	"fmt"
	"strings"
	"testing"

	"imagepadserver/internal/airplaycontract"
)

func TestDirectArgsQualityUsesCompleteImmutableOutput(t *testing.T) {
	output := DirectOutputConfig{
		Width: 640, Height: 360, SourceFPS: 60, OutputFPS: 30,
		VideoBitrateKbps: 1800, MaxRateKbps: 2200, BufferSizeKbps: 3600,
		AudioBitrateBps: 96000, GOPFrames: 30,
	}
	args := BuildGStreamerDirectArgs(5000, 5001, "rtsp://127.0.0.1/live", "recording.mp4", "stop", output)
	for flag, want := range map[string]int{
		"--width": output.Width, "--height": output.Height, "--source-fps": output.SourceFPS,
		"--fps": output.OutputFPS, "--bitrate": output.VideoBitrateKbps, "--maxrate": output.MaxRateKbps,
		"--buffer-size": output.BufferSizeKbps, "--audio-bitrate": output.AudioBitrateBps, "--gop": output.GOPFrames,
	} {
		requireFlagValueExactlyOnce(t, args, flag, fmt.Sprintf("%d", want))
	}
}

func buildSourceClockArgsForTest(t *testing.T, publishURL, recording, stopFile, sessionToken, sessionID string, generation uint64, paths airplaycontract.FixedPaths, output DirectOutputConfig) []string {
	t.Helper()
	switch build := any(BuildSourceClockDirectArgs).(type) {
	case func(string, string, string, string, string, DirectOutputConfig) []string:
		return build(publishURL, recording, stopFile, paths.Ready, sessionToken, output)
	case func(string, string, string, string, string, uint64, airplaycontract.FixedPaths, DirectOutputConfig) []string:
		return build(publishURL, recording, stopFile, sessionToken, sessionID, generation, paths, output)
	default:
		t.Fatalf("BuildSourceClockDirectArgs has unsupported signature %T", BuildSourceClockDirectArgs)
		return nil
	}
}

func buildSourceClockFixedPortArgsForTest(t *testing.T, videoPort, audioPort int, publishURL, recording, stopFile, sessionToken, sessionID string, generation uint64, paths airplaycontract.FixedPaths, output DirectOutputConfig) []string {
	t.Helper()
	switch build := any(buildSourceClockDirectArgs).(type) {
	case func(int, int, string, string, string, string, string, DirectOutputConfig) []string:
		return build(videoPort, audioPort, publishURL, recording, stopFile, paths.Ready, sessionToken, output)
	case func(int, int, string, string, string, string, string, uint64, airplaycontract.FixedPaths, DirectOutputConfig) []string:
		return build(videoPort, audioPort, publishURL, recording, stopFile, sessionToken, sessionID, generation, paths, output)
	default:
		t.Fatalf("buildSourceClockDirectArgs has unsupported signature %T", buildSourceClockDirectArgs)
		return nil
	}
}

func requireFlagValueExactlyOnce(t *testing.T, args []string, flag, value string) {
	t.Helper()
	flagCount := 0
	pairCount := 0
	for index, arg := range args {
		if arg != flag {
			continue
		}
		flagCount++
		if index+1 >= len(args) || args[index+1] != value {
			t.Fatalf("%s value in %q, want %q", flag, args, value)
		}
		pairCount++
	}
	if flagCount != 1 || pairCount != 1 {
		t.Fatalf("%s=%q occurrences: flag=%d pair=%d in %q", flag, value, flagCount, pairCount, args)
	}
}

func TestBuildSourceClockDirectArgsBindsEphemeralLoopbackListeners(t *testing.T) {
	output := DirectOutputConfig{
		Width: 1280, Height: 720,
		SourceFPS: 60, OutputFPS: 30,
		VideoBitrateKbps: 2500, MaxRateKbps: 3000, BufferSizeKbps: 5000,
		AudioBitrateBps: 128000, GOPFrames: 15,
	}
	paths, err := airplaycontract.FixedPathsForRecording("record.mp4")
	if err != nil {
		t.Fatal(err)
	}
	args := buildSourceClockArgsForTest(t, "rtsp://127.0.0.1:8554/path", "record.mp4", "stop", strings.Repeat("ab", 16), "obs-session", 7, paths, output)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--source-clock",
		"--video-listen-port 0",
		"--audio-listen-port 0",
		"--media-ready-file record.mp4.media-ready",
		"--width 1280",
		"--height 720",
		"--source-fps 60",
		"--fps 30",
		"--bitrate 2500",
		"--maxrate 3000",
		"--buffer-size 5000",
		"--audio-bitrate 128000",
		"--gop 15",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	requireFlagValueExactlyOnce(t, args, "--no-signal-seconds", "0")
}

func TestBuildSourceClockDirectArgsBindsRestartFixedPortsAndKeepsNoSignalDisabled(t *testing.T) {
	paths, err := airplaycontract.FixedPathsForRecording("restart.mp4")
	if err != nil {
		t.Fatal(err)
	}
	args := buildSourceClockFixedPortArgsForTest(
		t,
		63941,
		63942,
		"rtsp://127.0.0.1:8554/restart",
		"restart.mp4",
		"stop",
		strings.Repeat("ef", 16),
		"restart-session",
		8,
		paths,
		DirectOutputConfig{Width: 1920, Height: 1080, SourceFPS: 60, OutputFPS: 60},
	)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--video-listen-port 63941",
		"--audio-listen-port 63942",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %q", want, joined)
		}
	}
	requireFlagValueExactlyOnce(t, args, "--no-signal-seconds", "0")
}

func TestBuildSourceClockDirectArgsIncludesSchema2IdentityAndFixedPathsExactlyOnce(t *testing.T) {
	paths, err := airplaycontract.FixedPathsForRecording(`C:\capture\airplay.mp4`)
	if err != nil {
		t.Fatal(err)
	}
	args := buildSourceClockArgsForTest(
		t,
		"rtsp://127.0.0.1:8554/schema2",
		`C:\capture\airplay.mp4`,
		`C:\runtime\stop.request`,
		strings.Repeat("cd", 16),
		"obs-session-schema2",
		7,
		paths,
		DirectOutputConfig{Width: 1280, Height: 720, SourceFPS: 60, OutputFPS: 30},
	)

	for _, expected := range []struct {
		flag  string
		value string
	}{
		{flag: "--session-id", value: "obs-session-schema2"},
		{flag: "--publisher-generation", value: "7"},
		{flag: "--ready-file", value: paths.Ready},
		{flag: "--media-ready-file", value: paths.MediaReady},
		{flag: "--event-log", value: paths.EventLog},
	} {
		requireFlagValueExactlyOnce(t, args, expected.flag, expected.value)
	}
}

func TestBuildSourceClockPublisherArgsChangesOnlyGenerationArtifacts(t *testing.T) {
	root := t.TempDir()
	firstArtifacts := airplaycontract.NewPublisherArtifacts(root, "args-session", 1)
	secondArtifacts := airplaycontract.NewPublisherArtifacts(root, "args-session", 2)
	base := sourceClockPublisherArgsConfig{
		VideoListenPort: 47001,
		AudioListenPort: 47002,
		PublishURL:      "rtsp://127.0.0.1:8554/args-session",
		SessionToken:    strings.Repeat("ef", 16),
		Artifacts:       firstArtifacts,
		Output: DirectOutputConfig{
			Width: 1280, Height: 720, SourceFPS: 60, OutputFPS: 30,
			VideoBitrateKbps: 2500, MaxRateKbps: 3000, BufferSizeKbps: 5000,
			AudioBitrateBps: 128000, GOPFrames: 15,
		},
	}
	first := buildSourceClockPublisherArgs(base)
	base.Artifacts = secondArtifacts
	second := buildSourceClockPublisherArgs(base)

	for _, flag := range []string{
		"--video-listen-port", "--audio-listen-port", "--session-token", "--session-id",
		"--publish-url", "--width", "--height", "--source-fps", "--fps", "--bitrate",
		"--maxrate", "--buffer-size", "--audio-bitrate", "--gop", "--no-signal-seconds",
	} {
		if firstValue, secondValue := commandArgValue(first, flag), commandArgValue(second, flag); firstValue == "" || firstValue != secondValue {
			t.Fatalf("stable flag %s changed: first=%q second=%q", flag, firstValue, secondValue)
		}
	}
	for _, expected := range []struct {
		flag   string
		first  string
		second string
	}{
		{flag: "--publisher-generation", first: "1", second: "2"},
		{flag: "--recording", first: firstArtifacts.Recording, second: secondArtifacts.Recording},
		{flag: "--ready-file", first: firstArtifacts.Ready, second: secondArtifacts.Ready},
		{flag: "--media-ready-file", first: firstArtifacts.MediaReady, second: secondArtifacts.MediaReady},
		{flag: "--event-log", first: firstArtifacts.EventLog, second: secondArtifacts.EventLog},
		{flag: "--stop-file", first: firstArtifacts.StopRequest, second: secondArtifacts.StopRequest},
	} {
		requireFlagValueExactlyOnce(t, first, expected.flag, expected.first)
		requireFlagValueExactlyOnce(t, second, expected.flag, expected.second)
		for _, arg := range second {
			if arg == expected.first {
				t.Fatalf("generation-2 args retained generation-1 %s value %q", expected.flag, expected.first)
			}
		}
	}
}

func TestBuildGStreamerDirectArgsPublishesRTSPWithoutFFmpegPipe(t *testing.T) {
	output := DirectOutputConfig{
		Width: 1920, Height: 1080,
		SourceFPS: 60, OutputFPS: 30,
		VideoBitrateKbps: 9000, MaxRateKbps: 10400, BufferSizeKbps: 18000,
		AudioBitrateBps: 160000, GOPFrames: 30,
	}
	args := BuildGStreamerDirectArgs(41001, 41003, "rtsp://pub:pass@127.0.0.1:18554/obs_airplay", "C:\\tmp\\airplay-recording.mp4", "C:\\tmp\\airplay-stop.request", output)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--video-port 41001",
		"--audio-port 41003",
		"--publish-url rtsp://pub:pass@127.0.0.1:18554/obs_airplay",
		"--recording C:\\tmp\\airplay-recording.mp4",
		"--stop-file C:\\tmp\\airplay-stop.request",
		"--width 1920",
		"--height 1080",
		"--source-fps 60",
		"--fps 30",
		"--maxrate 10400",
		"--buffer-size 18000",
		"--audio-bitrate 160000",
		"--gop 30",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("direct args %q do not contain %q", joined, expected)
		}
	}
	if strings.Contains(joined, "--stdout") || strings.Contains(joined, "matroska") {
		t.Fatalf("direct args must not use the old binary pipe: %q", joined)
	}
	if strings.Contains(joined, "--no-signal-seconds") {
		t.Fatalf("ordinary direct args must not include source-clock no-signal policy: %q", joined)
	}
}
