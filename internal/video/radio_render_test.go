package video

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

func TestRadioFallbackFeederArgsConsumeRawVideoStandby(t *testing.T) {
	args := RadioFallbackFeederArgs(QualityPreset{Height: 720, AudioBitrate: "128k"}, 640, 360, 12.345)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-f rawvideo",
		"-pix_fmt rgb24",
		"-s 640x360",
		"-r 30",
		"-i pipe:0",
		"-vf scale=1280:720:flags=bicubic",
		"anullsrc=r=48000:cl=stereo",
		"-output_ts_offset 12.345",
		"-f mpegts pipe:1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("fallback args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-f mp4") || strings.Contains(joined, "out.mp4") {
		t.Fatalf("fallback feeder must generate a live MPEG-TS stream, not a cached MP4: %s", joined)
	}
	if strings.Contains(joined, "filter_complex") || strings.Contains(joined, "drawbox") || strings.Contains(joined, "drawtext") {
		t.Fatalf("fallback visuals must be generated by the Go renderer, not approximated with FFmpeg filters: %s", joined)
	}
}

func TestRadioFallbackRendererWritesAnimatedRGBFrames(t *testing.T) {
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	logo, err := WriteRadioFallbackLogo(dir)
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := NewRadioFallbackRenderer(320, 180, logo, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		t.Fatal(err)
	}
	first := renderer.RenderRGB(0)
	second := renderer.RenderRGB(1.25)
	if len(first) != 320*180*3 {
		t.Fatalf("first frame size = %d", len(first))
	}
	if len(second) != len(first) {
		t.Fatalf("second frame size = %d, want %d", len(second), len(first))
	}
	if bytes.Equal(first, second) {
		t.Fatal("fallback frames must change over time")
	}
}

func TestWriteRadioFallbackPanels(t *testing.T) {
	dir := t.TempDir()
	messagePanel, iconPanel, err := WriteRadioFallbackPanels(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{messagePanel, iconPanel} {
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatalf("panel was not written: %s: %v", path, err)
		}
		if stat.Size() == 0 {
			t.Fatalf("panel is empty: %s", path)
		}
	}
	if filepath.Base(messagePanel) != "radio-fallback-message-panel.png" {
		t.Fatalf("unexpected message panel path: %s", messagePanel)
	}
	if filepath.Base(iconPanel) != "radio-fallback-icon-panel.png" {
		t.Fatalf("unexpected icon panel path: %s", iconPanel)
	}
}

func TestRadioFallbackFeederFFmpegSmoke(t *testing.T) {
	ffmpeg, err := ffmpegPath()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	dir := t.TempDir()
	logo, err := WriteRadioFallbackLogo(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "fallback.ts")
	fonts, err := VisualizerFonts()
	if err != nil {
		t.Fatal(err)
	}
	renderer, err := NewRadioFallbackRenderer(640, 360, logo, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		t.Fatal(err)
	}
	args := RadioFallbackFeederArgs(QualityPreset{Height: 360, AudioBitrate: "96k"}, 640, 360, 0)
	args = append(args[:len(args)-3], "-t", "0.6", "-f", "mpegts", "-y", out)
	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 18; i++ {
		if _, err := stdin.Write(renderer.RenderRGB(float64(i) / 30)); err != nil {
			t.Fatal(err)
		}
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		output := stderr.Bytes()
		t.Fatalf("fallback ffmpeg smoke failed: %v\n%s", err, output)
	}
	if stat, err := os.Stat(out); err != nil || stat.Size() == 0 {
		t.Fatalf("fallback output missing or empty: stat=%v err=%v", stat, err)
	}
}
