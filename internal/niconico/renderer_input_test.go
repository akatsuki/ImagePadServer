package niconico

import "testing"

func TestRendererThreadsIncludeV1DefaultsAndSelectedForks(t *testing.T) {
	snapshot, err := NormalizeSnapshot(Snapshot{
		VideoID:       "sm9",
		SelectedForks: []string{"main", "owner"},
		Threads: []Thread{
			{ID: "main-thread", Fork: "main", Comments: []Comment{{ID: "c1", VposMs: 100, Body: "main"}}},
			{ID: "easy-thread", Fork: "easy", Comments: []Comment{{ID: "c2", VposMs: 200, Body: "easy"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.RendererThreads()
	if len(got) != 1 || got[0].Fork != "main" || len(got[0].Comments) != 1 {
		t.Fatalf("renderer threads = %#v", got)
	}
	comment := got[0].Comments[0]
	if comment.Commands == nil || comment.UserID != "" || comment.PostedAt != "" || comment.NicoruCount != 0 {
		t.Fatalf("renderer defaults = %#v", comment)
	}
}
