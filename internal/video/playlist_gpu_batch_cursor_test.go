package video

import "testing"

func TestPlaylistTimelineBatchCursorRequiresZeroBasedContiguousBatchIDs(t *testing.T) {
	frame := func(sequence uint64) FrameDirective {
		return FrameDirective{
			Sequence:   sequence,
			FrameIndex: sequence,
			PTSNs:      int64(sequence+1) * 33_333_333,
		}
	}
	chunk := func(start uint64) []FrameDirective {
		frames := make([]FrameDirective, GPUH264MaxBatchFrames)
		for i := range frames {
			frames[i] = frame(start + uint64(i))
		}
		return frames
	}

	var cursor playlistTimelineBatchCursor
	first, second, third := chunk(0), chunk(GPUH264MaxBatchFrames), chunk(2*GPUH264MaxBatchFrames)
	if err := cursor.Commit(1, 0, first); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Commit(1, 1, second); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Validate(1, 3, third); err == nil {
		t.Fatal("timeline batch id gap was accepted")
	}
	if err := cursor.Commit(1, 2, third); err != nil {
		t.Fatalf("contiguous third batch was rejected after failed validation: %v", err)
	}
	if err := cursor.Validate(1, 2, third); err == nil {
		t.Fatal("duplicate timeline batch id was accepted")
	}
	if err := cursor.Commit(2, 0, chunk(3*GPUH264MaxBatchFrames)); err != nil {
		t.Fatal(err)
	}
	if err := cursor.Validate(1, 3, chunk(4*GPUH264MaxBatchFrames)); err == nil {
		t.Fatal("stale timeline version was accepted after version advance")
	}
}
