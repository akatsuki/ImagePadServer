package iphonemodel

import (
	"testing"
	"time"
)

func TestBuiltInScenarioKeepsLegacyNames(t *testing.T) {
	names := []string{
		"steady-60", "video-gap-500ms", "both-gap-5s", "video-burst",
		"rotation-format-change", "session-reset", "audio-late-drop",
	}
	for _, name := range names {
		got, err := BuiltInScenario(name, 8*time.Second, 1)
		if err != nil || got.Name != name {
			t.Fatalf("%s: %#v %v", name, got, err)
		}
	}
}

func TestBuiltInVideoOnlyIsFiniteAndHasNoAudioEvents(t *testing.T) {
	scenario, err := BuiltInScenario("video-only", 24*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.Duration != 24*time.Second {
		t.Fatalf("duration=%s", scenario.Duration)
	}
	for _, event := range scenario.Events {
		if event.Stream == StreamAudio {
			t.Fatalf("video-only has audio event %#v", event)
		}
	}
}

func TestVideoOnlyForeverIsFiniteAlias(t *testing.T) {
	scenario, err := BuiltInScenario("video-only-forever", 2*time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.Duration != 2*time.Second {
		t.Fatalf("duration=%s", scenario.Duration)
	}
}

func TestBuiltInScenarioCoversIPhoneTransitions(t *testing.T) {
	names := []string{
		"connect-setup-repeat", "portrait-home", "youtube-home-short-home",
		"portrait-video-to-landscape", "landscape-to-pinp-home", "app-switch-gap",
		"audio-start-after-silence",
		"publisher-restart-once",
		"no-signal-after-media",
		"audio-format-change",
		"burst-no-catchup", "audio-clock-jump", "reconnect-same-generation",
		"reconnect-new-generation", "oversize-and-truncated", "soak-30m",
	}
	for _, name := range names {
		got, err := BuiltInScenario(name, 8*time.Second, 23)
		if err != nil || got.Name != name || got.Seed != 23 {
			t.Fatalf("%s: %#v %v", name, got, err)
		}
	}
}

func TestBuiltInAudioFormatChangeIsSingleSameSessionEvent(t *testing.T) {
	scenario, err := BuiltInScenario("audio-format-change", 5*time.Second, 23)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenario.Events) != 1 {
		t.Fatalf("events=%#v", scenario.Events)
	}
	event := scenario.Events[0]
	if event.At != 2*time.Second || event.Stream != StreamAudio || event.Kind != EventAudioFormatChange || event.Generation != 1 {
		t.Fatalf("event=%#v", event)
	}
}

func TestAudioStartAfterSilenceBeginsAudioAtEighteenSeconds(t *testing.T) {
	scenario, err := BuiltInScenario("audio-start-after-silence", 24*time.Second, 23)
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{At: 0, Stream: StreamAudio, Kind: EventGapStart, Duration: 18 * time.Second, Generation: 1},
		{At: 18 * time.Second, Stream: StreamAudio, Kind: EventGapEnd, Generation: 1},
	}
	if len(scenario.Events) != len(want) {
		t.Fatalf("events=%#v want=%#v", scenario.Events, want)
	}
	for index := range want {
		if scenario.Events[index] != want[index] {
			t.Fatalf("event[%d]=%#v want=%#v", index, scenario.Events[index], want[index])
		}
	}
}

func TestBuiltInScenarioKeepsLegacyFaultTiming(t *testing.T) {
	tests := []struct {
		name       string
		kind       EventKind
		stream     Stream
		at         time.Duration
		duration   time.Duration
		ntpDelta   time.Duration
		generation uint32
	}{
		{"video-gap-500ms", EventGapStart, StreamVideo, time.Second, 500 * time.Millisecond, 0, 1},
		{"both-gap-5s", EventGapStart, StreamAudio, time.Second, 5 * time.Second, 0, 1},
		{"rotation-format-change", EventRotation, StreamVideo, time.Second, 0, 0, 1},
		{"session-reset", EventSessionStart, StreamControl, 2 * time.Second, 0, 0, 2},
		{"audio-late-drop", EventNTPJump, StreamAudio, 2 * time.Second, 0, -3 * time.Second, 1},
	}
	for _, tt := range tests {
		scenario, err := BuiltInScenario(tt.name, 10*time.Second, 1)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range scenario.Events {
			if event.Kind == tt.kind && event.Stream == tt.stream && event.At == tt.at &&
				event.Duration == tt.duration && event.NTPDelta == tt.ntpDelta && event.Generation == tt.generation {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s missing event %#v in %#v", tt.name, tt, scenario.Events)
		}
	}
}

func TestValidateScenarioRejectsInvalidEvents(t *testing.T) {
	base := Scenario{Name: "test", Seed: 1, Duration: time.Second, Events: []Event{{
		At: 0, Stream: StreamVideo, Kind: EventRotation, Generation: 1,
	}}}
	tests := []struct {
		name string
		edit func(*Scenario)
	}{
		{"non-positive duration", func(s *Scenario) { s.Duration = 0 }},
		{"negative time", func(s *Scenario) { s.Events[0].At = -time.Nanosecond }},
		{"outside duration", func(s *Scenario) { s.Events[0].At = 2 * time.Second }},
		{"unknown stream", func(s *Scenario) { s.Events[0].Stream = "bogus" }},
		{"unknown kind", func(s *Scenario) { s.Events[0].Kind = "bogus" }},
		{"zero generation", func(s *Scenario) { s.Events[0].Generation = 0 }},
		{"negative fragment", func(s *Scenario) { s.Events[0].FragmentBytes = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := base
			got.Events = append([]Event(nil), base.Events...)
			tt.edit(&got)
			if err := ValidateScenario(got); err == nil {
				t.Fatalf("invalid scenario was accepted: %#v", got)
			}
		})
	}
}

func TestValidateScenarioRestrictsAudioFormatChange(t *testing.T) {
	base := Scenario{Name: "format-change", Seed: 1, Duration: time.Second, Events: []Event{{
		At: 500 * time.Millisecond, Stream: StreamAudio, Kind: EventAudioFormatChange, Generation: 1,
	}}}
	tests := []struct {
		name string
		edit func(*Scenario)
	}{
		{"wrong stream", func(s *Scenario) { s.Events[0].Stream = StreamVideo }},
		{"wrong generation", func(s *Scenario) { s.Events[0].Generation = 2 }},
		{"at start", func(s *Scenario) { s.Events[0].At = 0 }},
		{"at duration", func(s *Scenario) { s.Events[0].At = s.Duration }},
		{"duplicate", func(s *Scenario) { s.Events = append(s.Events, s.Events[0]) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := base
			got.Events = append([]Event(nil), base.Events...)
			tt.edit(&got)
			if err := ValidateScenario(got); err == nil {
				t.Fatalf("invalid audio format change was accepted: %#v", got)
			}
		})
	}
}
