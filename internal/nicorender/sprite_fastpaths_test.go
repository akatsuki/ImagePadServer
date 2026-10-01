package nicorender

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSpriteFastPathNativeLegacyFallbackAndExceptionFixtures(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "internal/nicorender/assets/sprite_fastpaths.test.js")
	cmd.Dir = "../../"
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sprite fast-path characterization: %v: %s", err, strings.TrimSpace(string(output)))
	}
	text := string(output)
	if !strings.Contains(text, "PASS Base64 legacy fixtures=10, native boundaries=10, fallback/override/exception") {
		t.Fatalf("unexpected sprite fast-path test result: %s", strings.TrimSpace(text))
	}
	if !strings.Contains(text, "PASS palette scan fixtures=9, rgba/palette/grayalpha exactness") {
		t.Fatalf("unexpected sprite palette fast-path test result: %s", strings.TrimSpace(text))
	}
}
