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
	first := readL16Packet(t, output)
	second := readL16Packet(t, output)
	if got, want := binary.BigEndian.Uint16(second[2:4]), binary.BigEndian.Uint16(first[2:4])+1; got != want {
		t.Fatalf("sequence = %d, want %d", got, want)
	}
	if got, want := binary.BigEndian.Uint32(second[4:8]), binary.BigEndian.Uint32(first[4:8])+l16FramesPerPacket; got != want {
		t.Fatalf("timestamp = %d, want %d", got, want)
	}
	if !bytes.Equal(first[12:], make([]byte, len(first)-12)) {
		t.Fatal("initial packet is not silence")
	}

	source, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: inputPort})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	payload := bytes.Repeat([]byte{0x12, 0x34}, l16FramesPerPacket*l16Channels)
	input := make([]byte, 12+len(payload))
	input[0], input[1] = 0x80, l16PayloadType
	copy(input[12:], payload)
	if _, err := source.Write(input); err != nil {
		t.Fatal(err)
	}
	relay.NotifyVideoActivity()
	forwarded := false
	for i := 0; i < 8; i++ {
		packet := readL16Packet(t, output)
		if bytes.Equal(packet[12:], payload) {
			forwarded = true
			break
		}
	}
	if !forwarded {
		t.Fatal("real L16 payload was not forwarded")
	}
	inputCount, outputCount, silenceCount := relay.Stats()
	if inputCount != 1 || outputCount < 3 || silenceCount < 2 {
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
