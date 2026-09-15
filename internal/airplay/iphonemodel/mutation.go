package iphonemodel

import (
	"fmt"
	"math/rand"
)

type Mutation struct {
	Name   string `json:"name"`
	Event  int    `json:"event"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type mutationCandidate struct {
	name  string
	event Event
}

func MutateScenario(input Scenario, seed uint64) (Scenario, Mutation, error) {
	if err := ValidateScenario(input); err != nil {
		return Scenario{}, Mutation{}, err
	}
	at := input.Duration / 2
	candidates := []mutationCandidate{
		{name: "fragment-video-one-byte", event: Event{At: at, Stream: StreamVideo, Kind: EventFragmentSize, FragmentBytes: 1, Generation: 1}},
		{name: "short-video-gap", event: Event{At: at, Stream: StreamVideo, Kind: EventGapStart, Duration: 75_000_000, Generation: 1}},
		{name: "audio-clock-backward", event: Event{At: at, Stream: StreamAudio, Kind: EventNTPJump, NTPDelta: -250_000_000, Generation: 1}},
		{name: "truncate-video-frame", event: Event{At: at, Stream: StreamVideo, Kind: EventTruncateNextFrame, Generation: 1}},
		{name: "oversize-video-frame", event: Event{At: at, Stream: StreamVideo, Kind: EventOversizeNextFrame, Generation: 1}},
	}
	random := rand.New(rand.NewSource(int64(seed)))
	selected := candidates[random.Intn(len(candidates))]

	output := input
	output.Events = append([]Event(nil), input.Events...)
	output.Events = append(output.Events, selected.event)
	normalizeScenario(&output)
	eventIndex := -1
	for index, event := range output.Events {
		if event == selected.event {
			eventIndex = index
			break
		}
	}
	if eventIndex < 0 {
		return Scenario{}, Mutation{}, fmt.Errorf("inserted mutation event was not found")
	}
	if err := ValidateScenario(output); err != nil {
		return Scenario{}, Mutation{}, err
	}
	mutation := Mutation{
		Name: selected.name, Event: eventIndex, Before: "none",
		After: fmt.Sprintf("%s/%s at %s", selected.event.Stream, selected.event.Kind, selected.event.At),
	}
	return output, mutation, nil
}
