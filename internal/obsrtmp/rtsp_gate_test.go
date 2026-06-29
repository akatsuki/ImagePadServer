package obsrtmp

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRTSPGateRejectsWindowsMediaFoundationUDPSetup(t *testing.T) {
	req := rtspRequest{
		Method: "SETUP",
		Headers: map[string]string{
			"user-agent": "WMPlayer/12.00.26100.8655 guid/3300AD50-2C39-46C0-AE0A",
			"transport":  "RTP/AVP/UDP;unicast;client_port=54184-54185;mode=PLAY",
		},
	}

	if !shouldRejectUDPSetup(req) {
		t.Fatal("Windows Media Foundation RTSP/UDP SETUP should be rejected so it can retry TCP")
	}
}

func TestRTSPGateAllowsAndroidUDPSetup(t *testing.T) {
	req := rtspRequest{
		Method: "SETUP",
		Headers: map[string]string{
			"user-agent": "stagefright/1.2 (Linux;Android)",
			"transport":  "RTP/AVP/UDP;unicast;client_port=39000-39001;mode=PLAY",
		},
	}

	if shouldRejectUDPSetup(req) {
		t.Fatal("Android RTSP/UDP SETUP should be allowed")
	}
}

func TestRewriteUDPSetupClientPorts(t *testing.T) {
	packet, err := rewriteUDPSetupRequest(
		[]byte("SETUP rtsp://example/live/trackID=0 RTSP/1.0\r\nCSeq: 2\r\nTransport: RTP/AVP/UDP;unicast;client_port=39000-39001;mode=PLAY\r\n\r\n"),
		41000,
		41001,
	)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	text := string(packet)
	if !strings.Contains(text, "client_port=41000-41001") {
		t.Fatalf("rewritten request missing proxy client ports:\n%s", text)
	}
	if strings.Contains(text, "client_port=39000-39001") {
		t.Fatalf("rewritten request leaked original client ports:\n%s", text)
	}
}

func TestRewriteUDPSetupResponseServerPorts(t *testing.T) {
	packet := rewriteUDPSetupResponse(
		[]byte("RTSP/1.0 200 OK\r\nCSeq: 2\r\nTransport: RTP/AVP/UDP;unicast;client_port=41000-41001;server_port=59000-59001;ssrc=1234\r\n\r\n"),
		59520,
		59521,
	)
	text := string(packet)
	if !strings.Contains(text, "server_port=59520-59521") {
		t.Fatalf("rewritten response missing public server ports:\n%s", text)
	}
	if strings.Contains(text, "server_port=59000-59001") {
		t.Fatalf("rewritten response leaked backend server ports:\n%s", text)
	}
}

func TestParseRTSPClientPorts(t *testing.T) {
	rtp, rtcp, ok := parseRTSPClientPorts("RTP/AVP/UDP;unicast;client_port=39000-39001;mode=PLAY")
	if !ok || rtp != 39000 || rtcp != 39001 {
		t.Fatalf("client ports = %d/%d/%v, want 39000/39001/true", rtp, rtcp, ok)
	}
}

func TestReadRTSPPacketPreservesContentLengthBody(t *testing.T) {
	const body = "v=0\r\nm=video 0 RTP/AVP 96\r\n"
	packet, err := readRTSPResponsePacket(bufio.NewReader(strings.NewReader(
		"RTSP/1.0 200 OK\r\nCSeq: 1\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body,
	)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(packet), body) {
		t.Fatalf("packet body was not preserved:\n%s", string(packet))
	}
}

func TestRTSPGateUDPRelayForwardsBackendPacketToClient(t *testing.T) {
	client, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	clientAddr := client.LocalAddr().(*net.UDPAddr)

	proxy, err := newRTSPUDPPair(net.ParseIP("127.0.0.1"), clientAddr.Port, clientAddr.Port+1, 59520, 59521)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()

	backend, err := net.Dial("udp4", proxy.rtpConn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if _, err := backend.Write([]byte("rtp")); err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 16)
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _, err := client.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "rtp" {
		t.Fatalf("forwarded packet = %q, want rtp", got)
	}
}
