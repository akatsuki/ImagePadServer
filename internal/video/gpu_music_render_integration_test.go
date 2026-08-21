package video

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMusicPreRenderFFmpegSmokeUsesCPUProduction(t *testing.T) {
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		t.Skip("set IMAGEPAD_FFMPEG for the CPU production smoke")
	}
	filters, err := exec.Command(ffmpeg, "-hide_banner", "-filters").CombinedOutput()
	if err != nil || !strings.Contains(string(filters), " ass ") {
		t.Skip("FFmpeg was built without the libass filter")
	}
	audio := filepath.Join(t.TempDir(), "tone.wav")
	if err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", audio).Run(); err != nil {
		t.Fatal(err)
	}
	analysis, err := AnalyzeAudio(context.Background(), ffmpeg, audio)
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderRadioTrack(context.Background(), t.TempDir(), ffmpeg, AudioRenderInput{SourcePath: audio, Kind: SourceMusic, Analysis: analysis}, "cpu-smoke", QualityPreset{Height: 360, VideoBitrate: "800k", MaxRate: "1000k", BufferSize: "1600k", AudioBitrate: "96k", RadioLatency: "rtsp-ultra"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("GPU render output invalid: %s %v", out, err)
	}
	if ffprobe := os.Getenv("IMAGEPAD_FFPROBE"); ffprobe != "" {
		probe := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,pix_fmt,nb_frames,duration,color_range,color_space,color_transfer,color_primaries", "-of", "default=nw=1", out)
		data, err := probe.Output()
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if text == "" || !strings.Contains(text, "codec_name=") || !strings.Contains(text, "pix_fmt=") {
			t.Fatalf("ffprobe returned incomplete video stream: %s", text)
		}
		if err := ValidateGPUVideoColorMetadata(text); err != nil {
			t.Fatal(err)
		}
	}
}
