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
