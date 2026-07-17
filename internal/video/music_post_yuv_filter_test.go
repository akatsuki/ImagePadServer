package video

import (
	"strings"
	"testing"
)

func TestBuildMusicPostYUVFilterOrdering(t *testing.T) {
	g := buildMusicPostYUVFilter(752, 168, 432, 320, "#FFFFFF@0.55", `C:\tmp\x.ass`, `C:\fonts`)
	if !(strings.Index(g, "showwaves") < strings.Index(g, "overlay") && strings.Index(g, "overlay") < strings.Index(g, "ass=")) {
		t.Fatalf("unexpected graph ordering: %s", g)
	}
	if got := musicPostYUVFilterSHA256(g); got == "" || got != musicPostYUVFilterSHA256(g) {
		t.Fatalf("unstable filter hash: %q", got)
	}
}
