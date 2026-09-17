package niconico

import "testing"

func TestFrameClockUsesFrameTimeInsteadOfProcessingRate(t *testing.T) {
	thirty := MustFrameClock(30, 1)
	sixty := MustFrameClock(60, 1)
	if got := thirty.CommentTimeMs(300); got != 10000 {
		t.Fatalf("30fps frame time = %dms, want 10000", got)
	}
	if got := sixty.CommentTimeMs(600); got != 10000 {
		t.Fatalf("60fps frame time = %dms, want 10000", got)
	}
}

func TestFrameClockPreservesNTSCFraction(t *testing.T) {
	clock := MustFrameClock(30000, 1001)
	if got := clock.CommentTimeMs(30000); got != 1001000 {
		t.Fatalf("29.97fps frame time = %dms, want 1001000", got)
	}
}

func TestFrameClockCeilsDurationWithoutDroppingTail(t *testing.T) {
	clock := MustFrameClock(30, 1)
	if got := clock.FrameCountForDurationMs(10001); got != 301 {
		t.Fatalf("frame count = %d, want 301", got)
	}
}
