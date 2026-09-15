package airplay

import (
	"testing"
	"time"
)

func TestBridgeRespawnBackoffEscalates(t *testing.T) {
	b := newBridgeRespawnBackoff(500*time.Millisecond, 5*time.Second, 8)

	want := []time.Duration{
		500 * time.Millisecond,
		time.Second,
		2 * time.Second,
		4 * time.Second,
		5 * time.Second,
	}
	for i, w := range want {
		got, exhausted := b.nextDelay()
		if exhausted {
			t.Fatalf("attempt %d: unexpectedly exhausted", i+1)
		}
		if got != w {
			t.Fatalf("attempt %d delay = %s; want %s", i+1, got, w)
		}
	}
}

func TestBridgeRespawnBackoffCapsAtMaxRetries(t *testing.T) {
	b := newBridgeRespawnBackoff(time.Millisecond, time.Millisecond, 3)
	for i := 0; i < 3; i++ {
		if _, exhausted := b.nextDelay(); exhausted {
			t.Fatalf("attempt %d: exhausted too early", i+1)
		}
	}
	if _, exhausted := b.nextDelay(); !exhausted {
		t.Fatal("expected exhaustion after maxRetries")
	}
}

func TestBridgeRespawnBackoffReset(t *testing.T) {
	b := newBridgeRespawnBackoff(time.Millisecond, 8*time.Millisecond, 3)
	b.nextDelay() // 1ms
	b.nextDelay() // 2ms
	b.reset()
	d, _ := b.nextDelay()
	if d != time.Millisecond {
		t.Fatalf("after reset delay = %s; want %s", d, time.Millisecond)
	}
	if b.attempts() != 1 {
		t.Fatalf("attempts after reset = %d; want 1", b.attempts())
	}
}
