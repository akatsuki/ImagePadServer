package video

import (
	"strings"
	"testing"
)

func TestRadioTrackFileName(t *testing.T) {
	// MP4 が必須: FLV/RTSP への copy remux は AAC グローバルヘッダーを要求する。
	if got := RadioTrackFileName("abc123"); got != "radio-track-abc123.mp4" {
		t.Fatalf("RadioTrackFileName = %q", got)
	}
}

func TestAudioVisualizerMP4ArgsWithEdgeFades(t *testing.T) {
	preset := QualityPreset{Height: 720, AudioBitrate: "192k", VideoBitrate: "4000k", MaxRate: "4500k", BufferSize: "8000k"}
	args := audioVisualizerMP4ArgsWithEncoder("song.m4a", "sub.ass", "fonts", "out/radio-track-x.mp4", preset, nil, CPUVideoEncoder(EncoderStandard), "", 200)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-f mp4") || !strings.Contains(joined, "-movflags +faststart") {
		t.Fatalf("args must select the mp4 muxer with faststart: %s", joined)
	}
	if args[len(args)-1] != "out/radio-track-x.mp4" {
		t.Fatalf("last arg must be output path, got %q", args[len(args)-1])
	}
	// 黒フェード: 曲頭フェードイン + 曲末フェードアウト（映像・音声とも）。
	for _, want := range []string{"fade=t=in:st=0:d=0.70", "fade=t=out:st=199.30:d=0.70", "afade=t=in:st=0:d=0.70", "afade=t=out:st=199.30:d=0.70", "-map [vfade]", "-map [afade]"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
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
	// 単曲モードにはフェードを入れない。
	if strings.Contains(joined, "fade=") {
		t.Fatalf("HLS args must not contain fades: %s", joined)
	}
}

func TestRadioPublisherArgs(t *testing.T) {
	args := RadioPublisherArgs("rtmp://127.0.0.1:1935/radio?user=u&pass=p")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-f mpegts -i pipe:0",
		"-c copy",
		"-bsf:a aac_adtstoasc", // TS の ADTS AAC を FLV 用 ASC に変換する要
		"-f flv",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("publisher args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-use_wallclock_as_timestamps") {
		t.Fatalf("publisher must preserve feeder PTS instead of deriving frame timing from pipe arrival: %s", joined)
	}
	if args[len(args)-1] != "rtmp://127.0.0.1:1935/radio?user=u&pass=p" {
		t.Fatalf("last arg must be the RTMP URL, got %q", args[len(args)-1])
	}
}

func TestRadioFeederArgs(t *testing.T) {
	base := strings.Join(RadioFeederArgs("track.mp4", 0, false, 0), " ")
	for _, want := range []string{"-re", "-i track.mp4", "-c copy", "-f mpegts pipe:1"} {
		if !strings.Contains(base, want) {
			t.Fatalf("feeder args missing %q: %s", want, base)
		}
	}
	if strings.Contains(base, "-stream_loop") || strings.Contains(base, "-ss") || strings.Contains(base, "-output_ts_offset") {
		t.Fatalf("plain feeder must not loop, seek, or offset timestamps: %s", base)
	}
	resumed := strings.Join(RadioFeederArgs("track.mp4", 97, false, 0), " ")
	if !strings.Contains(resumed, "-ss 97 -i track.mp4") {
		t.Fatalf("resume feeder must seek before the input: %s", resumed)
	}
	offset := strings.Join(RadioFeederArgs("track.mp4", 0, false, 123.4567), " ")
	if !strings.Contains(offset, "-output_ts_offset 123.457") {
		t.Fatalf("offset feeder must shift output timestamps for a continuous publisher stream: %s", offset)
	}
	filler := strings.Join(RadioFeederArgs("filler.mp4", 0, true, 0), " ")
	if !strings.Contains(filler, "-stream_loop -1") {
		t.Fatalf("filler feeder must loop forever: %s", filler)
	}
}
