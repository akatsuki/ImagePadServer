package video

import (
	"bytes"
	"testing"
)

func TestRingTransportRequestEncoding(t *testing.T) {
	req := &ringRenderRequest{
		batchID:         7,
		epoch:           3,
		timelineVersion: 11,
		frameIndices:    []uint64{0, 1, 2},
	}
	got := encodeRingRenderRequest(req)
	want := []byte{
		0x01,                   // tag
		7, 0, 0, 0, 0, 0, 0, 0, // batch_id LE
		3, 0, 0, 0, 0, 0, 0, 0, // epoch LE
		11, 0, 0, 0, 0, 0, 0, 0, // timeline_version LE
		3, 0, 0, 0, // frame_count LE
		0, 0, 0, 0, 0, 0, 0, 0, // frame_index 0
		1, 0, 0, 0, 0, 0, 0, 0, // frame_index 1
		2, 0, 0, 0, 0, 0, 0, 0, // frame_index 2
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeRingRenderRequest mismatch:\n got=% x\nwant=% x", got, want)
	}
}

func TestRingTransportBatchDecoding(t *testing.T) {
	src := []byte{
		0x02,                   // tag
		1, 0, 0, 0, 0, 0, 0, 0, // batch_id LE
		2, 0, 0, 0, 0, 0, 0, 0, // epoch LE
		3, 0, 0, 0, 0, 0, 0, 0, // timeline_version LE
		1, 0, 0, 0, // frame_count LE
		// frame 0
		0, 0, 0, 0, 0, 0, 0, 0, // sequence
		0xE8, 0x03, 0, 0, 0, 0, 0, 0, // pts_ns = 1000
		0, 0, 0, 0, 0, 0, 0, 0, // pixel_readback_bytes = 0
		4, 0, 0, 0, // payload_len
		0x00, 0x00, 0x01, 0x67, // payload
	}
	batch, err := decodeRingEncodedBatch(src)
	if err != nil {
		t.Fatalf("decodeRingEncodedBatch: %v", err)
	}
	if batch.batchID != 1 || batch.epoch != 2 || batch.timelineVersion != 3 {
		t.Fatalf("batch header mismatch: %+v", batch)
	}
	if len(batch.frames) != 1 {
		t.Fatalf("frame count = %d, want 1", len(batch.frames))
	}
	f := batch.frames[0]
	if f.sequence != 0 || f.ptsNs != 1000 || f.pixelReadbackBytes != 0 || !bytes.Equal(f.payload, []byte{0x00, 0x00, 0x01, 0x67}) {
		t.Fatalf("frame mismatch: %+v", f)
	}
	// Truncated payload must be rejected.
	if _, err := decodeRingEncodedBatch(src[:len(src)-1]); err == nil {
		t.Fatal("truncated payload not rejected")
	}
}
