package video

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"math"
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
	for _, want := range []string{"-i song.m4a", "-c:a aac", "-pix_fmt yuv420p", "showwaves", "-x264-params aud=1:repeat-headers=1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"-tune zerolatency", "slice-max-size"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("radio MP4 args must not use AVPro-incompatible option %q: %s", forbidden, joined)
		}
	}
}

func TestAudioVisualizerMP4ArgsUseRTSPSafeLiveEncoding(t *testing.T) {
	preset := QualityPreset{Height: 720, AudioBitrate: "128k", VideoBitrate: "900k", MaxRate: "1200k", BufferSize: "2200k"}
	args := audioVisualizerMP4ArgsWithEncoder("song.m4a", "sub.ass", "fonts", "out/radio-track-x.mp4", preset, nil, NewVideoEncoderProfile("h264_nvenc", EncoderLowLatency), "", 200)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c:v h264_nvenc",
		"-tune ull",
		"-b:v 900k",
		"-maxrate 1200k",
		"-bufsize 2200k",
		"-g 30",
		"-keyint_min 30",
		"-bf 0",
		"-force_key_frames expr:gte(t,n_forced*1)",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("radio MP4 args missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"-g 120", "-keyint_min 120", "-rc-lookahead 20", "-bf 3", "-spatial_aq 0"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("radio MP4 args must not use static-content option %q: %s", forbidden, joined)
		}
	}
}

func TestAudioVisualizerMP4ArgsUseAVProSafeCPUEncoding(t *testing.T) {
	preset := QualityPreset{Height: 720, AudioBitrate: "128k", VideoBitrate: "900k", MaxRate: "1200k", BufferSize: "2200k"}
	args := audioVisualizerMP4ArgsWithEncoder("song.m4a", "sub.ass", "fonts", "out/radio-track-x.mp4", preset, nil, CPUVideoEncoder(EncoderLowLatency), "", 200)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c:v libx264",
		"-preset ultrafast",
		"-b:v 900k",
		"-maxrate 1200k",
		"-bufsize 2200k",
		"-g 30",
		"-keyint_min 30",
		"-bf 0",
		"-sc_threshold 0",
		"-x264-params aud=1:repeat-headers=1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("radio CPU MP4 args missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"-tune zerolatency", "slice-max-size"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("radio CPU MP4 args must not use AVPro-incompatible option %q: %s", forbidden, joined)
		}
	}
}

func TestRadioTrackAndFallbackShareRTSPEncoderPolicy(t *testing.T) {
	preset := QualityPreset{Height: 720, AudioBitrate: "128k", VideoBitrate: "900k", MaxRate: "1200k", BufferSize: "2200k"}
	for _, encoder := range []VideoEncoderProfile{
		CPUVideoEncoder(EncoderLowLatency),
		NewVideoEncoderProfile("h264_nvenc", EncoderLowLatency),
	} {
		fallback := strings.Join(RadioFallbackFeederArgsWithEncoder(preset, 1280, 720, 0, encoder), " ")
		track := strings.Join(audioVisualizerMP4ArgsWithEncoder("song.m4a", "sub.ass", "fonts", "out/radio-track-x.mp4", preset, nil, encoder, "", 200), " ")
		for _, want := range append(radioRTSPVideoEncoderArgs(encoder, preset), radioRTSPEncodeOptions(encoder)...) {
			if !strings.Contains(fallback, want) {
				t.Fatalf("fallback args for %s missing shared option %q: %s", encoder.Name, want, fallback)
			}
			if !strings.Contains(track, want) {
				t.Fatalf("track args for %s missing shared option %q: %s", encoder.Name, want, track)
			}
		}
	}
}

func TestRadioProgramEncoderArgsUsePersistentAVProContract(t *testing.T) {
	preset := QualityPreset{
		Height:       720,
		AudioBitrate: "128k",
		VideoBitrate: "900k",
		MaxRate:      "1200k",
		BufferSize:   "2200k",
		RadioLatency: "rtsp-realtime",
	}
	args := strings.Join(RadioProgramEncoderArgs(
		preset,
		CPUVideoEncoder(EncoderLowLatency),
		1280,
		720,
		"tcp://127.0.0.1:41001",
		"tcp://127.0.0.1:41002",
	), "\x00")

	for _, want := range []string{
		"-f\x00rawvideo",
		"-pixel_format\x00rgba",
		"-framerate\x0030",
		"-f\x00s16le",
		"-ar\x0048000",
		"-ac\x002",
		"-pix_fmt\x00yuv420p",
		"-c:a\x00aac",
		"-bf\x000",
		"-g\x0015",
		"-keyint_min\x0015",
		"-fps_mode:v\x00passthrough",
		"-bsf:v\x00h264_metadata=aud=insert,dump_extra=freq=keyframe",
		"-f\x00mpegts",
		"pipe:1",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("program encoder args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "-c\x00copy") || strings.Contains(args, "-c:v\x00copy") {
		t.Fatalf("program encoder unexpectedly uses compatibility copy: %s", args)
	}
}

func TestRadioRTSPEncodeOptionsFollowPlaylistLatencyMode(t *testing.T) {
	tests := []struct {
		mode string
		gop  string
	}{
		{mode: "rtsp-low", gop: "60"},
		{mode: "rtsp-ultra", gop: "30"},
		{mode: "rtsp-realtime", gop: "15"},
		{mode: "", gop: "30"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			args := strings.Join(radioRTSPEncodeOptionsForMode(CPUVideoEncoder(EncoderLowLatency), tt.mode), " ")
			for _, want := range []string{
				"-g " + tt.gop,
				"-keyint_min " + tt.gop,
				"expr:gte(t,n_forced*" + radioLatencyKeyframeSeconds(tt.mode) + ")",
			} {
				if !strings.Contains(args, want) {
					t.Fatalf("latency mode %q args missing %q: %s", tt.mode, want, args)
				}
			}
		})
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
	args := RadioPublisherArgs("rtsp://u:p@127.0.0.1:8554/radio")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-f mpegts -i pipe:0",
		"-c:v copy",
		"-c:a copy",
		"-rtsp_transport tcp",
		"-pkt_size 1200",
		"-muxdelay 0",
		"-f rtsp",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("publisher args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-use_wallclock_as_timestamps") {
		t.Fatalf("publisher must preserve feeder PTS instead of deriving frame timing from pipe arrival: %s", joined)
	}
	if strings.Contains(joined, "-f flv") {
		t.Fatalf("RTSP publisher must not use RTMP/FLV output: %s", joined)
	}
	for _, forbidden := range []string{"-c:a aac", "-b:a", "-ar 48000", "-ac 2"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("publisher must not re-encode already-prepared radio audio with %q: %s", forbidden, joined)
		}
	}
	if args[len(args)-1] != "rtsp://u:p@127.0.0.1:8554/radio" {
		t.Fatalf("last arg must be the RTSP publish URL, got %q", args[len(args)-1])
	}
}

func TestRadioPublisherArgsForRTMPIngest(t *testing.T) {
	args := RadioPublisherArgs("rtmp://127.0.0.1:9999/radio?user=u&pass=p")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-f mpegts -i pipe:0",
		"-c:v copy",
		"-c:a copy",
		"-f flv",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("RTMP publisher args missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"-rtsp_transport", "-pkt_size", "-c:a aac", "-b:a", "-ar 48000", "-ac 2"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("RTMP publisher must not contain %q: %s", forbidden, joined)
		}
	}
	if args[len(args)-1] != "rtmp://127.0.0.1:9999/radio?user=u&pass=p" {
		t.Fatalf("last arg must be the RTMP ingest URL, got %q", args[len(args)-1])
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

func TestRadioRTSPPipelineKeepsAVProCompatibleCopyAndAACBoundary(t *testing.T) {
	feeder := strings.Join(RadioFeederArgs("track.mp4", 0, false, 0), " ")
	publisher := strings.Join(RadioPublisherArgs("rtsp://127.0.0.1:8554/radio"), " ")

	if !strings.Contains(feeder, "-c copy") {
		t.Fatalf("radio feeder must stream-copy the prepared MP4 into MPEG-TS: %s", feeder)
	}
	for _, forbidden := range []string{"-c:a aac", "-b:a", "-ar 48000", "-ac 2", "-c:v libx264", "-c:v h264_"} {
		if strings.Contains(feeder, forbidden) {
			t.Fatalf("radio feeder must not transcode with %q; prepared tracks already own AAC normalization: %s", forbidden, feeder)
		}
	}

	for _, want := range []string{"-c:v copy", "-c:a copy", "-rtsp_transport tcp"} {
		if !strings.Contains(publisher, want) {
			t.Fatalf("radio publisher missing AVPro-compatible boundary option %q: %s", want, publisher)
		}
	}
	for _, forbidden := range []string{"-c:a aac", "-b:a", "-ar 48000", "-ac 2"} {
		if strings.Contains(publisher, forbidden) {
			t.Fatalf("radio publisher must not add audio encoder delay with %q: %s", forbidden, publisher)
		}
	}
}

func TestRadioRTSPPipelineGuardsKnownBrokenDev23Boundary(t *testing.T) {
	feeder := strings.Join(RadioFeederArgs("track.mp4", 0, false, 0), " ")
	publisher := strings.Join(RadioPublisherArgs("rtsp://127.0.0.1:8554/radio"), " ")

	brokenFeederMarkers := []string{"-c:v copy -c:a aac", "-b:a 160k", "-ar 48000", "-ac 2"}
	for _, marker := range brokenFeederMarkers {
		if strings.Contains(feeder, marker) {
			t.Fatalf("radio feeder drifted toward the broken dev23 boundary %q: %s", marker, feeder)
		}
	}
	if !strings.Contains(publisher, "-c:a copy") {
		t.Fatalf("radio publisher must preserve feeder audio timestamps without re-encoding: %s", publisher)
	}
	for _, broken := range []string{"-c:a aac", "-b:a 128k", "-ar 48000", "-ac 2"} {
		if strings.Contains(publisher, broken) {
			t.Fatalf("radio publisher drifted toward delayed audio re-encode boundary %q: %s", broken, publisher)
		}
	}
}

func TestRadioFallbackFeederArgsConsumeRawVideoStandby(t *testing.T) {
	args := RadioFallbackFeederArgs(QualityPreset{Height: 720, AudioBitrate: "128k"}, 1280, 720, 12.345)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-f rawvideo",
		"-pix_fmt rgba",
		"-s 1280x720",
		"-r 30",
		"-i pipe:0",
		"anullsrc=r=48000:cl=stereo",
		"-output_ts_offset 12.345",
		"-preset ultrafast",
		"-b:v 900k",
		"-maxrate 1200k",
		"-bufsize 2200k",
		"-g 30",
		"-keyint_min 30",
		"-sc_threshold 0",
		"-force_key_frames expr:gte(t,n_forced*1)",
		"-flush_packets 1",
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
	for _, forbidden := range []string{"-profile:v main", "slice-max-size", "-tune zerolatency"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("CPU fallback must not use VRChat-incompatible x264 option %q: %s", forbidden, joined)
		}
	}
}

func TestRadioFallbackRenderSizeUsesLowerInternalResolution(t *testing.T) {
	width, height := RadioFallbackRenderSize(QualityPreset{Height: 720})
	if width != 960 || height != 540 {
		t.Fatalf("720p fallback render size = %dx%d, want 960x540", width, height)
	}
	args := strings.Join(RadioFallbackFeederArgs(QualityPreset{Height: 720, AudioBitrate: "128k"}, width, height, 0), " ")
	for _, want := range []string{"-s 960x540", "-vf scale=1280:720:flags=bicubic"} {
		if !strings.Contains(args, want) {
			t.Fatalf("scaled fallback args missing %q: %s", want, args)
		}
	}

	width, height = RadioFallbackRenderSize(QualityPreset{Height: 1080})
	if width != 1440 || height != 810 {
		t.Fatalf("1080p fallback render size = %dx%d, want 1440x810", width, height)
	}
	args = strings.Join(RadioFallbackFeederArgs(QualityPreset{Height: 1080, AudioBitrate: "128k"}, width, height, 0), " ")
	for _, want := range []string{"-s 1440x810", "-vf scale=1920:1080:flags=bicubic", "-b:v 1800k", "-maxrate 2400k", "-bufsize 4200k"} {
		if !strings.Contains(args, want) {
			t.Fatalf("1080p scaled fallback args missing %q: %s", want, args)
		}
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

func TestRadioFallbackCubeMotionDoesNotResetAtSharedLoopBoundary(t *testing.T) {
	scale := 1.0
	cubes := fallbackCubes(1280, 720, scale)
	if len(cubes) == 0 {
		t.Fatal("fallback cubes must not be empty")
	}
	cube := cubes[0]
	sharedResetSeconds := 92 * scale / 4.4
	_, beforeY := radioFallbackCubePosition(cube, sharedResetSeconds-0.001, 1280, 720, scale)
	_, afterY := radioFallbackCubePosition(cube, sharedResetSeconds+0.001, 1280, 720, scale)
	if jump := math.Abs(afterY - beforeY); jump > 2 {
		t.Fatalf("cube jumped %.2fpx at shared loop boundary; before=%.2f after=%.2f", jump, beforeY, afterY)
	}
}

func TestWriteTimedRadioFallbackFramesUsesFrameClockAndFadeOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := 0
	var rendered []float64
	var payloads [][]byte
	writer := writerFunc(func(p []byte) (int, error) {
		writes++
		payloads = append(payloads, append([]byte(nil), p...))
		if writes == 1 {
			cancel()
		}
		return len(p), nil
	})
	frames, err := WriteTimedRadioFallbackFrames(ctx, writer, func(seconds float64) []byte {
		rendered = append(rendered, seconds)
		return []byte{100, 100, 100, 255}
	})
	if err != nil {
		t.Fatal(err)
	}
	wantFrames := 1 + int((radioFallbackFadeOutSeconds+radioFallbackRetimingHoldSeconds)*radioFallbackFrameRate)
	if frames != wantFrames {
		t.Fatalf("frames = %d, want %d including fade-out and black retiming hold", frames, wantFrames)
	}
	if len(rendered) < 2 {
		t.Fatalf("rendered seconds = %#v, want multiple frame-clock samples", rendered)
	}
	if rendered[0] != 0 {
		t.Fatalf("first rendered second = %.3f, want frame-clock start 0", rendered[0])
	}
	if got, want := rendered[1], 1.0/30.0; math.Abs(got-want) > 0.0001 {
		t.Fatalf("second rendered second = %.6f, want %.6f", got, want)
	}
	last := payloads[len(payloads)-1]
	for i := 0; i+3 < len(last); i += 4 {
		if last[i] != 0 || last[i+1] != 0 || last[i+2] != 0 || last[i+3] != 255 {
			t.Fatalf("last retiming frame pixel %d = rgba(%d,%d,%d,%d), want opaque black", i/4, last[i], last[i+1], last[i+2], last[i+3])
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestAlphaRoundedRectKeepsTransparentCornersAndFillAlpha(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	alphaRoundedRect(img, image.Rect(0, 0, 40, 30), 8, color.RGBA{40, 80, 120, 96}, color.RGBA{160, 220, 255, 80})
	for _, pt := range []image.Point{{0, 0}, {39, 0}, {0, 29}, {39, 29}} {
		if got := img.RGBAAt(pt.X, pt.Y).A; got != 0 {
			t.Fatalf("corner %v alpha = %d, want 0", pt, got)
		}
	}
	if got := img.RGBAAt(20, 15).A; got == 0 || got == 255 {
		t.Fatalf("center alpha = %d, want semi-transparent", got)
	}
}

func BenchmarkRadioFallbackRendererRenderRGB720p(b *testing.B) {
	fonts, err := VisualizerFonts()
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	logo, err := WriteRadioFallbackLogo(dir)
	if err != nil {
		b.Fatal(err)
	}
	renderer, err := NewRadioFallbackRenderer(1280, 720, logo, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		b.Fatal(err)
	}
	defer renderer.Close()
	b.ReportAllocs()
	b.SetBytes(1280 * 720 * 3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderer.RenderRGB(float64(i) / 30)
	}
}

func BenchmarkRadioFallbackRendererRenderReusableRGB720p(b *testing.B) {
	fonts, err := VisualizerFonts()
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	logo, err := WriteRadioFallbackLogo(dir)
	if err != nil {
		b.Fatal(err)
	}
	renderer, err := NewRadioFallbackRenderer(1280, 720, logo, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		b.Fatal(err)
	}
	defer renderer.Close()
	b.ReportAllocs()
	b.SetBytes(1280 * 720 * 3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderer.RenderReusableRGB(float64(i) / 30)
	}
}

func BenchmarkRadioFallbackRendererRenderReusableRGBA720p(b *testing.B) {
	fonts, err := VisualizerFonts()
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	logo, err := WriteRadioFallbackLogo(dir)
	if err != nil {
		b.Fatal(err)
	}
	renderer, err := NewRadioFallbackRenderer(1280, 720, logo, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		b.Fatal(err)
	}
	defer renderer.Close()
	b.ReportAllocs()
	b.SetBytes(1280 * 720 * 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderer.RenderReusableRGBA(float64(i) / 30)
	}
}

func BenchmarkRadioFallbackRendererRenderReusableRGBA540p(b *testing.B) {
	fonts, err := VisualizerFonts()
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	logo, err := WriteRadioFallbackLogo(dir)
	if err != nil {
		b.Fatal(err)
	}
	renderer, err := NewRadioFallbackRenderer(960, 540, logo, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		b.Fatal(err)
	}
	defer renderer.Close()
	b.ReportAllocs()
	b.SetBytes(960 * 540 * 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderer.RenderReusableRGBA(float64(i) / 30)
	}
}

func BenchmarkRadioFallbackRendererRenderReusableRGBA810p(b *testing.B) {
	fonts, err := VisualizerFonts()
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	logo, err := WriteRadioFallbackLogo(dir)
	if err != nil {
		b.Fatal(err)
	}
	renderer, err := NewRadioFallbackRenderer(1440, 810, logo, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		b.Fatal(err)
	}
	defer renderer.Close()
	b.ReportAllocs()
	b.SetBytes(1440 * 810 * 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderer.RenderReusableRGBA(float64(i) / 30)
	}
}

func BenchmarkRadioFallbackRendererRenderRGB360p(b *testing.B) {
	fonts, err := VisualizerFonts()
	if err != nil {
		b.Fatal(err)
	}
	dir := b.TempDir()
	logo, err := WriteRadioFallbackLogo(dir)
	if err != nil {
		b.Fatal(err)
	}
	renderer, err := NewRadioFallbackRenderer(640, 360, logo, fonts.SemiBold600, fonts.Medium500)
	if err != nil {
		b.Fatal(err)
	}
	defer renderer.Close()
	b.ReportAllocs()
	b.SetBytes(640 * 360 * 3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderer.RenderRGB(float64(i) / 30)
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
		if _, err := stdin.Write(renderer.RenderReusableRGBA(float64(i) / 30)); err != nil {
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
