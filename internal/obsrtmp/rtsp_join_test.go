package obsrtmp

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func joinPacket(channel byte, seq uint16, ts uint32, marker bool, nal ...byte) []byte {
	p := make([]byte, 16+len(nal))
	p[0] = '$'
	p[1] = channel
	binary.BigEndian.PutUint16(p[2:4], uint16(12+len(nal)))
	p[4] = 0x80
	p[5] = 99
	if marker {
		p[5] |= 128
	}
	binary.BigEndian.PutUint16(p[6:8], seq)
	binary.BigEndian.PutUint32(p[8:12], ts)
	p[15] = 1
	copy(p[16:], nal)
	return p
}

func TestRTSPJoinDropsPartialFirstAUAndWaitsCompleteFragmentedIDR(t *testing.T) {
	first := joinPacket(2, 1, 1, true, 0x65, 0x80) // Join may be halfway through a multi-slice IDR.
	p := joinPacket(2, 2, 2, true, 0x41, 0x80)
	start := joinPacket(2, 3, 3, false, 0x7c, 0x85, 0x80)
	end := joinPacket(2, 4, 3, true, 0x7c, 0x45, 0x80)
	audio := joinPacket(0, 8, 9, true, 0x01)
	audio[5] = 96
	tail := joinPacket(2, 5, 4, true, 0x41, 0x80)
	input := bytes.Join([][]byte{first, p, start, audio, end, tail}, nil)
	reader := bufio.NewReader(bytes.NewReader(input))
	var output bytes.Buffer
	err := copyUntilH264IDR(&output, reader, 99, map[byte]bool{0: true, 2: true}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(&output, reader)
	want := bytes.Join([][]byte{audio, start, end, tail}, nil)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("non-IDR/partial AU leaked or timestamps changed: got %x want %x", output.Bytes(), want)
	}
}

func TestRTSPJoinRejectsIncompleteIDRAndMemoryOverflow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts [][]byte
		limit int
	}{
		{"missing-fragment", [][]byte{joinPacket(2, 1, 1, true, 0x41), joinPacket(2, 2, 2, false, 0x7c, 0x85, 1), joinPacket(2, 4, 2, true, 0x7c, 0x45, 2)}, 1024},
		{"missing-end", [][]byte{joinPacket(2, 1, 1, true, 0x41), joinPacket(2, 2, 2, true, 0x7c, 0x85, 1)}, 1024},
		{"limit", [][]byte{joinPacket(2, 1, 1, true, 0x41), joinPacket(2, 2, 2, true, 0x65, 1)}, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := copyUntilH264IDR(&out, bufio.NewReader(bytes.NewReader(bytes.Join(tc.parts, nil))), 99, map[byte]bool{2: true}, tc.limit)
			if err == nil || out.Len() != 0 {
				t.Fatalf("unsafe bootstrap accepted: err=%v bytes=%d", err, out.Len())
			}
		})
	}
}

func TestRTSPJoinPreservesControlAndRTCPWhileWaiting(t *testing.T) {
	control := []byte("RTSP/1.0 200 OK\r\nCSeq: 9\r\nContent-Length: 3\r\n\r\nabc")
	rtcp := []byte{'$', 3, 0, 4, 0x80, 200, 0, 0}
	var out bytes.Buffer
	err := copyUntilH264IDR(&out, bufio.NewReader(bytes.NewReader(append(control, rtcp...))), 99, map[byte]bool{2: true}, 1024)
	if err == nil || !bytes.Equal(out.Bytes(), append(control, rtcp...)) {
		t.Fatal("control/RTCP changed or EOF ignored")
	}
}

func TestRTSPJoinSDPRequiresUnambiguousH264AndParameterSets(t *testing.T) {
	good := "m=audio 0 RTP/AVP 96\r\na=rtpmap:96 mpeg4-generic/48000/2\r\nm=video 0 RTP/AVP 99\r\na=rtpmap:99 H264/90000\r\na=fmtp:99 packetization-mode=1; sprop-parameter-sets=Z0LAHtoCgL/lwFqDAwNSgAAAAwCAAAAeR4sXUA==,aM48gA==\r\n"
	if pt, err := h264JoinPayload([]byte(good)); err != nil || pt != 99 {
		t.Fatalf("valid SDP rejected: %v %d", err, pt)
	}
	for _, bad := range []string{"m=video 0 RTP/AVP 99\r\na=rtpmap:99 H265/90000\r\n", good + "m=audio 0 RTP/AVP 99\r\n", "m=video 0 RTP/AVP 99\r\na=rtpmap:99 H264/90000\r\n"} {
		if _, err := h264JoinPayload([]byte(bad)); err == nil {
			t.Fatal("unsafe SDP accepted")
		}
	}
}

func TestRTSPJoinSTAPAAndSequenceWrap(t *testing.T) {
	first := joinPacket(8, 65535, 100, true, 0x41, 1)
	idr := joinPacket(8, 0, 200, true, 0x78, 0, 2, 0x67, 1, 0, 2, 0x68, 1, 0, 2, 0x65, 1)
	var out bytes.Buffer
	if err := copyUntilH264IDR(&out, bufio.NewReader(bytes.NewReader(append(first, idr...))), 99, map[byte]bool{8: true}, 1024); err != nil || !bytes.Equal(out.Bytes(), idr) {
		t.Fatalf("STAP-A/wrap failed: %v", err)
	}
	for _, nal := range [][]byte{{0x78, 0, 3, 0x65}, {0x78, 0, 2, 0x65, 1, 0, 2, 0x41, 1}, {0x7c, 0xc5, 1}} {
		var au h264JoinAU
		if au.nal(nal) {
			t.Fatalf("malformed or mixed AU accepted: %x", nal)
		}
	}
}

func TestRTSPJoinNegotiatedChannel(t *testing.T) {
	response := []byte("RTSP/1.0 200 OK\r\nTransport: RTP/AVP/TCP;unicast;interleaved=8-9\r\n\r\n")
	if channel, ok := rtspJoinChannel(response); !ok || channel != 8 {
		t.Fatal("channel negotiation ignored")
	}
	for _, transport := range []string{"RTP/AVP;interleaved=8-9", "RTP/AVP/TCP;interleaved=8-8", "RTP/AVP/TCP;interleaved=999-1"} {
		if _, ok := rtspJoinChannel([]byte("Transport: " + transport + "\r\n")); ok {
			t.Fatal("invalid transport accepted")
		}
	}
}
