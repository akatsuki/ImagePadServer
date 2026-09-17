package niconico

import "testing"

func TestNormalizeSnapshotPreservesForkWhitespaceAndTime(t *testing.T) {
	in := Snapshot{
		VideoID: "sm9",
		Threads: []Thread{
			{ID: "main-thread", Fork: "main", Comments: []Comment{{ID: "c1", No: 7, VposMs: 1234, Body: "  A\nB  ", Commands: []string{"red"}}}},
			{ID: "easy-thread", Fork: "easy", Comments: []Comment{{ID: "c2", No: 8, VposMs: 1234, Body: "A\nB"}}},
		},
	}
	got, err := NormalizeSnapshot(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Threads) != 2 || got.Threads[0].Fork != "main" || got.Threads[1].Fork != "easy" {
		t.Fatalf("threads were not preserved: %#v", got.Threads)
	}
	if got.Threads[0].Comments[0].Body != "  A\nB  " {
		t.Fatalf("body whitespace changed: %q", got.Threads[0].Comments[0].Body)
	}
}

func TestNormalizeSnapshotSeparatesEmptyFromInvalid(t *testing.T) {
	if got, err := NormalizeSnapshot(Snapshot{VideoID: "sm9"}); err != nil || got.CommentStatus != CommentStatusEmpty {
		t.Fatalf("empty snapshot = %#v, err=%v", got, err)
	}
	negative, err := NormalizeSnapshot(Snapshot{VideoID: "sm9", Threads: []Thread{{Fork: "main", Comments: []Comment{{ID: "negative", VposMs: -1, Body: "early"}}}}})
	if err != nil || negative.Threads[0].Comments[0].VposMs != -1 {
		t.Fatalf("negative vpos was not preserved: %#v, err=%v", negative, err)
	}
	_, err = NormalizeSnapshot(Snapshot{VideoID: "sm9", Threads: []Thread{{Fork: "main", Comments: []Comment{{ID: ""}}}}})
	if err == nil {
		t.Fatal("expected invalid snapshot error")
	}
}

func TestRenderKeyChangesWhenSnapshotChanges(t *testing.T) {
	a, err := NormalizeSnapshot(Snapshot{VideoID: "sm9", Threads: []Thread{{Fork: "main", Comments: []Comment{{ID: "c1", VposMs: 1000, Body: "A"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NormalizeSnapshot(Snapshot{VideoID: "sm9", Threads: []Thread{{Fork: "main", Comments: []Comment{{ID: "c1", VposMs: 1000, Body: "B"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	ka, err := RenderKey(a, RenderOptions{RendererVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	kb, err := RenderKey(b, RenderOptions{RendererVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if ka == kb || len(ka) != 64 {
		t.Fatalf("keys = %q, %q", ka, kb)
	}
}

func TestRenderKeyIgnoresAcquisitionTimestamp(t *testing.T) {
	a, err := NormalizeSnapshot(Snapshot{VideoID: "sm9", AcquiredAt: "2026-09-16T00:00:00Z", Threads: []Thread{{Fork: "main", Comments: []Comment{{ID: "c1", VposMs: 1000, Body: "A"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	b := a
	b.AcquiredAt = "2026-09-16T01:00:00Z"
	ka, err := RenderKey(a, RenderOptions{RendererVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	kb, err := RenderKey(b, RenderOptions{RendererVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatalf("acquisition timestamp changed key: %q != %q", ka, kb)
	}
}

func TestNormalizeSnapshotCanonicalizesSelectedForks(t *testing.T) {
	a, err := NormalizeSnapshot(Snapshot{VideoID: "sm9", SelectedForks: []string{"owner", "main", "owner"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.SelectedForks) != 2 || a.SelectedForks[0] != "main" || a.SelectedForks[1] != "owner" {
		t.Fatalf("selected forks = %#v", a.SelectedForks)
	}
}

func TestNormalizeSnapshotPreservesNullableNicoruID(t *testing.T) {
	nicoruID := "nicoru-1"
	got, err := NormalizeSnapshot(Snapshot{VideoID: "sm9", Threads: []Thread{{Fork: "main", Comments: []Comment{{ID: "c1", Body: "A", NicoruID: &nicoruID}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Threads[0].Comments[0].NicoruID == nil || *got.Threads[0].Comments[0].NicoruID != nicoruID {
		t.Fatalf("nicoru ID = %#v, want %q", got.Threads[0].Comments[0].NicoruID, nicoruID)
	}
}
