package airplay

import "time"

type sourceClockTimeoutDecision string

const (
	sourceClockTimeoutNone           sourceClockTimeoutDecision = "none"
	sourceClockTimeoutNoInitialMedia sourceClockTimeoutDecision = "no-initial-media"
	sourceClockTimeoutNoSignal       sourceClockTimeoutDecision = "no-signal"
)

// SessionLifecycle tracks decoded media observations for one receiver session.
//
// The timestamps are advanced only by ObserveVideoDecoded and
// ObserveAudioDecoded. In particular, synthetic, composed, or sent frames do
// not extend the no-signal deadline.
type SessionLifecycle struct {
	StartedAt      time.Time
	FirstDecodedAt time.Time
	LastVideoAt    time.Time
	LastAudioAt    time.Time
	Timeout        time.Duration
}

// ObserveVideoDecoded records an actual decoded video observation.
func (s *SessionLifecycle) ObserveVideoDecoded(at time.Time) {
	s.observeDecoded(&s.LastVideoAt, at)
}

// ObserveAudioDecoded records an actual decoded audio observation.
func (s *SessionLifecycle) ObserveAudioDecoded(at time.Time) {
	s.observeDecoded(&s.LastAudioAt, at)
}

func (s *SessionLifecycle) observeDecoded(last *time.Time, at time.Time) {
	if at.IsZero() {
		return
	}
	if s.FirstDecodedAt.IsZero() {
		s.FirstDecodedAt = at
	}
	if last.IsZero() || at.After(*last) {
		*last = at
	}
}

// NoSignalExpired reports whether the session has reached its no-signal
// deadline. It is a read-only check suitable for repeated restart decisions.
func (s SessionLifecycle) NoSignalExpired(now time.Time) bool {
	if s.Timeout <= 0 || s.FirstDecodedAt.IsZero() {
		return false
	}

	last := s.FirstDecodedAt
	if s.LastVideoAt.After(last) {
		last = s.LastVideoAt
	}
	if s.LastAudioAt.After(last) {
		last = s.LastAudioAt
	}
	return !now.Before(last.Add(s.Timeout))
}

// NoSignalDecision reports which session timeout, if any, has elapsed.
func (s SessionLifecycle) NoSignalDecision(now time.Time) sourceClockTimeoutDecision {
	if s.Timeout <= 0 {
		return sourceClockTimeoutNone
	}
	if s.FirstDecodedAt.IsZero() {
		if s.StartedAt.IsZero() || now.Before(s.StartedAt.Add(s.Timeout)) {
			return sourceClockTimeoutNone
		}
		return sourceClockTimeoutNoInitialMedia
	}
	if s.NoSignalExpired(now) {
		return sourceClockTimeoutNoSignal
	}
	return sourceClockTimeoutNone
}
