package obsrtmp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestRTSPSingleSlicePolicySurvivesDiagnosticTunes(t *testing.T) {
	m := New("", "127.0.0.1", 1935, "key", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	}, Callbacks{})
	contract := m.argumentContract("slice-contract", video.ResolveQuality("720", 0), video.CPUVideoEncoder(video.EncoderLowLatency))
	for _, tune := range []string{"", "zerolatency", "animation", "film", "grain", "stillimage", "fastdecode"} {
		args, err := m.FFmpegRTSPArgsForDiagnostic(contract, "unused.mp4", "rtsp://127.0.0.1:1/unused", RTSPEncoderDiagnosticPolicy{Encoder: contract.VideoEncoderProfile, Tune: tune})
		if err != nil {
			t.Fatal(err)
		}
		options := ":" + optionValue(args, "-x264-params") + ":"
		for _, want := range []string{"sliced-threads=0", "slices=1", "slice-max-size=0", "slice-max-mbs=0"} {
			if !strings.Contains(options, ":"+want+":") {
				t.Errorf("tune %q missing %s: %s", tune, want, options)
			}
		}
	}
}

// Real compressed output, using the production RTSP encoder arguments. The
// input and muxer are isolated: no live server, receiver or recording is touched.
func TestRTSPSingleSliceRealEncoder(t *testing.T) {
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg unavailable; release validation requires IMAGEPAD_FFMPEG")
		}
	}
	m := New("", "127.0.0.1", 1935, "key", nil, func() LatencyProfile {
		return NormalizeLatencyProfile(LatencyModeRTSPRealtime)
	}, Callbacks{})
	for _, tc := range []struct {
		size    string
		threads int
		tune    string
	}{
		{"1280x720", 4, ""}, {"720x1280", 8, "zerolatency"}, {"1920x1080", 0, "animation"},
	} {
		t.Run(fmt.Sprintf("%s-threads%d-%s", tc.size, tc.threads, tc.tune), func(t *testing.T) {
			contract := m.argumentContract("slice-contract", video.ResolveQuality("720", 0), video.CPUVideoEncoder(video.EncoderLowLatency))
			production, err := m.FFmpegRTSPArgsForDiagnostic(contract, "unused.mp4", "rtsp://127.0.0.1:1/unused", RTSPEncoderDiagnosticPolicy{Encoder: contract.VideoEncoderProfile, Tune: tc.tune})
			if err != nil {
				t.Fatal(err)
			}
			start, end := -1, -1
			for i, arg := range production {
				if arg == "-c:v" {
					start = i
				}
				if arg == "-c:a" {
					end = i
					break
				}
			}
			if start < 0 || end <= start {
				t.Fatal("production encoder option group not found")
			}
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=" + tc.size + ":rate=30"}
			args = append(args, production[start:end]...)
			args = append(args, "-threads", fmt.Sprint(tc.threads), "-frames:v", "36", "-an", "-f", "h264", "pipe:1")
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, ffmpeg, args...)
			hideWindow(cmd)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err != nil {
				t.Fatalf("encode: %v: %s", err, stderr.String())
			}
			// AUD delimits each access unit. Count only VCL NAL units (1/5);
			// SPS, PPS and SEI must never be mistaken for extra slices.
			frames, slices := 0, 0
			for i := 0; i+3 < len(output); i++ {
				if output[i] != 0 || output[i+1] != 0 {
					continue
				}
				header := 0
				if output[i+2] == 1 {
					header = i + 3
				} else if i+4 < len(output) && output[i+2] == 0 && output[i+3] == 1 {
					header = i + 4
				}
				if header == 0 {
					continue
				}
				switch output[header] & 31 {
				case 9:
					if frames > 0 && slices != 1 {
						t.Fatalf("frame %d: %d slices, want 1", frames-1, slices)
					}
					frames++
					slices = 0
				case 1, 5:
					slices++
				}
				i = header
			}
			if frames != 36 || slices != 1 {
				t.Fatalf("frames=%d final slices=%d, want 36/1", frames, slices)
			}
		})
	}
}
