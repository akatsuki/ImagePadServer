package server

import (
	"fmt"
	"time"
)

const nicoProgressInterval = 300 * time.Millisecond

// newNicoProgressReporter limits the high-frequency per-frame callback while
// preserving the first update and the final frame update.
func newNicoProgressReporter(now func() time.Time, emit func(percent int, text string)) func(completed, total int64) {
	if now == nil {
		now = time.Now
	}
	if emit == nil {
		emit = func(int, string) {}
	}
	last := time.Time{}
	return func(completed, total int64) {
		current := now()
		final := total > 0 && completed >= total
		if !last.IsZero() && current.Sub(last) < nicoProgressInterval && !final {
			return
		}
		percent := int64(30)
		if total > 0 {
			if completed < 0 {
				completed = 0
			}
			percent += completed * 50 / total
		}
		if percent > 80 {
			percent = 80
		}
		if percent < 30 {
			percent = 30
		}
		emit(int(percent), fmt.Sprintf("コメント描画・合成中 %d/%dフレーム", completed, total))
		last = current
	}
}
