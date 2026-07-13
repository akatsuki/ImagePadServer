package obsrtmp

import (
	"bytes"
	"testing"
	"time"
)

func TestOverlayRendererHonorsModeWindowAndMissingNextTrack(t *testing.T) {
	renderer := NewOverlayRenderer()
	snapshot := OverlaySnapshot{
		Mode:          OverlayModeNotifications,
		Next:          &OverlayTrack{ID: "next", Title: "Next track", Artist: "Artist"},
		ShowNextFrom:  50 * time.Second,
		ShowNextUntil: time.Minute,
		Revision:      1,
	}

	if got := renderer.RenderRGBA(320, 180, 49*time.Second, snapshot); hasVisibleRGBA(got) {
		t.Fatal("overlay appeared before its notification window")
	}
	if got := renderer.RenderRGBA(320, 180, 51*time.Second, snapshot); !hasVisibleRGBA(got) {
		t.Fatal("overlay did not appear inside its notification window")
	}

	snapshot.Mode = OverlayModeOff
	if got := renderer.RenderRGBA(320, 180, 51*time.Second, snapshot); hasVisibleRGBA(got) {
		t.Fatal("overlay appeared while mode was off")
	}
	snapshot.Mode = OverlayModeNotifications
	snapshot.Next = nil
	if got := renderer.RenderRGBA(320, 180, 51*time.Second, snapshot); hasVisibleRGBA(got) {
		t.Fatal("overlay remained visible after the next track was cleared")
	}
}

func TestOverlayRevisionRestartsEntranceAnimation(t *testing.T) {
	renderer := NewOverlayRenderer()
	snapshot := OverlaySnapshot{
		Mode:          OverlayModeNotifications,
		Next:          &OverlayTrack{ID: "next", Title: "Next track"},
		ShowNextFrom:  0,
		ShowNextUntil: time.Minute,
		Revision:      1,
	}

	_ = renderer.RenderRGBA(320, 180, time.Second, snapshot)
	settled := renderer.RenderRGBA(320, 180, 2*time.Second, snapshot)
	snapshot.Revision++
	restarted := renderer.RenderRGBA(320, 180, 2*time.Second, snapshot)
	if bytes.Equal(settled, restarted) {
		t.Fatal("metadata revision change did not restart the entrance animation")
	}
}

func hasVisibleRGBA(frame []byte) bool {
	for i := 3; i < len(frame); i += 4 {
		if frame[i] != 0 {
			return true
		}
	}
	return false
}
