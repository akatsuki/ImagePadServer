package video

import (
	"strings"
	"testing"
)

func TestMotionScoreClassifiesVeryLowMotion(t *testing.T) {
	score := MotionScore{AverageFrameSize: 2 * 1024, NonIAverageFrameSize: 256, FrameCount: 30}
	if !score.IsVeryLowMotion() {
		t.Fatal("expected very low motion")
	}
	if !score.IsLowMotion() {
		t.Fatal("expected low motion")
	}
}

func TestMotionScoreClassifiesLowMotion(t *testing.T) {
	score := MotionScore{AverageFrameSize: 20 * 1024, NonIAverageFrameSize: 8 * 1024, FrameCount: 30}
	if score.IsVeryLowMotion() {
		t.Fatal("expected not very low motion")
	}
	if !score.IsLowMotion() {
		t.Fatal("expected low motion")
	}
}

func TestMotionScoreClassifiesHighMotion(t *testing.T) {
	score := MotionScore{AverageFrameSize: 200 * 1024, NonIAverageFrameSize: 100 * 1024, FrameCount: 30}
	if score.IsLowMotion() {
		t.Fatal("expected not low motion")
	}
	if score.IsVeryLowMotion() {
		t.Fatal("expected not very low motion")
	}
}

func TestAdaptPresetForContentBoostsVeryLowMotion(t *testing.T) {
	base := QualityPreset{
		Height:       720,
		VideoBitrate: "2500k",
		MaxRate:      "3000k",
		BufferSize:   "5000k",
		AudioBitrate: "128k",
		CRF:          29,
	}
	score := MotionScore{AverageFrameSize: 200 * 1024, NonIAverageFrameSize: 2 * 1024, FrameCount: 30}
	adapted := AdaptPresetForContent(base, score)

	if adapted.CRF != base.CRF+5 {
		t.Fatalf("CRF = %d, want %d", adapted.CRF, base.CRF+5)
	}
	if !strings.HasPrefix(adapted.MaxRate, "1200") {
		t.Fatalf("MaxRate = %q, want ~40%% of 3000k", adapted.MaxRate)
	}
	if !strings.HasPrefix(adapted.BufferSize, "2000") {
		t.Fatalf("BufferSize = %q, want ~40%% of 5000k", adapted.BufferSize)
	}
}

func TestAdaptPresetForContentBoostsLowMotion(t *testing.T) {
	base := QualityPreset{
		Height:       720,
		VideoBitrate: "2500k",
		MaxRate:      "3000k",
		BufferSize:   "5000k",
		AudioBitrate: "128k",
		CRF:          29,
	}
	score := MotionScore{AverageFrameSize: 30 * 1024, NonIAverageFrameSize: 8 * 1024, FrameCount: 30}
	adapted := AdaptPresetForContent(base, score)

	if adapted.CRF != base.CRF+3 {
		t.Fatalf("CRF = %d, want %d", adapted.CRF, base.CRF+3)
	}
	if !strings.HasPrefix(adapted.MaxRate, "1800") {
		t.Fatalf("MaxRate = %q, want ~60%% of 3000k", adapted.MaxRate)
	}
	if !strings.HasPrefix(adapted.BufferSize, "3000") {
		t.Fatalf("BufferSize = %q, want ~60%% of 5000k", adapted.BufferSize)
	}
}

func TestAdaptPresetForContentIgnoresHighMotion(t *testing.T) {
	base := QualityPreset{
		Height:       720,
		VideoBitrate: "2500k",
		MaxRate:      "3000k",
		BufferSize:   "5000k",
		AudioBitrate: "128k",
		CRF:          29,
	}
	score := MotionScore{AverageFrameSize: 200 * 1024, NonIAverageFrameSize: 100 * 1024, FrameCount: 30}
	adapted := AdaptPresetForContent(base, score)
	if adapted.CRF != base.CRF {
		t.Fatalf("CRF changed to %d, want unchanged %d", adapted.CRF, base.CRF)
	}
	if adapted.MaxRate != base.MaxRate {
		t.Fatalf("MaxRate changed to %q, want %q", adapted.MaxRate, base.MaxRate)
	}
}

func TestAdaptPresetForContentClampsCRF(t *testing.T) {
	base := QualityPreset{CRF: 38, MaxRate: "3000k", BufferSize: "5000k"}
	score := MotionScore{AverageFrameSize: 50 * 1024, NonIAverageFrameSize: 1024, FrameCount: 30}
	adapted := AdaptPresetForContent(base, score)
	if adapted.CRF > 40 {
		t.Fatalf("CRF = %d, want <= 40", adapted.CRF)
	}
}

func TestCapPresetToSourceBitrateLowersCeiling(t *testing.T) {
	base := QualityPreset{MaxRate: "3000k", BufferSize: "5000k"}
	capped := capPresetToSourceBitrate(base, 2_000_000) // 2 Mbps source
	if parseBitrateKInt(capped.MaxRate) >= 3000 {
		t.Fatalf("MaxRate = %q, want lower than 3000k", capped.MaxRate)
	}
	if !strings.HasPrefix(capped.MaxRate, "2500") {
		t.Fatalf("MaxRate = %q, want 2 Mbps * 1.25 = 2500k", capped.MaxRate)
	}
	if parseBitrateKInt(capped.BufferSize) >= 5000 {
		t.Fatalf("BufferSize = %q, want lower than 5000k", capped.BufferSize)
	}
}

func TestCapPresetToSourceBitrateIgnoresHigherBitrate(t *testing.T) {
	base := QualityPreset{MaxRate: "3000k", BufferSize: "5000k"}
	capped := capPresetToSourceBitrate(base, 5_000_000) // 5 Mbps source, cap is higher
	if capped.MaxRate != "3000k" {
		t.Fatalf("MaxRate = %q, want unchanged 3000k", capped.MaxRate)
	}
	if capped.BufferSize != "5000k" {
		t.Fatalf("BufferSize = %q, want unchanged 5000k", capped.BufferSize)
	}
}

func TestParseBitrateToBps(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"3000k", 3_000_000},
		{"2500K", 2_500_000},
		{"5M", 5_000_000},
		{"42", 42},
		{"", 0},
		{"bad", 0},
	}
	for _, tc := range tests {
		if got := parseBitrateToBps(tc.in); got != tc.want {
			t.Errorf("parseBitrateToBps(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func parseBitrateKInt(s string) int {
	s = strings.TrimSpace(s)
	unit := ""
	if s[len(s)-1] < '0' || s[len(s)-1] > '9' {
		unit = s[len(s)-1:]
		s = s[:len(s)-1]
	}
	v := 0
	for _, c := range s {
		v = v*10 + int(c-'0')
	}
	if unit == "m" || unit == "M" {
		v *= 1000
	}
	return v
}
