package video

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareNicoHLSForIDValidatesCompletedStaging(t *testing.T) {
	staging := t.TempDir()
	if err := os.WriteFile(filepath.Join(staging, "segment-00000.ts"), []byte("segment"), 0600); err != nil {
		t.Fatal(err)
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:3\n#EXTINF:4.000,\nsegment-00000.ts\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(filepath.Join(staging, "playlist.m3u8"), []byte(playlist), 0600); err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareNicoHLSForID(staging, "media-1", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(prepared.Playlist) != "playlist.m3u8" || len(prepared.Segments) != 1 {
		t.Fatalf("prepared = %#v", prepared)
	}
	if filepath.Base(prepared.Segments[0]) != "segment-00000.ts" {
		t.Fatalf("segments = %#v", prepared.Segments)
	}
	if prepared.MediaID != "media-1" || prepared.RunID != "run-1" {
		t.Fatalf("identity = %#v", prepared)
	}
}

func TestPrepareNicoHLSForIDRejectsEscapeAndMissingEndlist(t *testing.T) {
	tests := []struct {
		name     string
		playlist string
	}{
		{name: "escape", playlist: "#EXTM3U\n../outside.ts\n#EXT-X-ENDLIST\n"},
		{name: "missing endlist", playlist: "#EXTM3U\nsegment-00000.ts\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			staging := t.TempDir()
			if err := os.WriteFile(filepath.Join(staging, "playlist.m3u8"), []byte(tt.playlist), 0600); err != nil {
				t.Fatal(err)
			}
			if tt.name == "missing endlist" {
				if err := os.WriteFile(filepath.Join(staging, "segment-00000.ts"), []byte("segment"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := PrepareNicoHLSForID(staging, "media-1", "run-1")
			if err == nil || !strings.Contains(err.Error(), "playlist") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
