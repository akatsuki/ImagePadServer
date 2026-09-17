package server

import (
	"reflect"
	"testing"
	"time"
)

func TestNicoProgressThrottlesButEmitsFinal(t *testing.T) {
	now := time.Unix(0, 0)
	var percents []int
	report := newNicoProgressReporter(func() time.Time { return now }, func(percent int, _ string) {
		percents = append(percents, percent)
	})
	report(1, 100)
	now = now.Add(100 * time.Millisecond)
	report(2, 100)
	now = now.Add(200 * time.Millisecond)
	report(3, 100)
	report(100, 100)
	if !reflect.DeepEqual(percents, []int{30, 31, 80}) {
		t.Fatalf("percents = %v", percents)
	}
}

func TestNicoProgressHandlesZeroTotalAndNegativeCompleted(t *testing.T) {
	var got []string
	report := newNicoProgressReporter(func() time.Time { return time.Unix(1, 0) }, func(_ int, text string) {
		got = append(got, text)
	})
	report(-1, 0)
	if !reflect.DeepEqual(got, []string{"コメント描画・合成中 -1/0フレーム"}) {
		t.Fatalf("text = %v", got)
	}
}
