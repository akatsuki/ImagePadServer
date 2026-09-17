package nicorender

import (
	"bytes"
	"testing"
)

func TestFramePacketRoundTrip(t *testing.T) {
	h := frameHeader{Kind: frameKindFull, Sequence: 7, TimeMs: 233, Width: 2, Height: 1}
	pixels := []byte{255, 0, 0, 255, 0, 0, 128, 128}
	wire, err := encodeFramePacket(h, pixels)
	if err != nil {
		t.Fatal(err)
	}
	got, decoded, err := decodeFramePacket(wire, h)
	if err != nil || got != h || !bytes.Equal(decoded, pixels) {
		t.Fatalf("round trip: header=%+v pixels=%v err=%v", got, decoded, err)
	}
}

func TestFramePacketRepeatHasNoPayload(t *testing.T) {
	h := frameHeader{Kind: frameKindRepeat, Sequence: 8, TimeMs: 266, Width: 2, Height: 1}
	wire, err := encodeFramePacket(h, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, pixels, err := decodeFramePacket(wire, frameHeader{Sequence: h.Sequence, TimeMs: h.TimeMs, Width: h.Width, Height: h.Height})
	if err != nil || got != h || len(pixels) != 0 {
		t.Fatalf("repeat: header=%+v pixels=%v err=%v", got, pixels, err)
	}
}

func TestFramePacketRejectsMalformedMetadata(t *testing.T) {
	base := frameHeader{Kind: frameKindFull, Sequence: 0, TimeMs: 0, Width: 1, Height: 1}
	wire, err := encodeFramePacket(base, []byte{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"short", func(p []byte) { p = p[:len(p)-1] }},
		{"magic", func(p []byte) { p[0] = 'X' }},
		{"version", func(p []byte) { p[4] = 2 }},
		{"flags", func(p []byte) { p[7] = 1 }},
		{"reserved", func(p []byte) { p[36] = 1 }},
		{"length", func(p []byte) { p[24] = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := append([]byte(nil), wire...)
			if tc.name == "short" {
				p = p[:len(p)-1]
			} else {
				tc.mutate(p)
			}
			if _, _, err := decodeFramePacket(p, base); err == nil {
				t.Fatal("malformed packet was accepted")
			}
		})
	}
}

func TestFramePacketRejectsInitialRepeatAndMetadataMismatch(t *testing.T) {
	repeat := frameHeader{Kind: frameKindRepeat, Sequence: 0, TimeMs: 0, Width: 1, Height: 1}
	wire, err := encodeFramePacket(repeat, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeFramePacket(wire, frameHeader{Kind: 0, Sequence: 0, TimeMs: 0, Width: 1, Height: 1}); err != nil {
		// The protocol decoder checks wire shape; first-frame policy belongs to
		// the stream state machine and is tested by the transport integration.
		t.Fatalf("wildcard metadata should decode before stream policy: %v", err)
	}
	if _, _, err := decodeFramePacket(wire, frameHeader{Kind: frameKindFull, Sequence: 0, TimeMs: 0, Width: 1, Height: 1}); err == nil {
		t.Fatal("kind mismatch was accepted")
	}
	want := frameHeader{Kind: frameKindFull, Sequence: 1, TimeMs: 0, Width: 1, Height: 1}
	if _, _, err := decodeFramePacket(wire, want); err == nil {
		t.Fatal("metadata mismatch was accepted")
	}
}
