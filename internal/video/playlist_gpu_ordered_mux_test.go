package video

import "testing"

func TestPlaylistGPUOrderedMuxAcceptsOrderedCompressedFrames(t *testing.T) {
	var got []uint64
	mux, err := NewPlaylistGPUOrderedMux(10, 100, 3, func(frame EncodedH264Frame) error {
		got = append(got, frame.Sequence)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mux.PushBatch([]EncodedH264Frame{orderedMuxFrame(10, 100), orderedMuxFrame(11, 33_333_333)}); err != nil {
		t.Fatal(err)
	}
	if err := mux.PushBatch([]EncodedH264Frame{orderedMuxFrame(12, 66_666_666)}); err != nil {
		t.Fatal(err)
	}
	if err := mux.Finalize(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || mux.Count() != 3 {
		t.Fatalf("accepted sequences = %v, count=%d", got, mux.Count())
	}
	telemetry := mux.Telemetry()
	if telemetry.ExpectedFrames != 3 || telemetry.AcceptedFrames != 3 || telemetry.DuplicateCount != 0 || telemetry.ReorderCount != 0 || telemetry.PTSViolationCount != 0 || telemetry.PixelReadbackBytes != 0 || telemetry.PartialSuccessCount != 0 {
		t.Fatalf("unexpected clean telemetry: %+v", telemetry)
	}
	if err := telemetry.Validate(); err != nil {
		t.Fatalf("clean telemetry rejected: %v", err)
	}
}

func TestPlaylistGPUOrderedMuxRejectsDisorderAndReadback(t *testing.T) {
	calls := 0
	mux, err := NewPlaylistGPUOrderedMux(0, 0, 2, func(EncodedH264Frame) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mux.PushBatch([]EncodedH264Frame{orderedMuxFrame(0, 0), orderedMuxFrame(0, 1)}); err == nil {
		t.Fatal("duplicate sequence must be rejected")
	}
	bad := orderedMuxFrame(0, 0)
	bad.PixelReadbackBytes = 1
	if err := mux.PushBatch([]EncodedH264Frame{bad}); err == nil {
		t.Fatal("pixel readback must be rejected")
	}
	telemetry := mux.Telemetry()
	if telemetry.DuplicateCount != 1 || telemetry.PixelReadbackBytes != 1 || telemetry.PartialSuccessCount != 0 {
		t.Fatalf("unexpected disorder telemetry: %+v", telemetry)
	}
	if err := telemetry.Validate(); err == nil {
		t.Fatal("invalid telemetry must fail closed")
	}
	if calls != 0 {
		t.Fatalf("invalid batch reached sink %d times", calls)
	}
}

func TestPlaylistGPUOrderedMuxRejectsIncompleteFinalize(t *testing.T) {
	mux, err := NewPlaylistGPUOrderedMux(0, 0, 2, func(EncodedH264Frame) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := mux.PushBatch([]EncodedH264Frame{orderedMuxFrame(0, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := mux.Finalize(); err == nil {
		t.Fatal("incomplete stream must not finalize")
	}
	if mux.Telemetry().PartialSuccessCount != 1 {
		t.Fatalf("incomplete finalize telemetry = %+v", mux.Telemetry())
	}
}

func orderedMuxFrame(sequence uint64, pts int64) EncodedH264Frame {
	return EncodedH264Frame{
		Schema:             GPUContractVersion,
		Sequence:           sequence,
		PTSNs:              pts,
		Width:              640,
		Height:             360,
		Codec:              "h264",
		Backend:            "dx12",
		PixelReadbackBytes: 0,
		AssetCacheReady:    true,
		AssetReceipt: H264AssetReceipt{
			ArtworkHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			GlyphHash:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
		Payload: []byte{1},
	}
}
