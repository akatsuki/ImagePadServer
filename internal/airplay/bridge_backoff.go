package airplay

import "time"

const (
	// bridgeRespawnInitialDelay is the wait before the first bridge respawn
	// attempt after a failure.
	bridgeRespawnInitialDelay = 500 * time.Millisecond
	// bridgeRespawnMaxDelay caps the exponential backoff between attempts.
	bridgeRespawnMaxDelay = 5 * time.Second
	// bridgeRespawnStableDuration resets the retry budget: a bridge that ran
	// this long before exiting was doing real work rather than crash-looping.
	bridgeRespawnStableDuration = 30 * time.Second
	// bridgeRespawnMaxRetries is how many rapid failures are tolerated before
	// the bridge is given up on and the failure surfaced in status.
	bridgeRespawnMaxRetries = 8
)

// bridgeRespawnBackoff is the exponential backoff policy for recreating the
// FFmpeg bridge after an unexpected exit. It is only exercised once video has
// started flowing: before that, a bridge exit means the probe finished without
// an iPhone connecting, which must not consume the retry budget.
type bridgeRespawnBackoff struct {
	initial    time.Duration
	max        time.Duration
	maxRetries int
	retries    int
	delay      time.Duration
}

func newBridgeRespawnBackoff(initial, max time.Duration, maxRetries int) *bridgeRespawnBackoff {
	return &bridgeRespawnBackoff{
		initial:    initial,
		max:        max,
		maxRetries: maxRetries,
		delay:      initial,
	}
}

// reset clears the retry budget so a bridge that stabilised for a while does
// not inherit stale failures.
func (b *bridgeRespawnBackoff) reset() {
	b.retries = 0
	b.delay = b.initial
}

// nextDelay returns how long to wait before the next respawn attempt and
// whether the budget has been exhausted. The delay doubles on each call up to
// max, so the first failure waits initial, the second waits 2*initial, etc.
func (b *bridgeRespawnBackoff) nextDelay() (time.Duration, bool) {
	if b.retries >= b.maxRetries {
		return 0, true
	}
	b.retries++
	delay := b.delay
	if b.delay < b.max {
		b.delay *= 2
		if b.delay > b.max {
			b.delay = b.max
		}
	}
	return delay, false
}

// attempts reports how many failures have been counted so far.
func (b *bridgeRespawnBackoff) attempts() int {
	return b.retries
}
