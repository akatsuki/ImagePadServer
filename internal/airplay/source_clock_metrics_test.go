package airplay

import (
	"strings"
	"testing"
)

const canonicalSourceClockMetricsLine = `IMAGEPAD_METRICS_V1 {"receiverId":"receiver-1","sequence":7,"videoFrames":11,"audioFrames":0,"videoAgeMs":25,"audioAgeMs":null,"connectedConsumers":1,"sentFrames":9,"droppedFrames":2,"lastSocketError":0,"lastFailureReason":"queue_drop"}`
const canonicalSourceClockMetricsV2Line = `IMAGEPAD_METRICS_V1 {"receiverId":"receiver-1","sequence":7,"videoFrames":11,"audioFrames":0,"videoAgeMs":25,"audioAgeMs":null,"connectedConsumers":1,"sentFrames":9,"droppedFrames":2,"failureEvents":4,"lastSocketError":0,"lastFailureReason":""}`

func TestParseSourceClockMetricsLineAcceptsCanonicalContract(t *testing.T) {
	metrics, matched, err := parseSourceClockMetricsLine(canonicalSourceClockMetricsLine)
	if err != nil || !matched {
		t.Fatalf("parse canonical metrics: matched=%v err=%v", matched, err)
	}
	if metrics.ReceiverID != "receiver-1" || metrics.Sequence != 7 ||
		metrics.VideoFrames != 11 || metrics.AudioFrames != 0 ||
		metrics.VideoAgeMS == nil || *metrics.VideoAgeMS != 25 ||
		metrics.AudioAgeMS != nil || metrics.ConnectedConsumers != 1 ||
		metrics.SentFrames != 9 || metrics.DroppedFrames != 2 ||
		metrics.LastSocketError != 0 || metrics.LastFailureReason != "queue_drop" {
		t.Fatalf("unexpected metrics: %#v", metrics)
	}
	if metrics.HasFailureEvents {
		t.Fatal("legacy metrics fabricated failureEvents presence")
	}
}

func TestParseSourceClockMetricsLineAcceptsFailureEventsExtension(t *testing.T) {
	metrics, matched, err := parseSourceClockMetricsLine(canonicalSourceClockMetricsV2Line)
	if err != nil || !matched || !metrics.HasFailureEvents || metrics.FailureEvents != 4 {
		t.Fatalf("parse failureEvents metrics: matched=%v metrics=%#v err=%v", matched, metrics, err)
	}
}

func TestParseSourceClockMetricsLineAcceptsFailureEventsBounds(t *testing.T) {
	for _, value := range []string{"0", "18446744073709551615"} {
		line := strings.Replace(canonicalSourceClockMetricsV2Line, `"failureEvents":4`, `"failureEvents":`+value, 1)
		metrics, matched, err := parseSourceClockMetricsLine(line)
		if err != nil || !matched || !metrics.HasFailureEvents {
			t.Fatalf("failureEvents=%s: matched=%v metrics=%#v err=%v", value, matched, metrics, err)
		}
	}
}

func TestParseSourceClockMetricsLineAcceptsCRAndNullAges(t *testing.T) {
	line := strings.Replace(canonicalSourceClockMetricsLine, `"videoAgeMs":25`, `"videoAgeMs":null`, 1) + "\r"
	metrics, matched, err := parseSourceClockMetricsLine(line)
	if err != nil || !matched || metrics.VideoAgeMS != nil || metrics.AudioAgeMS != nil {
		t.Fatalf("parse null ages: matched=%v metrics=%#v err=%v", matched, metrics, err)
	}
}

func TestParseSourceClockMetricsLineAcceptsSignedSocketError(t *testing.T) {
	line := strings.Replace(canonicalSourceClockMetricsLine, `"lastSocketError":0`, `"lastSocketError":-2`, 1)
	metrics, matched, err := parseSourceClockMetricsLine(line)
	if err != nil || !matched || metrics.LastSocketError != -2 {
		t.Fatalf("parse signed socket error: matched=%v metrics=%#v err=%v", matched, metrics, err)
	}
}

func TestParseSourceClockMetricsLineIgnoresOrdinaryLog(t *testing.T) {
	metrics, matched, err := parseSourceClockMetricsLine("UxPlay 1.73.6 initialized")
	if err != nil || matched || metrics != (ReceiverMetrics{}) {
		t.Fatalf("ordinary log was not ignored: matched=%v metrics=%#v err=%v", matched, metrics, err)
	}
}

func TestParseSourceClockMetricsLineRejectsInvalidContract(t *testing.T) {
	validJSON := strings.TrimPrefix(canonicalSourceClockMetricsLine, "IMAGEPAD_METRICS_V1 ")
	tests := map[string]string{
		"empty payload":           "IMAGEPAD_METRICS_V1 ",
		"malformed json":          "IMAGEPAD_METRICS_V1 {",
		"trailing json":           canonicalSourceClockMetricsLine + `{}`,
		"missing field":           strings.Replace(canonicalSourceClockMetricsLine, `,"sentFrames":9`, "", 1),
		"unknown field":           "IMAGEPAD_METRICS_V1 " + strings.TrimSuffix(validJSON, "}") + `,"token":"secret"}`,
		"duplicate field":         strings.Replace(canonicalSourceClockMetricsLine, `"sequence":7`, `"sequence":7,"sequence":8`, 1),
		"null required field":     strings.Replace(canonicalSourceClockMetricsLine, `"sentFrames":9`, `"sentFrames":null`, 1),
		"null failure events":     strings.Replace(canonicalSourceClockMetricsV2Line, `"failureEvents":4`, `"failureEvents":null`, 1),
		"failure events overflow": strings.Replace(canonicalSourceClockMetricsV2Line, `"failureEvents":4`, `"failureEvents":18446744073709551616`, 1),
		"wrong numeric type":      strings.Replace(canonicalSourceClockMetricsLine, `"videoFrames":11`, `"videoFrames":"11"`, 1),
		"uint overflow":           strings.Replace(canonicalSourceClockMetricsLine, `"videoFrames":11`, `"videoFrames":18446744073709551616`, 1),
		"zero sequence":           strings.Replace(canonicalSourceClockMetricsLine, `"sequence":7`, `"sequence":0`, 1),
		"empty receiver":          strings.Replace(canonicalSourceClockMetricsLine, `"receiverId":"receiver-1"`, `"receiverId":""`, 1),
		"receiver newline":        strings.Replace(canonicalSourceClockMetricsLine, `"receiverId":"receiver-1"`, `"receiverId":"bad\\nreceiver"`, 1),
		"receiver too long":       strings.Replace(canonicalSourceClockMetricsLine, `"receiverId":"receiver-1"`, `"receiverId":"`+strings.Repeat("a", 65)+`"`, 1),
		"failure reason":          strings.Replace(canonicalSourceClockMetricsLine, `"lastFailureReason":"queue_drop"`, `"lastFailureReason":"other"`, 1),
		"oversized line":          "IMAGEPAD_METRICS_V1 " + strings.Repeat("x", 4097),
	}
	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			_, matched, err := parseSourceClockMetricsLine(line)
			if !matched || err == nil {
				t.Fatalf("invalid metrics accepted: matched=%v err=%v", matched, err)
			}
		})
	}
}

func TestParseSourceClockMetricsLineLeavesSequencePolicyToSession(t *testing.T) {
	first, firstMatched, firstErr := parseSourceClockMetricsLine(canonicalSourceClockMetricsLine)
	second, secondMatched, secondErr := parseSourceClockMetricsLine(canonicalSourceClockMetricsLine)
	if firstErr != nil || secondErr != nil || !firstMatched || !secondMatched || first.Sequence != second.Sequence {
		t.Fatalf("parser applied session sequence policy: first=%#v second=%#v firstErr=%v secondErr=%v", first, second, firstErr, secondErr)
	}
}
