package nicorender

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestTimelinePBOReadbackManagerNodeStateMachine(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "assets/timeline_pbo.test.js", "assets/timeline_pbo.js")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("PBO Node state-machine test: %v: %s", err, strings.TrimSpace(string(output)))
	}
}
