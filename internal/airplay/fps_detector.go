package airplay

import "time"

const (
	// h264RTPClockRate is the RTP timestamp clock for H.264 video (90 kHz).
	h264RTPClockRate uint32 = 90000
	// defaultH264RTPFrameTimestampStep is the 90 kHz timestamp increment for
	// one access unit at the fallback 30 fps cadence.
	defaultH264RTPFrameTimestampStep uint32 = h264RTPClockRate / 30
)

const (
	minH264RTPTimestampStep uint32 = h264RTPClockRate / 120 // bound: 120 fps
	maxH264RTPTimestampStep uint32 = h264RTPClockRate       // bound: 1 fps
)

const (
	// minObservedFrameInterval ignores sub-millisecond intervals that real
	// sources never produce but that appear in fast test loops and clock
	// anomalies, which would otherwise drag the moving average toward zero.
	minObservedFrameInterval = time.Millisecond
	// maxObservedFrameInterval ignores stalls longer than 5 s so a single
	// disconnect does not drag the moving average down.
	maxObservedFrameInterval = 5 * time.Second
)

// fpsDetector measures the wall-clock arrival rate of H.264 access units and
// derives a 90 kHz RTP timestamp step from the exponential moving average of
// the inter-frame interval. It is consulted only when the upstream supplies
// constant RTP timestamps (observed delta == 0), where the real frame cadence
// cannot be read from the timestamps themselves. UxPlay on Windows can emit
// every mirrored frame with the same timestamp; without this the output PTS
// would either freeze (if never advanced) or run at a hardcoded 30 fps that
// halves the speed of 60 fps content.
//
// All methods run on the single relay goroutine and are not safe for
// concurrent use.
type fpsDetector struct {
	initialized  bool
	lastFrameAt  time.Time
	emaInterval  time.Duration
	observations int
}

// observeFrame records the completion of one access unit (RTP marker bit set).
// Intervals below the minimum or above the maximum bounds are ignored so that
// neither sub-real fast loops nor a single stall drags the moving average.
func (d *fpsDetector) observeFrame(now time.Time) {
	if !d.initialized {
		d.initialized = true
		d.lastFrameAt = now
		return
	}
	interval := now.Sub(d.lastFrameAt)
	d.lastFrameAt = now
	if interval < minObservedFrameInterval || interval > maxObservedFrameInterval {
		return
	}
	d.observations++
	if d.observations == 1 {
		d.emaInterval = interval
		return
	}
	// Exponential moving average: recent frames weigh more, so the step tracks
	// a changing source fps while smoothing per-frame jitter.
	const alpha = 0.1
	d.emaInterval = time.Duration(float64(d.emaInterval)*(1-alpha) + float64(interval)*alpha)
}

// timestampStep returns the 90 kHz increment for one access unit derived from
// the measured frame rate, or the 30 fps default until enough frames have been
// observed.
func (d *fpsDetector) timestampStep() uint32 {
	if d.observations == 0 {
		return defaultH264RTPFrameTimestampStep
	}
	fps := float64(time.Second) / float64(d.emaInterval)
	step := uint32(float64(h264RTPClockRate)/fps + 0.5)
	if step < minH264RTPTimestampStep {
		return minH264RTPTimestampStep
	}
	if step > maxH264RTPTimestampStep {
		return maxH264RTPTimestampStep
	}
	return step
}
