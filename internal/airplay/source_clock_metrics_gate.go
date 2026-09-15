package airplay

import (
	"errors"
	"sync"
	"time"
)

type sourceClockMetricsDisposition uint8

const (
	sourceClockMetricsIgnoredLog sourceClockMetricsDisposition = iota
	sourceClockMetricsAccepted
	sourceClockMetricsParseFailed
	sourceClockMetricsReceiverMismatch
	sourceClockMetricsStaleSequence
	sourceClockMetricsCounterRegression
)

type sourceClockMetricsGateStats struct {
	HasMetrics               bool
	Latest                   ReceiverMetrics
	LastAcceptedAt           time.Time
	LastVideoInputUpperBound time.Time
	LastAudioInputUpperBound time.Time
	LastSentProgressAt       time.Time
	LastDropObservedAt       time.Time
	SequenceGapCount         uint64
	AgeConversionFailures    uint64
	ParseFailures            uint64
	ReceiverMismatches       uint64
	StaleSequences           uint64
	CounterRegressions       uint64
}

type sourceClockMetricsGate struct {
	mu                       sync.Mutex
	receiverID               string
	hasMetrics               bool
	latest                   ReceiverMetrics
	lastAcceptedAt           time.Time
	lastVideoInputUpperBound time.Time
	lastAudioInputUpperBound time.Time
	lastSentProgressAt       time.Time
	lastDropObservedAt       time.Time
	sequenceGapCount         uint64
	ageConversionFailures    uint64
	parseFailures            uint64
	receiverMismatches       uint64
	staleSequences           uint64
	counterRegressions       uint64
}

func newSourceClockMetricsGate(receiverID string) (*sourceClockMetricsGate, error) {
	if !validSourceClockReceiverID(receiverID) {
		return nil, errors.New("source-clock metrics gate receiverId is invalid")
	}
	return &sourceClockMetricsGate{receiverID: receiverID}, nil
}

func (g *sourceClockMetricsGate) consumeLine(line string, receivedAt time.Time) (sourceClockMetricsDisposition, error) {
	metrics, matched, err := parseSourceClockMetricsLine(line)
	if !matched {
		return sourceClockMetricsIgnoredLog, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.parseFailures++
		return sourceClockMetricsParseFailed, err
	}
	if metrics.ReceiverID != g.receiverID {
		g.receiverMismatches++
		return sourceClockMetricsReceiverMismatch, nil
	}
	if g.hasMetrics && metrics.Sequence <= g.latest.Sequence {
		g.staleSequences++
		return sourceClockMetricsStaleSequence, nil
	}
	if g.hasMetrics && (metrics.VideoFrames < g.latest.VideoFrames ||
		metrics.AudioFrames < g.latest.AudioFrames ||
		metrics.SentFrames < g.latest.SentFrames ||
		metrics.DroppedFrames < g.latest.DroppedFrames ||
		(g.latest.HasFailureEvents && (!metrics.HasFailureEvents || metrics.FailureEvents < g.latest.FailureEvents))) {
		g.counterRegressions++
		return sourceClockMetricsCounterRegression, nil
	}
	g.updateAcceptedActivity(metrics, receivedAt)
	g.hasMetrics = true
	g.latest = cloneReceiverMetrics(metrics)
	g.lastAcceptedAt = receivedAt
	return sourceClockMetricsAccepted, nil
}

func (g *sourceClockMetricsGate) updateAcceptedActivity(metrics ReceiverMetrics, receivedAt time.Time) {
	videoAdvanced := metrics.VideoFrames > 0
	audioAdvanced := metrics.AudioFrames > 0
	if g.hasMetrics {
		if metrics.Sequence > g.latest.Sequence {
			missing := metrics.Sequence - g.latest.Sequence - 1
			g.sequenceGapCount = saturatingUint64Add(g.sequenceGapCount, missing)
		}
		videoAdvanced = metrics.VideoFrames > g.latest.VideoFrames
		audioAdvanced = metrics.AudioFrames > g.latest.AudioFrames
		if metrics.SentFrames > g.latest.SentFrames {
			g.lastSentProgressAt = receivedAt
		}
		if metrics.DroppedFrames > g.latest.DroppedFrames {
			g.lastDropObservedAt = receivedAt
		}
	}
	if videoAdvanced && metrics.VideoAgeMS != nil {
		g.updateInputUpperBound(&g.lastVideoInputUpperBound, receivedAt, *metrics.VideoAgeMS)
	}
	if audioAdvanced && metrics.AudioAgeMS != nil {
		g.updateInputUpperBound(&g.lastAudioInputUpperBound, receivedAt, *metrics.AudioAgeMS)
	}
}

func (g *sourceClockMetricsGate) updateInputUpperBound(current *time.Time, receivedAt time.Time, ageMS uint64) {
	age, ok := sourceClockAgeDuration(ageMS)
	if !ok {
		g.ageConversionFailures = saturatingUint64Add(g.ageConversionFailures, 1)
		return
	}
	candidate := receivedAt.Add(-age)
	if current.IsZero() || candidate.After(*current) {
		*current = candidate
	}
}

func sourceClockAgeDuration(ageMS uint64) (time.Duration, bool) {
	const maxAgeMS = uint64(1<<63-1) / uint64(time.Millisecond)
	if ageMS > maxAgeMS {
		return 0, false
	}
	return time.Duration(ageMS) * time.Millisecond, true
}

func saturatingUint64Add(current, increment uint64) uint64 {
	if ^uint64(0)-current < increment {
		return ^uint64(0)
	}
	return current + increment
}

func (g *sourceClockMetricsGate) snapshot() sourceClockMetricsGateStats {
	g.mu.Lock()
	defer g.mu.Unlock()
	return sourceClockMetricsGateStats{
		HasMetrics:               g.hasMetrics,
		Latest:                   cloneReceiverMetrics(g.latest),
		LastAcceptedAt:           g.lastAcceptedAt,
		LastVideoInputUpperBound: g.lastVideoInputUpperBound,
		LastAudioInputUpperBound: g.lastAudioInputUpperBound,
		LastSentProgressAt:       g.lastSentProgressAt,
		LastDropObservedAt:       g.lastDropObservedAt,
		SequenceGapCount:         g.sequenceGapCount,
		AgeConversionFailures:    g.ageConversionFailures,
		ParseFailures:            g.parseFailures,
		ReceiverMismatches:       g.receiverMismatches,
		StaleSequences:           g.staleSequences,
		CounterRegressions:       g.counterRegressions,
	}
}

func cloneReceiverMetrics(metrics ReceiverMetrics) ReceiverMetrics {
	clone := metrics
	if metrics.VideoAgeMS != nil {
		value := *metrics.VideoAgeMS
		clone.VideoAgeMS = &value
	}
	if metrics.AudioAgeMS != nil {
		value := *metrics.AudioAgeMS
		clone.AudioAgeMS = &value
	}
	return clone
}
