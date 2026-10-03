package xpostcodec

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xpostvideo"
)

func TestEncodeMixedDemoWithLocalFFmpegAndCompositor(t *testing.T) {
	if os.Getenv("XPOST_CODEC_INTEGRATION") != "1" {
		t.Skip("set XPOST_CODEC_INTEGRATION=1 to run the local FFmpeg/WGPU end-to-end check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is unavailable")
	}
	compositor := os.Getenv("IMAGEPAD_XPOST_COMPOSITOR")
	if compositor == "" {
		compositor = filepath.Join("..", "..", "build", "xpost-compositord", "xpost-compositord.exe")
	}
	if _, err := os.Stat(compositor); err != nil {
		t.Skipf("xpost compositor is unavailable: %v", err)
	}
	root := t.TempDir()
	landscapeImage := filepath.Join(root, "landscape.png")
	portraitImage := filepath.Join(root, "portrait.png")
	cardA, panelA := filepath.Join(root, "card-a.png"), filepath.Join(root, "panel-a.png")
	cardB, panelB := filepath.Join(root, "card-b.png"), filepath.Join(root, "panel-b.png")
	for _, item := range []struct {
		path string
		w, h int
		c    color.RGBA
	}{{landscapeImage, 64, 36, color.RGBA{220, 40, 30, 255}}, {portraitImage, 36, 64, color.RGBA{20, 170, 80, 255}}, {cardA, 160, 90, color.RGBA{30, 50, 100, 255}}, {panelA, 80, 90, color.RGBA{10, 20, 50, 255}}, {cardB, 160, 90, color.RGBA{20, 90, 100, 255}}, {panelB, 80, 90, color.RGBA{20, 40, 60, 255}}} {
		if err := writeSolidPNG(item.path, item.w, item.h, item.c); err != nil {
			t.Fatal(err)
		}
	}
	landscapeVideo := filepath.Join(root, "landscape.mkv")
	portraitVideo := filepath.Join(root, "portrait.mkv")
	if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x36:rate=10:duration=1.2", "-itsoffset", "0.25", "-f", "lavfi", "-i", "sine=frequency=220:sample_rate=48000:duration=0.7", "-map", "0:v:0", "-map", "1:a:0", "-c:v", "ffv1", "-c:a", "pcm_s16le", "-t", "1.2", landscapeVideo}, nil); err != nil {
		t.Fatal(err)
	}
	if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=36x64:rate=10:duration=1.2", "-c:v", "ffv1", "-an", "-t", "1.2", portraitVideo}, nil); err != nil {
		t.Fatal(err)
	}
	speechA := filepath.Join(root, "speech-a.wav")
	speechB := filepath.Join(root, "speech-b.wav")
	for _, path := range []string{speechA, speechB} {
		if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000:duration=0.3", "-ac", "1", "-c:a", "pcm_s16le", path}, nil); err != nil {
			t.Fatal(err)
		}
	}
	media := []xpostmodel.Media{
		{Kind: "video", Path: landscapeVideo},
		{Kind: "image", Path: portraitImage},
		{Kind: "video", Path: portraitVideo},
		{Kind: "image", Path: landscapeImage},
	}
	media, err = PrepareMedia(ctx, ffmpeg, ffprobe, media, root, 160, 90, 10)
	if err != nil {
		t.Fatal(err)
	}
	speeches := []xpostmodel.Speech{{Path: speechA, Duration: .3, SampleRate: 48000, Channels: 1, Samples: 14400}, {Path: speechB, Duration: .3, SampleRate: 48000, Channels: 1, Samples: 14400}}
	pages := []xpostmodel.Page{{CardPath: cardA, PanelPath: panelA}, {CardPath: cardB, PanelPath: panelB}}
	plan, err := xpostvideo.Compile(pages, speeches, media, xpostvideo.Options{Width: 160, Height: 90, FPS: 10, PhotoSeconds: .3, TailSeconds: .1, CoverSeconds: .2, SlideSeconds: .2})
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "job")
	result, err := Encode(ctx, Options{FFmpeg: ffmpeg, FFprobe: ffprobe, Compositor: compositor, OutDir: outDir, Encoder: "libx264", CRF: 28, AudioBitrate: "96k"}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Frames != plan.TotalFrames || math.Abs(result.Duration-float64(plan.TotalFrames)/10) > .001 {
		t.Fatalf("unexpected output clock: %+v", result)
	}
	if result.RendererCalls == 0 || result.Adapter == "" || result.Backend == "" {
		t.Fatalf("missing compositor evidence: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(result.HLSDir, "playlist.m3u8")); err != nil {
		t.Fatalf("HLS playlist missing: %v", err)
	}
	assertAudioOffset(t, ctx, ffmpeg, result.OutputPath, plan)
}

func TestMediaDecoderKeepsFractionalDurationFinalFrame(t *testing.T) {
	if os.Getenv("XPOST_CODEC_INTEGRATION") != "1" {
		t.Skip("opt-in native FFmpeg decoder regression")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, rate           string
		frames, output, w, h int
	}{
		{"CFR-rounded-metadata", "30", 68, 68, 64, 36},
		{"NTSC-landscape", "30000/1001", 67, 68, 64, 36},
		{"NTSC-portrait", "30000/1001", 67, 68, 36, 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			root := t.TempDir()
			source := filepath.Join(root, "source.mp4")
			if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%s", tc.w, tc.h, tc.rate), "-frames:v", strconv.Itoa(tc.frames), "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", source}, nil); err != nil {
				t.Fatal(err)
			}
			media, err := PrepareMedia(ctx, ffmpeg, ffprobe, []xpostmodel.Media{{Kind: "video", Path: source}}, root, 64, 36, 30)
			if err != nil {
				t.Fatal(err)
			}
			options := xpostvideo.DefaultOptions()
			options.Width = 64
			options.Height = 36
			options.FPS = 30
			plan, err := xpostvideo.Compile([]xpostmodel.Page{{CardPath: media[0].FirstPath, PanelPath: media[0].FirstPath}}, []xpostmodel.Speech{{}}, media, options)
			if err != nil {
				t.Fatal(err)
			}
			var segment xpostvideo.Segment
			for _, s := range plan.Segments {
				if s.Kind == "video" {
					segment = s
					break
				}
			}
			if segment.Frames != tc.output {
				t.Errorf("planned %d frames, expected %d", segment.Frames, tc.output)
			}
			decoder, err := startMediaDecoder(ctx, ffmpeg, plan, assetSet{dims: map[string]image.Point{"media-0": image.Pt(tc.w, tc.h)}}, 0, tc.w > tc.h, segment.Frames)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if decoder != nil {
					_ = decoder.close()
				}
			}()
			for frame := 0; frame < segment.Frames; frame++ {
				if _, err := decoder.readFrame(); err != nil {
					t.Fatalf("decode media 0 frame %d: %v", frame, err)
				}
			}
			if err := decoder.close(); err != nil {
				t.Fatal(err)
			}
			decoder = nil
			t.Logf("%s input %d -> %d planned and decoded frames", tc.rate, tc.frames, segment.Frames)
		})
	}
}

// The caller supplies an already downloaded input. The test never downloads
// media or changes the running application's library or publication state.
func TestMediaDecoderRealSourceReachesPlannedEnd(t *testing.T) {
	source := os.Getenv("XPOST_SOURCE_REGRESSION_PATH")
	if source == "" {
		t.Skip("set XPOST_SOURCE_REGRESSION_PATH to check a local real-source video")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	media, err := PrepareMedia(ctx, ffmpeg, ffprobe, []xpostmodel.Media{{Kind: "video", Path: source}}, t.TempDir(), 64, 36, 30)
	if err != nil {
		t.Fatal(err)
	}
	options := xpostvideo.DefaultOptions()
	options.Width, options.Height = 64, 36
	plan, err := xpostvideo.Compile([]xpostmodel.Page{{CardPath: media[0].FirstPath, PanelPath: media[0].FirstPath}}, []xpostmodel.Speech{{}}, media, options)
	if err != nil {
		t.Fatal(err)
	}
	var segment xpostvideo.Segment
	for _, s := range plan.Segments {
		if s.Kind == "video" {
			segment = s
			break
		}
	}
	if segment.Frames == 0 {
		t.Fatal("missing video segment")
	}
	decoder, err := startMediaDecoder(ctx, ffmpeg, plan, assetSet{dims: map[string]image.Point{"media-0": image.Pt(64, 36)}}, 0, false, segment.Frames)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if decoder != nil {
			_ = decoder.close()
		}
	}()
	for frame := 0; frame < segment.Frames; frame++ {
		if _, err := decoder.readFrame(); err != nil {
			t.Fatalf("decode media 0 frame %d: %v", frame, err)
		}
	}
	if err := decoder.close(); err != nil {
		t.Fatal(err)
	}
	decoder = nil
	t.Logf("source duration %.12fs -> %d planned and decoded frames", media[0].Duration, segment.Frames)
}

func writeSolidPNG(path string, w, h int, c color.RGBA) error {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func assertAudioOffset(t *testing.T, ctx context.Context, ffmpeg, path string, plan xpostvideo.Plan) {
	t.Helper()
	var target *xpostvideo.AudioSpan
	for i := range plan.Audio {
		if plan.Audio[i].Media {
			target = &plan.Audio[i]
			break
		}
	}
	if target == nil {
		t.Fatal("test plan has no source-video audio span")
	}
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-map", "0:a:0", "-ac", "2", "-ar", "48000", "-f", "f32le", "pipe:1")
	hideWindow(cmd)
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("decode final audio: %v", err)
	}
	start := float64(target.StartFrame) / float64(plan.Options.FPS)
	before := rmsWindow(data, start+.06, start+.19)
	after := rmsWindow(data, start+.32, start+.48)
	if before > .01 {
		t.Fatalf("source audio leaked before its probed start offset: rms=%.4f", before)
	}
	if after < .03 {
		t.Fatalf("source audio was not mixed at its absolute offset: rms=%.4f", after)
	}
}

func rmsWindow(pcm []byte, start, end float64) float64 {
	first := int(start * 48000 * 2)
	last := int(end * 48000 * 2)
	if first < 0 {
		first = 0
	}
	if last > len(pcm)/4 {
		last = len(pcm) / 4
	}
	if last <= first {
		return 0
	}
	var sum float64
	for i := first; i < last; i++ {
		v := math.Float32frombits(binary.LittleEndian.Uint32(pcm[i*4:]))
		sum += float64(v * v)
	}
	return math.Sqrt(sum / float64(last-first))
}

func TestMixOneSpanDrainsLongAudioAfterCappedRead(t *testing.T) {
	if os.Getenv("XPOST_CODEC_INTEGRATION") != "1" {
		t.Skip("set XPOST_CODEC_INTEGRATION=1 to run the native FFmpeg audio check")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	source := filepath.Join(dir, "long.wav")
	if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000:duration=4", "-ac", "2", "-c:a", "pcm_s16le", source}, nil); err != nil {
		t.Fatal(err)
	}
	destinationPath := filepath.Join(dir, "short.pcm")
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	if err := destination.Truncate(4 * pcmRate * pcmChannels); err != nil {
		t.Fatal(err)
	}
	// Read only 100 ms from a 4-second source. FFmpeg must stop its output
	// after the requested span and still reach Wait so decoder errors surface.
	if err := mixOneSpan(ctx, ffmpeg, destination, source, 0, int64(.1*pcmRate*pcmChannels), 0); err != nil {
		t.Fatalf("capped audio span failed: %v", err)
	}
	data, err := os.ReadFile(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if rmsWindow(data, 0, .08) < .03 || rmsWindow(data, .2, .3) > .001 {
		t.Fatal("audio span was not capped to the requested 100 ms")
	}
}

func TestMixAudioKeepsNegativeOffsetInsideVideoSpan(t *testing.T) {
	if os.Getenv("XPOST_CODEC_INTEGRATION") != "1" {
		t.Skip("set XPOST_CODEC_INTEGRATION=1 to run the native FFmpeg audio check")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	source := filepath.Join(dir, "audio-before-video.mkv")
	if err := runFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-loglevel", "error", "-y", "-itsoffset", "0.2", "-f", "lavfi", "-i", "testsrc2=size=64x36:rate=10:duration=1.2", "-f", "lavfi", "-i", "sine=frequency=330:sample_rate=48000:duration=0.7", "-map", "0:v:0", "-map", "1:a:0", "-c:v", "ffv1", "-c:a", "pcm_s16le", "-t", "1.4", source}, nil); err != nil {
		t.Fatal(err)
	}
	offset, err := mediaAudioOffset(ctx, ffprobe, source)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(offset-(-.2)) > .02 {
		t.Fatalf("source A/V offset = %.3fs, want -0.200s", offset)
	}
	pcmPath := filepath.Join(dir, "mix.pcm")
	plan := xpostvideo.Plan{Options: xpostvideo.Options{FPS: 10}, TotalFrames: 40, Audio: []xpostvideo.AudioSpan{{Path: source, StartFrame: 20, Frames: 10, Media: true}}}
	if err := mixAudio(ctx, ffmpeg, ffprobe, pcmPath, plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(pcmPath)
	if err != nil {
		t.Fatal(err)
	}
	if rmsWindow(data, 1.8, 1.98) > .001 {
		t.Fatal("source audio overlapped the timeline before the video span")
	}
	if rmsWindow(data, 2.05, 2.45) < .03 {
		t.Fatal("source audio was not aligned at the video span start")
	}
	if rmsWindow(data, 2.6, 2.95) > .001 {
		t.Fatal("source audio escaped the video span end")
	}
}
