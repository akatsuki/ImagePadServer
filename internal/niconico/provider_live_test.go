package niconico

import (
	"os"
	"testing"
)

// TestClientFetchesLivePublicVideo is opt-in because it depends on NicoNico
// availability and should never make the ordinary unit-test suite flaky.
func TestClientFetchesLivePublicVideo(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_LIVE_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_LIVE_TEST=1 to run the live provider check")
	}
	snapshot, err := NewClient(nil, nil).Fetch(t.Context(), "sm9")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.VideoID != "sm9" {
		t.Fatalf("video ID = %q, want sm9", snapshot.VideoID)
	}
	if snapshot.CommentStatus != CommentStatusReady || snapshot.CommentCount == 0 {
		t.Fatalf("comment status/count = %q/%d, want ready/nonzero", snapshot.CommentStatus, snapshot.CommentCount)
	}
	if len(snapshot.Threads) == 0 {
		t.Fatal("live snapshot has no threads")
	}
	for _, thread := range snapshot.Threads {
		for _, comment := range thread.Comments {
			if comment.ID == "" {
				t.Fatal("live comment has no ID")
			}
		}
	}
}
