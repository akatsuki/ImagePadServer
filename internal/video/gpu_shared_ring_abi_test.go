package video

import "testing"

func TestSharedRingHeaderRoundTrip(t *testing.T) {
	b := make([]byte, GPUSharedRingHeaderSize)
	want := SharedRingHeader{Magic: GPUSharedRingMagic, Version: 1, Capacity: 4, SlotBytes: 1024, WriteSeq: 7, ReadSeq: 4}
	if err := want.Encode(b); err != nil { t.Fatal(err) }
	got, err := DecodeSharedRingHeader(b); if err != nil { t.Fatal(err) }
	if got != want { t.Fatalf("got %#v want %#v", got, want) }
}

func TestSharedRingHeaderRejectsOverrun(t *testing.T) {
	b := make([]byte, GPUSharedRingHeaderSize)
	h := SharedRingHeader{Magic: GPUSharedRingMagic, Version: 1, Capacity: 2, SlotBytes: 10, WriteSeq: 3, ReadSeq: 0}
	if err := h.Encode(b); err != nil { t.Fatal(err) }
	if _, err := DecodeSharedRingHeader(b); err == nil { t.Fatal("expected overrun rejection") }
}
