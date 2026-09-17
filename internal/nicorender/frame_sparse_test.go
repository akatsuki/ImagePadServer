package nicorender

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func sparseTestPacket(t *testing.T, width, height uint32, payload []byte) ([]byte, frameHeader) {
	t.Helper()
	h := frameHeader{Kind: frameKindFull, Width: width, Height: height}
	p, err := encodeFramePacket(h, make([]byte, int(width*height*4)))
	if err != nil {
		t.Fatal(err)
	}
	h.Kind = 3
	p[5] = h.Kind
	binary.LittleEndian.PutUint32(p[24:28], uint32(len(payload)))
	return append(p[:frameHeaderSize], payload...), h
}

func TestFrameSparseRunsRestoreExactPixels(t *testing.T) {
	// Each span carries absolute pixel offset, literal pixel count, RGBA.
	// Include nonzero RGB with zero alpha: the codec must remain byte-lossless.
	payload := []byte{1, 0, 0, 0, 2, 0, 0, 0, 7, 8, 9, 0, 10, 11, 12, 128}
	p, h := sparseTestPacket(t, 5, 2, payload)
	_, pixels, err := decodeFramePacket(p, h)
	if err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 40)
	copy(want[4:12], []byte{7, 8, 9, 0, 10, 11, 12, 128})
	if !bytes.Equal(pixels, want) {
		t.Fatalf("pixels=%v want=%v", pixels, want)
	}
}

func TestFrameSparseRunsRejectMalformedPayload(t *testing.T) {
	cases := map[string][]byte{
		"truncated header": {0, 0, 0, 0},
		"truncated pixels": {0, 0, 0, 0, 1, 0, 0, 0},
		"frame overflow":   {4, 0, 0, 0, 2, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8},
		"large count":      {0, 0, 0, 0, 255, 255, 255, 255},
		"trailing bytes":   {0, 0, 0, 0, 0, 0, 0, 0, 99},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			p, h := sparseTestPacket(t, 5, 1, payload)
			if _, _, err := decodeFramePacket(p, h); err == nil {
				t.Fatal("malformed sparse span accepted")
			}
		})
	}
}

func TestFrameSparseRunsRejectOverlappingSpans(t *testing.T) {
	payload := []byte{2, 0, 0, 0, 1, 0, 0, 0, 1, 2, 3, 4, 1, 0, 0, 0, 1, 0, 0, 0, 5, 6, 7, 8}
	p, h := sparseTestPacket(t, 10, 1, payload)
	if _, _, err := decodeFramePacket(p, h); err == nil {
		t.Fatal("backward sparse span accepted")
	}
}

func TestFrameSparseRunsEmptyFrame(t *testing.T) {
	p, h := sparseTestPacket(t, 5, 2, nil)
	_, pixels, err := decodeFramePacket(p, h)
	if err != nil || !bytes.Equal(pixels, make([]byte, 40)) {
		t.Fatalf("empty sparse frame: %v", err)
	}
}
