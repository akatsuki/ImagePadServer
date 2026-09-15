package obsrtmp

import (
	"bufio"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRTSPGatePreservesPLAYResponseAndFirstInterleavedRTP(t *testing.T) {
	backend := newRTSPGateTransportBackend(t, func(conn net.Conn, r *bufio.Reader) {
		for i := 0; i < 2; i++ {
			_, req, err := readRTSPPacket(r)
			if err != nil {
				return
			}
			response := "RTSP/1.0 200 OK\r\nCSeq: " + req.Headers["cseq"] + "\r\n"
			if req.Method == "SETUP" {
				response += "Transport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n"
			}
			response += "\r\n"
			if err := writeRTSPTransportFragments(conn, []byte(response)); err != nil {
				return
			}
		}
		_, play, err := readRTSPPacket(r)
		if err != nil || play.Method != "PLAY" {
			return
		}
		first := []byte{'$', 0, 0, 4, 0x80, 96, 0, 1}
		payload := append([]byte("RTSP/1.0 200 OK\r\nCSeq: "+play.Headers["cseq"]+"\r\n\r\n"), first...)
		_, _ = conn.Write(payload)
	})
	defer backend.close()
	gate, public := startRTSPGateTest(t, backend.port())
	defer stopRTSPGateTest(t, gate, public)

	client := dialRTSPGateTransport(t, public.Addr().String())
	defer client.Close()
	reader := bufio.NewReader(client)
	writeTransportRequest(t, client, "OPTIONS", "1", "")
	readTransportResponse(t, reader, "1")
	writeTransportRequest(t, client, "SETUP", "2", "Transport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n")
	readTransportResponse(t, reader, "2")
	writeTransportRequest(t, client, "PLAY", "3", "")
	got := readTransportResponse(t, reader, "3")
	if !strings.HasPrefix(string(got), "RTSP/1.0 200") {
		t.Fatalf("PLAY response = %q", got)
	}
	time.Sleep(25 * time.Millisecond)
	packet := make([]byte, 8)
	if _, err := io.ReadFull(client, packet); err != nil {
		t.Fatalf("first interleaved packet missing after PLAY response: %v", err)
	}
	if string(packet) != string([]byte{'$', 0, 0, 4, 0x80, 96, 0, 1}) {
		t.Fatalf("first interleaved packet = %x", packet)
	}
}

func TestRTSPGateDoesNotTunnelAfterFailedSETUP(t *testing.T) {
	backend := newRTSPGateTransportBackend(t, func(conn net.Conn, r *bufio.Reader) {
		for {
			_, req, err := readRTSPPacket(r)
			if err != nil {
				return
			}
			status := "200 OK"
			if req.Method == "SETUP" {
				status = "461 Unsupported Transport"
			}
			response := "RTSP/1.0 " + status + "\r\nCSeq: " + req.Headers["cseq"] + "\r\n\r\n"
			if _, err := io.WriteString(conn, response); err != nil {
				return
			}
			if req.Method == "PLAY" {
				_, _ = conn.Write([]byte{'$', 0, 0, 4, 0x80, 96, 0, 1})
				return
			}
		}
	})
	defer backend.close()
	gate, public := startRTSPGateTest(t, backend.port())
	defer stopRTSPGateTest(t, gate, public)
	client := dialRTSPGateTransport(t, public.Addr().String())
	defer client.Close()
	reader := bufio.NewReader(client)
	writeTransportRequest(t, client, "SETUP", "1", "Transport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n")
	readTransportResponse(t, reader, "1")
	writeTransportRequest(t, client, "PLAY", "2", "")
	readTransportResponse(t, reader, "2")
	if err := client.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	if n, err := client.Read(one); err == nil || n != 0 {
		t.Fatalf("failed SETUP was tunneled: n=%d err=%v", n, err)
	}
}

func TestRTSPGateDoesNotTunnelAfterNon200PLAY(t *testing.T) {
	backend := newRTSPGateTransportBackend(t, func(conn net.Conn, r *bufio.Reader) {
		for {
			_, req, err := readRTSPPacket(r)
			if err != nil {
				return
			}
			status := "200 OK"
			if req.Method == "PLAY" {
				status = "454 Session Not Found"
			}
			response := "RTSP/1.0 " + status + "\r\nCSeq: " + req.Headers["cseq"] + "\r\n\r\n"
			if _, err := io.WriteString(conn, response); err != nil {
				return
			}
			if req.Method == "PLAY" {
				_, _ = conn.Write([]byte{'$', 0, 0, 4, 0x80, 96, 0, 1})
				return
			}
		}
	})
	defer backend.close()
	gate, public := startRTSPGateTest(t, backend.port())
	defer stopRTSPGateTest(t, gate, public)
	client := dialRTSPGateTransport(t, public.Addr().String())
	defer client.Close()
	reader := bufio.NewReader(client)
	writeTransportRequest(t, client, "SETUP", "1", "Transport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n")
	readTransportResponse(t, reader, "1")
	writeTransportRequest(t, client, "PLAY", "2", "")
	response := readTransportResponse(t, reader, "2")
	if !strings.HasPrefix(string(response), "RTSP/1.0 454") {
		t.Fatalf("PLAY response = %q", response)
	}
	if err := client.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if n, err := client.Read(make([]byte, 1)); err == nil || n != 0 {
		t.Fatalf("non-200 PLAY was tunneled: n=%d err=%v", n, err)
	}
}

type rtspGateTransportBackend struct {
	listener net.Listener
	done     chan struct{}
}

func newRTSPGateTransportBackend(t *testing.T, handler func(net.Conn, *bufio.Reader)) *rtspGateTransportBackend {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &rtspGateTransportBackend{listener: listener, done: make(chan struct{})}
	go func() {
		defer close(b.done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handler(conn, bufio.NewReader(conn))
	}()
	return b
}

func (b *rtspGateTransportBackend) port() int { return b.listener.Addr().(*net.TCPAddr).Port }
func (b *rtspGateTransportBackend) close()    { _ = b.listener.Close(); <-b.done }

func dialRTSPGateTransport(t *testing.T, address string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return conn
}

func writeRTSPTransportFragments(conn net.Conn, packet []byte) error {
	for len(packet) > 0 {
		n := 1
		if len(packet) > 7 {
			n = 7
		}
		if _, err := conn.Write(packet[:n]); err != nil {
			return err
		}
		packet = packet[n:]
	}
	return nil
}

func writeTransportRequest(t *testing.T, conn net.Conn, method, cseq, headers string) {
	t.Helper()
	if _, err := io.WriteString(conn, method+" rtsp://example/live RTSP/1.0\r\nCSeq: "+cseq+"\r\n"+headers+"\r\n"); err != nil {
		t.Fatal(err)
	}
}

func readTransportResponse(t *testing.T, reader *bufio.Reader, cseq string) []byte {
	t.Helper()
	packet, err := readRTSPResponsePacket(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(packet), "CSeq: "+cseq) {
		t.Fatalf("response CSeq mismatch: %q", packet)
	}
	return packet
}
