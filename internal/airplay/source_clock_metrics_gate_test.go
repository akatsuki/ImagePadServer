package airplay

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func metricsLineWith(sequence, videoFrames, audioFrames, consumers uint64) string {
	line := canonicalSourceClockMetricsLine
	line = strings.Replace(line, `"sequence":7`, `"sequence":`+formatUint(sequence), 1)
	line = strings.Replace(line, `"videoFrames":11`, `"videoFrames":`+formatUint(videoFrames), 1)
	line = strings.Replace(line, `"audioFrames":0`, `"audioFrames":`+formatUint(audioFrames), 1)
	line = strings.Replace(line, `"connectedConsumers":1`, `"connectedConsumers":`+formatUint(consumers), 1)
	return line
}

func formatUint(value uint64) string {
	return fmt.Sprintf("%d", value)
}

func replaceMetricField(line, fieldName, oldValue, newValue string) string {
	return strings.Replace(line, `"`+fieldName+`":`+oldValue, `"`+fieldName+`":`+newValue, 1)
}

func requireGateMetricTime(t *testing.T, fieldName string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s: got=%v want=%v", fieldName, got, want)
	}
}

func requireGateMetricUint64(t *testing.T, fieldName string, got, want uint64) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got=%d want=%d", fieldName, got, want)
	}
}

func TestSourceClockMetricsGateAcceptsOnlyCurrentIncreasingSequence(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(100, 0)
	var wantAcceptedAt time.Time
	cases := []struct {
		name      string
		line      string
		want      sourceClockMetricsDisposition
		wantError bool
	}{
		{name: "ordinary log", line: "UxPlay initialized", want: sourceClockMetricsIgnoredLog},
		{name: "malformed metrics", line: "IMAGEPAD_METRICS_V1 {", want: sourceClockMetricsParseFailed, wantError: true},
		{name: "other receiver", line: strings.Replace(metricsLineWith(1, 1, 0, 0), `"receiverId":"receiver-1"`, `"receiverId":"receiver-2"`, 1), want: sourceClockMetricsReceiverMismatch},
		{name: "first", line: metricsLineWith(1, 1, 0, 0), want: sourceClockMetricsAccepted},
		{name: "duplicate", line: metricsLineWith(1, 2, 0, 0), want: sourceClockMetricsStaleSequence},
		{name: "next", line: metricsLineWith(2, 2, 0, 0), want: sourceClockMetricsAccepted},
		{name: "rollback", line: metricsLineWith(1, 3, 0, 0), want: sourceClockMetricsStaleSequence},
		{name: "zero sequence", line: metricsLineWith(0, 3, 0, 0), want: sourceClockMetricsParseFailed, wantError: true},
		{name: "counter regression", line: metricsLineWith(3, 1, 0, 0), want: sourceClockMetricsCounterRegression},
		{name: "next video only", line: metricsLineWith(4, 3, 0, 0), want: sourceClockMetricsAccepted},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			receivedAt := base.Add(time.Duration(index) * time.Second)
			got, consumeErr := gate.consumeLine(test.line, receivedAt)
			if got != test.want || (consumeErr != nil) != test.wantError {
				t.Fatalf("consume: got=%v err=%v want=%v wantError=%v", got, consumeErr, test.want, test.wantError)
			}
			if got == sourceClockMetricsAccepted {
				wantAcceptedAt = receivedAt
			}
			if acceptedAt := gate.snapshot().LastAcceptedAt; !acceptedAt.Equal(wantAcceptedAt) {
				t.Fatalf("last accepted time changed for disposition %v: got=%v want=%v", got, acceptedAt, wantAcceptedAt)
			}
		})
	}
	stats := gate.snapshot()
	if !stats.HasMetrics || stats.Latest.Sequence != 4 || stats.Latest.VideoFrames != 3 ||
		stats.Latest.AudioFrames != 0 || stats.Latest.ConnectedConsumers != 0 ||
		stats.ParseFailures != 2 || stats.ReceiverMismatches != 1 || stats.StaleSequences != 2 ||
		stats.CounterRegressions != 1 {
		t.Fatalf("unexpected gate stats: %#v", stats)
	}
}

func TestSourceClockMetricsGateStateSurvivesPublisherGenerationChanges(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	for generation, sequence := range []uint64{1, 2, 3} {
		disposition, consumeErr := gate.consumeLine(metricsLineWith(sequence, sequence, 0, 0), time.Unix(int64(200+generation), 0))
		if consumeErr != nil || disposition != sourceClockMetricsAccepted {
			t.Fatalf("generation %d: disposition=%v err=%v", generation+1, disposition, consumeErr)
		}
	}
	if got := gate.snapshot().Latest.Sequence; got != 3 {
		t.Fatalf("receiver state was reset across publisher generations: sequence=%d", got)
	}
}

func TestSourceClockMetricsGateAllowsV1ToV2UpgradeAndRejectsFailureCounterRegression(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(250, 0)
	v1 := metricsLineWith(1, 1, 0, 1)
	if disposition, consumeErr := gate.consumeLine(v1, base); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept v1: disposition=%v err=%v", disposition, consumeErr)
	}
	v2 := metricsLineWith(2, 2, 0, 1)
	v2 = strings.Replace(v2, `"lastSocketError":0`, `"failureEvents":5,"lastSocketError":0`, 1)
	if disposition, consumeErr := gate.consumeLine(v2, base.Add(time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept v2 upgrade: disposition=%v err=%v", disposition, consumeErr)
	}
	regressed := metricsLineWith(3, 3, 0, 1)
	regressed = strings.Replace(regressed, `"lastSocketError":0`, `"failureEvents":4,"lastSocketError":0`, 1)
	if disposition, consumeErr := gate.consumeLine(regressed, base.Add(2*time.Second)); consumeErr != nil || disposition != sourceClockMetricsCounterRegression {
		t.Fatalf("failure counter regression: disposition=%v err=%v", disposition, consumeErr)
	}
	downgraded := metricsLineWith(4, 4, 0, 1)
	if disposition, consumeErr := gate.consumeLine(downgraded, base.Add(3*time.Second)); consumeErr != nil || disposition != sourceClockMetricsCounterRegression {
		t.Fatalf("v2 to v1 downgrade: disposition=%v err=%v", disposition, consumeErr)
	}
	stats := gate.snapshot()
	if !stats.Latest.HasFailureEvents || stats.Latest.FailureEvents != 5 || stats.Latest.Sequence != 2 || stats.CounterRegressions != 2 {
		t.Fatalf("unexpected v2 gate state: %+v", stats)
	}
}

func TestSourceClockMetricsGateSnapshotOwnsAgeValues(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	if disposition, consumeErr := gate.consumeLine(canonicalSourceClockMetricsLine, time.Unix(300, 0)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("consume: disposition=%v err=%v", disposition, consumeErr)
	}
	first := gate.snapshot()
	if first.Latest.VideoAgeMS == nil {
		t.Fatal("video age is nil")
	}
	*first.Latest.VideoAgeMS = 999
	second := gate.snapshot()
	if second.Latest.VideoAgeMS == nil || *second.Latest.VideoAgeMS != 25 {
		t.Fatalf("snapshot aliases internal age: %#v", second.Latest.VideoAgeMS)
	}
}

func TestSourceClockMetricsGateRejectsEachCounterRegression(t *testing.T) {
	baseline := strings.Replace(canonicalSourceClockMetricsLine, `"audioFrames":0`, `"audioFrames":5`, 1)
	regressions := map[string][2]string{
		"video":   {`"videoFrames":11`, `"videoFrames":10`},
		"audio":   {`"audioFrames":5`, `"audioFrames":4`},
		"sent":    {`"sentFrames":9`, `"sentFrames":8`},
		"dropped": {`"droppedFrames":2`, `"droppedFrames":1`},
	}
	for name, replacement := range regressions {
		t.Run(name, func(t *testing.T) {
			gate, err := newSourceClockMetricsGate("receiver-1")
			if err != nil {
				t.Fatal(err)
			}
			acceptedAt := time.Unix(400, 0)
			if disposition, consumeErr := gate.consumeLine(baseline, acceptedAt); consumeErr != nil || disposition != sourceClockMetricsAccepted {
				t.Fatalf("accept baseline: disposition=%v err=%v", disposition, consumeErr)
			}
			line := strings.Replace(baseline, `"sequence":7`, `"sequence":8`, 1)
			line = strings.Replace(line, replacement[0], replacement[1], 1)
			disposition, consumeErr := gate.consumeLine(line, acceptedAt.Add(time.Second))
			if consumeErr != nil || disposition != sourceClockMetricsCounterRegression {
				t.Fatalf("regression accepted: disposition=%v err=%v", disposition, consumeErr)
			}
			stats := gate.snapshot()
			if stats.CounterRegressions != 1 || stats.Latest.Sequence != 7 ||
				stats.Latest.VideoFrames != 11 || stats.Latest.AudioFrames != 5 ||
				stats.Latest.SentFrames != 9 || stats.Latest.DroppedFrames != 2 ||
				!stats.LastAcceptedAt.Equal(acceptedAt) {
				t.Fatalf("regression changed accepted state: %#v", stats)
			}
		})
	}
}

func TestSourceClockMetricsGateDerivesInputUpperBoundsOnlyWhenEachInputCounterAdvances(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(500, 0)
	first := metricsLineWith(1, 1, 0, 0)
	if disposition, consumeErr := gate.consumeLine(first, base); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept first: disposition=%v err=%v", disposition, consumeErr)
	}
	stats := gate.snapshot()
	requireGateMetricTime(t, "LastVideoInputUpperBound", stats.LastVideoInputUpperBound, base.Add(-25*time.Millisecond))
	requireGateMetricTime(t, "LastAudioInputUpperBound", stats.LastAudioInputUpperBound, time.Time{})

	unchangedVideo := metricsLineWith(2, 1, 0, 0)
	unchangedVideo = replaceMetricField(unchangedVideo, "videoAgeMs", "25", "5")
	if disposition, consumeErr := gate.consumeLine(unchangedVideo, base.Add(time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept unchanged video counter: disposition=%v err=%v", disposition, consumeErr)
	}
	stats = gate.snapshot()
	requireGateMetricTime(t, "LastVideoInputUpperBound", stats.LastVideoInputUpperBound, base.Add(-25*time.Millisecond))

	videoOnly := metricsLineWith(3, 2, 0, 0)
	videoOnly = replaceMetricField(videoOnly, "videoAgeMs", "25", "5")
	if disposition, consumeErr := gate.consumeLine(videoOnly, base.Add(2*time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept video-only advance: disposition=%v err=%v", disposition, consumeErr)
	}
	stats = gate.snapshot()
	requireGateMetricTime(t, "LastVideoInputUpperBound", stats.LastVideoInputUpperBound, base.Add(2*time.Second-5*time.Millisecond))
	requireGateMetricTime(t, "LastAudioInputUpperBound", stats.LastAudioInputUpperBound, time.Time{})

	audioOnly := metricsLineWith(4, 2, 1, 0)
	audioOnly = replaceMetricField(audioOnly, "audioAgeMs", "null", "50")
	if disposition, consumeErr := gate.consumeLine(audioOnly, base.Add(3*time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept audio-only advance: disposition=%v err=%v", disposition, consumeErr)
	}
	stats = gate.snapshot()
	requireGateMetricTime(t, "LastVideoInputUpperBound", stats.LastVideoInputUpperBound, base.Add(2*time.Second-5*time.Millisecond))
	requireGateMetricTime(t, "LastAudioInputUpperBound", stats.LastAudioInputUpperBound, base.Add(3*time.Second-50*time.Millisecond))
	if stats.Latest.ConnectedConsumers != 0 {
		t.Fatalf("consumer count changed the input interpretation: %d", stats.Latest.ConnectedConsumers)
	}

	olderCandidate := metricsLineWith(5, 3, 2, 0)
	olderCandidate = replaceMetricField(olderCandidate, "videoAgeMs", "25", "2010")
	olderCandidate = replaceMetricField(olderCandidate, "audioAgeMs", "null", "2100")
	if disposition, consumeErr := gate.consumeLine(olderCandidate, base.Add(4*time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept older candidate: disposition=%v err=%v", disposition, consumeErr)
	}
	stats = gate.snapshot()
	requireGateMetricTime(t, "LastVideoInputUpperBound", stats.LastVideoInputUpperBound, base.Add(2*time.Second-5*time.Millisecond))
	requireGateMetricTime(t, "LastAudioInputUpperBound", stats.LastAudioInputUpperBound, base.Add(3*time.Second-50*time.Millisecond))
}

func TestSourceClockMetricsGateTracksSentAndDropProgressIndependently(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(600, 0)
	first := metricsLineWith(1, 0, 0, 0)
	first = replaceMetricField(first, "videoAgeMs", "25", "null")
	if disposition, consumeErr := gate.consumeLine(first, base); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept first: disposition=%v err=%v", disposition, consumeErr)
	}

	sentAdvance := replaceMetricField(first, "sequence", "1", "2")
	sentAdvance = replaceMetricField(sentAdvance, "sentFrames", "9", "10")
	if disposition, consumeErr := gate.consumeLine(sentAdvance, base.Add(time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept sent advance: disposition=%v err=%v", disposition, consumeErr)
	}
	stats := gate.snapshot()
	requireGateMetricTime(t, "LastSentProgressAt", stats.LastSentProgressAt, base.Add(time.Second))
	requireGateMetricTime(t, "LastDropObservedAt", stats.LastDropObservedAt, time.Time{})

	dropAdvance := replaceMetricField(sentAdvance, "sequence", "2", "3")
	dropAdvance = replaceMetricField(dropAdvance, "droppedFrames", "2", "3")
	if disposition, consumeErr := gate.consumeLine(dropAdvance, base.Add(2*time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept drop advance: disposition=%v err=%v", disposition, consumeErr)
	}
	stats = gate.snapshot()
	requireGateMetricTime(t, "LastSentProgressAt", stats.LastSentProgressAt, base.Add(time.Second))
	requireGateMetricTime(t, "LastDropObservedAt", stats.LastDropObservedAt, base.Add(2*time.Second))
}

func TestSourceClockMetricsGateCountsSequenceGapsWithSaturation(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(700, 0)
	if disposition, consumeErr := gate.consumeLine(metricsLineWith(1, 0, 0, 0), base); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept first: disposition=%v err=%v", disposition, consumeErr)
	}
	if disposition, consumeErr := gate.consumeLine(metricsLineWith(4, 0, 0, 0), base.Add(time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept gap: disposition=%v err=%v", disposition, consumeErr)
	}
	stats := gate.snapshot()
	requireGateMetricUint64(t, "SequenceGapCount", stats.SequenceGapCount, 2)

	gate.sequenceGapCount = ^uint64(0) - 1
	if disposition, consumeErr := gate.consumeLine(metricsLineWith(6, 0, 0, 0), base.Add(2*time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept saturating gap: disposition=%v err=%v", disposition, consumeErr)
	}
	requireGateMetricUint64(t, "SequenceGapCount", gate.snapshot().SequenceGapCount, ^uint64(0))
}

func TestSourceClockMetricsGateCountsAgeConversionFailuresWithoutRejectingSnapshot(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(800, 0)
	overflow := formatUint(^uint64(0))
	line := metricsLineWith(1, 1, 1, 0)
	line = replaceMetricField(line, "videoAgeMs", "25", overflow)
	line = replaceMetricField(line, "audioAgeMs", "null", overflow)
	disposition, consumeErr := gate.consumeLine(line, base)
	if consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("age overflow rejected snapshot: disposition=%v err=%v", disposition, consumeErr)
	}
	stats := gate.snapshot()
	requireGateMetricTime(t, "LastVideoInputUpperBound", stats.LastVideoInputUpperBound, time.Time{})
	requireGateMetricTime(t, "LastAudioInputUpperBound", stats.LastAudioInputUpperBound, time.Time{})
	requireGateMetricUint64(t, "AgeConversionFailures", stats.AgeConversionFailures, 2)

	gate.ageConversionFailures = ^uint64(0) - 1
	overflowAgain := metricsLineWith(2, 2, 2, 0)
	overflowAgain = replaceMetricField(overflowAgain, "videoAgeMs", "25", overflow)
	overflowAgain = replaceMetricField(overflowAgain, "audioAgeMs", "null", overflow)
	if disposition, consumeErr := gate.consumeLine(overflowAgain, base.Add(time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept saturating age overflow: disposition=%v err=%v", disposition, consumeErr)
	}
	requireGateMetricUint64(t, "AgeConversionFailures", gate.snapshot().AgeConversionFailures, ^uint64(0))

	valid := metricsLineWith(3, 3, 3, 0)
	valid = replaceMetricField(valid, "audioAgeMs", "null", "50")
	if disposition, consumeErr := gate.consumeLine(valid, base.Add(2*time.Second)); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept after age overflow: disposition=%v err=%v", disposition, consumeErr)
	}
	stats = gate.snapshot()
	requireGateMetricTime(t, "LastVideoInputUpperBound", stats.LastVideoInputUpperBound, base.Add(2*time.Second-25*time.Millisecond))
	requireGateMetricTime(t, "LastAudioInputUpperBound", stats.LastAudioInputUpperBound, base.Add(2*time.Second-50*time.Millisecond))
	requireGateMetricUint64(t, "AgeConversionFailures", stats.AgeConversionFailures, ^uint64(0))
}

func TestSourceClockMetricsGateRejectedSnapshotsDoNotAdvanceActivityMetrics(t *testing.T) {
	gate, err := newSourceClockMetricsGate("receiver-1")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(900, 0)
	accepted := metricsLineWith(1, 1, 0, 0)
	if disposition, consumeErr := gate.consumeLine(accepted, base); consumeErr != nil || disposition != sourceClockMetricsAccepted {
		t.Fatalf("accept baseline: disposition=%v err=%v", disposition, consumeErr)
	}
	want := gate.snapshot()
	rejects := []struct {
		name string
		line string
	}{
		{name: "malformed", line: "IMAGEPAD_METRICS_V1 {"},
		{name: "receiver mismatch", line: strings.Replace(accepted, `"receiverId":"receiver-1"`, `"receiverId":"receiver-2"`, 1)},
		{name: "stale", line: replaceMetricField(accepted, "videoFrames", "1", "2")},
		{name: "counter regression", line: replaceMetricField(replaceMetricField(accepted, "sequence", "1", "2"), "videoFrames", "1", "0")},
	}
	for index, reject := range rejects {
		t.Run(reject.name, func(t *testing.T) {
			_, _ = gate.consumeLine(reject.line, base.Add(time.Duration(index+1)*time.Second))
			got := gate.snapshot()
			if got.Latest.Sequence != want.Latest.Sequence || !got.LastAcceptedAt.Equal(want.LastAcceptedAt) {
				t.Fatalf("rejected snapshot advanced accepted state: got=%#v want=%#v", got, want)
			}
			requireGateMetricTime(t, "LastVideoInputUpperBound", got.LastVideoInputUpperBound, want.LastVideoInputUpperBound)
			requireGateMetricTime(t, "LastAudioInputUpperBound", got.LastAudioInputUpperBound, want.LastAudioInputUpperBound)
			requireGateMetricTime(t, "LastSentProgressAt", got.LastSentProgressAt, want.LastSentProgressAt)
			requireGateMetricTime(t, "LastDropObservedAt", got.LastDropObservedAt, want.LastDropObservedAt)
			requireGateMetricUint64(t, "SequenceGapCount", got.SequenceGapCount, want.SequenceGapCount)
			requireGateMetricUint64(t, "AgeConversionFailures", got.AgeConversionFailures, want.AgeConversionFailures)
		})
	}
}

func TestNewSourceClockMetricsGateRejectsInvalidReceiverID(t *testing.T) {
	for _, receiverID := range []string{"", "bad receiver", strings.Repeat("a", 65)} {
		if gate, err := newSourceClockMetricsGate(receiverID); err == nil || gate != nil {
			t.Fatalf("invalid receiver accepted: id=%q gate=%v err=%v", receiverID, gate, err)
		}
	}
}
