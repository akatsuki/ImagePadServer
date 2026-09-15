package airplay

import (
	"encoding/json"
	"fmt"
	"os"

	"imagepadserver/internal/airplaycontract"
)

func readSourceClockMediaReady(path, sessionID string, publisherGeneration uint64) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read source-clock media-ready file: %w", err)
	}
	var event airplaycontract.Event
	if err := json.Unmarshal(data, &event); err != nil {
		return false, fmt.Errorf("parse source-clock media-ready file: %w", err)
	}
	return event.MediaReadyFor(sessionID, publisherGeneration), nil
}

// Forward only the two atomic, identity-checked readiness snapshots while a
// publisher is alive. The append-only event log is still consumed after Wait;
// reading that log early could mistake a partial tail for final evidence.
func observeSourceClockPublisherReadiness(observer airplaycontract.PublisherObserver, readyPath, mediaPath, sessionID string, generation uint64) (bool, error) {
	if observer == nil {
		return false, nil
	}
	var events [2]airplaycontract.Event
	for index, path := range []string{readyPath, mediaPath} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read publisher readiness snapshot: %w", err)
		}
		if err := json.Unmarshal(data, &events[index]); err != nil {
			return false, fmt.Errorf("parse publisher readiness snapshot: %w", err)
		}
	}
	if !events[0].ReadyFor(sessionID, generation) || !events[1].MediaReadyFor(sessionID, generation) {
		return false, nil
	}
	observer.ObservePublisher(events[0])
	observer.ObservePublisher(events[1])
	return true, nil
}

func (m *Manager) observeSourceClockGenerationMediaReady(path, sessionID string, publisherGeneration uint64) (bool, error) {
	ready, err := readSourceClockMediaReady(path, sessionID, publisherGeneration)
	if err != nil || !ready {
		return ready, err
	}
	m.setSourceClockMediaReady()
	return true, nil
}

func (m *Manager) observeSourceClockGenerationMediaReadyIfRunning(publisherRunning bool, path, sessionID string, publisherGeneration uint64) (bool, error) {
	if !publisherRunning {
		return false, nil
	}
	return m.observeSourceClockGenerationMediaReady(path, sessionID, publisherGeneration)
}
