package xpostexport

import (
	"fmt"
	"math"
	"time"
)

type frameProgress struct {
	started, lastUpdate time.Time
}

func (p *frameProgress) update(done, total int, now time.Time) (string, bool) {
	if total < 1 || done < 0 || done > total || (done != total && now.Sub(p.lastUpdate) < 500*time.Millisecond) {
		return "", false
	}
	p.lastUpdate = now
	return frameProgressText(done, total, now.Sub(p.started)), true
}

func frameProgressText(done, total int, elapsed time.Duration) string {
	counts := fmt.Sprintf("%d / %d フレーム", done, total)
	if done == total {
		return "映像変換完了 " + counts + "・音声と動画を仕上げ・検証中"
	}
	if elapsed < 5*time.Second || done < 30 {
		return "映像変換 " + counts + "・残り時間を計算中"
	}
	// This measures this export, including compositor startup. Audio mixing,
	// muxing, and final validation follow rendering and have their own status.
	seconds := int(math.Ceil(elapsed.Seconds() * float64(total-done) / float64(done)))
	remaining := fmt.Sprintf("%d秒", seconds)
	if seconds >= 3600 {
		minutes := (seconds + 59) / 60
		remaining = fmt.Sprintf("%d時間%d分", minutes/60, minutes%60)
	} else if seconds >= 60 {
		remaining = fmt.Sprintf("%d分%d秒", seconds/60, seconds%60)
	}
	return "映像変換 " + counts + "・映像の残り約" + remaining
}
