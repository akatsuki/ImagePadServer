package obsrtmp

import (
	"context"
	"errors"
	"math"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

const (
	recordingReasonVerified                          = "verified"
	recordingReasonNoRealVideo                       = "no-real-video"
	recordingReasonCloseNotConfirmed                 = "recording-close-not-confirmed"
	recordingReasonProbeTimeout                      = "probe-timeout"
	recordingReasonProbeFailed                       = "probe-failed"
	recordingReasonDurationInvalid                   = "duration-invalid"
	recordingReasonVideoMissing                      = "video-stream-missing"
	recordingReasonResolutionMismatch                = "resolution-mismatch"
	recordingReasonExpectedResolutionInvalid         = "expected-resolution-invalid"
	recordingNanosecondsPerSecond            float64 = 1_000_000_000
)

func classifyDirectRecordingOutcome(artifacts airplaycontract.PublisherArtifacts, finalized airplaycontract.Event, hasRealVideo bool, expectedWidth, expectedHeight int, probe video.MediaProbe, probeErr error) airplaycontract.RecordingOutcome {
	outcome := airplaycontract.RecordingOutcome{
		Artifacts:    artifacts,
		Closed:       finalized.RecordingFinalizedFor(artifacts.SessionID, artifacts.Generation, artifacts.Recording),
		HasRealVideo: hasRealVideo,
	}
	if !hasRealVideo {
		outcome.Reason = recordingReasonNoRealVideo
		return outcome
	}
	if !outcome.Closed {
		outcome.Reason = recordingReasonCloseNotConfirmed
		return outcome
	}
	if errors.Is(probeErr, context.DeadlineExceeded) {
		outcome.Reason = recordingReasonProbeTimeout
		return outcome
	}
	if probeErr != nil {
		outcome.Reason = recordingReasonProbeFailed
		return outcome
	}
	maxDurationSeconds := float64(^uint64(0)) / recordingNanosecondsPerSecond
	if math.IsNaN(probe.Duration) || math.IsInf(probe.Duration, 0) || probe.Duration <= 0 || probe.Duration >= maxDurationSeconds {
		outcome.Reason = recordingReasonDurationInvalid
		return outcome
	}
	outcome.DurationNS = uint64(math.Round(probe.Duration * recordingNanosecondsPerSecond))
	if outcome.DurationNS == 0 {
		outcome.Reason = recordingReasonDurationInvalid
		return outcome
	}
	if expectedWidth <= 0 || expectedHeight <= 0 {
		outcome.Reason = recordingReasonExpectedResolutionInvalid
		return outcome
	}
	hasVideo := false
	hasExpectedResolution := false
	for _, stream := range probe.Streams {
		if stream.CodecType != "video" || stream.AttachedPic {
			continue
		}
		hasVideo = true
		if stream.Width == expectedWidth && stream.Height == expectedHeight {
			hasExpectedResolution = true
		}
	}
	if !hasVideo {
		outcome.Reason = recordingReasonVideoMissing
		return outcome
	}
	if !hasExpectedResolution {
		outcome.Reason = recordingReasonResolutionMismatch
		return outcome
	}
	outcome.ProbeOK = true
	outcome.Reason = recordingReasonVerified
	return outcome
}
