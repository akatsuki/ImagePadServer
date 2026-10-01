package nicorender

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type cdpProbeStep struct {
	Name      string  `json:"name"`
	ElapsedMS float64 `json:"elapsed_ms"`
	OK        bool    `json:"ok"`
	Error     string  `json:"error,omitempty"`
}

// TestNicoCDPMinimal is an opt-in environment probe. It deliberately uses a
// tiny local page so a renderer/WebGL failure cannot be mistaken for a CDP
// startup failure. It never runs in the normal test suite.
func TestNicoCDPMinimal(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_CDP_PROBE") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_CDP_PROBE=1 for the opt-in browser probe")
	}
	page := filepath.Join(t.TempDir(), "probe.html")
	if err := os.WriteFile(page, []byte(`<!doctype html><meta charset="utf-8"><body>probe</body><script>window.__probeReady=true</script>`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	session, err := startBrowser(ctx, os.Getenv("IMAGEPAD_NICONICO_RENDER_BROWSER"), page)
	if err != nil {
		t.Fatalf("browser startup: %v", err)
	}
	defer session.close()

	steps := make([]cdpProbeStep, 0, 5)
	callStep := func(name, method string, params map[string]any) {
		started := time.Now()
		_, callErr := session.call(ctx, method, params)
		step := cdpProbeStep{Name: name, ElapsedMS: float64(time.Since(started).Microseconds()) / 1000, OK: callErr == nil}
		if callErr != nil {
			step.Error = callErr.Error()
		}
		steps = append(steps, step)
		if callErr != nil {
			t.Fatalf("step %s failed after %.1fms: %v", name, step.ElapsedMS, callErr)
		}
	}
	callStep("runtime_literal", "Runtime.evaluate", map[string]any{"expression": "1+1", "returnByValue": true})
	callStep("document_ready_state", "Runtime.evaluate", map[string]any{"expression": "document.readyState", "returnByValue": true})
	callStep("probe_script", "Runtime.evaluate", map[string]any{"expression": "Boolean(window.__probeReady)", "returnByValue": true})
	callStep("layout_metrics", "Page.getLayoutMetrics", nil)
	callStep("screenshot", "Page.captureScreenshot", map[string]any{"format": "png"})

	data, marshalErr := json.Marshal(steps)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	t.Logf("cdp_probe gpu_mode=%q steps=%s", os.Getenv("IMAGEPAD_NICONICO_RENDER_GPU"), string(data))
	if output := os.Getenv("IMAGEPAD_NICONICO_CDP_PROBE_OUTPUT"); output != "" {
		if err := os.WriteFile(output, append(data, '\n'), 0600); err != nil {
			t.Fatal(fmt.Errorf("write probe output: %w", err))
		}
	}
}
