package airplay

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestH264RTPRelayImmediatelyReplaysWithoutNewInputPacket(t *testing.T) {
	outputConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer outputConn.Close()
	inputReservation, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	inputPort := inputReservation.LocalAddr().(*net.UDPAddr).Port
	if err := inputReservation.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay, err := startH264RTPRelay(ctx, inputPort, outputConn.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	sender, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: inputPort})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	initial := [][]byte{
		testRTPPacket(1, 100, []byte{7, 1}),
		testRTPPacket(2, 100, []byte{8, 2}),
		testRTPPacket(3, 100, []byte{28, 0x85, 3}),
		testMarkedRTPPacket(4, 100, []byte{28, 0x45, 4}),
	}
	for _, packet := range initial {
		if _, err := sender.Write(packet); err != nil {
			t.Fatal(err)
		}
	}
	readRTPPackets(t, outputConn, 6)

	relay.ReplayParameterSets()
	replayed := readRTPPackets(t, outputConn, 4)
	wantTypes := []byte{7, 8, 5, 5}
	for i, want := range wantTypes {
		payload, _, ok := rtpPayload(replayed[i])
		if !ok {
			t.Fatalf("replayed output %d is malformed RTP", i)
		}
		got := payload[0] & 0x1f
		if got == 28 {
			got = payload[1] & 0x1f
		}
		if got != want {
			t.Fatalf("replayed output %d NAL type = %d; want %d", i, got, want)
		}
	}
}

func readRTPPackets(t *testing.T, conn *net.UDPConn, count int) [][]byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	packets := make([][]byte, 0, count)
	buffer := make([]byte, 65535)
	for len(packets) < count {
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			t.Fatalf("read RTP packet %d/%d: %v", len(packets)+1, count, err)
		}
		packets = append(packets, append([]byte(nil), buffer[:n]...))
	}
	return packets
}

func TestH264RTPRelayStateRepeatsParameterSetsBeforeLaterIDR(t *testing.T) {
	var state h264RTPRelayState

	initial := []struct {
		sequence uint16
		nal      byte
	}{
		{sequence: 100, nal: 7},
		{sequence: 101, nal: 8},
		{sequence: 102, nal: 5},
		{sequence: 103, nal: 1},
	}
	for _, packet := range initial {
		outputs := state.forward(testRTPPacket(packet.sequence, 42, []byte{packet.nal, 0xaa}))
		wantCount := 1
		if packet.nal == 5 {
			wantCount = 3
		}
		if len(outputs) != wantCount {
			t.Fatalf("NAL type %d produced %d packets; want %d", packet.nal, len(outputs), wantCount)
		}
	}

	outputs := state.forward(testRTPPacket(104, 99, []byte{5, 0xbb}))
	if len(outputs) != 3 {
		t.Fatalf("later IDR produced %d packets; want SPS, PPS, IDR", len(outputs))
	}
	wantTypes := []byte{7, 8, 5}
	wantSequences := []uint16{106, 107, 108}
	for i, output := range outputs {
		payload, _, ok := rtpPayload(output)
		if !ok {
			t.Fatalf("output %d is not a valid RTP packet", i)
		}
		if got := payload[0] & 0x1f; got != wantTypes[i] {
			t.Fatalf("output %d NAL type = %d; want %d", i, got, wantTypes[i])
		}
		if got := binary.BigEndian.Uint16(output[2:4]); got != wantSequences[i] {
			t.Fatalf("output %d sequence = %d; want %d", i, got, wantSequences[i])
		}
	}
}

func TestH264RTPRelayStateReplaysParameterSetsWithConstantTimestamp(t *testing.T) {
	var state h264RTPRelayState
	packets := [][]byte{
		testRTPPacket(20, 500, []byte{7, 1}),
		testRTPPacket(21, 500, []byte{8, 2}),
		testRTPPacket(22, 500, []byte{5, 3}),
	}
	wantCounts := []int{1, 1, 3}
	for i, packet := range packets {
		outputs := state.forward(packet)
		if len(outputs) != wantCounts[i] {
			t.Fatalf("packet %d produced %d outputs; want %d", i, len(outputs), wantCounts[i])
		}
	}
}

func TestH264RTPRelayStateRecognizesFUStartAsIDR(t *testing.T) {
	var state h264RTPRelayState
	state.forward(testRTPPacket(1, 1, []byte{7, 1}))
	state.forward(testRTPPacket(2, 1, []byte{8, 2}))

	fuAIDRStart := []byte{28, 0x80 | 5, 0xcc}
	outputs := state.forward(testRTPPacket(3, 2, fuAIDRStart))
	if len(outputs) != 3 {
		t.Fatalf("FU-A IDR start produced %d packets; want 3", len(outputs))
	}
}

func TestH264RTPRelayStateReassemblesFUParameterSets(t *testing.T) {
	var state h264RTPRelayState
	fragments := []struct {
		sequence uint16
		payload  []byte
	}{
		{sequence: 1, payload: []byte{0x7c, 0x80 | 7, 0x64, 0x00}},
		{sequence: 2, payload: []byte{0x7c, 0x40 | 7, 0x1f}},
		{sequence: 3, payload: []byte{0x7c, 0x80 | 8, 0xee}},
		{sequence: 4, payload: []byte{0x7c, 0x40 | 8, 0x06}},
	}
	for _, fragment := range fragments {
		outputs := state.forward(testRTPPacket(fragment.sequence, 10, fragment.payload))
		if len(outputs) != 1 {
			t.Fatalf("FU-A fragment %d produced %d packets; want 1", fragment.sequence, len(outputs))
		}
	}

	outputs := state.forward(testRTPPacket(5, 20, []byte{5, 0xbb}))
	if len(outputs) != 3 {
		t.Fatalf("IDR after FU-A parameter sets produced %d packets; want 3", len(outputs))
	}
	wantPayloads := [][]byte{{0x67, 0x64, 0x00, 0x1f}, {0x68, 0xee, 0x06}, {5, 0xbb}}
	for i, output := range outputs {
		payload, _, ok := rtpPayload(output)
		if !ok {
			t.Fatalf("output %d is not a valid RTP packet", i)
		}
		if !bytes.Equal(payload, wantPayloads[i]) {
			t.Fatalf("output %d payload = %x; want %x", i, payload, wantPayloads[i])
		}
	}
}

func TestH264RTPRelayStateWaitsForIDRToSatisfyReplay(t *testing.T) {
	var state h264RTPRelayState
	state.forward(testRTPPacket(1, 10, []byte{7, 1}))
	state.forward(testRTPPacket(2, 10, []byte{8, 2}))

	outputs, replayed := state.forwardWithReplay(testMarkedRTPPacket(3, 20, []byte{1, 3}), true)
	if replayed {
		t.Fatal("non-IDR packet unexpectedly satisfied replay request")
	}
	if len(outputs) != 0 {
		t.Fatalf("non-IDR packet produced %d packets while waiting for IDR; want none", len(outputs))
	}
	outputs, replayed = state.forwardWithReplay(testMarkedRTPPacket(4, 30, []byte{5, 4}), true)
	if !replayed {
		t.Fatal("IDR did not satisfy replay request")
	}
	if len(outputs) != 3 {
		t.Fatalf("IDR replay produced %d packets; want SPS, PPS, IDR", len(outputs))
	}
}

func TestH264RTPRelayStateReplaysCompleteCachedIDRAccessUnit(t *testing.T) {
	var state h264RTPRelayState
	state.forward(testMarkedRTPPacket(1, 100, []byte{7, 1}))
	state.forward(testMarkedRTPPacket(2, 100, []byte{8, 2}))

	idrStart := testRTPPacket(3, 100, []byte{28, 0x80 | 5, 3})
	idrEnd := testMarkedRTPPacket(4, 100, []byte{28, 0x40 | 5, 4})
	state.forward(idrStart)
	state.forward(idrEnd)
	if !state.hasCachedIDR() {
		t.Fatal("complete IDR access unit was not cached")
	}

	outputs, replayed := state.forwardWithReplay(testRTPPacket(5, 100, []byte{1, 5}), true)
	if !replayed {
		t.Fatal("replay request was not satisfied")
	}
	if len(outputs) != 4 {
		t.Fatalf("cached IDR replay produced %d packets; want SPS, PPS, and two IDR fragments", len(outputs))
	}
	wantTypes := []byte{7, 8, 28, 28}
	wantTimestamp := binary.BigEndian.Uint32(outputs[0][4:8])
	for i, output := range outputs {
		payload, _, ok := rtpPayload(output)
		if !ok {
			t.Fatalf("output %d is not a valid RTP packet", i)
		}
		if got := payload[0] & 0x1f; got != wantTypes[i] {
			t.Fatalf("output %d NAL type = %d; want %d", i, got, wantTypes[i])
		}
		if got := binary.BigEndian.Uint32(output[4:8]); got != wantTimestamp {
			t.Fatalf("output %d timestamp = %d; want %d", i, got, wantTimestamp)
		}
		if i > 0 {
			previous := binary.BigEndian.Uint16(outputs[i-1][2:4])
			if got := binary.BigEndian.Uint16(output[2:4]); got != previous+1 {
				t.Fatalf("output %d sequence = %d; want %d", i, got, previous+1)
			}
		}
	}
	if outputs[3][1]&0x80 == 0 {
		t.Fatal("last cached IDR fragment lost its RTP marker")
	}

	dropped, replayed := state.forwardWithReplay(testMarkedRTPPacket(6, 100, []byte{1, 6}), false)
	if replayed {
		t.Fatal("partial access unit tail unexpectedly satisfied a replay request")
	}
	if len(dropped) != 0 {
		t.Fatalf("partial access unit tail produced %d packets after replay; want none", len(dropped))
	}
	next := state.forward(testMarkedRTPPacket(7, 100, []byte{1, 7}))
	if len(next) != 1 {
		t.Fatalf("next complete access unit produced %d packets; want 1", len(next))
	}
	if got := binary.BigEndian.Uint32(next[0][4:8]); got <= wantTimestamp {
		t.Fatalf("next access unit timestamp = %d; want greater than replay timestamp %d", got, wantTimestamp)
	}
}

func TestH264RTPRelayStateKeepsReplayPendingUntilParameterSetsArrive(t *testing.T) {
	var state h264RTPRelayState
	if _, replayed := state.forwardWithReplay(testRTPPacket(1, 10, []byte{1, 1}), true); replayed {
		t.Fatal("replay request was satisfied without parameter sets")
	}
	if _, replayed := state.forwardWithReplay(testRTPPacket(2, 20, []byte{7, 2}), true); replayed {
		t.Fatal("replay request was satisfied without PPS")
	}
	if _, replayed := state.forwardWithReplay(testRTPPacket(3, 20, []byte{8, 3}), true); replayed {
		t.Fatal("replay request was satisfied without an IDR")
	}
	if _, replayed := state.forwardWithReplay(testMarkedRTPPacket(4, 30, []byte{5, 4}), true); !replayed {
		t.Fatal("replay request was not satisfied by a complete decoder refresh")
	}
}

func TestH264RTPRelayStateSynthesizesTimestampForConstantInput(t *testing.T) {
	state := readyH264RTPRelayState()
	want := []uint32{0, 3000, 6000}
	for i, wantTimestamp := range want {
		outputs := state.forward(testMarkedRTPPacket(uint16(i+1), 500, []byte{1, byte(i)}))
		if len(outputs) != 1 {
			t.Fatalf("frame %d produced %d packets; want 1", i, len(outputs))
		}
		if got := binary.BigEndian.Uint32(outputs[0][4:8]); got != wantTimestamp {
			t.Fatalf("frame %d timestamp = %d; want %d", i, got, wantTimestamp)
		}
	}
}

func TestH264RTPRelayStatePreservesValidTimestampDeltas(t *testing.T) {
	state := readyH264RTPRelayState()
	input := []uint32{1000, 4000, 10000}
	want := []uint32{0, 3000, 9000}
	for i, timestamp := range input {
		outputs := state.forward(testMarkedRTPPacket(uint16(i+1), timestamp, []byte{1, byte(i)}))
		if got := binary.BigEndian.Uint32(outputs[0][4:8]); got != want[i] {
			t.Fatalf("frame %d timestamp = %d; want %d (zero-based origin)", i, got, want[i])
		}
	}
}

func TestH264RTPRelayStateImmediatelyReplaysCachedIDRWithoutNewInput(t *testing.T) {
	var state h264RTPRelayState
	// UxPlay sends unmarked SPS/PPS immediately before the fragmented IDR.
	// Parameter sets must not be duplicated inside the cached access unit.
	state.forward(testRTPPacket(1, 100, []byte{7, 1}))
	state.forward(testRTPPacket(2, 100, []byte{8, 2}))
	state.forward(testRTPPacket(3, 100, []byte{28, 0x85, 3}))
	state.forward(testMarkedRTPPacket(4, 100, []byte{28, 0x45, 4}))

	outputs, replayed := state.replayCachedDecoderRefresh()
	if !replayed {
		t.Fatal("cached decoder refresh was not replayed")
	}
	if len(outputs) != 4 {
		t.Fatalf("replay produced %d packets; want SPS, PPS, and two IDR fragments", len(outputs))
	}
	wantTypes := []byte{7, 8, 5, 5}
	for i, want := range wantTypes {
		payload, _, ok := rtpPayload(outputs[i])
		if !ok {
			t.Fatalf("output %d is malformed RTP", i)
		}
		got := payload[0] & 0x1f
		if got == 28 {
			got = payload[1] & 0x1f
		}
		if got != want {
			t.Fatalf("output %d NAL type = %d; want %d", i, got, want)
		}
	}
}

func TestH264RTPRelayStateKeepsPacketsInAccessUnitAtSameTimestamp(t *testing.T) {
	state := readyH264RTPRelayState()
	first := state.forward(testRTPPacket(1, 700, []byte{1, 1}))
	last := state.forward(testMarkedRTPPacket(2, 700, []byte{1, 2}))
	next := state.forward(testMarkedRTPPacket(3, 700, []byte{1, 3}))
	if got := binary.BigEndian.Uint32(first[0][4:8]); got != 0 {
		t.Fatalf("first packet timestamp = %d; want 0 (zero-based origin)", got)
	}
	if got := binary.BigEndian.Uint32(last[0][4:8]); got != 0 {
		t.Fatalf("last packet timestamp = %d; want 0 (zero-based origin)", got)
	}
	if got := binary.BigEndian.Uint32(next[0][4:8]); got != 3000 {
		t.Fatalf("next access unit timestamp = %d; want 3000", got)
	}
}

func TestH264RTPRelayStateDropsDamagedAccessUnitAndWaitsForIDR(t *testing.T) {
	var state h264RTPRelayState
	state.forward(testMarkedRTPPacket(1, 100, []byte{7, 1}))
	state.forward(testMarkedRTPPacket(2, 100, []byte{8, 2}))
	state.forward(testMarkedRTPPacket(3, 100, []byte{5, 3}))

	if outputs := state.forward(testRTPPacket(4, 200, []byte{1, 4})); len(outputs) != 1 {
		t.Fatalf("first access-unit fragment produced %d packets; want 1", len(outputs))
	}
	if outputs := state.forward(testMarkedRTPPacket(6, 200, []byte{1, 6})); len(outputs) != 0 {
		t.Fatalf("damaged access-unit tail produced %d packets; want none", len(outputs))
	}
	if outputs := state.forward(testMarkedRTPPacket(7, 300, []byte{1, 7})); len(outputs) != 0 {
		t.Fatalf("P-frame after packet loss produced %d packets; want none before IDR", len(outputs))
	}
	outputs := state.forward(testMarkedRTPPacket(8, 400, []byte{5, 8}))
	if len(outputs) != 3 {
		t.Fatalf("recovery IDR produced %d packets; want SPS, PPS, IDR", len(outputs))
	}
	if state.inputDiscontinuities != 1 {
		t.Fatalf("input discontinuities = %d; want 1", state.inputDiscontinuities)
	}
}

func TestH264RTPRelayStateInvalidatesCachedIDROnParameterSetChange(t *testing.T) {
	var state h264RTPRelayState
	state.forward(testMarkedRTPPacket(1, 100, []byte{7, 1}))
	state.forward(testMarkedRTPPacket(2, 100, []byte{8, 1}))
	state.forward(testMarkedRTPPacket(3, 100, []byte{5, 3}))
	if !state.hasCachedIDR() {
		t.Fatal("initial IDR was not cached")
	}

	state.forward(testMarkedRTPPacket(4, 200, []byte{7, 2}))
	state.forward(testMarkedRTPPacket(5, 200, []byte{8, 2}))
	if state.hasCachedIDR() {
		t.Fatal("old-format IDR remained cached after parameter-set change")
	}
	if outputs, replayed := state.forwardWithReplay(testMarkedRTPPacket(6, 300, []byte{1, 6}), true); replayed || len(outputs) != 0 {
		t.Fatalf("P-frame after format change produced %d packets, replayed=%t; want none, false", len(outputs), replayed)
	}
	outputs, replayed := state.forwardWithReplay(testMarkedRTPPacket(7, 400, []byte{5, 7}), true)
	if !replayed || len(outputs) != 3 {
		t.Fatalf("new-format IDR produced %d packets, replayed=%t; want SPS, PPS, IDR", len(outputs), replayed)
	}
	if state.formatChanges != 1 {
		t.Fatalf("parameter-set changes = %d; want 1", state.formatChanges)
	}
}

func TestH264RTPRelayStateDropsMalformedRTP(t *testing.T) {
	var state h264RTPRelayState
	if outputs := state.forward([]byte{1, 2, 3}); outputs != nil {
		t.Fatalf("malformed RTP produced %d packets; want none", len(outputs))
	}
}

func TestH264RTPRelayStateNormalizesToZeroOrigin(t *testing.T) {
	state := readyH264RTPRelayState()
	// iOS starts its RTP timeline at an arbitrary offset (e.g. 1407000 = 15.6 s
	// at 90 kHz). The relay must discard that offset so video shares a zero
	// origin with the audio relay (asetpts=N/SR/TB); otherwise A/V drifts by
	// the offset (~3.8 s on the real device).
	input := []uint32{1407000, 1410000, 1413000}
	want := []uint32{0, 3000, 6000}
	for i, timestamp := range input {
		outputs := state.forward(testMarkedRTPPacket(uint16(i+1), timestamp, []byte{1, byte(i)}))
		if len(outputs) != 1 {
			t.Fatalf("frame %d produced %d packets; want 1", i, len(outputs))
		}
		if got := binary.BigEndian.Uint32(outputs[0][4:8]); got != want[i] {
			t.Fatalf("frame %d timestamp = %d; want %d (zero-based origin)", i, got, want[i])
		}
	}
}

func TestH264RTPRelayStateAcceptsFragmentedRecoveryIDRAfterGap(t *testing.T) {
	var state h264RTPRelayState
	state.forward(testMarkedRTPPacket(1, 100, []byte{7, 1}))
	state.forward(testMarkedRTPPacket(2, 100, []byte{8, 2}))
	state.forward(testRTPPacket(3, 100, []byte{28, 0x80 | 5, 3}))
	state.forward(testMarkedRTPPacket(4, 100, []byte{28, 0x40 | 5, 4}))

	// A sequence gap (5 skipped) is followed immediately by a fragmented
	// recovery IDR with no marked packet in between. The relay must accept the
	// IDR rather than dropping it until a marker (the marker sits on the last
	// fragment, so the old logic discarded the whole IDR and then dropped every
	// P frame until the next keyframe).
	outputs := state.forward(testRTPPacket(6, 500, []byte{28, 0x80 | 5, 6}))
	if len(outputs) != 3 {
		t.Fatalf("recovery IDR start produced %d packets; want SPS, PPS, IDR", len(outputs))
	}
	state.forward(testMarkedRTPPacket(7, 500, []byte{28, 0x40 | 5, 7}))

	if outputs := state.forward(testMarkedRTPPacket(8, 800, []byte{1, 8})); len(outputs) != 1 {
		t.Fatalf("P-frame after recovery produced %d packets; want 1", len(outputs))
	}
}

func TestH264RTPRelayStateResetsTimestampBaselineOnSSRCSwitch(t *testing.T) {
	var state h264RTPRelayState
	state.forward(testMarkedRTPPacket(1, 100, []byte{7, 1}))
	state.forward(testMarkedRTPPacket(2, 100, []byte{8, 2}))
	state.forward(testMarkedRTPPacket(3, 100, []byte{5, 3}))

	// A new stream on a different SSRC has a fresh RTP timestamp origin. The
	// relay must keep the output timeline monotonic instead of advancing it by
	// the huge delta between the old and new origins.
	newSSRC := uint32(0x87654321)
	sps := testRTPPacket(1, 500000, []byte{7, 1})
	binary.BigEndian.PutUint32(sps[8:12], newSSRC)
	state.forward(sps)
	pps := testRTPPacket(2, 500000, []byte{8, 2})
	binary.BigEndian.PutUint32(pps[8:12], newSSRC)
	state.forward(pps)
	idr := testMarkedRTPPacket(3, 500000, []byte{5, 3})
	binary.BigEndian.PutUint32(idr[8:12], newSSRC)
	outputs := state.forward(idr)
	if len(outputs) != 3 {
		t.Fatalf("new-SSRC IDR produced %d packets; want SPS, PPS, IDR", len(outputs))
	}
	if got := binary.BigEndian.Uint32(outputs[2][4:8]); got >= 500000 {
		t.Fatalf("new-SSRC IDR timestamp = %d; want a small monotonic value (huge jump detected)", got)
	}
}

func readyH264RTPRelayState() h264RTPRelayState {
	return h264RTPRelayState{
		inputSSRCReady: true,
		inputSSRC:      0x12345678,
	}
}

func testMarkedRTPPacket(sequence uint16, timestamp uint32, payload []byte) []byte {
	packet := testRTPPacket(sequence, timestamp, payload)
	packet[1] |= 0x80
	return packet
}

func testRTPPacket(sequence uint16, timestamp uint32, payload []byte) []byte {
	packet := make([]byte, 12+len(payload))
	packet[0] = 0x80
	packet[1] = 96
	binary.BigEndian.PutUint16(packet[2:4], sequence)
	binary.BigEndian.PutUint32(packet[4:8], timestamp)
	binary.BigEndian.PutUint32(packet[8:12], 0x12345678)
	copy(packet[12:], payload)
	return packet
}
func TestH264RTPRelaySignalsFormatChange(t *testing.T) {
	outputConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer outputConn.Close()
	inputReservation, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	inputPort := inputReservation.LocalAddr().(*net.UDPAddr).Port
	if err := inputReservation.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	relay, err := startH264RTPRelay(ctx, inputPort, outputConn.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	sender, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: inputPort})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()

	send := func(packet []byte) {
		if _, err := sender.Write(packet); err != nil {
			t.Fatal(err)
		}
	}
	waitSignal := func() {
		select {
		case <-relay.FormatChanges():
		case <-time.After(3 * time.Second):
			t.Fatal("expected a format-change signal")
		}
	}
	assertNoSignal := func() {
		select {
		case <-relay.FormatChanges():
			t.Fatal("unexpected format-change signal")
		case <-time.After(250 * time.Millisecond):
		}
	}

	// Phase 1: establish a stream on SSRC A. No format change has happened yet.
	send(testRTPPacket(1, 100, []byte{7, 1}))       // SPS
	send(testRTPPacket(2, 100, []byte{8, 1}))       // PPS
	send(testMarkedRTPPacket(3, 100, []byte{5, 3})) // IDR
	assertNoSignal()

	// Phase 2: an SSRC switch is an input discontinuity and must signal.
	newSSRC := uint32(0x87654321)
	withSSRC := func(packet []byte) []byte {
		binary.BigEndian.PutUint32(packet[8:12], newSSRC)
		return packet
	}
	send(withSSRC(testRTPPacket(1, 100, []byte{7, 1})))
	send(withSSRC(testRTPPacket(2, 100, []byte{8, 1})))
	send(withSSRC(testMarkedRTPPacket(3, 100, []byte{5, 3})))
	waitSignal()

	// Phase 3: a parameter-set change on the same SSRC (the real rotation:
	// the receiver emits fresh SPS/PPS without changing SSRC) must signal.
	send(withSSRC(testRTPPacket(4, 200, []byte{7, 2})))
	send(withSSRC(testRTPPacket(5, 200, []byte{8, 2})))
	send(withSSRC(testMarkedRTPPacket(6, 200, []byte{5, 4})))
	waitSignal()
}
