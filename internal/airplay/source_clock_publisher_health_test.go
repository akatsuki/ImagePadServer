package airplay

import (
	"math"
	"testing"
	"time"
)

func TestSourceClockPublisherHealthRejectsSparseObservationsAndStaleOutput(t *testing.T) {
	base := time.Unix(3500, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	ready := sourceClockPublisherHealthObservation{Generation: 1, PublisherReady: true}
	tracker.Observe(base, ready)
	if tracker.Observe(base.Add(30*time.Second), ready) {
		t.Fatal("two sparse ready observations became continuous health")
	}

	tracker = newSourceClockPublisherHealthTracker(30 * time.Second)
	media := sourceClockPublisherHealthObservation{
		Generation: 1, PublisherReady: true, MediaStarted: true, OutputCounter: 100, ErrorFree: true,
	}
	tracker.Observe(base, media)
	media.OutputCounter++
	tracker.Observe(base.Add(time.Second), media)
	for second := 2; second <= 30; second++ {
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), media) {
			t.Fatal("stale one-time output progress became continuous health")
		}
	}
}

func TestSourceClockPublisherHealthRequiresContinuousReadyBeforeMedia(t *testing.T) {
	base := time.Unix(4000, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	if tracker.Observe(base, sourceClockPublisherHealthObservation{Generation: 1}) {
		t.Fatal("process existence without publisher-ready became healthy")
	}
	ready := sourceClockPublisherHealthObservation{Generation: 1, PublisherReady: true}
	for second := 0; second < 30; second++ {
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), ready) {
			t.Fatalf("publisher became healthy at second %d", second)
		}
	}
	if !tracker.Observe(base.Add(30*time.Second), ready) {
		t.Fatal("continuous publisher-ready did not become healthy at 30 seconds")
	}
	if tracker.Observe(base.Add(31*time.Second), ready) {
		t.Fatal("healthy transition was emitted more than once")
	}
}

func TestSourceClockPublisherHealthAfterMediaRequiresContinuousOutputAndNoError(t *testing.T) {
	base := time.Unix(5000, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	observation := sourceClockPublisherHealthObservation{
		Generation: 1, PublisherReady: true, MediaStarted: true, OutputCounter: 100, ErrorFree: true,
	}
	if tracker.Observe(base, observation) {
		t.Fatal("post-media health reset immediately")
	}
	for second := 1; second < 30; second++ {
		observation.OutputCounter++
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), observation) {
			t.Fatalf("post-media publisher became healthy at second %d", second)
		}
	}
	observation.OutputCounter++
	if !tracker.Observe(base.Add(30*time.Second), observation) {
		t.Fatal("continuous error-free output did not become healthy")
	}
}

func TestSourceClockPublisherHealthResetsOnReadyErrorAndGenerationChanges(t *testing.T) {
	base := time.Unix(6000, 0)
	tests := []struct {
		name   string
		mutate func(*sourceClockPublisherHealthObservation)
	}{
		{name: "ready loss", mutate: func(observation *sourceClockPublisherHealthObservation) { observation.PublisherReady = false }},
		{name: "error", mutate: func(observation *sourceClockPublisherHealthObservation) { observation.ErrorFree = false }},
		{name: "generation", mutate: func(observation *sourceClockPublisherHealthObservation) { observation.Generation++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
			observation := sourceClockPublisherHealthObservation{
				Generation: 1, PublisherReady: true, MediaStarted: true, OutputCounter: 10, ErrorFree: true,
			}
			tracker.Observe(base, observation)
			for second := 1; second <= 20; second++ {
				observation.OutputCounter++
				tracker.Observe(base.Add(time.Duration(second)*time.Second), observation)
			}
			test.mutate(&observation)
			tracker.Observe(base.Add(21*time.Second), observation)
			observation.PublisherReady = true
			observation.ErrorFree = true
			healthyAt := 52
			if test.name == "generation" {
				healthyAt = 51
			}
			for second := 22; second < healthyAt; second++ {
				observation.OutputCounter++
				if tracker.Observe(base.Add(time.Duration(second)*time.Second), observation) {
					t.Fatalf("health window survived %s at second %d", test.name, second)
				}
			}
			observation.OutputCounter++
			if !tracker.Observe(base.Add(time.Duration(healthyAt)*time.Second), observation) {
				t.Fatalf("new 30-second window after %s did not become healthy", test.name)
			}
		})
	}
}

func TestSourceClockPublisherHealthResetsOnClockAndCounterRegression(t *testing.T) {
	base := time.Unix(7000, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	ready := sourceClockPublisherHealthObservation{Generation: 1, PublisherReady: true}
	tracker.Observe(base, ready)
	tracker.Observe(base.Add(time.Second), ready)
	tracker.Observe(base, ready)
	for second := 1; second < 30; second++ {
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), ready) {
			t.Fatal("clock regression preserved the old health window")
		}
	}
	if !tracker.Observe(base.Add(30*time.Second), ready) {
		t.Fatal("clock-regression replacement window did not become healthy")
	}

	tracker = newSourceClockPublisherHealthTracker(30 * time.Second)
	media := sourceClockPublisherHealthObservation{
		Generation: 1, PublisherReady: true, MediaStarted: true, OutputCounter: math.MaxUint64 - 1, ErrorFree: true,
	}
	tracker.Observe(base, media)
	media.OutputCounter = math.MaxUint64
	tracker.Observe(base.Add(time.Second), media)
	media.OutputCounter = 0
	tracker.Observe(base.Add(2*time.Second), media)
	for second := 3; second < 32; second++ {
		media.OutputCounter++
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), media) {
			t.Fatal("counter wrap preserved the old health window")
		}
	}
	media.OutputCounter++
	if !tracker.Observe(base.Add(32*time.Second), media) {
		t.Fatal("counter-wrap replacement window did not become healthy")
	}
}

func TestSourceClockPublisherHealthRearmsAfterHealthyThenUnhealthy(t *testing.T) {
	base := time.Unix(8000, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	ready := sourceClockPublisherHealthObservation{Generation: 1, PublisherReady: true}
	for second := 0; second <= 30; second++ {
		got := tracker.Observe(base.Add(time.Duration(second)*time.Second), ready)
		if got != (second == 30) {
			t.Fatalf("initial healthy transition at second %d = %t", second, got)
		}
	}
	ready.PublisherReady = false
	tracker.Observe(base.Add(31*time.Second), ready)
	ready.PublisherReady = true
	for second := 32; second < 62; second++ {
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), ready) {
			t.Fatalf("rehealthy transition occurred early at second %d", second)
		}
	}
	if !tracker.Observe(base.Add(62*time.Second), ready) {
		t.Fatal("healthy tracker did not rearm after ready loss")
	}
}

func TestSourceClockPublisherHealthKeepsMediaStartedAcrossGeneration(t *testing.T) {
	base := time.Unix(9000, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	preMedia := sourceClockPublisherHealthObservation{Generation: 1, PublisherReady: true}
	for second := 0; second < 20; second++ {
		tracker.Observe(base.Add(time.Duration(second)*time.Second), preMedia)
	}
	media := sourceClockPublisherHealthObservation{
		Generation: 1, PublisherReady: true, MediaStarted: true, OutputCounter: 30, ErrorFree: true,
	}
	tracker.Observe(base.Add(20*time.Second), media)
	for second := 21; second < 50; second++ {
		media.OutputCounter++
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), media) {
			t.Fatal("media transition kept the pre-media health window")
		}
	}
	media.OutputCounter++
	if !tracker.Observe(base.Add(50*time.Second), media) {
		t.Fatal("post-media replacement window did not become healthy")
	}

	tracker = newSourceClockPublisherHealthTracker(30 * time.Second)
	tracker.Observe(base, media)
	next := sourceClockPublisherHealthObservation{Generation: 2, PublisherReady: true, OutputCounter: media.OutputCounter, ErrorFree: true}
	for second := 1; second <= 30; second++ {
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), next) {
			t.Fatal("new generation became healthy without post-media output")
		}
	}
	for second := 31; second < 60; second++ {
		next.OutputCounter++
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), next) {
			t.Fatalf("new generation rehealthy occurred early at second %d", second)
		}
	}
	next.OutputCounter++
	if !tracker.Observe(base.Add(60*time.Second), next) {
		t.Fatal("media-started state was not preserved across publisher generation")
	}
}

func TestSourceClockPublisherHealthHandlesZeroTimeAndRejectsDisabledDuration(t *testing.T) {
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	ready := sourceClockPublisherHealthObservation{Generation: 1, PublisherReady: true}
	for second := 0; second <= 30; second++ {
		got := tracker.Observe(time.Time{}.Add(time.Duration(second)*time.Second), ready)
		if got != (second == 30) {
			t.Fatalf("zero-time window transition at second %d = %t", second, got)
		}
	}
	for _, duration := range []time.Duration{0, -time.Second} {
		tracker = newSourceClockPublisherHealthTracker(duration)
		if tracker.Observe(time.Time{}, ready) || tracker.Observe(time.Time{}.Add(time.Hour), ready) {
			t.Fatalf("disabled duration %s became healthy", duration)
		}
	}
}

func TestSourceClockPublisherHealthRestartsWindowWhenFailureCounterChanges(t *testing.T) {
	base := time.Unix(10000, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	observation := sourceClockPublisherHealthObservation{
		Generation: 1, PublisherReady: true, MediaStarted: true, OutputCounter: 100, FailureCounter: 5, ErrorFree: true,
	}
	tracker.Observe(base, observation)
	for second := 1; second <= 20; second++ {
		observation.OutputCounter++
		tracker.Observe(base.Add(time.Duration(second)*time.Second), observation)
	}
	observation.FailureCounter++
	observation.OutputCounter++
	if tracker.Observe(base.Add(21*time.Second), observation) {
		t.Fatal("failure counter change preserved the old healthy window")
	}
	for second := 22; second < 51; second++ {
		observation.OutputCounter++
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), observation) {
			t.Fatalf("replacement window completed early at second %d", second)
		}
	}
	observation.OutputCounter++
	if !tracker.Observe(base.Add(51*time.Second), observation) {
		t.Fatal("failure-counter replacement window did not become healthy")
	}
}

func TestSourceClockPublisherHealthRestartsPreMediaWindowOnFailureCounterChange(t *testing.T) {
	base := time.Unix(11000, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	observation := sourceClockPublisherHealthObservation{Generation: 1, PublisherReady: true, FailureCounter: 3}
	for second := 0; second <= 20; second++ {
		tracker.Observe(base.Add(time.Duration(second)*time.Second), observation)
	}
	observation.FailureCounter++
	if tracker.Observe(base.Add(21*time.Second), observation) {
		t.Fatal("pre-media failure counter change preserved the old window")
	}
	for second := 22; second < 51; second++ {
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), observation) {
			t.Fatalf("pre-media replacement window completed early at second %d", second)
		}
	}
	if !tracker.Observe(base.Add(51*time.Second), observation) {
		t.Fatal("pre-media failure-counter replacement window did not become healthy")
	}
}

func TestSourceClockPublisherHealthTreatsFailureCounterWrapAsNewWindow(t *testing.T) {
	base := time.Unix(12000, 0)
	tracker := newSourceClockPublisherHealthTracker(30 * time.Second)
	observation := sourceClockPublisherHealthObservation{Generation: 1, PublisherReady: true, FailureCounter: math.MaxUint64}
	tracker.Observe(base, observation)
	observation.FailureCounter = 0
	tracker.Observe(base.Add(time.Second), observation)
	for second := 2; second < 31; second++ {
		if tracker.Observe(base.Add(time.Duration(second)*time.Second), observation) {
			t.Fatalf("failure counter wrap preserved old window at second %d", second)
		}
	}
	if !tracker.Observe(base.Add(31*time.Second), observation) {
		t.Fatal("failure-counter wrap replacement window did not become healthy")
	}
}
