package airplay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeArtifactMarkerRequiresExactArchiveIdentity(t *testing.T) {
	_, artifact := runtimeArtifactBytes(t, "marker-test")
	root := t.TempDir()
	if err := writeRuntimeArtifactMarker(root, artifact); err != nil {
		t.Fatal(err)
	}
	if !runtimeArtifactMatchesInstalled(root, artifact) {
		t.Fatal("matching runtime artifact marker was rejected")
	}
	changed := artifact
	changed.ArchiveSHA256 = artifact.ArchiveSHA256[:len(artifact.ArchiveSHA256)-1] + "0"
	if runtimeArtifactMatchesInstalled(root, changed) {
		t.Fatal("runtime artifact marker accepted a different archive hash")
	}
	if err := os.Remove(filepath.Join(root, runtimeArtifactMarkerName)); err != nil {
		t.Fatal(err)
	}
	if runtimeArtifactMatchesInstalled(root, artifact) {
		t.Fatal("runtime without marker was accepted")
	}
}
