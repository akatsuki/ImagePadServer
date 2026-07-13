package tunnel

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanupStaleCloudflaredUsesAppLocalPathMarker(t *testing.T) {
	oldKill := killOwnedCloudflared
	t.Cleanup(func() { killOwnedCloudflared = oldKill })

	var executable string
	var marker string
	killOwnedCloudflared = func(executableBase, requiredMarker string, preferredPIDs []int) (int, error) {
		executable = executableBase
		marker = requiredMarker
		if len(preferredPIDs) != 0 {
			t.Fatalf("preferredPIDs = %v, want none", preferredPIDs)
		}
		return 2, errors.New("partial failure")
	}

	count, err := CleanupStaleCloudflared()
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	if err == nil || !strings.Contains(err.Error(), "partial failure") {
		t.Fatalf("error = %v, want propagated partial failure", err)
	}
	if executable != cloudflaredExecutableBase() {
		t.Fatalf("executable = %q, want %q", executable, cloudflaredExecutableBase())
	}
	if marker != localCloudflaredPath() {
		t.Fatalf("marker = %q, want local cloudflared path", marker)
	}
}
