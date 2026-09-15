package airplaycontract

import "time"

const (
	RecoveryMaxAttempts = 5
	RecoveryWindow      = 60 * time.Second
)

type RecoveryPermission struct {
	Allowed bool
	Attempt int
	Delay   time.Duration
	Reason  string
}

// RecoveryBudget is owner-serialized. Elapsed time alone cannot reopen it.
type RecoveryBudget struct {
	attempts        int
	firstAttemptAt  time.Time
	exhaustedReason string
}

func (b *RecoveryBudget) Next(now time.Time) RecoveryPermission {
	if b.exhaustedReason != "" {
		return RecoveryPermission{Reason: b.exhaustedReason}
	}
	if b.attempts > 0 && !now.Before(b.firstAttemptAt.Add(RecoveryWindow)) {
		b.exhaustedReason = "retry-window-exhausted"
		return RecoveryPermission{Reason: b.exhaustedReason}
	}
	if b.attempts >= RecoveryMaxAttempts {
		b.exhaustedReason = "retry-attempts-exhausted"
		return RecoveryPermission{Reason: b.exhaustedReason}
	}
	if b.attempts == 0 {
		b.firstAttemptAt = now
	}
	b.attempts++
	return RecoveryPermission{Allowed: true, Attempt: b.attempts, Delay: RecoveryDelay(b.attempts)}
}

func (b *RecoveryBudget) ResetAfterHealthy() { *b = RecoveryBudget{} }

func RecoveryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 250 * time.Millisecond
	for i := 1; i < attempt && delay < 4*time.Second; i++ {
		delay *= 2
	}
	return min(delay, 4*time.Second)
}
