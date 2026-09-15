package airplaycontract

import "errors"

var ErrSourceClockPostWatermarkEvidence = errors.New("invalid source-clock post-watermark evidence")

// SourceClockPostWatermarkProof is the immutable evidence that one candidate
// publisher emitted the first three video stages after the prior source
// watermark. Time fields are intentionally absent: source sequence is the
// proof of ordering and freshness.
type SourceClockPostWatermarkProof struct {
	SessionID               string
	PublisherGeneration     uint64
	SourceSessionGeneration uint64
	WatermarkSequence       uint64
	Sequence                uint64
}

func sourceClockEventMatchesExactIdentity(event Event, sessionID string, publisherGeneration uint64) bool {
	return sessionID != "" && publisherGeneration != 0 &&
		event.Schema == 2 && event.SessionID == sessionID &&
		event.PublisherGeneration == publisherGeneration && !event.At.IsZero()
}

func SourceClockVideoWatermarkFinalEvent(events []Event, sessionID string, publisherGeneration uint64) (Event, error) {
	if sessionID == "" || publisherGeneration == 0 {
		return Event{}, ErrSourceClockPostWatermarkEvidence
	}
	var watermark Event
	count := 0
	for _, event := range events {
		if !sourceClockEventMatchesExactIdentity(event, sessionID, publisherGeneration) {
			return Event{}, ErrSourceClockPostWatermarkEvidence
		}
		if event.Event != "video-watermark-final" {
			continue
		}
		count++
		if count != 1 || event.SourceSessionGeneration == 0 || event.SourceVideoSequence == nil || *event.SourceVideoSequence == 0 {
			return Event{}, ErrSourceClockPostWatermarkEvidence
		}
		watermark = event
	}
	if count != 1 {
		return Event{}, ErrSourceClockPostWatermarkEvidence
	}
	return watermark, nil
}

// NewSourceClockPostWatermarkProof accepts only a complete, exact-identity
// candidate sequence. It deliberately does not inspect At or RunningTimeNS.
func NewSourceClockPostWatermarkProof(oldEvents, candidateEvents []Event, sessionID string, oldPublisherGeneration, candidatePublisherGeneration uint64) (SourceClockPostWatermarkProof, error) {
	if oldPublisherGeneration == 0 || candidatePublisherGeneration == 0 || candidatePublisherGeneration <= oldPublisherGeneration {
		return SourceClockPostWatermarkProof{}, ErrSourceClockPostWatermarkEvidence
	}
	watermark, err := SourceClockVideoWatermarkFinalEvent(oldEvents, sessionID, oldPublisherGeneration)
	if err != nil {
		return SourceClockPostWatermarkProof{}, err
	}
	if len(candidateEvents) == 0 {
		return SourceClockPostWatermarkProof{}, ErrSourceClockPostWatermarkEvidence
	}

	const (
		stageReady = iota + 1
		stageInputIDR
		stageDecoded
		stageEncodedIDR
	)
	stage := stageReady
	seen := make(map[string]bool, 4)
	var sourceSessionGeneration uint64
	var sequence uint64
	for _, event := range candidateEvents {
		if !sourceClockEventMatchesExactIdentity(event, sessionID, candidatePublisherGeneration) {
			return SourceClockPostWatermarkProof{}, ErrSourceClockPostWatermarkEvidence
		}
		var requiredStage int
		switch event.Event {
		case "publisher-ready":
			if seen[event.Event] || stage != stageReady || !event.ReadyFor(sessionID, candidatePublisherGeneration) {
				return SourceClockPostWatermarkProof{}, ErrSourceClockPostWatermarkEvidence
			}
			seen[event.Event] = true
			stage = stageInputIDR
			continue
		case "video-input-idr":
			requiredStage = stageInputIDR
		case "video-decoded":
			requiredStage = stageDecoded
		case "video-encoded-idr":
			requiredStage = stageEncodedIDR
		default:
			continue
		}
		if seen[event.Event] || stage != requiredStage || event.SourceSessionGeneration == 0 || event.SourceVideoSequence == nil || *event.SourceVideoSequence == 0 {
			return SourceClockPostWatermarkProof{}, ErrSourceClockPostWatermarkEvidence
		}
		if sourceSessionGeneration == 0 {
			sourceSessionGeneration = event.SourceSessionGeneration
			sequence = *event.SourceVideoSequence
		} else if sourceSessionGeneration != event.SourceSessionGeneration || sequence != *event.SourceVideoSequence {
			return SourceClockPostWatermarkProof{}, ErrSourceClockPostWatermarkEvidence
		}
		if sourceSessionGeneration != watermark.SourceSessionGeneration || sequence <= *watermark.SourceVideoSequence {
			return SourceClockPostWatermarkProof{}, ErrSourceClockPostWatermarkEvidence
		}
		seen[event.Event] = true
		stage++
	}
	if stage != stageEncodedIDR+1 || !seen["publisher-ready"] || !seen["video-input-idr"] || !seen["video-decoded"] || !seen["video-encoded-idr"] {
		return SourceClockPostWatermarkProof{}, ErrSourceClockPostWatermarkEvidence
	}
	return SourceClockPostWatermarkProof{
		SessionID:               sessionID,
		PublisherGeneration:     candidatePublisherGeneration,
		SourceSessionGeneration: sourceSessionGeneration,
		WatermarkSequence:       *watermark.SourceVideoSequence,
		Sequence:                sequence,
	}, nil
}
