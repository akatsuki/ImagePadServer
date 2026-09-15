package airplay

import "testing"

func TestValidatePublishURLAcceptsDirectRTSP(t *testing.T) {
	if err := validatePublishURL("rtsp://pub:pass@127.0.0.1:18554/obs_airplay", "rtsp"); err != nil {
		t.Fatalf("direct RTSP URL should be accepted: %v", err)
	}
}

func TestValidatePublishURLAcceptsLegacyRTMP(t *testing.T) {
	if err := validatePublishURL("rtmp://127.0.0.1:1935/live/obs", "rtmp"); err != nil {
		t.Fatalf("legacy RTMP URL should be accepted: %v", err)
	}
}

func TestValidatePublishURLRejectsUnexpectedScheme(t *testing.T) {
	if err := validatePublishURL("http://127.0.0.1:8080/live", "rtsp"); err == nil {
		t.Fatal("unexpected URL scheme should be rejected")
	}
}
