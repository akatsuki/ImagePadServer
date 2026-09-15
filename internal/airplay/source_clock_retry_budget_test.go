package airplay

import (
	"testing"
	"time"
)

func TestSourceClockRetryBudgetLimitsAttemptsAndLatchesExhaustion(t *testing.T) {
	base := time.Unix(1000, 0)
	wantDelays := []time.Duration{
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		4 * time.Second,
	}
	var budget sourceClockRetryBudget
	for index, wantDelay := range wantDelays {
		permission := budget.Next(base.Add(time.Duration(index) * time.Second))
		if !permission.Allowed || permission.Attempt != index+1 || permission.Delay != wantDelay {
			t.Fatalf("attempt %d permission=%+v, want allowed delay=%s", index+1, permission, wantDelay)
		}
	}
	if got := budget.Next(base.Add(5 * time.Second)); got.Allowed || got.Reason != "retry-attempts-exhausted" {
		t.Fatalf("sixth retry permission=%+v", got)
	}
	if got := budget.Next(base.Add(24 * time.Hour)); got.Allowed || got.Reason != "retry-attempts-exhausted" {
		t.Fatalf("elapsed time reopened exhausted budget: %+v", got)
	}
	budget.ResetAfterHealthy()
	if got := budget.Next(base.Add(24 * time.Hour)); !got.Allowed || got.Attempt != 1 || got.Delay != 250*time.Millisecond {
		t.Fatalf("healthy reset did not open a fresh budget: %+v", got)
	}
}

func TestSourceClockRetryBudgetLatchesWindowExpiry(t *testing.T) {
	base := time.Unix(2000, 0)
	var budget sourceClockRetryBudget
	if got := budget.Next(base); !got.Allowed {
		t.Fatalf("first retry rejected: %+v", got)
	}
	if got := budget.Next(base.Add(60*time.Second - time.Nanosecond)); !got.Allowed || got.Attempt != 2 {
		t.Fatalf("retry immediately before window boundary=%+v", got)
	}
	if got := budget.Next(base.Add(60 * time.Second)); got.Allowed || got.Reason != "retry-window-exhausted" {
		t.Fatalf("retry at exact window boundary=%+v", got)
	}
	if got := budget.Next(base.Add(61 * time.Second)); got.Allowed || got.Reason != "retry-window-exhausted" {
		t.Fatalf("time passage reopened expired window: %+v", got)
	}
	budget.ResetAfterHealthy()
	if got := budget.Next(base.Add(61 * time.Second)); !got.Allowed || got.Attempt != 1 {
		t.Fatalf("healthy reset did not reopen window-exhausted budget: %+v", got)
	}
}

func TestSourceClockRetryBudgetHandlesZeroAndBackwardTime(t *testing.T) {
	var budget sourceClockRetryBudget
	if got := budget.Next(time.Time{}); !got.Allowed || got.Attempt != 1 {
		t.Fatalf("zero-time first retry=%+v", got)
	}
	budget.ResetAfterHealthy()
	base := time.Unix(2500, 0)
	if got := budget.Next(base); !got.Allowed || got.Attempt != 1 {
		t.Fatalf("first retry before clock regression=%+v", got)
	}
	if got := budget.Next(base.Add(-time.Second)); !got.Allowed || got.Attempt != 2 || got.Delay != 500*time.Millisecond {
		t.Fatalf("backward-time retry=%+v", got)
	}
}

func TestSourceClockRetryBudgetPrefersWindowReasonAtSimultaneousLimit(t *testing.T) {
	base := time.Unix(3000, 0)
	var budget sourceClockRetryBudget
	for attempt := 0; attempt < sourceClockRetryMaxAttempts; attempt++ {
		if got := budget.Next(base.Add(time.Duration(attempt) * time.Second)); !got.Allowed {
			t.Fatalf("attempt %d rejected: %+v", attempt+1, got)
		}
	}
	if got := budget.Next(base.Add(sourceClockRetryWindow)); got.Allowed || got.Reason != "retry-window-exhausted" {
		t.Fatalf("simultaneous attempt/window limit=%+v", got)
	}
}

func TestSourceClockPublisherRetryDelaySequence(t *testing.T) {
	want := []time.Duration{
		250 * time.Millisecond,
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		4 * time.Second,
	}
	for index, wantDelay := range want {
		if got := sourceClockPublisherRetryDelay(index + 1); got != wantDelay {
			t.Fatalf("attempt %d delay=%s, want %s", index+1, got, wantDelay)
		}
	}
	if got := sourceClockPublisherRetryDelay(100); got != 4*time.Second {
		t.Fatalf("delay after supported attempts=%s, want 4s cap", got)
	}
}
