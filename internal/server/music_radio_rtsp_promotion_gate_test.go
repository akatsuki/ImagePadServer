//go:build rtspacceptance

package server

import (
	"os"
	"testing"
)

// TestMusicRadioRTSPPromotionGate is intentionally excluded from the ordinary
// unit suite. When selected with -tags=rtspacceptance it never skips: missing,
// blocked, failed, or malformed metrics fail the promotion command.
func TestMusicRadioRTSPPromotionGate(t *testing.T) {
	if err := validateRTSPPromotionMetricsFile(os.Getenv("IMAGEPAD_RTSP_ACCEPTANCE_OUTPUT")); err != nil {
		t.Fatal(err)
	}
}
