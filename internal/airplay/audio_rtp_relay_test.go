package airplay

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestL16RTPRelayMaintainsClockAndForwardsAudio(t *testing.T) {
	output, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	inputPort := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()

	relay, err := startL16RTPRelay(context.Background(), inputPort, output.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	relay.NotifyVideoActivity()

	source, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: inputPort})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	payload := bytes.Repeat([]byte{0x12, 0x34}, l16FramesPerPacket*l16Channels)
	input := make([]byte, 12+len(payload))
	input[0], input[1] = 0x80, l16PayloadType
	copy(input[12:], payload)
	for i := 0; i < l16StartupQueuePackets; i++ {
		if _, err := source.Write(input); err != nil {
			t.Fatal(err)
		}
	}
	forwarded := false
	var first, second []byte
	for i := 0; i < l16StartupQueuePackets; i++ {
		packet := readL16Packet(t, output)
		if i == 0 {
			first = packet
		}
		if i == 1 {
			second = packet
		}
		if bytes.Equal(packet[12:], payload) {
			forwarded = true
		}
	}
	if !forwarded {
		t.Fatal("real L16 payload was not forwarded")
	}
	if got, want := binary.BigEndian.Uint16(second[2:4]), binary.BigEndian.Uint16(first[2:4])+1; got != want {
		t.Fatalf("sequence = %d, want %d", got, want)
	}
	if got, want := binary.BigEndian.Uint32(second[4:8]), binary.BigEndian.Uint32(first[4:8])+l16FramesPerPacket; got != want {
		t.Fatalf("timestamp = %d, want %d", got, want)
	}
	inputCount, outputCount, silenceCount := relay.Stats()
	if inputCount != l16StartupQueuePackets || outputCount < l16StartupQueuePackets || silenceCount != 0 {
		t.Fatalf("stats input=%d output=%d silence=%d", inputCount, outputCount, silenceCount)
	}
}

func TestL16RTPRelayDropsUnexpectedPayloadType(t *testing.T) {
	output, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	probe, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	inputPort := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()
	relay, err := startL16RTPRelay(context.Background(), inputPort, output.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	source, _ := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: inputPort})
	defer source.Close()
	packet := make([]byte, 16)
	packet[0], packet[1] = 0x80, l16PayloadType+1
	source.Write(packet)
	time.Sleep(30 * time.Millisecond)
	input, _, _ := relay.Stats()
	if input != 0 {
		t.Fatalf("accepted unexpected payload type: %d packets", input)
	}
	if pt, ok := relay.UnsupportedCodecPT(); !ok || pt != l16PayloadType+1 {
		t.Fatalf("unsupported codec = %d, ok=%t; want %d, true", pt, ok, l16PayloadType+1)
	}
}

func readL16Packet(t *testing.T, conn *net.UDPConn) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), buf[:n]...)
}

func TestL16RTPRelayLeavesOutputPairAvailable(t *testing.T) {
	input, err := reserveRTPPortPair()
	if err != nil {
		t.Fatal(err)
	}
	inputPort := input.port
	input.close()
	output, err := reserveRTPPortPair()
	if err != nil {
		t.Fatal(err)
	}
	outputPort := output.port
	output.close()
	relay, err := startL16RTPRelay(context.Background(), inputPort, outputPort)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	rtp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: outputPort})
	if err != nil {
		t.Fatalf("bind output RTP: %v", err)
	}
	defer rtp.Close()
	rtcp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: outputPort + 1})
	if err != nil {
		t.Fatalf("bind output RTCP: %v", err)
	}
	defer rtcp.Close()
}

func TestL16RTPRelayStartsSilenceWhenVideoArrivesWithoutAudio(t *testing.T) {
	input, err := reserveRTPPortPair()
	if err != nil {
		t.Fatal(err)
	}
	inputPort := input.port
	input.close()
	output, err := reserveRTPPortPair()
	if err != nil {
		t.Fatal(err)
	}
	outputPort := output.port
	output.close()

	relay, err := startL16RTPRelay(context.Background(), inputPort, outputPort)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	rtp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: outputPort})
	if err != nil {
		t.Fatalf("bind output RTP: %v", err)
	}
	defer rtp.Close()

	relay.NotifyVideoActivity()
	packet := readL16Packet(t, rtp)
	if len(packet) != 12+l16PacketBytes {
		t.Fatalf("silence packet length = %d; want %d", len(packet), 12+l16PacketBytes)
	}
	if packet[1]&0x7f != l16PayloadType || packet[1]&0x80 != 0 {
		t.Fatalf("silence packet payload type/marker = %#x; want PT %d without marker", packet[1], l16PayloadType)
	}
	if !bytes.Equal(packet[12:], make([]byte, l16PacketBytes)) {
		t.Fatal("startup packet was not silence")
	}
}

func TestL16RTPRelayStateKeepsOutputClockAtNominalRate(t *testing.T) {
	state := newL16RTPRelayState()
	// The input RTP timestamps can jump when UxPlay delivers a burst or loses
	// packets. They must not change playback speed: the repacketizer's output
	// clock advances by exactly one L16 packet per emitted packet.
	full := bytes.Repeat([]byte{0x12, 0x34, 0x56, 0x78}, l16FramesPerPacket)
	state.ingest(1000, full, l16MaxQueueBytes)
	first, firstSilence := state.emit()
	state.ingest(2000, full, l16MaxQueueBytes)
	second, secondSilence := state.emit()
	if firstSilence || secondSilence {
		t.Fatal("real packets emitted as silence")
	}
	if got := binary.BigEndian.Uint32(first[4:8]); got != 0 {
		t.Fatalf("first timestamp = %d; want 0", got)
	}
	if got := binary.BigEndian.Uint32(second[4:8]); got != l16FramesPerPacket {
		t.Fatalf("second timestamp = %d; want %d (nominal packet step)", got, l16FramesPerPacket)
	}
}

func TestL16RTPRelayStateDoesNotRewindAfterSilence(t *testing.T) {
	state := newL16RTPRelayState()
	full := bytes.Repeat([]byte{0x12, 0x34, 0x56, 0x78}, l16FramesPerPacket)
	state.ingest(1000, full, l16MaxQueueBytes)
	first, firstSilence := state.emit()
	if firstSilence {
		t.Fatal("real packet emitted as silence")
	}
	state.emit()
	state.emit()
	state.ingest(1000, full, l16MaxQueueBytes)
	resumed, resumedSilence := state.emit()
	if resumedSilence {
		t.Fatal("resumed real packet emitted as silence")
	}
	if got, want := binary.BigEndian.Uint32(resumed[4:8]), binary.BigEndian.Uint32(first[4:8])+3*l16FramesPerPacket; got != want {
		t.Fatalf("resumed timestamp = %d; want %d after silence", got, want)
	}
}

func TestL16RTPRelayPacesBurstAudio(t *testing.T) {
	output, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	inputPort := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()

	relay, err := startL16RTPRelay(context.Background(), inputPort, output.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	relay.NotifyVideoActivity()

	source, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: inputPort})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	payload := bytes.Repeat([]byte{0x12, 0x34, 0x56, 0x78}, l16FramesPerPacket)
	input := make([]byte, 12+len(payload))
	input[0], input[1] = 0x80, l16PayloadType
	copy(input[12:], payload)
	for i := 0; i < 4; i++ {
		if _, err := source.Write(input); err != nil {
			t.Fatal(err)
		}
	}

	arrival := make([]time.Time, 4)
	for i := range arrival {
		_ = readL16Packet(t, output)
		arrival[i] = time.Now()
	}
	for i := 2; i < len(arrival); i++ {
		if gap := arrival[i].Sub(arrival[i-1]); gap < l16PacketInterval/2 {
			t.Fatalf("burst packet %d arrived after %s; want pacing near %s", i, gap, l16PacketInterval)
		}
	}
}

func TestL16RTPRelayDropsLeadInWhenVideoStarts(t *testing.T) {
	output, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	inputPort := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()

	relay, err := startL16RTPRelay(context.Background(), inputPort, output.LocalAddr().(*net.UDPAddr).Port)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	source, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: inputPort})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	packet := func(fill byte) []byte {
		payload := bytes.Repeat([]byte{fill}, l16PacketBytes)
		input := make([]byte, 12+len(payload))
		input[0], input[1] = 0x80, l16PayloadType
		copy(input[12:], payload)
		return input
	}
	for i := 0; i < 4; i++ {
		if _, err := source.Write(packet(0x11)); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		inputCount, _, _ := relay.Stats()
		if inputCount >= 4 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	relay.NotifyVideoActivity()
	for i := 0; i < 4; i++ {
		if _, err := source.Write(packet(0x22)); err != nil {
			t.Fatal(err)
		}
	}

	oldPayload := bytes.Repeat([]byte{0x11}, l16PacketBytes)
	newPayload := bytes.Repeat([]byte{0x22}, l16PacketBytes)
	for i := 0; i < 8; i++ {
		got := readL16Packet(t, output)
		if bytes.Equal(got[12:], oldPayload) {
			t.Fatal("audio buffered before video start was replayed")
		}
		if bytes.Equal(got[12:], newPayload) {
			return
		}
	}
	t.Fatal("post-video audio was not emitted")
}

func TestAudioCodecLabel(t *testing.T) {
	if got := audioCodecLabel(nil); got != "" {
		t.Fatalf("nil label = %q, want empty", got)
	}
	r := &l16RTPRelay{}
	if got := audioCodecLabel(r); got != "l16" {
		t.Fatalf("default label = %q, want l16", got)
	}
	r.unsupportedPayloadType.Store(l16PayloadType + 1)
	if got := audioCodecLabel(r); got != "unsupported(pt=97)" {
		t.Fatalf("unsupported label = %q, want unsupported(pt=97)", got)
	}
}
