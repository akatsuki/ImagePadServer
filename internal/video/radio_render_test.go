package video

import (
	"strings"
	"testing"
)

func TestRadioTrackFileName(t *testing.T) {
	if got := RadioTrackFileName("abc123"); got != "radio-track-abc123.ts" {
		t.Fatalf("RadioTrackFileName = %q", got)
	}
}

func TestAudioVisualizerMPEGTSArgs(t *testing.T) {
	preset := QualityPreset{Height: 720, AudioBitrate: "192k", VideoBitrate: "4000k", MaxRate: "4500k", BufferSize: "8000k"}
	args := audioVisualizerMPEGTSArgsWithEncoder("song.m4a", "sub.ass", "fonts", "out/radio-track-x.ts", preset, nil, CPUVideoEncoder(EncoderStandard), "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-f mpegts") {
		t.Fatalf("args must select mpegts muxer: %s", joined)
	}
	if args[len(args)-1] != "out/radio-track-x.ts" {
		t.Fatalf("last arg must be output path, got %q", args[len(args)-1])
	}
	if strings.Contains(joined, "-f hls") || strings.Contains(joined, "hls_segment_filename") {
		t.Fatalf("mpegts args must not contain HLS options: %s", joined)
	}
	// Core render options shared with the HLS path must be present.
	for _, want := range []string{"-i song.m4a", "-c:a aac", "-pix_fmt yuv420p", "showwaves"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
}

func TestHLSArgsUnchangedByRefactor(t *testing.T) {
	preset := QualityPreset{Height: 720, AudioBitrate: "192k", VideoBitrate: "4000k", MaxRate: "4500k", BufferSize: "8000k"}
	args := audioVisualizerFFmpegArgsWithEncoder("song.m4a", "sub.ass", "fonts", "media1", preset, nil, CPUVideoEncoder(EncoderStandard), "")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-f hls", "-hls_playlist_type event", segmentPattern("media1"), playlistName("media1")} {
		if !strings.Contains(joined, want) {
			t.Fatalf("HLS args missing %q: %s", want, joined)
		}
	}
}

func TestRadioPushArgs(t *testing.T) {
	args := RadioPushArgs("out/radio-track-x.ts", "rtsp://127.0.0.1:8554/radio")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-re", "-i out/radio-track-x.ts", "-c copy", "-f rtsp", "-rtsp_transport tcp"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("push args missing %q: %s", want, joined)
		}
	}
	if args[len(args)-1] != "rtsp://127.0.0.1:8554/radio" {
		t.Fatalf("last arg must be RTSP URL, got %q", args[len(args)-1])
	}
	if strings.Contains(joined, "libx264") || strings.Contains(joined, "aac") {
		t.Fatalf("push must be codec copy only: %s", joined)
	}
}
