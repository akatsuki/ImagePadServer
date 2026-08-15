package playlist

import (
	"testing"
)

func addReady(t *testing.T, q *Queue, title string) Track {
	t.Helper()
	tr := q.Add(Track{Title: title, Status: TrackPreparing})
	if tr.ID == "" {
		t.Fatalf("Add(%q) did not assign an ID", title)
	}
	if !q.MarkReady(tr.ID, "media/"+tr.ID+".ts", 180) {
		t.Fatalf("MarkReady(%q) failed", tr.ID)
	}
	got, ok := q.Get(tr.ID)
	if !ok {
		t.Fatalf("Get(%q) not found", tr.ID)
	}
	return got
}

func TestAddAssignsIDAndPreservesOrder(t *testing.T) {
	q := NewQueue()
	a := q.Add(Track{Title: "A"})
	b := q.Add(Track{Title: "B"})
	if a.ID == b.ID {
		t.Fatalf("IDs must be unique, both %q", a.ID)
	}
	if a.AddedAt.IsZero() {
		t.Fatal("AddedAt must be set")
	}
	if a.Status != TrackPreparing {
		t.Fatalf("default status = %q, want preparing", a.Status)
	}
	snap := q.Snapshot()
	if len(snap) != 2 || snap[0].Title != "A" || snap[1].Title != "B" {
		t.Fatalf("Snapshot order wrong: %+v", snap)
	}
}

func TestMarkReadyAndFailed(t *testing.T) {
	q := NewQueue()
	tr := q.Add(Track{Title: "A"})
	if !q.MarkReady(tr.ID, "m.ts", 240) {
		t.Fatal("MarkReady returned false")
	}
	got, _ := q.Get(tr.ID)
	if got.Status != TrackReady || got.MediaPath != "m.ts" || got.DurationSeconds != 240 {
		t.Fatalf("after MarkReady: %+v", got)
	}
	if !q.MarkFailed(tr.ID, "boom") {
		t.Fatal("MarkFailed returned false")
	}
	got, _ = q.Get(tr.ID)
	if got.Status != TrackFailed || got.Error != "boom" {
		t.Fatalf("after MarkFailed: %+v", got)
	}
	if q.MarkReady("nope", "x", 1) {
		t.Fatal("MarkReady on unknown ID must return false")
	}
}

func TestRemove(t *testing.T) {
	q := NewQueue()
	a := addReady(t, q, "A")
	b := addReady(t, q, "B")
	if !q.Remove(a.ID) {
		t.Fatal("Remove returned false")
	}
	if q.Remove(a.ID) {
		t.Fatal("second Remove must return false")
	}
	snap := q.Snapshot()
	if len(snap) != 1 || snap[0].ID != b.ID {
		t.Fatalf("Snapshot after remove: %+v", snap)
	}
}

func TestSetOrderRequiresPermutation(t *testing.T) {
	q := NewQueue()
	a := addReady(t, q, "A")
	b := addReady(t, q, "B")
	c := addReady(t, q, "C")
	if !q.SetOrder([]string{c.ID, a.ID, b.ID}) {
		t.Fatal("valid permutation rejected")
	}
	snap := q.Snapshot()
	if snap[0].ID != c.ID || snap[1].ID != a.ID || snap[2].ID != b.ID {
		t.Fatalf("order not applied: %+v", snap)
	}
	if q.SetOrder([]string{a.ID, b.ID}) {
		t.Fatal("incomplete permutation must be rejected")
	}
	if q.SetOrder([]string{a.ID, b.ID, "bogus"}) {
		t.Fatal("unknown ID must be rejected")
	}
}

func TestSetCurrentAndClear(t *testing.T) {
	q := NewQueue()
	a := addReady(t, q, "A")
	if q.CurrentID() != "" {
		t.Fatal("fresh queue must have no current track")
	}
	if !q.SetCurrent(a.ID) {
		t.Fatal("SetCurrent returned false")
	}
	if q.CurrentID() != a.ID {
		t.Fatalf("CurrentID = %q, want %q", q.CurrentID(), a.ID)
	}
	if q.SetCurrent("bogus") {
		t.Fatal("SetCurrent on unknown ID must return false")
	}
	q.ClearCurrent()
	if q.CurrentID() != "" {
		t.Fatal("ClearCurrent did not clear")
	}
}

func TestNextSequential(t *testing.T) {
	q := NewQueue()
	a := addReady(t, q, "A")
	b := addReady(t, q, "B")
	c := addReady(t, q, "C")

	first, ok := q.Next()
	if !ok || first.ID != a.ID {
		t.Fatalf("first Next = %+v ok=%v, want A", first, ok)
	}
	second, ok := q.Next()
	if !ok || second.ID != b.ID {
		t.Fatalf("second Next = %+v ok=%v, want B", second, ok)
	}
	third, ok := q.Next()
	if !ok || third.ID != c.ID {
		t.Fatalf("third Next = %+v ok=%v, want C", third, ok)
	}
	if _, ok := q.Next(); ok {
		t.Fatal("Next past end without loop must return ok=false")
	}
	if q.CurrentID() != "" {
		t.Fatal("current must clear when playlist ends")
	}
}

func TestPreviewNextReservesSequentialTargetWithoutMutatingPlayback(t *testing.T) {
	q := NewQueue()
	addReady(t, q, "A")
	addReady(t, q, "B")
	if q.CurrentID() != "" {
		t.Fatal("preview must not set current track")
	}
	first, ok := q.PreviewNext()
	if !ok || first.ID == "" {
		t.Fatalf("PreviewNext = %+v ok=%v", first, ok)
	}
	second, ok := q.PreviewNext()
	if !ok || second.ID != first.ID {
		t.Fatalf("repeated PreviewNext changed reservation: first=%+v second=%+v", first, second)
	}
	committed, ok := q.Next()
	if !ok || committed.ID != first.ID {
		t.Fatalf("Next did not commit preview: %+v ok=%v", committed, ok)
	}
}

func TestPreviewNextCommitsTheShuffleReservation(t *testing.T) {
	q := NewQueue()
	addReady(t, q, "A")
	addReady(t, q, "B")
	addReady(t, q, "C")
	q.SetShuffle(true)
	preview, ok := q.PreviewNext()
	if !ok {
		t.Fatal("shuffle PreviewNext returned no track")
	}
	committed, ok := q.Next()
	if !ok || committed.ID != preview.ID {
		t.Fatalf("shuffle Next did not commit preview: preview=%+v committed=%+v ok=%v", preview, committed, ok)
	}
}

func TestSequentialCursor(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, q *Queue)
	}{
		{
			name: "continues from exhausted tail when a track is added",
			run: func(t *testing.T, q *Queue) {
				a := addReady(t, q, "A")
				b := addReady(t, q, "B")
				if got, _ := q.Next(); got.ID != a.ID {
					t.Fatalf("first Next = %q, want %q", got.ID, a.ID)
				}
				if got, _ := q.Next(); got.ID != b.ID {
					t.Fatalf("second Next = %q, want %q", got.ID, b.ID)
				}
				if _, ok := q.Next(); ok {
					t.Fatal("exhausted queue must not select a track")
				}
				c := addReady(t, q, "C")
				if got, ok := q.Next(); !ok || got.ID != c.ID {
					t.Fatalf("Next after adding C = %+v ok=%v, want C", got, ok)
				}
			},
		},
		{
			name: "uses the reordered successor of the last played track",
			run: func(t *testing.T, q *Queue) {
				a := addReady(t, q, "A")
				b := addReady(t, q, "B")
				c := addReady(t, q, "C")
				q.Next()
				q.Next()
				if !q.SetOrder([]string{c.ID, a.ID, b.ID}) {
					t.Fatal("SetOrder failed")
				}
				if got, ok := q.Next(); !ok || got.ID != c.ID {
					t.Fatalf("Next after reorder = %+v ok=%v, want C", got, ok)
				}
			},
		},
		{
			name: "continues at the first unplayed track when the cursor track is removed",
			run: func(t *testing.T, q *Queue) {
				a := addReady(t, q, "A")
				b := addReady(t, q, "B")
				c := addReady(t, q, "C")
				q.Next()
				q.Next()
				if !q.Remove(b.ID) {
					t.Fatal("Remove(B) failed")
				}
				if got, ok := q.Next(); !ok || got.ID != c.ID {
					t.Fatalf("Next after removing cursor track = %+v ok=%v, want C", got, ok)
				}
				if q.CurrentID() != c.ID || q.CurrentID() == a.ID {
					t.Fatalf("current after removal = %q, want C", q.CurrentID())
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, NewQueue())
		})
	}
}

func TestResetPlaybackCycleRestartsSequentialSelection(t *testing.T) {
	q := NewQueue()
	a := addReady(t, q, "A")
	addReady(t, q, "B")
	q.Next()
	q.Next()
	if _, ok := q.Next(); ok {
		t.Fatal("queue must be exhausted before reset")
	}
	q.ResetPlaybackCycle()
	if got, ok := q.Next(); !ok || got.ID != a.ID {
		t.Fatalf("Next after ResetPlaybackCycle = %+v ok=%v, want A", got, ok)
	}
}

func TestNextSkipsNotReady(t *testing.T) {
	q := NewQueue()
	q.Add(Track{Title: "pending"}) // stays preparing
	b := addReady(t, q, "B")
	got, ok := q.Next()
	if !ok || got.ID != b.ID {
		t.Fatalf("Next = %+v ok=%v, want ready track B", got, ok)
	}
}

func TestNextLoopWrapsAround(t *testing.T) {
	q := NewQueue()
	a := addReady(t, q, "A")
	b := addReady(t, q, "B")
	q.SetLoop(true)
	if got, _ := q.Next(); got.ID != a.ID {
		t.Fatalf("want A first, got %+v", got)
	}
	if got, _ := q.Next(); got.ID != b.ID {
		t.Fatalf("want B second, got %+v", got)
	}
	got, ok := q.Next()
	if !ok || got.ID != a.ID {
		t.Fatalf("loop must wrap to A, got %+v ok=%v", got, ok)
	}
}

func TestNextShuffleVisitsAllWithoutRepeats(t *testing.T) {
	q := NewQueue()
	ids := map[string]bool{}
	for _, name := range []string{"A", "B", "C", "D"} {
		ids[addReady(t, q, name).ID] = true
	}
	q.SetShuffle(true)
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		got, ok := q.Next()
		if !ok {
			t.Fatalf("Next #%d returned ok=false", i)
		}
		if seen[got.ID] {
			t.Fatalf("shuffle repeated track %q before finishing all", got.ID)
		}
		if !ids[got.ID] {
			t.Fatalf("unknown track %q", got.ID)
		}
		seen[got.ID] = true
	}
	if _, ok := q.Next(); ok {
		t.Fatal("shuffle without loop must stop after all tracks played")
	}
}

func TestNextShuffleLoopResets(t *testing.T) {
	q := NewQueue()
	addReady(t, q, "A")
	addReady(t, q, "B")
	q.SetShuffle(true)
	q.SetLoop(true)
	seen := 0
	for i := 0; i < 6; i++ {
		if _, ok := q.Next(); ok {
			seen++
		}
	}
	if seen != 6 {
		t.Fatalf("shuffle+loop must keep producing tracks, got %d/6", seen)
	}
}

func TestMutateUpdatesFields(t *testing.T) {
	q := NewQueue()
	tr := q.Add(Track{Title: "raw"})
	if !q.Mutate(tr.ID, func(t *Track) { t.Title = "Polished"; t.Artist = "Band" }) {
		t.Fatal("Mutate returned false")
	}
	got, _ := q.Get(tr.ID)
	if got.Title != "Polished" || got.Artist != "Band" {
		t.Fatalf("Mutate not applied: %+v", got)
	}
	if q.Mutate("bogus", func(t *Track) {}) {
		t.Fatal("Mutate on unknown ID must return false")
	}
}

func TestReplaceAllResetsQueue(t *testing.T) {
	q := NewQueue()
	addReady(t, q, "old")
	q.SetCurrent(q.Snapshot()[0].ID)
	q.ReplaceAll([]Track{
		{ID: "n1", Title: "New1", Status: TrackReady, MediaPath: "n1.ts"},
		{ID: "n2", Title: "New2", Status: TrackReady, MediaPath: "n2.ts"},
	})
	snap := q.Snapshot()
	if len(snap) != 2 || snap[0].ID != "n1" || snap[1].ID != "n2" {
		t.Fatalf("ReplaceAll result: %+v", snap)
	}
	if q.CurrentID() != "" {
		t.Fatal("ReplaceAll must clear current")
	}
	got, ok := q.Next()
	if !ok || got.ID != "n1" {
		t.Fatalf("Next after ReplaceAll = %+v ok=%v", got, ok)
	}
}

func TestReplaceAllResetsSequentialGeneration(t *testing.T) {
	q := NewQueue()
	q.ReplaceAll([]Track{{ID: "same", Title: "Old", Status: TrackReady, MediaPath: "old.ts"}})
	if _, ok := q.Next(); !ok {
		t.Fatal("initial queue must be playable")
	}
	if _, ok := q.Next(); ok {
		t.Fatal("single-track queue must be exhausted")
	}
	q.ReplaceAll([]Track{{ID: "same", Title: "Reloaded", Status: TrackReady, MediaPath: "new.ts"}})
	if got, ok := q.Next(); !ok || got.ID != "same" || got.Title != "Reloaded" {
		t.Fatalf("Next after load = %+v ok=%v, want reloaded track", got, ok)
	}
}

func TestReplaceAllReturnsDisplacedTracks(t *testing.T) {
	q := NewQueue()
	old := addReady(t, q, "old")
	previous := q.ReplaceAll([]Track{{ID: "new", Title: "new", Status: TrackReady}})
	if len(previous) != 1 || previous[0].ID != old.ID {
		t.Fatalf("ReplaceAll previous = %+v, want displaced track %q", previous, old.ID)
	}
}

func TestSetCurrentMarksPlayedForShuffle(t *testing.T) {
	q := NewQueue()
	a := addReady(t, q, "A")
	b := addReady(t, q, "B")
	q.SetShuffle(true)
	if !q.SetCurrent(a.ID) { // 割り込み再生
		t.Fatal("SetCurrent failed")
	}
	got, ok := q.Next()
	if !ok || got.ID != b.ID {
		t.Fatalf("after playing A via SetCurrent, Next must pick B, got %+v ok=%v", got, ok)
	}
}
