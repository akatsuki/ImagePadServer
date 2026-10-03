package xpostcodec

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"testing"

	"imagepadserver/internal/video"
)

func TestEncoderArgsKeepSingleSliceAndBoundedGOP(t *testing.T) {
	args := encoderArgs(video.NewVideoEncoderProfile("libx264", video.EncoderStandard), 30)
	joined := strings.Join(args, " ")
	for _, want := range []string{"-bf 0", "-g 120", "sliced-threads=0", "slices=1", "slice-max-size=0", "slice-max-mbs=0"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("encoder args missing %q: %s", want, joined)
		}
	}
}

func TestDurationUsesExactStreamTimeBaseBeforeRoundedDecimal(t *testing.T) {
	var info probeResult
	if err := json.Unmarshal([]byte(`{"streams":[{"duration":"2.266667","duration_ts":34816,"time_base":"1/15360"}],"format":{"duration":"2.266667"}}`), &info); err != nil {
		t.Fatal(err)
	}
	duration := durationSeconds(info, info.Streams[0])
	if math.Abs(duration-68.0/30) > 1e-12 {
		t.Fatalf("rounded duration adds a frame: %.12f, expected %.12f", duration, 68.0/30)
	}
}

func TestDurationFallsBackWhenStreamTimeBaseIsUnavailable(t *testing.T) {
	var info probeResult
	info.Format.Duration = "5.5"
	for _, base := range []string{"", "0/1", "1/0", "N/A", "1/2/3"} {
		stream := streamInfo{DurationTS: 100, TimeBase: base, Duration: "2.25"}
		if got := durationSeconds(info, stream); got != 2.25 {
			t.Fatalf("time base %q: duration %.12f, want stream duration 2.25", base, got)
		}
		stream.Duration = "N/A"
		if got := durationSeconds(info, stream); got != 5.5 {
			t.Fatalf("time base %q: duration %.12f, want format duration 5.5", base, got)
		}
	}
}

func TestRawDecoderRejectsTruncatedFrame(t *testing.T) {
	decoder := rawDecoder{stdout: io.NopCloser(strings.NewReader(strings.Repeat("x", 16+3))), width: 2, height: 2, frames: 2}
	if _, err := decoder.readFrame(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.readFrame(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated frame error = %v, want unexpected EOF", err)
	}
	if decoder.readFrames != 1 {
		t.Fatalf("truncated frame counted as complete: %d", decoder.readFrames)
	}
}

func TestEncoderArgsRequestOneSliceForSelectedHardwareEncoder(t *testing.T) {
	args := strings.Join(encoderArgs(video.NewVideoEncoderProfile("h264_nvenc", video.EncoderStandard), 60), " ")
	for _, want := range []string{"-bf 0", "-g 240", "-slices 1", "-aud 1"} {
		if !strings.Contains(args, want) {
			t.Fatalf("selected encoder args missing %q: %s", want, args)
		}
	}
}

func TestDisplayDimensionsAppliesSampleAspectRatioAndRotation(t *testing.T) {
	w, h, err := displayDimensions(streamInfo{Width: 720, Height: 480, SAR: "4:3", SideData: []struct {
		Rotation float64 `json:"rotation"`
		Type     string  `json:"side_data_type"`
	}{{Rotation: -90}}})
	if err != nil {
		t.Fatal(err)
	}
	if w != 480 || h != 960 {
		t.Fatalf("display dimensions = %dx%d, want 480x960", w, h)
	}
}

func TestHDRDetectionRejectsHDRMetadataButAllowsSDR10Bit(t *testing.T) {
	if isHDR(streamInfo{PixFmt: "yuv420p10le", Transfer: "bt709", Primaries: "bt709"}) {
		t.Fatal("SDR 10-bit input should not be classified as HDR solely by bit depth")
	}
	if !isHDR(streamInfo{Transfer: "smpte2084"}) {
		t.Fatal("PQ transfer should be classified as HDR")
	}
	if !isHDR(streamInfo{SideData: []struct {
		Rotation float64 `json:"rotation"`
		Type     string  `json:"side_data_type"`
	}{{Type: "Mastering display metadata"}}}) {
		t.Fatal("mastering display metadata should be classified as HDR")
	}
}

func TestMuxArgsCopyVideoAndDoNotShortenToAudio(t *testing.T) {
	args := muxArgs("video.mp4", "audio.f32le", "final.mp4")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-c:v copy", "-c:a aac", "-ar 48000", "-ac 2"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("mux args missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "-shortest") {
		t.Fatalf("mux must preserve the exact video duration: %s", joined)
	}
}

func TestMixPCMAtPreservesSilenceOffsetAndAddsOverlap(t *testing.T) {
	dst := make([]float32, 8)
	mixPCMAt(dst, []float32{.25, .5}, 4)
	if dst[0] != 0 || dst[3] != 0 || dst[4] != .25 || dst[5] != .5 {
		t.Fatalf("span was not placed at the requested sample offset: %v", dst)
	}
	mixPCMAt(dst, []float32{.5, -.25, 1}, 4)
	if dst[4] != .75 || dst[5] != .25 || dst[6] != 1 {
		t.Fatalf("overlapping audio was not summed: %v", dst)
	}
}
