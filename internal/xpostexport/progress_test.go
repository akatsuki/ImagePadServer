package xpostexport

import (
	"testing"
	"time"
)

func TestFrameProgressShowsCountAndWaitsForTimingSample(t *testing.T) {
	for _, tc := range []struct {
		done    int
		elapsed time.Duration
		counts  string
	}{
		{0, 0, "0 / 1200 フレーム"},
		{600, 4 * time.Second, "600 / 1200 フレーム"},
		{1, 10 * time.Second, "1 / 1200 フレーム"},
	} {
		want := "映像変換 " + tc.counts + "・残り時間を計算中"
		if got := frameProgressText(tc.done, 1200, tc.elapsed); got != want {
			t.Fatalf("progress = %q, want %q", got, want)
		}
	}
}

func TestFrameProgressEstimatesRemainingFromMeasuredSpeed(t *testing.T) {
	if got := frameProgressText(300, 1500, 20*time.Second); got != "映像変換 300 / 1500 フレーム・映像の残り約1分20秒" {
		t.Fatalf("progress = %q", got)
	}
	if got := frameProgressText(30, 390, 5*time.Minute); got != "映像変換 30 / 390 フレーム・映像の残り約1時間0分" {
		t.Fatalf("long progress = %q", got)
	}
}

func TestFrameProgressKeepsFinishingSeparateFromVideoEstimate(t *testing.T) {
	want := "映像変換完了 1200 / 1200 フレーム・音声と動画を仕上げ・検証中"
	if got := frameProgressText(1200, 1200, 20*time.Second); got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

func TestFrameProgressThrottlesUpdatesButAlwaysSendsFinalCount(t *testing.T) {
	start := time.Unix(0, 0)
	tracker := frameProgress{started: start, lastUpdate: start}
	if _, ok := tracker.update(10, 100, start.Add(499*time.Millisecond)); ok {
		t.Fatal("update emitted before the 500 ms interval")
	}
	if _, ok := tracker.update(50, 100, start.Add(500*time.Millisecond)); !ok {
		t.Fatal("timed update missing")
	}
	if _, ok := tracker.update(100, 100, start.Add(501*time.Millisecond)); !ok {
		t.Fatal("final frame count must be emitted immediately")
	}
}
