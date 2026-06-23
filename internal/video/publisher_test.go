package video

import (
	"strings"
	"testing"
)

func TestResolveQualityForMusicIsMoreCompressedThanUpload(t *testing.T) {
	music := ResolveQualityForMusic("720", 0, 0)
	upload := ResolveQualityForUpload("720", 0, 0)

	if music.CRF <= upload.CRF {
		t.Fatalf("music CRF %d should be higher than upload CRF %d", music.CRF, upload.CRF)
	}
	if music.MaxRate == "" || upload.MaxRate == "" {
		t.Fatal("missing maxrate")
	}
	musicMax := parseBitrateK(t, music.MaxRate)
	uploadMax := parseBitrateK(t, upload.MaxRate)
	if musicMax >= uploadMax {
		t.Fatalf("music maxrate %dk should be lower than upload maxrate %dk", musicMax, uploadMax)
	}
	// 720p upload MaxRate is 3000k; music 20% -> 600k.
	if musicMax != 600 {
		t.Fatalf("music maxrate = %dk, want 600k", musicMax)
	}
}

func TestResolveQualityForMusicTargetsSmallLongFiles(t *testing.T) {
	preset := ResolveQualityForMusic("auto", 100, 20)
	// 5 minutes at the 1080p music ceiling: ~1040k * 300 / 8 / 1024 = ~38 MB max,
	// but CRF 29 keeps the average far below that. For 720p the ceiling is
	// ~600k -> ~22 MB max. The goal is ~10 MB for 5 min; this gets closer while
	// relying on strong spatial AQ to preserve the waveform area.
	if preset.CRF < 27 || preset.CRF > 40 {
		t.Fatalf("CRF = %d, want 27-40", preset.CRF)
	}
}

func parseBitrateK(t *testing.T, s string) int {
	t.Helper()
	s = strings.TrimSpace(s)
	unit := ""
	if s[len(s)-1] < '0' || s[len(s)-1] > '9' {
		unit = s[len(s)-1:]
		s = s[:len(s)-1]
	}
	v := 0
	for _, c := range s {
		v = v*10 + int(c-'0')
	}
	if unit == "m" || unit == "M" {
		v *= 1000
	}
	return v
}
