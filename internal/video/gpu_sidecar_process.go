package video

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

func validateGlyphAtlasReceipt(atlas *GlyphAtlasMetadata, receipt *GlyphAtlasReceipt) error {
	if receipt == nil { return errors.New("sidecar glyph atlas receipt missing") }
	sum := sha256.Sum256(atlas.Payload)
	want := fmt.Sprintf("%x", sum[:])
	if receipt.SHA256 != want || receipt.Width != atlas.Width || receipt.Height != atlas.Height || receipt.RowStride != atlas.RowStride || receipt.GlyphCount != uint32(len(atlas.Glyphs)) || receipt.TextRunCount != uint32(len(atlas.TextRuns)) || receipt.Format != string(PixelRGBA8) {
		return fmt.Errorf("sidecar glyph atlas receipt mismatch: got=%+v want_sha256=%s", *receipt, want)
	}
	return nil
}

const sidecarStderrLimit = 32 * 1024

type boundedBuffer struct {
	mu        sync.Mutex
	b         bytes.Buffer
	max       int
	truncated bool
}

func (w *boundedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.b.Len() < w.max {
		n := w.max - w.b.Len()
		if n > len(p) {
			n = len(p)
		}
		_, _ = w.b.Write(p[:n])
		if n < len(p) {
			w.truncated = true
		}
	} else if len(p) != 0 {
		w.truncated = true
	}
	return len(p), nil
}

func (w *boundedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.b.String()
	if w.truncated {
		s += "\n[stderr truncated]"
	}
	return s
}

// SidecarDiagnostics is local process evidence used by diagnostics and tests.
// It deliberately excludes stderr from normal request errors.
type SidecarDiagnostics struct {
	Executable string
	Args       []string
	StartedAt  time.Time
	FinishedAt time.Time
	ExitError  error
	Stderr     string
	Adapter    string
	Backend    string
	Toolchain  string
	GlyphAtlasReceipt *GlyphAtlasReceipt
}

// SidecarFingerprint identifies the runtime selected by the sidecar. Empty
// values are intentionally preserved so callers can distinguish an
// unreported fingerprint from an explicitly reported one.
type SidecarFingerprint struct {
	Adapter   string `json:"adapter,omitempty"`
	Backend   string `json:"backend,omitempty"`
	Toolchain string `json:"toolchain,omitempty"`
}

// ProbeGPUFingerprint starts the configured sidecar, performs the protocol
// hello handshake, and returns the fingerprint reported by the actual runtime.
// It is intended for diagnostics/compare tooling; it does not render or alter
// the production route.
func ProbeGPUFingerprint(ctx context.Context, executable, session string) (SidecarFingerprint, error) {
	p, err := StartSidecar(ctx, executable, session)
	if err != nil {
		return SidecarFingerprint{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, session); err != nil {
		return SidecarFingerprint{}, err
	}
	d := p.Diagnostics()
	return SidecarFingerprint{Adapter: d.Adapter, Backend: d.Backend, Toolchain: d.Toolchain}, nil
}

type sidecarRequest struct {
	Type    string `json:"type"`
	Version uint16 `json:"version,omitempty"`
	Session string `json:"session,omitempty"`
}
type sidecarResponse struct {
	Type      string    `json:"type"`
	Version   uint16    `json:"version,omitempty"`
	Ready     bool      `json:"ready,omitempty"`
	Code      string    `json:"code,omitempty"`
	Message   string    `json:"message,omitempty"`
	Frame     *GpuFrame `json:"frame,omitempty"`
	Adapter   string    `json:"adapter,omitempty"`
	Backend   string    `json:"backend,omitempty"`
	Toolchain string    `json:"toolchain,omitempty"`
}

// Render requests one GPU-produced frame. The response is validated before it
// enters the bounded transport, so malformed sidecar output fails closed.
func (p *SidecarProcess) Render(ctx context.Context, width, height uint32, sequence uint64, ptsNS int64) (GpuFrame, error) {
	return p.RenderScene(ctx, width, height, sequence, ptsNS, nil)
}

// RenderScene sends the canonical music scene when provided. Keeping the
// legacy Render wrapper preserves callers that intentionally exercise the
// compatibility renderer.
func (p *SidecarProcess) RenderScene(ctx context.Context, width, height uint32, sequence uint64, ptsNS int64, scene *MusicScenePayload) (GpuFrame, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return GpuFrame{}, ErrGPUUnavailable
	}
	// Encode the render fields as a small anonymous request to preserve the
	// existing JSONL protocol without widening the control struct used by Hello.
	b, _ := json.Marshal(struct {
		Type     string             `json:"type"`
		Width    uint32             `json:"width"`
		Height   uint32             `json:"height"`
		Sequence uint64             `json:"sequence"`
		PTSNS    int64              `json:"pts_ns"`
		Scene    *MusicScenePayload `json:"scene,omitempty"`
	}{"render", width, height, sequence, ptsNS, scene})
	if _, err := fmt.Fprintln(p.in, string(b)); err != nil {
		return GpuFrame{}, err
	}
	line := make(chan []byte, 1)
	errs := make(chan error, 1)
	go func() {
		s, e := p.out.ReadBytes('\n')
		if e != nil {
			errs <- e
		} else {
			line <- s
		}
	}()
	select {
	case <-ctx.Done():
		return GpuFrame{}, ctx.Err()
	case e := <-errs:
		return GpuFrame{}, e
	case b := <-line:
		var resp sidecarResponse
		if err := json.Unmarshal(b, &resp); err != nil {
			return GpuFrame{}, err
		}
		if resp.Code != "" {
			return GpuFrame{}, fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
		}
		if resp.Frame == nil {
			return GpuFrame{}, errors.New("sidecar frame missing")
		}
		if err := resp.Frame.Validate(); err != nil {
			return GpuFrame{}, err
		}
		if scene != nil && scene.GlyphAtlas != nil {
			if err := validateGlyphAtlasReceipt(scene.GlyphAtlas, resp.Frame.GlyphAtlasReceipt); err != nil { return GpuFrame{}, err }
		}
		p.glyphReceipt = resp.Frame.GlyphAtlasReceipt
		return *resp.Frame, nil
	}
}

// SidecarProcess is the JSONL control-plane client for playlist-compositord.
// Frame bytes intentionally stay on GPUFrameTransport until the native mapping
// backend is available.
type SidecarProcess struct {
	cmd         *exec.Cmd
	in          io.WriteCloser
	out         *bufio.Reader
	mu          sync.Mutex
	wait        chan error
	stderr      *boundedBuffer
	startedAt   time.Time
	finishedAt  time.Time
	exitError   error
	closed      bool
	fingerprint SidecarFingerprint
	glyphReceipt *GlyphAtlasReceipt
}

func StartSidecar(ctx context.Context, executable, session string) (*SidecarProcess, error) {
	cmd := exec.CommandContext(ctx, executable)
	hideWindow(cmd)
	return startSidecarCommand(ctx, cmd, session)
}
func startSidecarCommand(ctx context.Context, cmd *exec.Cmd, session string) (*SidecarProcess, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &boundedBuffer{max: sidecarStderrLimit}
	cmd.Stderr = stderr
	startedAt := time.Now()
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	p := &SidecarProcess{cmd: cmd, in: in, out: bufio.NewReader(stdout), wait: make(chan error, 1), stderr: stderr, startedAt: startedAt}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.finishedAt, p.exitError = time.Now(), err
		p.mu.Unlock()
		p.wait <- err
	}()
	return p, nil
}

// Diagnostics returns a snapshot for local troubleshooting. Callers should
// not include this data in user-facing request errors.
func (p *SidecarProcess) Diagnostics() SidecarDiagnostics {
	p.mu.Lock()
	defer p.mu.Unlock()
	d := SidecarDiagnostics{StartedAt: p.startedAt, FinishedAt: p.finishedAt, ExitError: p.exitError}
	d.Adapter, d.Backend, d.Toolchain = p.fingerprint.Adapter, p.fingerprint.Backend, p.fingerprint.Toolchain
	d.GlyphAtlasReceipt = p.glyphReceipt
	if p.cmd != nil {
		d.Executable, d.Args = p.cmd.Path, append([]string(nil), p.cmd.Args[1:]...)
	}
	if p.stderr != nil {
		d.Stderr = p.stderr.String()
	}
	return d
}

func (p *SidecarProcess) rpc(ctx context.Context, req sidecarRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrGPUUnavailable
	}
	b, _ := json.Marshal(req)
	if _, err := fmt.Fprintln(p.in, string(b)); err != nil {
		return err
	}
	line := make(chan []byte, 1)
	errs := make(chan error, 1)
	go func() {
		s, e := p.out.ReadBytes('\n')
		if e != nil {
			errs <- e
			return
		}
		line <- s
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case e := <-errs:
		return e
	case b := <-line:
		var r sidecarResponse
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		if r.Type == "error" {
			return fmt.Errorf("sidecar %s: %s", r.Code, r.Message)
		}
		if r.Adapter != "" {
			p.fingerprint.Adapter = r.Adapter
		}
		if r.Backend != "" {
			p.fingerprint.Backend = r.Backend
		}
		if r.Toolchain != "" {
			p.fingerprint.Toolchain = r.Toolchain
		}
		return nil
	}
}
func (p *SidecarProcess) Health(ctx context.Context) error {
	return p.rpc(ctx, sidecarRequest{Type: "health"})
}
func (p *SidecarProcess) Hello(ctx context.Context, session string) error {
	return p.rpc(ctx, sidecarRequest{Type: "hello", Version: 1, Session: session})
}
func (p *SidecarProcess) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	// If the child has already exited, its stdout may be closed and a shutdown
	// RPC can wait forever for a response. Prefer the lifecycle signal first.
	select {
	case err := <-p.wait:
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		return err
	default:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = p.rpc(shutdownCtx, sidecarRequest{Type: "shutdown"})
	cancel()
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	_ = p.in.Close()
	select {
	case err := <-p.wait:
		return err
	case <-time.After(2 * time.Second):
		if p.cmd != nil && p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
		return <-p.wait
	}
}

var ErrSharedMappingUnsupported = errors.New("gpu shared mapping backend is not supported on this build")

type TransportBackend interface {
	Submit(context.Context, GpuFrame) error
	Receive(context.Context) (GpuFrame, error)
	Close(error)
}

// NewSharedMappingTransport creates the native-mapping frame transport. The
// fixed-slot ABI is shared with playlist-compositord; callers must use the
// same session name, capacity, and slot size on both processes.
func NewSharedMappingTransport(name string, capacity uint16, slotBytes uint32) (TransportBackend, error) {
	return NewMappedFrameRing(name, capacity, slotBytes)
}

// NewSharedMappingBackend is retained as an explicit migration sentinel for
// callers that have not supplied a session-scoped mapping configuration.
func NewSharedMappingBackend() (TransportBackend, error) { return nil, ErrSharedMappingUnsupported }
