package video

import (
	"testing"
	"time"
)

func TestNicoStageTimerRecordsOrderedStages(t *testing.T) {
	var observed []NicoStageTiming
	timer := newNicoStageTimer(func(stage NicoStageTiming) {
		observed = append(observed, stage)
	})
	start := time.Now().Add(-10 * time.Millisecond)
	timer.mark("decode_filter", start)
	timer.mark("encode_mux", start)
	if len(timer.timings) != 2 || len(observed) != 2 {
		t.Fatalf("timings=%v observed=%v, want two ordered stages", timer.timings, observed)
	}
	if timer.timings[0].Name != "decode_filter" || timer.timings[1].Name != "encode_mux" {
		t.Fatalf("stage order=%v", timer.timings)
	}
	if timer.timings[0].Elapsed <= 0 || timer.timings[1].Elapsed <= 0 {
		t.Fatalf("elapsed=%v, want positive durations", timer.timings)
	}
	if timer.timings[0].StartedAt.IsZero() || timer.timings[0].FinishedAt.IsZero() || timer.timings[0].FinishedAt.Before(timer.timings[0].StartedAt) {
		t.Fatalf("timestamps=%v, want ordered timestamps", timer.timings[0])
	}
}

func TestNicoStageTimerRecordsByteCount(t *testing.T) {
	timer := newNicoStageTimer(nil)
	timer.markBytes("frame_pipe_write", time.Now().Add(-time.Millisecond), 1234)
	if got := timer.timings[0].Bytes; got != 1234 {
		t.Fatalf("bytes=%d, want 1234", got)
	}
}

func TestNicoStageTimerRecordsExplicitTiming(t *testing.T) {
	timer := newNicoStageTimer(nil)
	started := time.Now().Add(-2 * time.Millisecond)
	finished := time.Now()
	timer.record(NicoStageTiming{
		Name:       "native_compositor",
		StartedAt:  started,
		FinishedAt: finished,
		Elapsed:    finished.Sub(started),
	})
	if len(timer.timings) != 1 || timer.timings[0].Name != "native_compositor" {
		t.Fatalf("timings=%v, want one explicit timing", timer.timings)
	}
	if timer.timings[0].Elapsed <= 0 {
		t.Fatalf("elapsed=%v, want positive duration", timer.timings[0].Elapsed)
	}
}
