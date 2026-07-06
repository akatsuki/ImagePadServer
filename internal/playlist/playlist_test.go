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
