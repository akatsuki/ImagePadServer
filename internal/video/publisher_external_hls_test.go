package video

import (
	"context"
	"testing"
)

func TestCurrentStatusForExternalHLSIsReadyWithoutConversion(t *testing.T) {
	outDir := t.TempDir()
	done := make(chan struct{})
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	BeginExternalHLS(outDir, "airplay-live", QualityPreset{}, cancel, done)
	t.Cleanup(func() { EndExternalHLS(outDir, done) })

	status := CurrentStatusForID(outDir, "airplay-live")
	if !status.OK || !status.HLS {
		t.Fatalf("external HLS status = %+v, want ready HLS", status)
	}
	if status.Active || status.ProgressText != "" {
		t.Fatalf("external HLS status = %+v, must not appear as a conversion job", status)
	}
}
