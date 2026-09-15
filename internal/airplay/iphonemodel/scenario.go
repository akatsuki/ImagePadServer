package iphonemodel

import (
	"fmt"
	"sort"
	"time"
)

type Stream string

const (
	StreamControl Stream = "control"
	StreamVideo   Stream = "video"
	StreamAudio   Stream = "audio"
)

type EventKind string

const (
	EventGapStart          EventKind = "gap-start"
	EventGapEnd            EventKind = "gap-end"
	EventRotation          EventKind = "rotation"
	EventSessionStart      EventKind = "session-start"
	EventSessionEnd        EventKind = "session-end"
	EventNTPJump           EventKind = "ntp-jump"
	EventBurstStart        EventKind = "burst-start"
	EventBurstEnd          EventKind = "burst-end"
	EventDisconnect        EventKind = "disconnect"
	EventReconnect         EventKind = "reconnect"
	EventFragmentSize      EventKind = "fragment-size"
	EventTruncateNextFrame EventKind = "truncate-next-frame"
	EventOversizeNextFrame EventKind = "oversize-next-frame"
	EventAudioFormatChange EventKind = "audio-format-change"
)

type Event struct {
	At            time.Duration `json:"at"`
	Stream        Stream        `json:"stream"`
	Kind          EventKind     `json:"kind"`
	Duration      time.Duration `json:"duration,omitempty"`
	NTPDelta      time.Duration `json:"ntpDelta,omitempty"`
	FragmentBytes int           `json:"fragmentBytes,omitempty"`
	Generation    uint32        `json:"generation"`
}

type Scenario struct {
	Name     string        `json:"name"`
	Seed     uint64        `json:"seed"`
	Duration time.Duration `json:"duration"`
	Events   []Event       `json:"events"`
}

var builtInScenarioNames = []string{
	"steady-60", "video-gap-500ms", "both-gap-5s", "video-burst",
	"rotation-format-change", "session-reset", "audio-late-drop",
	"video-only", "video-only-forever",
	"connect-setup-repeat", "portrait-home", "youtube-home-short-home",
	"portrait-video-to-landscape", "landscape-to-pinp-home", "app-switch-gap",
	"audio-start-after-silence", "publisher-restart-once", "no-signal-after-media", "no-initial-media",
	"audio-format-change",
	"burst-no-catchup", "audio-clock-jump", "reconnect-same-generation",
	"reconnect-new-generation", "oversize-and-truncated", "soak-30m",
}

func BuiltInScenario(name string, duration time.Duration, seed uint64) (Scenario, error) {
	scenario := Scenario{Name: name, Seed: seed, Duration: duration, Events: []Event{}}
	add := func(at time.Duration, stream Stream, kind EventKind, generation uint32) {
		scenario.Events = append(scenario.Events, Event{At: at, Stream: stream, Kind: kind, Generation: generation})
	}
	window := func(at, length time.Duration, stream Stream, start, end EventKind) {
		scenario.Events = append(scenario.Events, Event{
			At: at, Stream: stream, Kind: start, Duration: length, Generation: 1,
		})
		if at+length <= duration {
			add(at+length, stream, end, 1)
		}
	}

	switch name {
	case "steady-60":
	case "video-only", "video-only-forever":
	case "video-gap-500ms":
		window(time.Second, 500*time.Millisecond, StreamVideo, EventGapStart, EventGapEnd)
	case "both-gap-5s":
		window(time.Second, 5*time.Second, StreamVideo, EventGapStart, EventGapEnd)
		window(time.Second, 5*time.Second, StreamAudio, EventGapStart, EventGapEnd)
	case "video-burst":
		window(time.Second, 500*time.Millisecond, StreamVideo, EventBurstStart, EventBurstEnd)
	case "rotation-format-change":
		add(time.Second, StreamVideo, EventRotation, 1)
	case "session-reset":
		add(2*time.Second, StreamControl, EventSessionEnd, 1)
		add(2*time.Second, StreamControl, EventSessionStart, 2)
	case "audio-late-drop":
		scenario.Events = append(scenario.Events, Event{At: 2 * time.Second, Stream: StreamAudio, Kind: EventNTPJump, NTPDelta: -3 * time.Second, Generation: 1})
	case "connect-setup-repeat", "portrait-home":
		add(0, StreamControl, EventSessionStart, 1)
	case "youtube-home-short-home":
		window(time.Second, 350*time.Millisecond, StreamVideo, EventBurstStart, EventBurstEnd)
		window(2*time.Second, 250*time.Millisecond, StreamVideo, EventGapStart, EventGapEnd)
	case "portrait-video-to-landscape":
		window(900*time.Millisecond, 250*time.Millisecond, StreamVideo, EventGapStart, EventGapEnd)
		add(time.Second, StreamVideo, EventRotation, 1)
	case "landscape-to-pinp-home":
		add(250*time.Millisecond, StreamVideo, EventRotation, 1)
		window(2*time.Second, 500*time.Millisecond, StreamVideo, EventGapStart, EventGapEnd)
		add(2*time.Second, StreamVideo, EventRotation, 1)
	case "app-switch-gap":
		window(time.Second, 350*time.Millisecond, StreamVideo, EventGapStart, EventGapEnd)
		window(1100*time.Millisecond, 150*time.Millisecond, StreamAudio, EventGapStart, EventGapEnd)
	case "audio-start-after-silence":
		window(0, 18*time.Second, StreamAudio, EventGapStart, EventGapEnd)
	case "publisher-restart-once":
	case "no-initial-media":
		window(0, duration, StreamVideo, EventGapStart, EventGapEnd)
		window(0, duration, StreamAudio, EventGapStart, EventGapEnd)
	case "no-signal-after-media":
		window(duration/2, duration-duration/2, StreamVideo, EventGapStart, EventGapEnd)
	case "audio-format-change":
		add(2*time.Second, StreamAudio, EventAudioFormatChange, 1)
	case "burst-no-catchup":
		window(time.Second, 750*time.Millisecond, StreamVideo, EventBurstStart, EventBurstEnd)
	case "audio-clock-jump":
		scenario.Events = append(scenario.Events, Event{At: 2 * time.Second, Stream: StreamAudio, Kind: EventNTPJump, NTPDelta: -3 * time.Second, Generation: 1})
	case "reconnect-same-generation":
		add(time.Second, StreamVideo, EventDisconnect, 1)
		add(time.Second, StreamAudio, EventDisconnect, 1)
		add(1100*time.Millisecond, StreamVideo, EventReconnect, 1)
		add(1100*time.Millisecond, StreamAudio, EventReconnect, 1)
	case "reconnect-new-generation":
		add(time.Second, StreamVideo, EventDisconnect, 1)
		add(time.Second, StreamAudio, EventDisconnect, 1)
		add(1100*time.Millisecond, StreamControl, EventSessionStart, 2)
		add(1100*time.Millisecond, StreamVideo, EventReconnect, 2)
		add(1100*time.Millisecond, StreamAudio, EventReconnect, 2)
	case "oversize-and-truncated":
		add(time.Second, StreamVideo, EventOversizeNextFrame, 1)
		add(2*time.Second, StreamVideo, EventTruncateNextFrame, 1)
	case "soak-30m":
		add(0, StreamControl, EventSessionStart, 1)
	default:
		return Scenario{}, fmt.Errorf("unknown scenario %q (want one of %v)", name, builtInScenarioNames)
	}

	normalizeScenario(&scenario)
	if err := ValidateScenario(scenario); err != nil {
		return Scenario{}, fmt.Errorf("scenario %q: %w", name, err)
	}
	return scenario, nil
}

func ValidateScenario(scenario Scenario) error {
	if scenario.Name == "" {
		return fmt.Errorf("name is required")
	}
	if scenario.Duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	validStreams := map[Stream]bool{StreamControl: true, StreamVideo: true, StreamAudio: true}
	validKinds := map[EventKind]bool{
		EventGapStart: true, EventGapEnd: true, EventRotation: true,
		EventSessionStart: true, EventSessionEnd: true, EventNTPJump: true,
		EventBurstStart: true, EventBurstEnd: true, EventDisconnect: true,
		EventReconnect: true, EventFragmentSize: true, EventTruncateNextFrame: true,
		EventOversizeNextFrame: true,
		EventAudioFormatChange: true,
	}
	audioFormatChangeCount := 0
	for index, event := range scenario.Events {
		if event.At < 0 || event.At > scenario.Duration {
			return fmt.Errorf("event %d time %s is outside scenario duration %s", index, event.At, scenario.Duration)
		}
		if !validStreams[event.Stream] {
			return fmt.Errorf("event %d has unknown stream %q", index, event.Stream)
		}
		if !validKinds[event.Kind] {
			return fmt.Errorf("event %d has unknown kind %q", index, event.Kind)
		}
		if event.Generation == 0 {
			return fmt.Errorf("event %d has zero generation", index)
		}
		if event.Duration < 0 {
			return fmt.Errorf("event %d has negative duration", index)
		}
		if event.FragmentBytes < 0 {
			return fmt.Errorf("event %d has negative fragment size", index)
		}
		if event.Kind == EventAudioFormatChange {
			audioFormatChangeCount++
			if event.Stream != StreamAudio {
				return fmt.Errorf("event %d audio format change must use audio stream", index)
			}
			if event.Generation != 1 {
				return fmt.Errorf("event %d audio format change must use generation 1", index)
			}
			if event.At <= 0 || event.At >= scenario.Duration {
				return fmt.Errorf("event %d audio format change must occur strictly inside scenario duration", index)
			}
			if audioFormatChangeCount > 1 {
				return fmt.Errorf("scenario has more than one audio format change")
			}
		}
	}
	return nil
}

func normalizeScenario(scenario *Scenario) {
	sort.SliceStable(scenario.Events, func(i, j int) bool {
		left, right := scenario.Events[i], scenario.Events[j]
		if left.At != right.At {
			return left.At < right.At
		}
		if left.Stream != right.Stream {
			return left.Stream < right.Stream
		}
		return left.Kind < right.Kind
	})
}
