package airplay

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"imagepadserver/internal/airplaycontract"
)

const sourceClockPublisherEventMaxBytes = 1024 * 1024

var errSourceClockPostWatermarkEvidence = airplaycontract.ErrSourceClockPostWatermarkEvidence

type sourceClockPostWatermarkProof = airplaycontract.SourceClockPostWatermarkProof

func sourceClockVideoWatermarkFinalEvent(events []airplaycontract.Event, sessionID string, publisherGeneration uint64) (airplaycontract.Event, error) {
	return airplaycontract.SourceClockVideoWatermarkFinalEvent(events, sessionID, publisherGeneration)
}

func sourceClockVideoWatermarkFinal(events []airplaycontract.Event, sessionID string, publisherGeneration uint64) (uint64, error) {
	event, err := sourceClockVideoWatermarkFinalEvent(events, sessionID, publisherGeneration)
	if err != nil {
		return 0, err
	}
	return *event.SourceVideoSequence, nil
}

func newSourceClockPostWatermarkProof(oldEvents, candidateEvents []airplaycontract.Event, sessionID string, oldPublisherGeneration, candidatePublisherGeneration uint64) (sourceClockPostWatermarkProof, error) {
	return airplaycontract.NewSourceClockPostWatermarkProof(oldEvents, candidateEvents, sessionID, oldPublisherGeneration, candidatePublisherGeneration)
}

// sourceClockPublisherReadinessComplete is a generation-scoped preflight
// check. It is deliberately stricter than the session-level media-ready
// observation, but it is not sufficient for active promotion: the legacy
// native event sequence does not provide the post-watermark proof above or
// prove that the encoded buffer reached RTSP/MediaMTX. The planned executor
// therefore uses this only to reject obviously incomplete candidates and
// still fails closed before active promotion.
func sourceClockPublisherReadinessComplete(events []airplaycontract.Event, sessionID string, generation uint64, startedAt time.Time) bool {
	if sessionID == "" || generation == 0 || startedAt.IsZero() {
		return false
	}
	stage := 0
	for _, event := range events {
		if event.Schema != 2 || event.SessionID != sessionID || event.PublisherGeneration != generation || event.At.IsZero() {
			return false
		}
		switch event.Event {
		case "publisher-ready":
			if stage == 0 && event.ReadyFor(sessionID, generation) {
				stage = 1
			}
		case "video-input-idr":
			if stage == 1 && !event.At.Before(startedAt) && event.RunningTimeNS != nil && *event.RunningTimeNS > 0 {
				stage = 2
			}
		case "video-decoded":
			if stage == 2 && !event.At.Before(startedAt) && event.MediaReadyFor(sessionID, generation) && event.RunningTimeNS != nil && *event.RunningTimeNS > 0 {
				stage = 3
			}
		case "video-encoded":
			if stage == 3 && !event.At.Before(startedAt) && event.RunningTimeNS != nil && *event.RunningTimeNS > 0 {
				stage = 4
			}
		}
	}
	return stage == 4
}

func waitSourceClockPublisherReadiness(ctx context.Context, path, sessionID string, generation uint64, startedAt time.Time, timeout time.Duration) error {
	err, _ := waitSourceClockPublisherReadinessWithDone(ctx, path, sessionID, generation, startedAt, nil, timeout)
	return err
}

// waitSourceClockPublisherReadinessWithDone has one consumer for the
// candidate process result. A candidate that exits while its evidence is
// incomplete fails immediately; the caller must not wait until the readiness
// deadline and must not read the same done channel a second time.
func waitSourceClockPublisherReadinessWithDone(ctx context.Context, path, sessionID string, generation uint64, startedAt time.Time, publisherDone <-chan error, timeout time.Duration) (error, bool) {
	if strings.TrimSpace(path) == "" {
		return errors.New("source-clock publisher event log path is empty"), false
	}
	if timeout <= 0 {
		return errors.New("source-clock publisher readiness timeout is invalid"), false
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := readSourceClockPublisherEvents(path)
		if err == nil && sourceClockPublisherReadinessComplete(events, sessionID, generation, startedAt) {
			return nil, false
		}
		select {
		case <-ctx.Done():
			return ctx.Err(), false
		case waitErr := <-publisherDone:
			return &sourceClockPublisherExitedBeforeReadyError{waitErr: waitErr}, true
		case <-deadline.C:
			if err != nil {
				return fmt.Errorf("source-clock publisher readiness timeout: %w", err), false
			}
			return errors.New("source-clock publisher readiness timeout"), false
		case <-ticker.C:
		}
	}
}

// readSourceClockPublisherEvents reads only the valid prefix of one
// generation-scoped JSONL file. A malformed line stops the read so an event
// written after corruption can never be treated as authoritative.
func readSourceClockPublisherEvents(path string) ([]airplaycontract.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), sourceClockPublisherEventMaxBytes)
	events := make([]airplaycontract.Event, 0, 4)
	line := 0
	for scanner.Scan() {
		line++
		var event airplaycontract.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return events, fmt.Errorf("parse source-clock publisher event line %d: %w", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("read source-clock publisher event log after line %d: %w", line, err)
	}
	return events, nil
}

// observeSourceClockPublisherEventLog delivers events only after the process
// that owned the generation has exited. Missing logs are valid for failures
// that happen before the native publisher creates its event stream.
func observeSourceClockPublisherEventLog(observer airplaycontract.PublisherObserver, artifacts airplaycontract.PublisherArtifacts) error {
	if observer == nil {
		return nil
	}
	events, err := readSourceClockPublisherEvents(artifacts.EventLog)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	for index, event := range events {
		if event.Schema != 2 || event.SessionID != artifacts.SessionID || event.PublisherGeneration != artifacts.Generation || event.At.IsZero() {
			return fmt.Errorf("source-clock publisher event identity mismatch at event %d", index+1)
		}
		observer.ObservePublisher(event)
	}
	return err
}

func observeSourceClockPublisherEventLogAfterExit(observer airplaycontract.PublisherObserver, artifacts airplaycontract.PublisherArtifacts, waitErr error) error {
	if observer == nil {
		return nil
	}
	if errors.Is(waitErr, errDirectGStreamerProcessExitUnconfirmed) {
		return waitErr
	}
	return observeSourceClockPublisherEventLog(observer, artifacts)
}

func completeSourceClockPublisher(observer airplaycontract.PublisherObserver, artifacts airplaycontract.PublisherArtifacts, started bool, waitErr error) error {
	return completeSourceClockPublisherProcess(observer, artifacts, started, 0, time.Time{}, waitErr)
}

func completeSourceClockPublisherProcess(observer airplaycontract.PublisherObserver, artifacts airplaycontract.PublisherArtifacts, started bool, processID int, completedAt time.Time, waitErr error) error {
	if observer == nil {
		return nil
	}
	completion := airplaycontract.PublisherCompletion{Artifacts: artifacts, Started: started}
	if !started {
		observer.CompletePublisher(completion)
		return nil
	}
	if errors.Is(waitErr, errDirectGStreamerProcessExitUnconfirmed) {
		observer.CompletePublisher(completion)
		return waitErr
	}
	completion.ExitConfirmed = true
	if processID > 0 && !completedAt.IsZero() {
		exit := sourceClockProcessExitFromError(waitErr)
		completion.ProcessID = processID
		completion.CompletedAt = completedAt.UTC()
		completion.ExitCode = exit.Code
		completion.ExitCodeKnown = exit.Known
	}
	eventErr := observeSourceClockPublisherEventLog(observer, artifacts)
	if eventErr != nil {
		completion.EventLogError = eventErr.Error()
	}
	observer.CompletePublisher(completion)
	return eventErr
}
