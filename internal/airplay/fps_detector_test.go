package airplay

import (
	"encoding/binary"
	"testing"
	"time"
)

// feedAtInterval primes a detector with a steady cadence using synthetic times,
// so the unit tests do not depend on wall-clock timing.
func feedAtInterval(t *testing.T, d *fpsDetector, interval time.Duration, count int) {
	t.Helper()
	start := time.Unix(1_700_000_000, 0)
	for i := 0; i < count; i++ {
		d.observeFrame(start.Add(time.Duration(i) * interval))
	}
}

func TestFPSDetectorDefaultsTo30FPS(t *testing.T) {
	var d fpsDetector
	if got := d.timestampStep(); got != defaultH264RTPFrameTimestampStep {
		t.Fatalf("default timestamp step = %d; want %d", got, defaultH264RTPFrameTimestampStep)
	}
}

func TestFPSDetectorMeasures60FPS(t *testing.T) {
	var d fpsDetector
	feedAtInterval(t, &d, time.Second/60, 60)
	if got := d.timestampStep(); got != h264RTPClockRate/60 {
		t.Fatalf("60 fps timestamp step = %d; want %d", got, h264RTPClockRate/60)
	}
}

func TestFPSDetectorMeasures30FPS(t *testing.T) {
	var d fpsDetector
	feedAtInterval(t, &d, time.Second/30, 30)
	if got := d.timestampStep(); got != h264RTPClockRate/30 {
		t.Fatalf("30 fps timestamp step = %d; want %d", got, h264RTPClockRate/30)
	}
}

func TestFPSDetectorIgnoresStall(t *testing.T) {
	var d fpsDetector
	feedAtInterval(t, &d, time.Second/30, 30)
	// A multi-second stall must not drag the average down from 30 fps.
	d.observeFrame(time.Unix(1_700_000_030, 0))
	if got := d.timestampStep(); got != h264RTPClockRate/30 {
		t.Fatalf("30 fps step after stall = %d; want %d", got, h264RTPClockRate/30)
	}
}

func TestFPSDetectorIgnoresSubMillisecondIntervals(t *testing.T) {
	var d fpsDetector
	// Prime with 30 fps, then feed implausibly fast frames (as a tight test
	// loop would): the average must stay pinned at 30 fps.
	feedAtInterval(t, &d, time.Second/30, 30)
	start := time.Unix(1_700_000_040, 0)
	for i := 0; i < 100; i++ {
		d.observeFrame(start.Add(time.Duration(i) * time.Microsecond))
	}
	if got := d.timestampStep(); got != h264RTPClockRate/30 {
		t.Fatalf("30 fps step after fast intervals = %d; want %d", got, h264RTPClockRate/30)
	}
}

func TestH264RTPRelayStateSynthesizesConstantTimestampsFromMeasuredFPS(t *testing.T) {
	state := readyH264RTPRelayState()
	// Prime the detector with a steady 60 fps cadence; the relay must then
	// advance broken constant timestamps at 1500 (90000/60) rather than the
	// hardcoded 30 fps default.
	feedAtInterval(t, &state.fps, time.Second/60, 60)

	step := h264RTPClockRate / 60
	want := []uint32{0, step, 2 * step}
	for i, wantTimestamp := range want {
		outputs := state.forward(testMarkedRTPPacket(uint16(i+1), 500, []byte{1, byte(i)}))
		if len(outputs) != 1 {
			t.Fatalf("frame %d produced %d packets; want 1", i, len(outputs))
		}
		if got := binary.BigEndian.Uint32(outputs[0][4:8]); got != wantTimestamp {
			t.Fatalf("frame %d timestamp = %d; want %d (60 fps step)", i, got, wantTimestamp)
		}
	}
}
