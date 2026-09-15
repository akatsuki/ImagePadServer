package airplay

import (
	"path/filepath"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

func TestSourceClockLifecycleTrackerIgnoresMetricsBeforeDecodedMedia(t *testing.T) {
	base := time.Unix(100, 0)
	tracker := newSourceClockLifecycleTracker(3 * time.Minute)
	tracker.Observe(false, base.Add(time.Second), sourceClockMetricsGateStats{
		LastVideoInputUpperBound: base,
		LastAudioInputUpperBound: base,
	})

	if got := tracker.Snapshot(); !got.FirstDecodedAt.IsZero() || !got.LastVideoAt.IsZero() || !got.LastAudioAt.IsZero() {
		t.Fatalf("pre-decode metrics advanced lifecycle: %+v", got)
	}
}

func TestSourceClockLifecycleTrackerStartsOnceAndUsesOnlyNewerInputActivity(t *testing.T) {
	base := time.Unix(200, 0)
	tracker := newSourceClockLifecycleTracker(10 * time.Second)
	tracker.Observe(true, base, sourceClockMetricsGateStats{})
	tracker.Observe(true, base.Add(2*time.Second), sourceClockMetricsGateStats{
		LastVideoInputUpperBound: base.Add(time.Second),
		LastAudioInputUpperBound: base.Add(3 * time.Second),
	})
	tracker.Observe(false, base.Add(4*time.Second), sourceClockMetricsGateStats{
		LastVideoInputUpperBound: base.Add(500 * time.Millisecond),
		LastAudioInputUpperBound: base.Add(2 * time.Second),
	})

	got := tracker.Snapshot()
	if !got.FirstDecodedAt.Equal(base) {
		t.Fatalf("FirstDecodedAt=%s, want %s", got.FirstDecodedAt, base)
	}
	if !got.LastVideoAt.Equal(base.Add(time.Second)) {
		t.Fatalf("LastVideoAt=%s", got.LastVideoAt)
	}
	if !got.LastAudioAt.Equal(base.Add(3 * time.Second)) {
		t.Fatalf("LastAudioAt=%s", got.LastAudioAt)
	}
	if tracker.NoSignalExpired(base.Add(12 * time.Second)) {
		t.Fatal("newer audio activity did not extend deadline")
	}
	if !tracker.NoSignalExpired(base.Add(13 * time.Second)) {
		t.Fatal("deadline did not expire at last activity plus timeout")
	}
}

func TestSourceClockLifecycleTrackerSurvivesPublisherRestartWithoutSyntheticExtension(t *testing.T) {
	base := time.Unix(300, 0)
	tracker := newSourceClockLifecycleTracker(time.Minute)
	tracker.Observe(true, base, sourceClockMetricsGateStats{})
	want := tracker.Snapshot()

	// A publisher generation change supplies neither decoded media nor new
	// receiver input activity. It must not rebuild or extend session state.
	tracker.Observe(false, base.Add(30*time.Second), sourceClockMetricsGateStats{})
	if got := tracker.Snapshot(); got != want {
		t.Fatalf("publisher restart changed lifecycle: got %+v want %+v", got, want)
	}
}

func TestSourceClockInitialMediaAt179SecondsReturnsNone(t *testing.T) {
	startedAt := time.Unix(400, 0)
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)

	if got := tracker.NoSignalDecision(startedAt.Add(179 * time.Second)); got != sourceClockTimeoutDecision("none") {
		t.Fatalf("decision at 179 seconds = %q, want %q", got, "none")
	}
}

func TestSourceClockInitialMediaAt180SecondsReturnsNoInitialMedia(t *testing.T) {
	startedAt := time.Unix(500, 0)
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)

	if got := tracker.NoSignalDecision(startedAt.Add(180 * time.Second)); got != sourceClockTimeoutDecision("no-initial-media") {
		t.Fatalf("decision at 180 seconds = %q, want %q", got, "no-initial-media")
	}
}

func TestSourceClockFirstDecodedVideoSwitchesToPostReadyNoSignal(t *testing.T) {
	startedAt := time.Unix(600, 0)
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)
	firstVideoAt := startedAt.Add(10 * time.Second)
	tracker.Observe(true, firstVideoAt, sourceClockMetricsGateStats{})

	if got := tracker.NoSignalDecision(firstVideoAt.Add(179 * time.Second)); got != sourceClockTimeoutDecision("none") {
		t.Fatalf("decision before post-ready deadline = %q, want %q", got, "none")
	}
	if got := tracker.NoSignalDecision(firstVideoAt.Add(180 * time.Second)); got != sourceClockTimeoutDecision("no-signal") {
		t.Fatalf("decision at post-ready deadline = %q, want %q", got, "no-signal")
	}
}

func TestSourceClockAudioOnlyBeforeFirstDecodedVideoCannotSuppressNoInitialMedia(t *testing.T) {
	startedAt := time.Unix(700, 0)
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)
	tracker.Observe(false, startedAt.Add(30*time.Second), sourceClockMetricsGateStats{
		LastAudioInputUpperBound: startedAt.Add(30 * time.Second),
	})

	if got := tracker.NoSignalDecision(startedAt.Add(180 * time.Second)); got != sourceClockTimeoutDecision("no-initial-media") {
		t.Fatalf("audio-only decision at 180 seconds = %q, want %q", got, "no-initial-media")
	}
}

func TestSourceClockRealMediaVideoAndAudioExtendPostReadyDeadline(t *testing.T) {
	startedAt := time.Unix(800, 0)
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)
	tracker.Observe(true, startedAt.Add(10*time.Second), sourceClockMetricsGateStats{
		LastVideoInputUpperBound: startedAt.Add(60 * time.Second),
		LastAudioInputUpperBound: startedAt.Add(90 * time.Second),
	})

	if got := tracker.NoSignalDecision(startedAt.Add(269 * time.Second)); got != sourceClockTimeoutDecision("none") {
		t.Fatalf("decision before latest real input deadline = %q, want %q", got, "none")
	}
	if got := tracker.NoSignalDecision(startedAt.Add(270 * time.Second)); got != sourceClockTimeoutDecision("no-signal") {
		t.Fatalf("decision at latest real input deadline = %q, want %q", got, "no-signal")
	}
}

func TestSourceClockSyntheticAndOutputProgressDoNotExtendDeadline(t *testing.T) {
	startedAt := time.Unix(900, 0)
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)
	firstVideoAt := startedAt.Add(10 * time.Second)
	lastRealVideoAt := startedAt.Add(20 * time.Second)
	lastRealAudioAt := startedAt.Add(30 * time.Second)
	tracker.Observe(true, firstVideoAt, sourceClockMetricsGateStats{
		LastVideoInputUpperBound: lastRealVideoAt,
		LastAudioInputUpperBound: lastRealAudioAt,
	})
	tracker.Observe(false, startedAt.Add(200*time.Second), sourceClockMetricsGateStats{
		LastSentProgressAt: startedAt.Add(400 * time.Second),
		LastDropObservedAt: startedAt.Add(401 * time.Second),
	})

	if got := tracker.NoSignalDecision(lastRealAudioAt.Add(180 * time.Second)); got != sourceClockTimeoutDecision("no-signal") {
		t.Fatalf("synthetic/output progress decision = %q, want %q", got, "no-signal")
	}
}

func TestSourceClockPublisherGenerationChangeDoesNotResetDeadline(t *testing.T) {
	startedAt := time.Unix(1000, 0)
	mediaReadyPath := filepath.Join(t.TempDir(), "media-ready.json")
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	if disposition, consumeErr := gate.consumeLine(metricsLineWith(1, 1, 0, 0), startedAt); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("initial metrics disposition=%v err=%v", disposition, consumeErr)
	}
	tracker := newSourceClockLifecycleTrackerAt(3*time.Minute, startedAt)
	if _, err := sourceClockLifecycleTick(tracker, gate, mediaReadyPath, "session-1", 1, startedAt.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceClockLifecycleTick(tracker, gate, mediaReadyPath, "session-1", 2, startedAt.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}

	if got := tracker.NoSignalDecision(startedAt.Add(179 * time.Second)); got != sourceClockTimeoutDecision("none") {
		t.Fatalf("publisher generation change decision before initial deadline = %q, want %q", got, "none")
	}
	if got := tracker.NoSignalDecision(startedAt.Add(180 * time.Second)); got != sourceClockTimeoutDecision("no-initial-media") {
		t.Fatalf("publisher generation change decision = %q, want %q", got, "no-initial-media")
	}

	running := uint64(0)
	writeSourceClockEventForTest(t, mediaReadyPath, airplaycontract.Event{
		Schema: 2, SessionID: "session-1", PublisherGeneration: 2,
		Event: "video-decoded", At: startedAt.Add(200 * time.Second).UTC(), VideoDecoded: true,
		RunningTimeNS: &running,
	})
	if _, err := sourceClockLifecycleTick(tracker, gate, mediaReadyPath, "session-1", 2, startedAt.Add(200*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceClockLifecycleTick(tracker, gate, mediaReadyPath, "session-1", 3, startedAt.Add(379*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := tracker.NoSignalDecision(startedAt.Add(379 * time.Second)); got != sourceClockTimeoutDecision("none") {
		t.Fatalf("post-ready decision before original deadline = %q, want %q", got, "none")
	}
	if got := tracker.NoSignalDecision(startedAt.Add(380 * time.Second)); got != sourceClockTimeoutDecision("no-signal") {
		t.Fatalf("post-ready decision at original deadline = %q, want %q", got, "no-signal")
	}
}

func TestSourceClockNoSignalExpiredCompatibilityRemainsAvailable(t *testing.T) {
	startedAt := time.Unix(1100, 0)
	tracker := newSourceClockLifecycleTracker(10 * time.Second)
	tracker.Observe(true, startedAt, sourceClockMetricsGateStats{})

	if tracker.NoSignalExpired(startedAt.Add(9 * time.Second)) {
		t.Fatal("NoSignalExpired expired before the existing post-ready deadline")
	}
	if !tracker.NoSignalExpired(startedAt.Add(10 * time.Second)) {
		t.Fatal("NoSignalExpired did not expire at the existing post-ready deadline")
	}
}

func TestSourceClockDisabledTimeoutNoSignalDecisionIsAlwaysNone(t *testing.T) {
	startedAt := time.Unix(1200, 0)
	for _, timeout := range []time.Duration{0, -time.Second} {
		tracker := newSourceClockLifecycleTrackerAt(timeout, startedAt)
		if got := tracker.NoSignalDecision(startedAt.Add(24 * time.Hour)); got != sourceClockTimeoutDecision("none") {
			t.Fatalf("timeout %s before media decision = %q, want %q", timeout, got, "none")
		}
		tracker.Observe(true, startedAt, sourceClockMetricsGateStats{})
		if got := tracker.NoSignalDecision(startedAt.Add(24 * time.Hour)); got != sourceClockTimeoutDecision("none") {
			t.Fatalf("timeout %s after media decision = %q, want %q", timeout, got, "none")
		}
	}
}
