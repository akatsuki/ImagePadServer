package video

import (
	"strings"
	"testing"
)

func TestNicoEncodeArgsKeepProductionClockAndSliceContract(t *testing.T) {
	args := nicoEncodeArgs("source.mp4", "output.mp4", NicoEncodeOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k",
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-framerate 30/1",
		"fps=30/1",
		"scale=1920:1080",
		"overlay=0:0:format=auto",
		"tpad=stop_mode=clone:stop_duration=1",
		"format=yuv420p",
		"-c:v libx264",
		"sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0",
		"-g 120",
		"-movflags +faststart",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-shortest") {
		t.Fatalf("audio short tail must not truncate the video: %s", joined)
	}
	for _, forbidden := range []string{"-filter_complex_threads", "-threads:v"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("default production args must not force diagnostic thread option %q: %s", forbidden, joined)
		}
	}
}

func TestNicoEncodeArgsCoversCeiledFinalFrame(t *testing.T) {
	args := nicoEncodeArgs("source.mp4", "output.mp4", NicoEncodeOptions{
		Width: 1920, Height: 1080, DurationMs: 154955, FPSNum: 30, FPSDen: 1,
		CRF: 26, AudioBitrate: "160k",
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-t 154.966666667") {
		t.Fatalf("duration must cover the ceiled final frame: %s", joined)
	}
	if !strings.Contains(joined, "-frames:v 4649") {
		t.Fatalf("video frame count must be explicit: %s", joined)
	}
}

func TestNicoEncodeArgsScopesDiagnosticThreadOptions(t *testing.T) {
	args := nicoEncodeArgs("source.mp4", "output.mp4", NicoEncodeOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1, CRF: 26,
		FilterThreads: 1, DecoderThreads: 1, EncoderThreads: 1,
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{"-filter_complex_threads 1", "-threads 1", "-threads:v 1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
	filterIndex := indexOfArgPair(args, "-filter_complex_threads", "1")
	decoderIndex := indexOfArgPair(args, "-threads", "1")
	encoderIndex := indexOfArgPair(args, "-threads:v", "1")
	inputIndex := indexOfArgPair(args, "-i", "source.mp4")
	codecIndex := indexOfArgPair(args, "-c:v", "libx264")
	if filterIndex < 0 || decoderIndex < 0 || encoderIndex < 0 || inputIndex < 0 || codecIndex < 0 {
		t.Fatalf("missing scoped option pair: %v", args)
	}
	if !(filterIndex < inputIndex && decoderIndex < inputIndex && codecIndex < encoderIndex) {
		t.Fatalf("thread options are not scoped to their intended stage: %v", args)
	}
}

func TestNicoEncodeArgsNVENCProfileKeepsSingleSliceContract(t *testing.T) {
	args := nicoEncodeArgs("source.mp4", "output.mp4", NicoEncodeOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1, CRF: 29,
		Encoder: "nvenc",
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-c:v h264_nvenc",
		"-preset p4",
		"-tune hq",
		"-rc vbr",
		"-cq 29",
		"-b:v 0",
		"-bf 3",
		"-slices 1",
		"-forced-idr 1",
		"-no-scenecut 1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("NVENC args missing %q: %s", want, joined)
		}
	}
	for _, forbidden := range []string{"-c:v libx264", "-crf 29", "-x264-params"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("NVENC args must not contain %q: %s", forbidden, joined)
		}
	}
}

func TestNicoEncodeArgsTeeUsesFixedRelativeMP4AndHLSOutputs(t *testing.T) {
	args := nicoEncodeArgs("source.mp4", "ignored-output.mp4", NicoEncodeOptions{
		Width: 64, Height: 36, DurationMs: 200, FPSNum: 30, FPSDen: 1, CRF: 28,
		OutputMode: NicoOutputTee, HLSOutputDir: "hls-staging",
	})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-flags:v +global_header") || !strings.Contains(joined, "-f tee") {
		t.Fatalf("tee args missing global header or tee muxer: %s", joined)
	}
	for _, want := range []string{
		"[f=mp4:movflags=+faststart:onfail=abort]out.mp4",
		"[f=hls:hls_time=4:hls_playlist_type=vod:hls_flags=independent_segments:start_number=0:hls_segment_filename=segment-%05d.ts:onfail=abort]playlist.m3u8",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("tee args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "ignored-output.mp4") || strings.Contains(joined, "hls-staging") {
		t.Fatalf("tee args leaked caller paths into tee URL: %s", joined)
	}
}

func TestNicoEncodeOptionsRejectsUnknownEncoder(t *testing.T) {
	options := NicoEncodeOptions{
		Width: 64, Height: 36, DurationMs: 100, FPSNum: 30, FPSDen: 1, CRF: 26,
		Encoder: "not-a-real-encoder",
	}
	if _, err := options.validate(); err == nil || !strings.Contains(err.Error(), "encoder") {
		t.Fatalf("encoder validation error = %v", err)
	}
}

func indexOfArgPair(args []string, key, value string) int {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == key && args[index+1] == value {
			return index
		}
	}
	return -1
}
