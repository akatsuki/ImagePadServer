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
	PublicRTSPPort  int
	PublicRTPPort   int
	PublicRTCPPort  int
	BackendRTSPPort int
	Path            string
}

type rtspGate struct {
	cfg      rtspGateConfig
	listener net.Listener
	done     chan struct{}
	cancel   context.CancelFunc

	mu    sync.Mutex
	pairs []*rtspUDPPair
}

type rtspRequest struct {
	Method  string
	Headers map[string]string
}

func newRTSPGate(cfg rtspGateConfig) *rtspGate {
	return &rtspGate{cfg: cfg, done: make(chan struct{})}
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
	for _, pair := range g.pairs {
		pair.close()
	}
	g.pairs = nil
	g.mu.Unlock()
	select {
	case <-g.done:
	case <-time.After(2 * time.Second):
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
		go g.handleConn(ctx, conn)
	}
}

func (g *rtspGate) handleConn(ctx context.Context, client net.Conn) {
	defer client.Close()
	backend, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", g.cfg.BackendRTSPPort))
	if err != nil {
		return
	}
	defer backend.Close()

	clientReader := bufio.NewReader(client)
	backendReader := bufio.NewReader(backend)
	clientIP := net.ParseIP("127.0.0.1")
	if tcpAddr, ok := client.RemoteAddr().(*net.TCPAddr); ok {
		clientIP = tcpAddr.IP
	}
	var session rtspGateSessionState

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
		session.noteRequest(req)
		if isUDPSetup(req) {
			rtpClient, rtcpClient, ok := parseRTSPClientPorts(req.Headers["transport"])
			if ok && g.cfg.PublicRTPPort > 0 && g.cfg.PublicRTCPPort > 0 {
				pair, err := newRTSPUDPPair(clientIP, rtpClient, rtcpClient, g.cfg.PublicRTPPort, g.cfg.PublicRTCPPort)
				if err == nil {
					g.mu.Lock()
					g.pairs = append(g.pairs, pair)
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
		if _, err := client.Write(resp); err != nil {
			return
		}
		if session.shouldTunnelAfter(req) {
			go io.Copy(backend, clientReader)
			_, _ = io.Copy(client, backendReader)
			return
		}
	}
}

type rtspGateSessionState struct {
	tcpInterleaved bool
}

func (s *rtspGateSessionState) noteRequest(req rtspRequest) {
	if isTCPSetup(req) {
		s.tcpInterleaved = true
	}
}

func (s rtspGateSessionState) shouldTunnelAfter(req rtspRequest) bool {
	return req.Method == "PLAY" && s.tcpInterleaved
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
	done           chan struct{}
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
		done:           make(chan struct{}),
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
