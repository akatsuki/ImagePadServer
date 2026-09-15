package rtspdiagnostic

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func rtp(seq uint16, ts uint32, marker bool, payload []byte) []byte {
	b := make([]byte, 12+len(payload))
	b[0] = 0x80
	if marker {
		b[1] = 0x80 | 96
	} else {
		b[1] = 96
	}
	binary.BigEndian.PutUint16(b[2:4], seq)
	binary.BigEndian.PutUint32(b[4:8], ts)
	copy(b[12:], payload)
	return b
}

func TestNewCaptureDirIsUniqueAndPersistsMetadata(t *testing.T) {
	root := t.TempDir()
	a, ma, err := NewCaptureDir(root, "run", "direct-rtsp")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := NewCaptureDir(root, "run", "direct-rtsp")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("capture directories collided")
	}
	ma.ExitCode = 4
	if err := ma.Write(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a, "metadata.json")); err != nil {
		t.Fatal(err)
	}
}

func TestParseRTPAcceptsExtensionAndPadding(t *testing.T) {
	p := make([]byte, 12+4+4+4+1+1)
	p[0] = 0xB1 // v2, extension, one CSRC
	p[1] = 96
	binary.BigEndian.PutUint16(p[2:4], 7)
	binary.BigEndian.PutUint32(p[4:8], 9)
	binary.BigEndian.PutUint32(p[8:12], 11)
	binary.BigEndian.PutUint32(p[12:16], 12)
	binary.BigEndian.PutUint16(p[16:18], 1)
	binary.BigEndian.PutUint16(p[18:20], 1)
	p[24] = 0x65
	p[len(p)-1] = 1
	p[0] |= 0x20 // padding
	got, err := ParseRTP(p)
	if err != nil || got.Sequence != 7 || len(got.Payload) != 1 || got.Payload[0] != 0x65 {
		t.Fatalf("parse = %#v, %v", got, err)
	}
}

func TestH264AssemblerHandlesSingleSTAPAndFUA(t *testing.T) {
	a := NewAssembler()
	packets := [][]byte{
		rtp(10, 100, false, []byte{0x67, 0x01}),
		rtp(11, 100, false, []byte{24, 0, 2, 0x68, 0x02, 0, 2, 0x65, 0x03}),
		rtp(12, 100, false, []byte{0x7c, 0x85, 0xaa}),
		rtp(13, 100, true, []byte{0x7c, 0x45, 0xbb}),
	}
	var aus []AccessUnit
	for _, p := range packets {
		packet, _ := ParseRTP(p)
		aus = append(aus, a.Push(packet)...)
	}
	if len(aus) != 1 || aus[0].Complete != true || len(aus[0].NALs) != 4 {
		t.Fatalf("AUs = %#v", aus)
	}
}

func TestH264AssemblerRejectsGapOversizeAndEOF(t *testing.T) {
	a := NewAssembler(WithMaxNALSize(3))
	p, _ := ParseRTP(rtp(1, 1, false, []byte{0x7c, 0x85, 1}))
	if got := a.Push(p); len(got) != 0 {
		t.Fatal("start fragment emitted an AU")
	}
	p, _ = ParseRTP(rtp(3, 1, true, []byte{0x7c, 0x45, 2}))
	if got := a.Push(p); len(got) != 0 {
		t.Fatal("gapped fragment emitted an AU")
	}
	if got := a.Flush(); len(got) != 0 {
		t.Fatal("EOF emitted an incomplete AU")
	}
	if a.Stats().IncompleteAUs == 0 || a.Stats().SequenceGaps == 0 {
		t.Fatalf("stats = %#v", a.Stats())
	}
}

func TestH264AssemblerDoesNotCompleteAUAfterNormalPacketGap(t *testing.T) {
	a := NewAssembler()
	for _, raw := range [][]byte{rtp(10, 2, false, []byte{1, 1}), rtp(12, 2, true, []byte{1, 2})} {
		p, _ := ParseRTP(raw)
		if got := a.Push(p); len(got) != 0 {
			t.Fatal("gapped AU was emitted")
		}
	}
	if a.Stats().SequenceGaps != 1 {
		t.Fatalf("stats = %#v", a.Stats())
	}
}

func TestSequenceAndTimestampWrapAreNotDrops(t *testing.T) {
	a := NewAssembler()
	for _, p := range [][]byte{rtp(65535, ^uint32(0)-1, false, []byte{1, 1}), rtp(0, 0, true, []byte{1, 2})} {
		packet, _ := ParseRTP(p)
		a.Push(packet)
	}
	if got := a.Stats(); got.SequenceGaps != 0 || got.TimestampWraps != 1 {
		t.Fatalf("stats = %#v", got)
	}
}
