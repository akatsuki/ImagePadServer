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
	if receipt == nil {
		return errors.New("sidecar glyph atlas receipt missing")
	}
	sum := sha256.Sum256(atlas.Payload)
	want := fmt.Sprintf("%x", sum[:])
	if receipt.SHA256 != want || receipt.Width != atlas.Width || receipt.Height != atlas.Height || receipt.RowStride != atlas.RowStride || receipt.GlyphCount != uint32(len(atlas.Glyphs)) || receipt.TextRunCount != uint32(len(atlas.TextRuns)) || receipt.Format != string(PixelRGBA8) {
		return fmt.Errorf("sidecar glyph atlas receipt mismatch: got=%+v want_sha256=%s", *receipt, want)
	}
	return nil
}

func validateTextOverlayReceipt(overlay *TextOverlayMetadata, receipt *TextOverlayReceipt) error {
	if overlay == nil || receipt == nil {
		return errors.New("sidecar text overlay receipt missing")
	}
	sum := sha256.Sum256(overlay.Payload)
	want := fmt.Sprintf("%x", sum[:])
	if receipt.SHA256 != want || receipt.Width != overlay.Width || receipt.Height != overlay.Height || receipt.RowStride != overlay.RowStride || receipt.Format == "" || receipt.ColorSpace == "" || receipt.RendererID != overlay.RendererID || receipt.RendererVersion != overlay.RendererVersion {
		return fmt.Errorf("sidecar text overlay receipt mismatch")
	}
	return nil
}
func validateArtworkReceipt(a *ArtworkMetadata, r *ArtworkReceipt) error {
	if a == nil || r == nil {
		return errors.New("sidecar artwork receipt missing")
	}
	sum := sha256.Sum256(a.Payload)
	want := fmt.Sprintf("%x", sum[:])
	if r.SHA256 != want || r.SourceWidth != a.Width || r.SourceHeight != a.Height || r.CropMode == "" || r.AspectMode == "" {
		return errors.New("sidecar artwork receipt mismatch")
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
	Executable        string
	Args              []string
	StartedAt         time.Time
	FinishedAt        time.Time
	ExitError         error
	Stderr            string
	Adapter           string
	Backend           string
	Toolchain         string
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

// ProbeGPUSceneGlyphDiagnostics performs one diagnostic-only scene render and
// returns the exact glyph instance metadata built by the sidecar. Production
// rendering does not call this helper; compare tooling uses it as evidence.
func ProbeGPUSceneGlyphDiagnostics(ctx context.Context, executable, session string, width, height uint32, scene *MusicScenePayload) (*GlyphRenderDiagnostics, error) {
	p, err := StartSidecar(ctx, executable, session)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	if err := p.Hello(ctx, session); err != nil {
		return nil, err
	}
	frame, err := p.RenderScene(ctx, width, height, 0, 0, scene)
	if err != nil {
		return nil, err
	}
	return frame.GlyphDiagnostics, nil
}

// ProbeGPUSceneGlyphOnly renders a diagnostic-only synthetic frame containing
// glyph atlas coverage without background or other compositor layers. The
// reserved sequence is consumed only by the sidecar shader diagnostic path;
// production rendering never uses it.
func ProbeGPUSceneGlyphOnly(ctx context.Context, executable, session string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, session)
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, session); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, ^uint64(0), 0, scene)
}

// ProbeGPUSceneTextOverlayComposite renders only the uploaded canonical text
// overlay through the diagnostic shader branch.
func ProbeGPUSceneTextOverlayComposite(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-text-overlay-composite")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-text-overlay-composite"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffffe, 0, scene)
}

func ProbeGPUSceneArtworkComposite(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-artwork-composite")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-artwork-composite"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffffd, 0, scene)
}

// ProbeGPUSceneFlatBackground renders only the canonical background palette.
// It is diagnostic-only and never changes the production scene route.
func ProbeGPUSceneFlatBackground(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-flat-background")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-flat-background"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffffc, 0, scene)
}

// ProbeGPUSceneBaseTexture renders the uploaded immutable CPU compositor base
// through the reserved diagnostic branch. It is evidence-only; production
// frames continue to use the normal scene sequence.
func ProbeGPUSceneBaseTexture(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-base-texture")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-base-texture"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffffa, 0, scene)
}

// ProbeGPUSceneBaseText renders the diagnostic static base plus screen text
// composite. Analytic dynamic layers are bypassed by the reserved sequence.
func ProbeGPUSceneBaseText(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-base-text")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-base-text"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xffffffe0, 0, scene)
}

// ProbeGPUSceneSpectrum renders only the CPU-shaped spectrum bar mask. It is
// evidence-only and uses the reserved sequence 0xfffffffb.
func ProbeGPUSceneSpectrum(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-spectrum")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-spectrum"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffffb, 0, scene)
}

// ProbeGPUSceneProgress renders only the progress rail and thumb mask.
func ProbeGPUSceneProgress(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-progress")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-progress"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffff9, 0, scene)
}

// ProbeGPUSceneLoudness renders only the envelope/trend/guide mask.
func ProbeGPUSceneLoudness(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-loudness")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-loudness"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffff8, 0, scene)
}

// ProbeGPUSceneLoudnessOff renders the normal composite with only loudness
// disabled. It is diagnostic-only and does not alter production sequences.
func ProbeGPUSceneLoudnessOff(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-loudness-off")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-loudness-off"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xffffffe1, 0, scene)
}

// ProbeGPUSceneLoudnessTexture reads back the optional loudness texture.
func ProbeGPUSceneLoudnessTexture(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-loudness-texture")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-loudness-texture"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xffffffe2, 0, scene)
}

// ProbeGPUSceneWaveform reads back the exact per-frame waveform texture
// without the rest of the compositor, for CPU showwaves hash/alpha parity.
func ProbeGPUSceneWaveform(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-waveform")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-waveform"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffff7, 0, scene)
}

// ProbeGPUSceneSpectrumTexture reads back the canonical bars+wave texture
// uploaded at binding 14 without compositor layers.
func ProbeGPUSceneSpectrumTexture(ctx context.Context, executable string, width, height uint32, scene *MusicScenePayload) (GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "compare-spectrum-texture")
	if err != nil {
		return GpuFrame{}, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "compare-spectrum-texture"); err != nil {
		return GpuFrame{}, err
	}
	return p.RenderScene(ctx, width, height, 0xfffffff5, 0, scene)
}

// ProbeGPUSceneWaveformSequence reuses one sidecar session for a complete
// frame sequence so upload/readback parity is measured without process-start
// noise or a different adapter state per frame.
func ProbeGPUSceneWaveformSequence(ctx context.Context, executable string, width, height uint32, scenes []*MusicScenePayload) ([]GpuFrame, error) {
	p, err := StartSidecar(ctx, executable, "waveform-sequence")
	if err != nil {
		return nil, err
	}
	defer p.Close()
	if err := p.Hello(ctx, "waveform-sequence"); err != nil {
		return nil, err
	}
	frames := make([]GpuFrame, 0, len(scenes))
	for i, scene := range scenes {
		frame, err := p.RenderScene(ctx, width, height, 0xfffffff7, int64(i)*int64(1_000_000_000/30), scene)
		if err != nil {
			return nil, fmt.Errorf("waveform frame %d: %w", i, err)
		}
		frames = append(frames, frame)
	}
	return frames, nil
}

// ProbeGPUSceneTextOverlay is an opt-in compare diagnostic. It returns only
// transport receipt evidence and does not alter production shader output.
func ProbeGPUSceneTextOverlay(ctx context.Context, executable, session string, width, height uint32, scene *MusicScenePayload) (*TextOverlayReceipt, error) {
	p, err := StartSidecar(ctx, executable, session)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	if err := p.Hello(ctx, session); err != nil {
		return nil, err
	}
	frame, err := p.RenderScene(ctx, width, height, 0, 0, scene)
	if err != nil {
		return nil, err
	}
	return frame.TextOverlayReceipt, nil
}

type sidecarRequest struct {
	Type    string `json:"type"`
	Version uint16 `json:"version,omitempty"`
	Session string `json:"session,omitempty"`
}
type sidecarResponse struct {
	Type      string                 `json:"type"`
	Version   uint16                 `json:"version,omitempty"`
	Ready     bool                   `json:"ready,omitempty"`
	Code      string                 `json:"code,omitempty"`
	Message   string                 `json:"message,omitempty"`
	Frame     *GpuFrame              `json:"frame,omitempty"`
	YuvFrame  *YUV420PFrame          `json:"-"`
	Adapter   string                 `json:"adapter,omitempty"`
	Backend   string                 `json:"backend,omitempty"`
	Toolchain string                 `json:"toolchain,omitempty"`
	Outputs   *GPUOutputCapabilities `json:"outputs,omitempty"`
}

// UnmarshalJSON accepts both the legacy frame response and the additive
// yuv_frame response. Keeping the discriminator here avoids duplicate
// `frame` JSON tags and makes the YUV validation boundary explicit.
func (r *sidecarResponse) UnmarshalJSON(data []byte) error {
	type responseFields struct {
		Type      string                 `json:"type"`
		Version   uint16                 `json:"version,omitempty"`
		Ready     bool                   `json:"ready,omitempty"`
		Code      string                 `json:"code,omitempty"`
		Message   string                 `json:"message,omitempty"`
		Frame     json.RawMessage        `json:"frame,omitempty"`
		Adapter   string                 `json:"adapter,omitempty"`
		Backend   string                 `json:"backend,omitempty"`
		Toolchain string                 `json:"toolchain,omitempty"`
		Outputs   *GPUOutputCapabilities `json:"outputs,omitempty"`
	}
	var f responseFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*r = sidecarResponse{Type: f.Type, Version: f.Version, Ready: f.Ready, Code: f.Code, Message: f.Message, Adapter: f.Adapter, Backend: f.Backend, Toolchain: f.Toolchain, Outputs: f.Outputs}
	if len(f.Frame) == 0 || string(f.Frame) == "null" {
		return nil
	}
	switch f.Type {
	case "frame":
		var frame GpuFrame
		if err := json.Unmarshal(f.Frame, &frame); err != nil {
			return err
		}
		r.Frame = &frame
	case "yuv_frame":
		var frame YUV420PFrame
		if err := json.Unmarshal(f.Frame, &frame); err != nil {
			return err
		}
		if err := frame.Validate(); err != nil {
			return fmt.Errorf("invalid sidecar yuv frame: %w", err)
		}
		r.YuvFrame = &frame
	}
	return nil
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
		if resp.YuvFrame != nil {
			return GpuFrame{}, errors.New("sidecar yuv frame received by rgba render")
		}
		if resp.Frame == nil {
			return GpuFrame{}, errors.New("sidecar frame missing")
		}
		if err := resp.Frame.Validate(); err != nil {
			return GpuFrame{}, err
		}
		if scene != nil && scene.GlyphAtlas != nil {
			if err := validateGlyphAtlasReceipt(scene.GlyphAtlas, resp.Frame.GlyphAtlasReceipt); err != nil {
				return GpuFrame{}, err
			}
		}
		if scene != nil && scene.TextOverlay != nil {
			if err := validateTextOverlayReceipt(scene.TextOverlay, resp.Frame.TextOverlayReceipt); err != nil {
				return GpuFrame{}, err
			}
		}
		if scene != nil && scene.Artwork != nil {
			if err := validateArtworkReceipt(scene.Artwork, resp.Frame.ArtworkReceipt); err != nil {
				return GpuFrame{}, err
			}
		}
		p.glyphReceipt = resp.Frame.GlyphAtlasReceipt
		return *resp.Frame, nil
	}
}

// RenderSceneYUV requests a compute-produced planar YUV420P frame. This is a
// deliberately separate API from RenderScene: a legacy RGBA response is an
// error, never an invitation to perform a CPU colorspace conversion.
func (p *SidecarProcess) RenderSceneYUV(ctx context.Context, width, height uint32, sequence uint64, ptsNS int64, scene *MusicScenePayload) (YUV420PFrame, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return YUV420PFrame{}, ErrGPUUnavailable
	}
	b, _ := json.Marshal(struct {
		Type     string             `json:"type"`
		Output   string             `json:"output"`
		Width    uint32             `json:"width"`
		Height   uint32             `json:"height"`
		Sequence uint64             `json:"sequence"`
		PTSNS    int64              `json:"pts_ns"`
		Scene    *MusicScenePayload `json:"scene,omitempty"`
	}{"render", string(GPUOutputYUV420P), width, height, sequence, ptsNS, scene})
	if _, err := fmt.Fprintln(p.in, string(b)); err != nil {
		return YUV420PFrame{}, err
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
		return YUV420PFrame{}, ctx.Err()
	case e := <-errs:
		return YUV420PFrame{}, e
	case b := <-line:
		var resp sidecarResponse
		if err := json.Unmarshal(b, &resp); err != nil {
			return YUV420PFrame{}, err
		}
		if resp.Code != "" {
			return YUV420PFrame{}, fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
		}
		if resp.Frame != nil {
			return YUV420PFrame{}, errors.New("sidecar rgba frame received by yuv render")
		}
		if resp.Type != "yuv_frame" || resp.YuvFrame == nil {
			return YUV420PFrame{}, errors.New("sidecar yuv frame missing")
		}
		frame := *resp.YuvFrame
		if frame.Sequence != sequence || frame.PTSNs != ptsNS || frame.Width != width || frame.Height != height {
			return YUV420PFrame{}, errors.New("sidecar yuv frame metadata mismatch")
		}
		return frame, nil
	}
}

// SidecarProcess is the JSONL control-plane client for playlist-compositord.
// Frame bytes intentionally stay on GPUFrameTransport until the native mapping
// backend is available.
type SidecarProcess struct {
	cmd          *exec.Cmd
	in           io.WriteCloser
	out          *bufio.Reader
	mu           sync.Mutex
	wait         chan error
	stderr       *boundedBuffer
	startedAt    time.Time
	finishedAt   time.Time
	exitError    error
	closed       bool
	fingerprint  SidecarFingerprint
	glyphReceipt *GlyphAtlasReceipt
	outputs      *GPUOutputCapabilities
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

// OutputCapabilities returns the most recently negotiated sidecar output
// capabilities. A nil result means the sidecar did not advertise outputs.
func (p *SidecarProcess) OutputCapabilities() *GPUOutputCapabilities {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.outputs == nil {
		return nil
	}
	c := *p.outputs
	c.Formats = append([]GPUOutputFormat(nil), p.outputs.Formats...)
	return &c
}

// RequireYUV420Output enforces the GPU-only production boundary. Callers must
// invoke this after Hello and before selecting the YUV transport; absence of
// an advertisement is an explicit unsupported result, never an invitation to
// perform CPU RGBA conversion.
func (p *SidecarProcess) RequireYUV420Output() error {
	return RequireGPUYUV420Output(p.OutputCapabilities())
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
		if r.Outputs != nil {
			c := *r.Outputs
			c.Formats = append([]GPUOutputFormat(nil), r.Outputs.Formats...)
			p.outputs = &c
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
