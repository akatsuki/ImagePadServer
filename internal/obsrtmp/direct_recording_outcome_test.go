package obsrtmp

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
	"imagepadserver/internal/video"
)

func TestClassifyDirectRecordingOutcomeSeparatesFailureReasons(t *testing.T) {
	artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), "0123456789abcdef", 2)
	finalized := airplaycontract.Event{
		Schema: 2, SessionID: artifacts.SessionID, PublisherGeneration: artifacts.Generation,
		Event: "recording-finalized", At: time.Unix(100, 0).UTC(),
		RecordingPath: artifacts.Recording, RecordingClosed: true,
	}
	goodProbe := video.MediaProbe{
		Duration: 1.5,
		Streams:  []video.MediaStream{{CodecType: "video", Width: 1920, Height: 1080}},
	}

	tests := []struct {
		name         string
		hasRealVideo bool
		finalized    airplaycontract.Event
		probe        video.MediaProbe
		probeErr     error
		width        int
		height       int
		wantReason   string
		wantClosed   bool
		wantDuration uint64
	}{
		{name: "no real video", finalized: finalized, probe: goodProbe, width: 1920, height: 1080, wantReason: recordingReasonNoRealVideo, wantClosed: true},
		{name: "no real video wins over missing close and timeout", probeErr: context.DeadlineExceeded, width: 1920, height: 1080, wantReason: recordingReasonNoRealVideo},
		{name: "close not confirmed", hasRealVideo: true, probe: goodProbe, width: 1920, height: 1080, wantReason: recordingReasonCloseNotConfirmed},
		{name: "missing close wins over probe timeout", hasRealVideo: true, probeErr: context.DeadlineExceeded, width: 1920, height: 1080, wantReason: recordingReasonCloseNotConfirmed},
		{name: "wrong finalized generation", hasRealVideo: true, finalized: func() airplaycontract.Event { event := finalized; event.PublisherGeneration++; return event }(), probe: goodProbe, width: 1920, height: 1080, wantReason: recordingReasonCloseNotConfirmed},
		{name: "probe timeout", hasRealVideo: true, finalized: finalized, probeErr: context.DeadlineExceeded, width: 1920, height: 1080, wantReason: recordingReasonProbeTimeout, wantClosed: true},
		{name: "wrapped probe timeout", hasRealVideo: true, finalized: finalized, probeErr: errors.Join(errors.New("ffprobe"), context.DeadlineExceeded), width: 1920, height: 1080, wantReason: recordingReasonProbeTimeout, wantClosed: true},
		{name: "probe failure", hasRealVideo: true, finalized: finalized, probeErr: errors.New("bad mp4"), width: 1920, height: 1080, wantReason: recordingReasonProbeFailed, wantClosed: true},
		{name: "zero duration", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Streams: goodProbe.Streams}, width: 1920, height: 1080, wantReason: recordingReasonDurationInvalid, wantClosed: true},
		{name: "negative duration", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: -1, Streams: goodProbe.Streams}, width: 1920, height: 1080, wantReason: recordingReasonDurationInvalid, wantClosed: true},
		{name: "nan duration", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: math.NaN(), Streams: goodProbe.Streams}, width: 1920, height: 1080, wantReason: recordingReasonDurationInvalid, wantClosed: true},
		{name: "infinite duration", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: math.Inf(1), Streams: goodProbe.Streams}, width: 1920, height: 1080, wantReason: recordingReasonDurationInvalid, wantClosed: true},
		{name: "negative infinite duration", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: math.Inf(-1), Streams: goodProbe.Streams}, width: 1920, height: 1080, wantReason: recordingReasonDurationInvalid, wantClosed: true},
		{name: "duration rounds to zero", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: 0.4e-9, Streams: goodProbe.Streams}, width: 1920, height: 1080, wantReason: recordingReasonDurationInvalid, wantClosed: true},
		{name: "no video stream", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: 1, Streams: []video.MediaStream{{CodecType: "audio"}}}, width: 1920, height: 1080, wantReason: recordingReasonVideoMissing, wantClosed: true, wantDuration: 1_000_000_000},
		{name: "attached picture only", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: 1, Streams: []video.MediaStream{{CodecType: "video", AttachedPic: true, Width: 1920, Height: 1080}}}, width: 1920, height: 1080, wantReason: recordingReasonVideoMissing, wantClosed: true, wantDuration: 1_000_000_000},
		{name: "resolution mismatch", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: 2, Streams: []video.MediaStream{{CodecType: "video", Width: 1280, Height: 720}}}, width: 1920, height: 1080, wantReason: recordingReasonResolutionMismatch, wantClosed: true, wantDuration: 2_000_000_000},
		{name: "attached match cannot hide real mismatch", hasRealVideo: true, finalized: finalized, probe: video.MediaProbe{Duration: 2, Streams: []video.MediaStream{{CodecType: "video", AttachedPic: true, Width: 1920, Height: 1080}, {CodecType: "video", Width: 1280, Height: 720}}}, width: 1920, height: 1080, wantReason: recordingReasonResolutionMismatch, wantClosed: true, wantDuration: 2_000_000_000},
		{name: "invalid expected resolution", hasRealVideo: true, finalized: finalized, probe: goodProbe, wantReason: recordingReasonExpectedResolutionInvalid, wantClosed: true, wantDuration: 1_500_000_000},
		{name: "invalid expected width", hasRealVideo: true, finalized: finalized, probe: goodProbe, width: -1, height: 1080, wantReason: recordingReasonExpectedResolutionInvalid, wantClosed: true, wantDuration: 1_500_000_000},
		{name: "invalid expected height", hasRealVideo: true, finalized: finalized, probe: goodProbe, width: 1920, height: -1, wantReason: recordingReasonExpectedResolutionInvalid, wantClosed: true, wantDuration: 1_500_000_000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := classifyDirectRecordingOutcome(artifacts, test.finalized, test.hasRealVideo, test.width, test.height, test.probe, test.probeErr)
			if outcome.Artifacts != artifacts || outcome.Reason != test.wantReason || outcome.Closed != test.wantClosed || outcome.HasRealVideo != test.hasRealVideo || outcome.ProbeOK || outcome.DurationNS != test.wantDuration {
				t.Fatalf("outcome=%+v", outcome)
			}
		})
	}
}

func TestDirectRecordingOutcomeReasonsAreStableAndDistinct(t *testing.T) {
	reasons := []struct {
		got  string
		want string
	}{
		{recordingReasonVerified, "verified"},
		{recordingReasonNoRealVideo, "no-real-video"},
		{recordingReasonCloseNotConfirmed, "recording-close-not-confirmed"},
		{recordingReasonProbeTimeout, "probe-timeout"},
		{recordingReasonProbeFailed, "probe-failed"},
		{recordingReasonDurationInvalid, "duration-invalid"},
		{recordingReasonVideoMissing, "video-stream-missing"},
		{recordingReasonResolutionMismatch, "resolution-mismatch"},
		{recordingReasonExpectedResolutionInvalid, "expected-resolution-invalid"},
	}
	seen := make(map[string]bool, len(reasons))
	for _, reason := range reasons {
		if reason.got != reason.want || seen[reason.got] {
			t.Fatalf("unstable or duplicate reason: got=%q want=%q", reason.got, reason.want)
		}
		seen[reason.got] = true
	}
}

func TestClassifyDirectRecordingOutcomeAcceptsVerifiedVideo(t *testing.T) {
	artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), "0123456789abcdef", 7)
	finalized := airplaycontract.Event{
		Schema: 2, SessionID: artifacts.SessionID, PublisherGeneration: artifacts.Generation,
		Event: "recording-finalized", At: time.Unix(200, 0).UTC(),
		RecordingPath: artifacts.Recording, RecordingClosed: true,
	}
	probe := video.MediaProbe{
		Duration: 3.25,
		Streams: []video.MediaStream{
			{CodecType: "audio"},
			{CodecType: "video", AttachedPic: true, Width: 640, Height: 640},
			{CodecType: "video", Width: 1280, Height: 720},
		},
	}

	outcome := classifyDirectRecordingOutcome(artifacts, finalized, true, 1280, 720, probe, nil)
	if outcome.Artifacts != artifacts || !outcome.Closed || !outcome.HasRealVideo || !outcome.ProbeOK || outcome.DurationNS != 3_250_000_000 || outcome.Reason != recordingReasonVerified {
		t.Fatalf("outcome=%+v", outcome)
	}
}

func TestClassifyDirectRecordingOutcomeAcceptsOneMatchingRealVideoStream(t *testing.T) {
	artifacts := airplaycontract.NewPublisherArtifacts(t.TempDir(), "0123456789abcdef", 8)
	finalized := airplaycontract.Event{
		Schema: 2, SessionID: artifacts.SessionID, PublisherGeneration: artifacts.Generation,
		Event: "recording-finalized", At: time.Unix(201, 0).UTC(),
		RecordingPath: artifacts.Recording, RecordingClosed: true,
	}
	probe := video.MediaProbe{Duration: 1, Streams: []video.MediaStream{
		{CodecType: "video", Width: 640, Height: 360},
		{CodecType: "video", Width: 1920, Height: 1080},
	}}
	outcome := classifyDirectRecordingOutcome(artifacts, finalized, true, 1920, 1080, probe, nil)
	if !outcome.ProbeOK || outcome.Reason != recordingReasonVerified {
		t.Fatalf("outcome=%+v", outcome)
	}
}
