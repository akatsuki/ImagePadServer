package airplay

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestL16RTPRelayStateFrameAlignedTrim(t *testing.T) {
	s := newL16RTPRelayState()
	// A payload with a partial trailing frame (3 bytes short of a 4-byte frame)
	// must be truncated to a whole-frame boundary on ingest.
	payload := bytes.Repeat([]byte{0xAB}, l16PacketBytes+3)
	s.ingest(0, payload, l16MaxQueueBytes)
	if got := len(s.queue); got != l16PacketBytes {
		t.Fatalf("queue length after partial-frame ingest = %d; want %d", got, l16PacketBytes)
	}
	packet, isSilence := s.emit()
	if isSilence {
		t.Fatal("expected real audio, got silence")
	}
	if got := len(packet); got != 12+l16PacketBytes {
		t.Fatalf("packet length = %d; want %d", got, 12+l16PacketBytes)
	}
}

func TestL16RTPRelayStateTrimKeepsFrameBoundary(t *testing.T) {
	s := newL16RTPRelayState()
	packet := make([]byte, l16PacketBytes)
	for i := 0; i < 20; i++ {
		s.ingest(0, packet, l16MaxQueueBytes)
	}
	if got := len(s.queue); got != l16MaxQueueBytes {
		t.Fatalf("queue length = %d; want %d", got, l16MaxQueueBytes)
	}
	if got := len(s.queue) % l16FrameBytes; got != 0 {
		t.Fatalf("queue length %d is not frame-aligned", got)
	}
}

func TestL16RTPRelayStateMarkerOnTalkSpurt(t *testing.T) {
	s := newL16RTPRelayState()
	// Initial silence: no marker.
	p, isSilence := s.emit()
	if !isSilence || p[1]&0x80 != 0 {
		t.Fatalf("initial silence: isSilence=%t marker=%d; want silence without marker", isSilence, p[1]&0x80)
	}
	// First talk-spurt: marker set.
	s.ingest(0, make([]byte, l16PacketBytes), l16MaxQueueBytes)
	p, isSilence = s.emit()
	if isSilence || p[1]&0x80 == 0 {
		t.Fatalf("first talk-spurt: isSilence=%t marker=%d; want audio with marker", isSilence, p[1]&0x80)
	}
	// Continuing audio: no marker.
	s.ingest(0, make([]byte, l16PacketBytes), l16MaxQueueBytes)
	p, _ = s.emit()
	if p[1]&0x80 != 0 {
		t.Fatal("continuing audio must not set the marker")
	}
	// Underflow to silence: no marker.
	p, isSilence = s.emit()
	if !isSilence || p[1]&0x80 != 0 {
		t.Fatal("silence after audio must not set the marker")
	}
	// Second talk-spurt: marker set again.
	s.ingest(0, make([]byte, l16PacketBytes), l16MaxQueueBytes)
	p, isSilence = s.emit()
	if isSilence || p[1]&0x80 == 0 {
		t.Fatal("second talk-spurt must set the marker")
	}
}

func TestL16RTPRelayStateInputDrivenEmission(t *testing.T) {
	s := newL16RTPRelayState()
	// Fewer than a full packet must not form a full packet (input-driven).
	s.ingest(0, make([]byte, l16PacketBytes-4), l16MaxQueueBytes)
	if s.hasFullPacket() {
		t.Fatal("partial input must not form a full packet")
	}
	// Completing the packet triggers a full packet.
	s.ingest(0, make([]byte, 4), l16MaxQueueBytes)
	if !s.hasFullPacket() {
		t.Fatal("completed input must form a full packet")
	}
	packet, isSilence := s.emit()
	if isSilence {
		t.Fatal("expected real audio after a full packet was buffered")
	}
	if got := binary.BigEndian.Uint32(packet[4:8]); got != 0 {
		t.Fatalf("first output timestamp = %d; want 0", got)
	}
}

func TestL16RTPRelayStateClockAdvancesPerPacket(t *testing.T) {
	s := newL16RTPRelayState()
	s.ingest(0, make([]byte, l16PacketBytes*3), l16MaxQueueBytes)
	var lastTS uint32
	var lastSeq uint16
	for i := 0; i < 3; i++ {
		packet, _ := s.emit()
		ts := binary.BigEndian.Uint32(packet[4:8])
		seq := binary.BigEndian.Uint16(packet[2:4])
		if i > 0 {
			if ts != lastTS+l16FramesPerPacket {
				t.Fatalf("packet %d timestamp = %d; want %d", i, ts, lastTS+l16FramesPerPacket)
			}
			if seq != lastSeq+1 {
				t.Fatalf("packet %d sequence = %d; want %d", i, seq, lastSeq+1)
			}
		}
		lastTS, lastSeq = ts, seq
	}
}

func TestL16RTPRelayStateLeadInBuffering(t *testing.T) {
	s := newL16RTPRelayState()
	filler := func(b byte) []byte { return bytes.Repeat([]byte{b}, l16PacketBytes) }
	// Three packets buffered before video starts (larger lead-in bound).
	s.ingest(0, filler(0x01), l16LeadInMaxBytes)
	s.ingest(0, filler(0x02), l16LeadInMaxBytes)
	s.ingest(0, filler(0x03), l16LeadInMaxBytes)
	if !s.hasFullPacket() {
		t.Fatal("lead-in audio must be buffered")
	}
	// Replay from the head: the first emitted packet is the oldest audio.
	packet, isSilence := s.emit()
	if isSilence {
		t.Fatal("replay must emit real audio, not silence")
	}
	if !bytes.Equal(packet[12:], filler(0x01)) {
		t.Fatal("replay must begin at the head (oldest) of the lead-in buffer")
	}
}
