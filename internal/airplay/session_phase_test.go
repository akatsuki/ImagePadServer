package airplay

import (
	"reflect"
	"testing"
	"time"
)

func TestSessionLifecycleBeforeFirstDecodedIsNeverExpired(t *testing.T) {
	lifecycle := SessionLifecycle{Timeout: time.Minute}

	if lifecycle.NoSignalExpired(time.Now().Add(30 * time.Minute)) {
		t.Fatal("session must not expire before the first decoded media observation")
	}
}

func TestSessionLifecycleTimeoutDisabled(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	for _, timeout := range []time.Duration{0, -time.Second} {
		lifecycle := SessionLifecycle{Timeout: timeout}
		lifecycle.ObserveVideoDecoded(base)

		if lifecycle.NoSignalExpired(base.Add(24 * time.Hour)) {
			t.Fatalf("timeout %s must disable expiration", timeout)
		}
	}
}

func TestSessionLifecycleUsesFirstDecodedAsInitialDeadline(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	lifecycle := SessionLifecycle{FirstDecodedAt: base, Timeout: time.Second}

	if !lifecycle.NoSignalExpired(base.Add(time.Second)) {
		t.Fatal("first decoded time must be the initial deadline when no stream timestamp is newer")
	}
}

func TestSessionLifecycleUsesNewestStreamAndExtendsOnEitherStream(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	lifecycle := SessionLifecycle{Timeout: 10 * time.Second}

	lifecycle.ObserveVideoDecoded(base)
	if !lifecycle.NoSignalExpired(base.Add(10 * time.Second)) {
		t.Fatal("expiration must include the exact last-video-plus-timeout boundary")
	}

	lifecycle.ObserveAudioDecoded(base.Add(5 * time.Second))
	if lifecycle.NoSignalExpired(base.Add(14 * time.Second)) {
		t.Fatal("newer audio must extend the no-signal deadline")
	}
	if !lifecycle.NoSignalExpired(base.Add(15 * time.Second)) {
		t.Fatal("expiration must use the newer audio observation")
	}

	lifecycle.ObserveVideoDecoded(base.Add(20 * time.Second))
	if lifecycle.NoSignalExpired(base.Add(29 * time.Second)) {
		t.Fatal("newer video must extend the no-signal deadline")
	}
}

func TestSessionLifecycleIgnoresOlderObservationsAndSetsFirstDecodedOnce(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	lifecycle := SessionLifecycle{Timeout: time.Minute}

	lifecycle.ObserveVideoDecoded(base.Add(10 * time.Second))
	lifecycle.ObserveAudioDecoded(base.Add(5 * time.Second))
	lifecycle.ObserveVideoDecoded(base.Add(9 * time.Second))
	lifecycle.ObserveAudioDecoded(base.Add(4 * time.Second))

	want := SessionLifecycle{
		FirstDecodedAt: base.Add(10 * time.Second),
		LastVideoAt:    base.Add(10 * time.Second),
		LastAudioAt:    base.Add(5 * time.Second),
		Timeout:        time.Minute,
	}
	if !reflect.DeepEqual(lifecycle, want) {
		t.Fatalf("lifecycle timestamps moved backward or first decoded was reset: got %#v, want %#v", lifecycle, want)
	}
}

func TestSessionLifecycleSupportsVideoOnlyAndAudioOnly(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	videoOnly := SessionLifecycle{Timeout: time.Second}
	videoOnly.ObserveVideoDecoded(base)
	if !videoOnly.NoSignalExpired(base.Add(time.Second)) {
		t.Fatal("video-only session must expire from its last video observation")
	}

	audioOnly := SessionLifecycle{Timeout: time.Second}
	audioOnly.ObserveAudioDecoded(base)
	if !audioOnly.NoSignalExpired(base.Add(time.Second)) {
		t.Fatal("audio-only session must expire from its last audio observation")
	}
}

func TestSessionLifecycleExpirationCheckDoesNotMutateState(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	lifecycle := SessionLifecycle{Timeout: time.Second}
	lifecycle.ObserveVideoDecoded(base)
	lifecycle.ObserveAudioDecoded(base.Add(500 * time.Millisecond))
	want := lifecycle

	_ = lifecycle.NoSignalExpired(base.Add(10 * time.Second))
	_ = lifecycle.NoSignalExpired(base.Add(20 * time.Second))

	if !reflect.DeepEqual(lifecycle, want) {
		t.Fatalf("expiration checks must not mutate lifecycle: got %#v, want %#v", lifecycle, want)
	}
}
