package obsrtmp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rtspGateConfig struct {
	PublicRTSPPort     int
	PublicRTPPort      int
	PublicRTCPPort     int
	BackendRTSPPort    int
	Path               string
	WaitInitialH264IDR bool
	Diagnostics        *rtspDiagnostics
}

type rtspGate struct {
	cfg      rtspGateConfig
	listener net.Listener
	done     chan struct{}
	cancel   context.CancelFunc

	backendRouter *directBackendRouter

	// Lock order is gate.mu -> backendRouter.mu. Router methods never acquire
	// gate.mu, so accept/drain/stop cannot form a lock cycle.
	mu          sync.Mutex
	connections map[*rtspGateConnection]struct{}
	draining    *rtspGateDrainHandle
	diagnostics *rtspDiagnostics
}

var (
	errRTSPGateDrainTimeout    = errors.New("RTSP gate drain timed out")
	errRTSPGateDrainIncomplete = errors.New("RTSP gate drain is not complete")
	errRTSPGateNotDraining     = errors.New("RTSP gate is not draining")
)

type rtspGateConnection struct {
	client       net.Conn
	backend      net.Conn
	route        directBackendRoute
	pairs        []*rtspUDPPair
	drain        *rtspGateDrainHandle
	closed       bool
	finished     bool
	diagnosticID string
}

type rtspGateDrainHandle struct {
	gate     *rtspGate
	expected directBackendRoute
	done     chan struct{}
	pending  int
}

func (h *rtspGateDrainHandle) wait(timeout time.Duration) error {
	if timeout <= 0 {
		select {
		case <-h.done:
			return nil
		default:
			return errRTSPGateDrainTimeout
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-h.done:
		return nil
	case <-timer.C:
		return errRTSPGateDrainTimeout
	}
}

type rtspRequest struct {
	Method  string
	Headers map[string]string
}

func newRTSPGate(cfg rtspGateConfig) *rtspGate {
	return &rtspGate{
		cfg:           cfg,
		done:          make(chan struct{}),
		backendRouter: newLegacyDirectBackendRouter(cfg.BackendRTSPPort),
		connections:   make(map[*rtspGateConnection]struct{}),
		diagnostics:   cfg.Diagnostics,
	}
}

func (g *rtspGate) start(ctx context.Context) error {
	if g.cfg.PublicRTSPPort <= 0 || g.cfg.BackendRTSPPort <= 0 {
		return errors.New("RTSP gate ports are required")
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", g.cfg.PublicRTSPPort))
	if err != nil {
		return fmt.Errorf("start RTSP gate: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	g.listener = ln
	g.cancel = cancel
	go g.serve(runCtx)
	return nil
}

func (g *rtspGate) stop() error {
	if g.cancel != nil {
		g.cancel()
	}
	if g.listener != nil {
		_ = g.listener.Close()
	}
	g.mu.Lock()
	g.backendRouter.markTerminal()
	for connection := range g.connections {
		g.closeConnectionLocked(connection)
	}
	g.mu.Unlock()
	select {
	case <-g.done:
	case <-time.After(2 * time.Second):
	}
	if g.diagnostics != nil {
		_ = g.diagnostics.flush()
	}
	return nil
}

func (g *rtspGate) serve(ctx context.Context) {
	defer close(g.done)
	for {
		conn, err := g.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		connection, ok := g.registerConnection(conn)
		if !ok {
			_ = conn.Close()
			continue
		}
		go g.handleConn(ctx, connection)
	}
}

func (g *rtspGate) registerConnection(client net.Conn) (*rtspGateConnection, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.backendRouter.canAccept() {
		return nil, false
	}
	connection := &rtspGateConnection{
		client: client,
		route:  g.backendRouter.snapshot(),
	}
	if g.diagnostics != nil {
		connection.diagnosticID = g.diagnostics.nextConnectionID()
	}
	if g.connections == nil {
		g.connections = make(map[*rtspGateConnection]struct{})
	}
	g.connections[connection] = struct{}{}
	return connection, true
}

func (g *rtspGate) handleConn(ctx context.Context, connection *rtspGateConnection) {
	client := connection.client
	defer func() {
		g.closeConnection(connection)
		g.finishConnection(connection)
	}()
	backendRoute := connection.route
	backend, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", backendRoute.privateRTSPPort))
	if err != nil {
		return
	}
	defer backend.Close()
	g.mu.Lock()
	if connection.closed {
		g.mu.Unlock()
		return
	}
	connection.backend = backend
	g.mu.Unlock()

	clientReader := bufio.NewReader(client)
	backendReader := bufio.NewReader(backend)
	clientIP := net.ParseIP("127.0.0.1")
	if tcpAddr, ok := client.RemoteAddr().(*net.TCPAddr); ok {
		clientIP = tcpAddr.IP
	}
	var session rtspGateSessionState
	var joinPayload byte
	var joinSDPReady bool
	joinChannels := make(map[byte]bool)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		packet, req, err := readRTSPPacket(clientReader)
		if err != nil {
			return
		}
		if shouldRejectUDPSetup(req) {
			_, _ = client.Write(unsupportedTransportResponse(req))
			continue
		}
		if g.diagnostics != nil {
			g.diagnostics.record(rtspDiagnosticEvent{ConnectionID: connection.diagnosticID, PublisherGeneration: backendRoute.generation, Backend: "mediamtx", Gate: "public", Request: req.Method, UserAgent: req.Headers["user-agent"], CSeq: req.Headers["cseq"], Transport: req.Headers["transport"]})
		}
		if isUDPSetup(req) {
			rtpClient, rtcpClient, ok := parseRTSPClientPorts(req.Headers["transport"])
			if ok && g.cfg.PublicRTPPort > 0 && g.cfg.PublicRTCPPort > 0 {
				pair, err := newRTSPUDPPair(clientIP, rtpClient, rtcpClient, g.cfg.PublicRTPPort, g.cfg.PublicRTCPPort)
				if err == nil {
					g.mu.Lock()
					if connection.closed {
						g.mu.Unlock()
						pair.close()
						return
					}
					connection.pairs = append(connection.pairs, pair)
					g.mu.Unlock()
					packet, err = rewriteUDPSetupRequest(packet, pair.rtpPort(), pair.rtcpPort())
					if err != nil {
						return
					}
				}
			}
		}
		if _, err := backend.Write(packet); err != nil {
			return
		}
		resp, err := readRTSPResponsePacket(backendReader)
		if err != nil {
			return
		}
		if isUDPSetup(req) && g.cfg.PublicRTPPort > 0 && g.cfg.PublicRTCPPort > 0 {
			resp = rewriteUDPSetupResponse(resp, g.cfg.PublicRTPPort, g.cfg.PublicRTCPPort)
		}
		session.noteResponse(req, resp)
		if g.cfg.WaitInitialH264IDR && bytes.HasPrefix(resp, []byte("RTSP/1.0 200 ")) {
			if req.Method == "DESCRIBE" {
				_, body, ok := bytes.Cut(resp, []byte("\r\n\r\n"))
				var parseErr error
				joinPayload, parseErr = h264JoinPayload(body)
				joinSDPReady = ok && parseErr == nil
			}
			if req.Method == "SETUP" {
				if channel, ok := rtspJoinChannel(resp); ok {
					joinChannels[channel] = true
				}
			}
		}
		if _, err := client.Write(resp); err != nil {
			return
		}
		if g.diagnostics != nil {
			g.diagnostics.record(rtspDiagnosticEvent{ConnectionID: connection.diagnosticID, PublisherGeneration: backendRoute.generation, Backend: "mediamtx", Gate: "public", Response: rtspResponseStatus(resp), CSeq: req.Headers["cseq"], Transport: req.Headers["transport"]})
		}
		if session.shouldTunnelAfter(req, resp) {
			go g.copyRTSP(backend, clientReader, connection.diagnosticID, backendRoute.generation, rtspDiagnosticDirectionClientToBackend)
			if g.cfg.WaitInitialH264IDR {
				if !joinSDPReady || len(joinChannels) == 0 || !bytes.HasPrefix(resp, []byte("RTSP/1.0 200 ")) {
					return
				}
				deadline := time.Now().Add(10 * time.Second)
				_ = backend.SetReadDeadline(deadline)
				_ = client.SetWriteDeadline(deadline)
				if err := copyUntilH264IDR(client, backendReader, joinPayload, joinChannels, rtspJoinMaxBytes); err != nil {
					return
				}
				_ = backend.SetReadDeadline(time.Time{})
				_ = client.SetWriteDeadline(time.Time{})
			}
			_ = g.copyRTSP(client, backendReader, connection.diagnosticID, backendRoute.generation, rtspDiagnosticDirectionBackendToClient)
			return
		}
	}
}

func (g *rtspGate) copyRTSP(dst io.Writer, src io.Reader, connectionID string, generation uint64, direction rtspDiagnosticDirection) error {
	start := time.Now()
	n, err := io.Copy(dst, src)
	if g.diagnostics != nil {
		g.diagnostics.copyFinished(connectionID, generation, direction, n, time.Since(start).Milliseconds(), err)
	}
	return err
}

func (g *rtspGate) diagnosticsSnapshot() []rtspDiagnosticEvent {
	if g == nil || g.diagnostics == nil {
		return nil
	}
	return g.diagnostics.snapshot()
}

func rtspResponseStatus(packet []byte) string {
	line, _, _ := strings.Cut(strings.ReplaceAll(string(packet), "\r\n", "\n"), "\n")
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		return fields[0] + " " + fields[1]
	}
	return ""
}

func (g *rtspGate) closeConnection(connection *rtspGateConnection) {
	g.mu.Lock()
	g.closeConnectionLocked(connection)
	g.mu.Unlock()
}

func (g *rtspGate) closeConnectionLocked(connection *rtspGateConnection) {
	if connection.closed {
		return
	}
	connection.closed = true
	_ = connection.client.Close()
	if connection.backend != nil {
		_ = connection.backend.Close()
		connection.backend = nil
	}
	for _, pair := range connection.pairs {
		pair.close()
	}
	connection.pairs = nil
}

func (g *rtspGate) finishConnection(connection *rtspGateConnection) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if connection.finished {
		return
	}
	connection.finished = true
	delete(g.connections, connection)
	if connection.drain == nil {
		return
	}
	connection.drain.pending--
	if connection.drain.pending == 0 {
		close(connection.drain.done)
	}
}

func (g *rtspGate) beginDrain(expected directBackendRoute) (*rtspGateDrainHandle, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.backendRouter.beginDrain(expected); err != nil {
		return nil, err
	}
	drain := &rtspGateDrainHandle{
		gate:     g,
		expected: expected,
		done:     make(chan struct{}),
	}
	g.draining = drain
	for connection := range g.connections {
		if connection.route != expected {
			continue
		}
		connection.drain = drain
		drain.pending++
		g.closeConnectionLocked(connection)
	}
	if drain.pending == 0 {
		close(drain.done)
	}
	return drain, nil
}

func (g *rtspGate) commitDrained(expected, candidate directBackendRoute) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.draining == nil || g.draining.expected != expected {
		return errRTSPGateNotDraining
	}
	select {
	case <-g.draining.done:
	default:
		return errRTSPGateDrainIncomplete
	}
	if err := g.backendRouter.commitDrained(expected, candidate); err != nil {
		return err
	}
	g.draining = nil
	return nil
}

func (g *rtspGate) abortDrain(drain *rtspGateDrainHandle) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if drain == nil || g.draining != drain {
		return errRTSPGateNotDraining
	}
	if err := g.backendRouter.abortDrain(drain.expected); err != nil {
		return err
	}
	g.draining = nil
	return nil
}

type rtspGateSessionState struct {
	tcpInterleaved bool
}


func (s *rtspGateSessionState) noteResponse(req rtspRequest, resp []byte) {
	if req.Method == "SETUP" && isTCPSetup(req) {
		s.tcpInterleaved = bytes.HasPrefix(resp, []byte("RTSP/1.0 200 "))
	}
}

// noteRequest remains a test/helper compatibility shim. The live gate records
// TCP capability only from the corresponding successful SETUP response.
func (s *rtspGateSessionState) noteRequest(req rtspRequest) {
	if isTCPSetup(req) {
		s.tcpInterleaved = true
	}
}

func (s rtspGateSessionState) shouldTunnelAfter(req rtspRequest, response ...[]byte) bool {
	if req.Method != "PLAY" || !s.tcpInterleaved {
		return false
	}
	return len(response) == 0 || bytes.HasPrefix(response[0], []byte("RTSP/1.0 200 "))
}

func readRTSPPacket(r *bufio.Reader) ([]byte, rtspRequest, error) {
	packet, err := readRTSPHeaderBlock(r)
	if err != nil {
		return nil, rtspRequest{}, err
	}
	return packet, parseRTSPRequest(packet), nil
}

func readRTSPResponsePacket(r *bufio.Reader) ([]byte, error) {
	return readRTSPHeaderBlock(r)
}

func readRTSPHeaderBlock(r *bufio.Reader) ([]byte, error) {
	var out bytes.Buffer
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		out.Write(line)
		if bytes.Equal(line, []byte("\r\n")) || bytes.Equal(line, []byte("\n")) {
			header := out.Bytes()
			length := rtspContentLength(header)
			if length <= 0 {
				return header, nil
			}
			body := make([]byte, length)
			if _, err := io.ReadFull(r, body); err != nil {
				return nil, err
			}
			return append(header, body...), nil
		}
	}
}

func rtspContentLength(header []byte) int {
	lines := strings.Split(strings.ReplaceAll(string(header), "\r\n", "\n"), "\n")
	for _, line := range lines {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 0 {
			return 0
		}
		return n
	}
	return 0
}

func parseRTSPRequest(packet []byte) rtspRequest {
	lines := strings.Split(strings.ReplaceAll(string(packet), "\r\n", "\n"), "\n")
	req := rtspRequest{Headers: map[string]string{}}
	if len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) > 0 {
			req.Method = strings.ToUpper(fields[0])
		}
	}
	for _, line := range lines[1:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		req.Headers[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return req
}

func shouldRejectUDPSetup(req rtspRequest) bool {
	if !isUDPSetup(req) {
		return false
	}
	ua := strings.ToLower(req.Headers["user-agent"])
	return strings.Contains(ua, "wmplayer") ||
		strings.Contains(ua, "mediafoundation") ||
		strings.Contains(ua, "mf-mediaengine")
}

func isUDPSetup(req rtspRequest) bool {
	if req.Method != "SETUP" {
		return false
	}
	transport := strings.ToUpper(req.Headers["transport"])
	if strings.Contains(transport, "INTERLEAVED=") {
		return false
	}
	return strings.Contains(transport, "RTP/AVP/UDP") || (strings.Contains(transport, "RTP/AVP") && !strings.Contains(transport, "TCP"))
}

func isTCPSetup(req rtspRequest) bool {
	if req.Method != "SETUP" {
		return false
	}
	transport := strings.ToUpper(req.Headers["transport"])
	return strings.Contains(transport, "RTP/AVP/TCP") || strings.Contains(transport, "INTERLEAVED=")
}

func unsupportedTransportResponse(req rtspRequest) []byte {
	cseq := req.Headers["cseq"]
	if cseq == "" {
		cseq = "1"
	}
	return []byte("RTSP/1.0 461 Unsupported Transport\r\nCSeq: " + cseq + "\r\n\r\n")
}

var (
	clientPortRe = regexp.MustCompile(`client_port=([0-9]+)-([0-9]+)`)
	serverPortRe = regexp.MustCompile(`server_port=([0-9]+)-([0-9]+)`)
)

func parseRTSPClientPorts(transport string) (int, int, bool) {
	match := clientPortRe.FindStringSubmatch(transport)
	if len(match) != 3 {
		return 0, 0, false
	}
	rtp, err1 := strconv.Atoi(match[1])
	rtcp, err2 := strconv.Atoi(match[2])
	return rtp, rtcp, err1 == nil && err2 == nil
}

func rewriteUDPSetupRequest(packet []byte, rtpPort, rtcpPort int) ([]byte, error) {
	if !clientPortRe.Match(packet) {
		return nil, errors.New("RTSP SETUP request missing client_port")
	}
	repl := []byte(fmt.Sprintf("client_port=%d-%d", rtpPort, rtcpPort))
	return clientPortRe.ReplaceAll(packet, repl), nil
}

func rewriteUDPSetupResponse(packet []byte, rtpPort, rtcpPort int) []byte {
	repl := []byte(fmt.Sprintf("server_port=%d-%d", rtpPort, rtcpPort))
	if serverPortRe.Match(packet) {
		return serverPortRe.ReplaceAll(packet, repl)
	}
	return packet
}

type rtspUDPPair struct {
	clientIP       net.IP
	clientRTPPort  int
	clientRTCPPort int
	publicRTPPort  int
	publicRTCPPort int
	rtpConn        *net.UDPConn
	rtcpConn       *net.UDPConn
}

func newRTSPUDPPair(clientIP net.IP, clientRTPPort, clientRTCPPort, publicRTPPort, publicRTCPPort int) (*rtspUDPPair, error) {
	rtpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	rtcpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		_ = rtpConn.Close()
		return nil, err
	}
	pair := &rtspUDPPair{
		clientIP:       clientIP,
		clientRTPPort:  clientRTPPort,
		clientRTCPPort: clientRTCPPort,
		publicRTPPort:  publicRTPPort,
		publicRTCPPort: publicRTCPPort,
		rtpConn:        rtpConn,
		rtcpConn:       rtcpConn,
	}
	go pair.forwardBackendToClient(rtpConn, clientRTPPort)
	go pair.forwardBackendToClient(rtcpConn, clientRTCPPort)
	return pair, nil
}

func (p *rtspUDPPair) rtpPort() int {
	return p.rtpConn.LocalAddr().(*net.UDPAddr).Port
}

func (p *rtspUDPPair) rtcpPort() int {
	return p.rtcpConn.LocalAddr().(*net.UDPAddr).Port
}

func (p *rtspUDPPair) forwardBackendToClient(conn *net.UDPConn, clientPort int) {
	buf := make([]byte, 2048)
	dst := &net.UDPAddr{IP: p.clientIP, Port: clientPort}
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		_, _ = conn.WriteToUDP(buf[:n], dst)
	}
}

func (p *rtspUDPPair) close() {
	_ = p.rtpConn.Close()
	_ = p.rtcpConn.Close()
}
