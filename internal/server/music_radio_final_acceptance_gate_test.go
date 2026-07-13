//go:build finalacceptance

package server

import (
	"os"
	"testing"
)

// TestMusicRadioFinalAcceptancePromotionGate never skips. It rejects missing,
// blocked, partial, or failed #20B evidence after the opt-in producer test.
func TestMusicRadioFinalAcceptancePromotionGate(t *testing.T) {
	if err := validateFinalAcceptanceMetricsFile(os.Getenv("IMAGEPAD_FINAL_ACCEPTANCE_OUTPUT")); err != nil {
		t.Fatal(err)
	}
}
