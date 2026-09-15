package obsrtmp

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRTSPGateNewConnectionsUseCommittedBackendWhileExistingConnectionKeepsSnapshot(t *testing.T) {
	oldBackend := newRTSPGateTestBackend(t, "old")
	defer oldBackend.close()
	newBackend := newRTSPGateTestBackend(t, "new")
	defer newBackend.close()

	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: oldBackend.port()})
	initialRoute := gate.backendRouter.snapshot()
	if initialRoute.generation != 1 || initialRoute.privateRTSPPort != oldBackend.port() || initialRoute.requestID == "" {
		t.Fatalf("legacy gate route = %+v, want generation 1 with a request identity", initialRoute)
	}
	public, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate.listener = public
	listenerIdentity := gate.listener
	ctx, cancel := context.WithCancel(context.Background())
	gate.cancel = cancel
	go gate.serve(ctx)
	defer func() {
		cancel()
		_ = public.Close()
		select {
		case <-gate.done:
		case <-time.After(time.Second):
			t.Fatal("RTSP gate did not stop")
		}
	}()

	client1 := newRTSPGateTestClient(t, public.Addr().String())
	defer client1.Close()
	writeRTSPOptions(t, client1)
	if got := readRTSPBackendLabel(t, client1); got != "old" {
		t.Fatalf("existing connection backend = %q, want old", got)
	}

	active := gate.backendRouter.snapshot()
	candidate := active
	candidate.generation = 2
	candidate.requestID = "request-2"
	candidate.privateRTSPPort = newBackend.port()
	candidate, err = gate.backendRouter.prepare(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.backendRouter.commit(active, candidate); err != nil {
		t.Fatal(err)
	}
	if gate.listener != listenerIdentity {
		t.Fatal("backend commit replaced the public listener identity")
	}

	writeRTSPOptions(t, client1)
	if got := readRTSPBackendLabel(t, client1); got != "old" {
		t.Fatalf("existing connection after commit backend = %q, want old", got)
	}

	client2 := newRTSPGateTestClient(t, public.Addr().String())
	defer client2.Close()
	writeRTSPOptions(t, client2)
	if got := readRTSPBackendLabel(t, client2); got != "new" {
		t.Fatalf("new connection backend = %q, want new", got)
	}
}

func TestRTSPGateDrainClosesOldGenerationBeforeCommit(t *testing.T) {
	oldBackend := newRTSPGateTestBackend(t, "old")
	defer oldBackend.close()
	newBackend := newRTSPGateTestBackend(t, "new")
	defer newBackend.close()
	gate, public := startRTSPGateTest(t, oldBackend.port())
	defer stopRTSPGateTest(t, gate, public)

	client := newRTSPGateTestClient(t, public.Addr().String())
	defer client.Close()
	writeRTSPOptions(t, client)
	if got := readRTSPBackendLabel(t, client); got != "old" {
		t.Fatalf("initial backend = %q, want old", got)
	}
	expected := gate.backendRouter.snapshot()
	drain, err := gate.beginDrain(expected)
	if err != nil {
		t.Fatalf("begin drain: %v", err)
	}

	rejected := newRTSPGateTestClient(t, public.Addr().String())
	defer rejected.Close()
	if _, err := rejected.Write([]byte("OPTIONS rtsp://example/live RTSP/1.0\r\nCSeq: 1\r\n\r\n")); err != nil {
		t.Fatalf("write rejected connection: %v", err)
	}
	if _, err := readRTSPResponsePacket(rejected.reader); err == nil {
		t.Fatal("new connection received a response while gate was draining")
	}

	if err := drain.wait(time.Second); err != nil {
		t.Fatalf("drain wait: %v", err)
	}
	candidate := expected
	candidate.generation = expected.generation + 1
	candidate.requestID = "request-2"
	candidate.privateRTSPPort = newBackend.port()
	if err := gate.commitDrained(expected, candidate); err != nil {
		t.Fatalf("commit drained: %v", err)
	}

	client2 := newRTSPGateTestClient(t, public.Addr().String())
	defer client2.Close()
	writeRTSPOptions(t, client2)
	if got := readRTSPBackendLabel(t, client2); got != "new" {
		t.Fatalf("post-drain backend = %q, want new", got)
	}
}

func TestRTSPGateDrainTimeoutCanAbortWithoutChangingRoute(t *testing.T) {
	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: 50001})
	expected := gate.backendRouter.snapshot()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	connection := &rtspGateConnection{client: server, route: expected}
	gate.mu.Lock()
	gate.connections = map[*rtspGateConnection]struct{}{connection: {}}
	gate.mu.Unlock()

	drain, err := gate.beginDrain(expected)
	if err != nil {
		t.Fatalf("begin drain: %v", err)
	}
	if err := drain.wait(10 * time.Millisecond); !errors.Is(err, errRTSPGateDrainTimeout) {
		t.Fatalf("drain wait error = %v, want %v", err, errRTSPGateDrainTimeout)
	}
	if err := gate.abortDrain(drain); err != nil {
		t.Fatalf("abort drain: %v", err)
	}
	if got := gate.backendRouter.snapshot(); got != expected {
		t.Fatalf("route changed after abort: %+v", got)
	}
}

func TestRTSPGateDrainRejectsStaleTerminalAndInvalidCommit(t *testing.T) {
	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: 50001})
	expected := gate.backendRouter.snapshot()
	stale := expected
	stale.generation++
	if _, err := gate.beginDrain(stale); !errors.Is(err, errDirectBackendRouterStaleCommit) {
		t.Fatalf("stale begin error = %v, want %v", err, errDirectBackendRouterStaleCommit)
	}

	drain, err := gate.beginDrain(expected)
	if err != nil {
		t.Fatalf("begin drain: %v", err)
	}
	if err := drain.wait(time.Second); err != nil {
		t.Fatalf("empty drain wait: %v", err)
	}
	invalid := expected
	invalid.generation++
	invalid.requestID = ""
	if err := gate.commitDrained(expected, invalid); !errors.Is(err, errDirectBackendRouterInvalidRoute) {
		t.Fatalf("invalid commit error = %v, want %v", err, errDirectBackendRouterInvalidRoute)
	}
	if got := gate.backendRouter.snapshot(); got != expected {
		t.Fatalf("route changed after invalid commit: %+v", got)
	}
	if err := gate.abortDrain(drain); err != nil {
		t.Fatalf("abort invalid drain: %v", err)
	}

	gate.backendRouter.markTerminal()
	if _, err := gate.beginDrain(expected); !errors.Is(err, errDirectBackendRouterTerminal) {
		t.Fatalf("terminal begin error = %v, want %v", err, errDirectBackendRouterTerminal)
	}
}

func TestRTSPGateAcceptDrainRaceNeverCreatesMixedRoute(t *testing.T) {
	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: 50001})
	expected := gate.backendRouter.snapshot()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var routesMu sync.Mutex
	var acceptedRoutes []directBackendRoute

	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			client, peer := net.Pipe()
			defer peer.Close()
			connection, ok := gate.registerConnection(client)
			if !ok {
				_ = client.Close()
				return
			}
			routesMu.Lock()
			acceptedRoutes = append(acceptedRoutes, connection.route)
			routesMu.Unlock()
			gate.closeConnection(connection)
			gate.finishConnection(connection)
		}()
	}

	drainResult := make(chan struct {
		handle *rtspGateDrainHandle
		err    error
	}, 1)
	go func() {
		<-start
		handle, err := gate.beginDrain(expected)
		drainResult <- struct {
			handle *rtspGateDrainHandle
			err    error
		}{handle: handle, err: err}
	}()
	close(start)
	wg.Wait()
	result := <-drainResult
	if result.err != nil {
		t.Fatalf("begin drain: %v", result.err)
	}
	if err := result.handle.wait(time.Second); err != nil {
		t.Fatalf("drain wait: %v", err)
	}
	for _, route := range acceptedRoutes {
		if route != expected {
			t.Fatalf("accepted connection route = %+v, want exact pre-drain route %+v", route, expected)
		}
	}
	if err := gate.abortDrain(result.handle); err != nil {
		t.Fatalf("abort drain: %v", err)
	}
}

func TestRTSPGateDrainClosesUnresponsiveBackend(t *testing.T) {
	backendListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendListener.Close()
	backendConnCh := make(chan net.Conn, 1)
	go func() {
		backendConn, err := backendListener.Accept()
		if err == nil {
			backendConnCh <- backendConn
		}
	}()

	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: backendListener.Addr().(*net.TCPAddr).Port})
	client, peer := net.Pipe()
	defer peer.Close()
	connection, ok := gate.registerConnection(client)
	if !ok {
		t.Fatal("connection was rejected before drain")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gate.handleConn(ctx, connection)

	backendConn := <-backendConnCh
	defer backendConn.Close()
	deadline := time.Now().Add(time.Second)
	for {
		gate.mu.Lock()
		registered := connection.backend != nil
		gate.mu.Unlock()
		if registered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backend was not registered on the owned connection")
		}
		time.Sleep(time.Millisecond)
	}

	expected := connection.route
	drain, err := gate.beginDrain(expected)
	if err != nil {
		t.Fatalf("begin drain: %v", err)
	}
	if err := drain.wait(time.Second); err != nil {
		t.Fatalf("drain wait with unresponsive backend: %v", err)
	}
	if err := backendConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var buf [1]byte
	if _, err := backendConn.Read(buf[:]); err == nil {
		t.Fatal("unresponsive backend connection remained open after drain")
	}
}

func TestRTSPGateConnectionReleasesOwnedUDPPairsOnFinish(t *testing.T) {
	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: 50001})
	client, peer := net.Pipe()
	defer peer.Close()
	connection, ok := gate.registerConnection(client)
	if !ok {
		t.Fatal("connection was rejected")
	}
	pair, err := newRTSPUDPPair(net.ParseIP("127.0.0.1"), 39000, 39001, 59520, 59521)
	if err != nil {
		t.Fatal(err)
	}
	gate.mu.Lock()
	connection.pairs = append(connection.pairs, pair)
	gate.mu.Unlock()

	gate.closeConnection(connection)
	gate.finishConnection(connection)
	if len(connection.pairs) != 0 {
		t.Fatalf("owned UDP pairs retained after finish: %d", len(connection.pairs))
	}
	gate.mu.Lock()
	remaining := len(gate.connections)
	gate.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("finished connection retained in gate: %d", remaining)
	}
}

func startRTSPGateTest(t *testing.T, backendPort int) (*rtspGate, net.Listener) {
	t.Helper()
	gate := newRTSPGate(rtspGateConfig{BackendRTSPPort: backendPort})
	public, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gate.listener = public
	ctx, cancel := context.WithCancel(context.Background())
	gate.cancel = cancel
	go gate.serve(ctx)
	return gate, public
}

func stopRTSPGateTest(t *testing.T, gate *rtspGate, public net.Listener) {
	t.Helper()
	gate.stop()
	_ = public.Close()
	select {
	case <-gate.done:
	case <-time.After(time.Second):
		t.Fatal("RTSP gate did not stop")
	}
}

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

func TestRTSPGateSwitchesToRawTunnelAfterTCPPlay(t *testing.T) {
	var session rtspGateSessionState
	session.noteRequest(parseRTSPRequest([]byte("SETUP rtsp://example/live/trackID=0 RTSP/1.0\r\nTransport: RTP/AVP/TCP;unicast;interleaved=0-1\r\n\r\n")))

	if !session.shouldTunnelAfter(parseRTSPRequest([]byte("PLAY rtsp://example/live RTSP/1.0\r\nCSeq: 3\r\n\r\n"))) {
		t.Fatal("TCP interleaved RTSP session must switch to raw tunneling after PLAY")
	}
}

func TestRTSPGateTreatsInterleavedTransportAsTCPWithoutTCPToken(t *testing.T) {
	req := rtspRequest{
		Method: "SETUP",
		Headers: map[string]string{
			"user-agent": "MF-MediaEngine-Hardware",
			"transport":  "RTP/AVP;unicast;interleaved=0-1;mode=PLAY",
		},
	}

	if isUDPSetup(req) {
		t.Fatal("interleaved RTSP transport must not be classified as UDP")
	}
	if shouldRejectUDPSetup(req) {
		t.Fatal("interleaved RTSP transport must not be rejected as Media Foundation UDP")
	}
	if !isTCPSetup(req) {
		t.Fatal("interleaved RTSP transport must be classified as TCP")
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

type rtspGateTestBackend struct {
	listener net.Listener
	label    string
	done     chan struct{}
}

func newRTSPGateTestBackend(t *testing.T, label string) *rtspGateTestBackend {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := &rtspGateTestBackend{listener: listener, label: label, done: make(chan struct{})}
	go func() {
		defer close(backend.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				for {
					_, req, err := readRTSPPacket(reader)
					if err != nil {
						return
					}
					cseq := req.Headers["cseq"]
					if cseq == "" {
						cseq = "1"
					}
					response := "RTSP/1.0 200 OK\r\nCSeq: " + cseq + "\r\nX-Backend: " + label + "\r\n\r\n"
					if _, err := conn.Write([]byte(response)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return backend
}

func (b *rtspGateTestBackend) port() int {
	return b.listener.Addr().(*net.TCPAddr).Port
}

func (b *rtspGateTestBackend) close() {
	_ = b.listener.Close()
	<-b.done
}

type rtspGateTestClient struct {
	net.Conn
	reader *bufio.Reader
}

func newRTSPGateTestClient(t *testing.T, address string) *rtspGateTestClient {
	t.Helper()
	client, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
		client.Close()
		t.Fatal(err)
	}
	return &rtspGateTestClient{Conn: client, reader: bufio.NewReader(client)}
}

func writeRTSPOptions(t *testing.T, client *rtspGateTestClient) {
	t.Helper()
	if _, err := client.Write([]byte("OPTIONS rtsp://example/live RTSP/1.0\r\nCSeq: 1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
}

func readRTSPBackendLabel(t *testing.T, client *rtspGateTestClient) string {
	t.Helper()
	packet, err := readRTSPResponsePacket(client.reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(packet), "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "X-Backend") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
