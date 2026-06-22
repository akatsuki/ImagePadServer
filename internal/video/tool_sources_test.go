package video

import (
	"strings"
	"testing"
)

// TestFFmpegWindowsSourcesPrimaryIsGitHub guards the source ordering: the
// primary FFmpeg source must be the fast GitHub mirror and must carry an inline
// checksum (GitHub has no .sha256 sidecar, so a missing inline checksum would
// silently drop verification on the happy path).
func TestFFmpegWindowsSourcesPrimaryIsGitHub(t *testing.T) {
	sources := ffmpegWindowsSources()
	if len(sources) < 2 {
		t.Fatalf("expected at least 2 FFmpeg sources, got %d", len(sources))
	}

	primary := sources[0]
	if !strings.Contains(primary.url, "github.com") {
		t.Errorf("primary source url = %q, want a github.com URL", primary.url)
	}
	if strings.TrimSpace(primary.checksum) == "" {
		t.Errorf("primary GitHub source must carry an inline checksum, got empty")
	}
	if primary.checksumURL != "" {
		t.Errorf("primary GitHub source should not use a sidecar checksumURL, got %q", primary.checksumURL)
	}
}

// TestFFmpegWindowsSourcesFallbackUsesSidecar verifies the gyan.dev fallback
// keeps its .sha256 sidecar so it verifies independently of the pinned primary
// hash (avoids version-skew breakage when gyan.dev advances).
func TestFFmpegWindowsSourcesFallbackUsesSidecar(t *testing.T) {
	sources := ffmpegWindowsSources()
	if len(sources) < 2 {
		t.Fatalf("expected at least 2 FFmpeg sources, got %d", len(sources))
	}

	fallback := sources[1]
	if fallback.checksumURL == "" {
		t.Errorf("fallback source must keep a checksumURL sidecar, got empty")
	}
	if fallback.checksum != "" {
		t.Errorf("fallback source should rely on the sidecar, not an inline checksum, got %q", fallback.checksum)
	}
}
