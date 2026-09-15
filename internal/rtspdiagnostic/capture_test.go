package rtspdiagnostic

import "testing"

func TestParseNegotiationMetadataAndFraming(t *testing.T) {
	got, err := ParseNegotiation([]string{"sdp=H264", "transport=TCP", "empty="})
	if err != nil || got["sdp"] != "H264" || got["transport"] != "TCP" || got["empty"] != "" {
		t.Fatalf("metadata = %#v, err=%v", got, err)
	}
	if _, err := ParseNegotiation([]string{"missing-separator"}); err == nil {
		t.Fatal("invalid negotiation entry accepted")
	}
}
