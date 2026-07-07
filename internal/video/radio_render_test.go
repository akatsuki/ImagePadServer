package video

import (
	"strings"
	"testing"
)

func TestRadioTrackFileName(t *testing.T) {
	// MP4 が必須: RTSP muxer は ADTS(TS) の AAC を global header なしとして拒否する。
	if got := RadioTrackFileName("abc123"); got != "radio-track-abc123.mp4" {
		t.Fatalf("RadioTrackFileName = %q", got)
	}
}

func TestAudioVisualizerMP4Args(t *testing.T) {
	preset := QualityPreset{Height: 720, AudioBitrate: "192k", VideoBitrate: "4000k", MaxRate: "4500k", BufferSize: "8000k"}
	args := audioVisualizerMP4ArgsWithEncoder("song.m4a", "sub.ass", "fonts", "out/radio-track-x.mp4", preset, nil, CPUVideoEncoder(EncoderStandard), "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-f mp4") || !strings.Contains(joined, "-movflags +faststart") {
		t.Fatalf("args must select the mp4 muxer with faststart: %s", joined)
	}
	if args[len(args)-1] != "out/radio-track-x.mp4" {
		t.Fatalf("last arg must be output path, got %q", args[len(args)-1])
	}
	if strings.Contains(joined, "-f hls") || strings.Contains(joined, "hls_segment_filename") || strings.Contains(joined, "mpegts") {
		t.Fatalf("mp4 args must not contain HLS/TS options: %s", joined)
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
	// segmentPattern はタイムスタンプ入りなので固定部分だけ検査する。
	for _, want := range []string{"-f hls", "-hls_playlist_type event", "current-media1-", "-%d.ts", playlistName("media1")} {
		if !strings.Contains(joined, want) {
			t.Fatalf("HLS args missing %q: %s", want, joined)
		}
	}
}

func TestRadioPushArgs(t *testing.T) {
	args := RadioPushArgs("out/radio-track-x.mp4", 0, "rtsp://127.0.0.1:8554/radio")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "-ss") {
		t.Fatalf("offset 0 must not add -ss: %s", joined)
	}
	resumed := strings.Join(RadioPushArgs("out/radio-track-x.mp4", 97, "rtsp://127.0.0.1:8554/radio"), " ")
	if !strings.Contains(resumed, "-ss 97 -i out/radio-track-x.mp4") {
		t.Fatalf("resume args must seek before the input: %s", resumed)
	}
	for _, want := range []string{"-re", "-i out/radio-track-x.mp4", "-c copy", "-f rtsp", "-rtsp_transport tcp"} {
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
