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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

func gpuStageTimingEnabled() bool {
	return os.Getenv("IMAGEPAD_GPU_STAGE_TIMING") == "1"
}

func gpuStageLog(format string, args ...any) {
	if gpuStageTimingEnabled() {
		_, _ = fmt.Fprintf(os.Stderr, "[gpu-stage] "+format+"\n", args...)
	}
}

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

const (
	sidecarStderrLimit         = 32 * 1024
	sidecarExtendedStderrLimit = 8 * 1024 * 1024
)

func sidecarStderrCaptureLimit() int {
	if os.Getenv("IMAGEPAD_GPU_STAGE_TRACE_FULL") == "1" {
		return sidecarExtendedStderrLimit
	}
	return sidecarStderrLimit
}

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
	Type                     string                 `json:"type"`
	Version                  uint16                 `json:"version,omitempty"`
	Ready                    bool                   `json:"ready,omitempty"`
	Code                     string                 `json:"code,omitempty"`
	Message                  string                 `json:"message,omitempty"`
	FailedSequence           *uint64                `json:"failed_sequence,omitempty"`
	Frame                    *GpuFrame              `json:"-"`
	YuvFrame                 *YUV420PFrame          `json:"-"`
	EncodedFrame             *EncodedH264Frame      `json:"-"`
	EncodedFrames            []EncodedH264Frame     `json:"-"`
	EncodedTimelineFrames    []EncodedH264Frame     `json:"-"`
	BatchID                  uint64                 `json:"batch_id,omitempty"`
	Epoch                    uint64                 `json:"epoch,omitempty"`
	AssetsHash               string                 `json:"assets_hash,omitempty"`
	TimelineVersion          uint64                 `json:"timeline_version,omitempty"`
	AppliedTransitionVersion *uint64                `json:"applied_transition_version,omitempty"`
	FrameCount               uint64                 `json:"frame_count,omitempty"`
	Adapter                  string                 `json:"adapter,omitempty"`
	Backend                  string                 `json:"backend,omitempty"`
	Toolchain                string                 `json:"toolchain,omitempty"`
	Outputs                  *GPUOutputCapabilities `json:"outputs,omitempty"`
}

// UnmarshalJSON accepts both the legacy frame response and the additive
// yuv_frame response. Keeping the discriminator here avoids duplicate
// `frame` JSON tags and makes the YUV validation boundary explicit.
func (r *sidecarResponse) UnmarshalJSON(data []byte) error {
	type responseFields struct {
		Type                     string                 `json:"type"`
		Version                  uint16                 `json:"version,omitempty"`
		Ready                    bool                   `json:"ready,omitempty"`
		Code                     string                 `json:"code,omitempty"`
		Message                  string                 `json:"message,omitempty"`
		FailedSequence           *uint64                `json:"failed_sequence,omitempty"`
		Frame                    json.RawMessage        `json:"frame,omitempty"`
		Frames                   json.RawMessage        `json:"frames,omitempty"`
		BatchID                  uint64                 `json:"batch_id,omitempty"`
		Epoch                    uint64                 `json:"epoch,omitempty"`
		AssetsHash               string                 `json:"assets_hash,omitempty"`
		TimelineVersion          uint64                 `json:"timeline_version,omitempty"`
		AppliedTransitionVersion *uint64                `json:"applied_transition_version,omitempty"`
		Adapter                  string                 `json:"adapter,omitempty"`
		Backend                  string                 `json:"backend,omitempty"`
		Toolchain                string                 `json:"toolchain,omitempty"`
		Outputs                  *GPUOutputCapabilities `json:"outputs,omitempty"`
	}
	var f responseFields
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	*r = sidecarResponse{Type: f.Type, Version: f.Version, Ready: f.Ready, Code: f.Code, Message: f.Message, FailedSequence: f.FailedSequence, Adapter: f.Adapter, Backend: f.Backend, Toolchain: f.Toolchain, Outputs: f.Outputs, BatchID: f.BatchID, Epoch: f.Epoch, AssetsHash: f.AssetsHash, TimelineVersion: f.TimelineVersion, AppliedTransitionVersion: f.AppliedTransitionVersion}
	if len(f.Frame) == 0 || string(f.Frame) == "null" {
		if f.Type != "encoded_batch" && f.Type != "encoded_timeline_batch" {
			return nil
		}
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
	case "encoded_frame":
		var frame EncodedH264Frame
		if err := json.Unmarshal(f.Frame, &frame); err != nil {
			return err
		}
		if err := frame.Validate(); err != nil {
			return fmt.Errorf("invalid sidecar encoded frame: %w", err)
		}
		r.EncodedFrame = &frame
	case "encoded_batch":
		if len(f.Frames) == 0 || string(f.Frames) == "null" {
			return errors.New("sidecar encoded batch missing frames")
		}
		var frames []EncodedH264Frame
		if err := json.Unmarshal(f.Frames, &frames); err != nil {
			return err
		}
		if len(frames) == 0 || len(frames) > GPUH264MaxBatchFrames {
			return fmt.Errorf("invalid sidecar encoded batch size: %d", len(frames))
		}
		for i := range frames {
			if err := frames[i].Validate(); err != nil {
				return fmt.Errorf("invalid sidecar encoded batch frame %d: %w", i, err)
			}
		}
		r.EncodedFrames = frames
	case "encoded_timeline_batch":
		if len(f.Frames) == 0 || string(f.Frames) == "null" {
			return errors.New("sidecar encoded timeline batch missing frames")
		}
		var frames []EncodedH264Frame
		if err := json.Unmarshal(f.Frames, &frames); err != nil {
			return err
		}
		if len(frames) == 0 || len(frames) > GPUH264MaxBatchFrames {
			return fmt.Errorf("invalid sidecar encoded timeline batch size: %d", len(frames))
		}
		for i := range frames {
			if err := frames[i].Validate(); err != nil {
				return fmt.Errorf("invalid sidecar encoded timeline frame %d: %w", i, err)
			}
		}
		r.EncodedTimelineFrames = frames
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

// H264RenderBatchFrame is one ordered scene in a bounded direct-H264 batch.
type H264RenderBatchFrame struct {
	Sequence uint64
	PTSNS    int64
	Scene    *MusicScenePayload
}

// PreparePlaylistTrack sends immutable CPU-prepared assets through the
// explicit playlist GPU evaluation protocol. It never changes the normal CPU
// renderer route.
func (p *SidecarProcess) PreparePlaylistTrack(ctx context.Context, epoch uint64, assets TrackAssets, width, height, fps uint32) (err error) {
	p.mu.Lock()
	requestSent := false
	defer func() {
		if requestSent && err != nil {
			p.poisonLocked()
		}
		p.mu.Unlock()
	}()
	if p.closed {
		return ErrGPUUnavailable
	}
	if epoch == 0 {
		return errors.New("playlist GPU prepare epoch must be non-zero")
	}
	if width == 0 || height == 0 || fps == 0 {
		return errors.New("playlist GPU surface dimensions and fps must be non-zero")
	}
	if err := assets.Validate(); err != nil {
		return fmt.Errorf("playlist GPU assets: %w", err)
	}
	b, err := json.Marshal(struct {
		Type    string      `json:"type"`
		Epoch   uint64      `json:"epoch"`
		TrackID string      `json:"track_id"`
		Width   uint32      `json:"width"`
		Height  uint32      `json:"height"`
		FPS     uint32      `json:"fps"`
		Assets  TrackAssets `json:"assets"`
	}{"prepare_track", epoch, assets.TrackID, width, height, fps, assets})
	if err != nil {
		return fmt.Errorf("encode playlist GPU prepare request: %w", err)
	}
	requestSent = true
	if _, err := fmt.Fprintln(p.in, string(b)); err != nil {
		return err
	}
	line, err := p.readSidecarLine(ctx)
	if err != nil {
		return err
	}
	var resp sidecarResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Code != "" {
		return fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
	}
	if resp.Type != "track_ready" || resp.Epoch != epoch || resp.AssetsHash != assets.AssetsHash {
		return errors.New("playlist GPU track-ready acknowledgement mismatch")
	}
	p.playlistEpoch = epoch
	p.playlistTrackID = assets.TrackID
	p.playlistAssetsHash = assets.AssetsHash
	p.playlistTimelineVersion = 0
	p.playlistTimelineCursor = playlistTimelineBatchCursor{}
	p.playlistPendingBatch = nil
	p.playlistTimelinePrepared = false
	p.playlistFrameTemplate = &EncodedH264Frame{
		Schema:          GPUContractVersion,
		Width:           width,
		Height:          height,
		Codec:           "h264",
		Backend:         "dx12",
		AssetCacheReady: true,
		AssetReceipt: H264AssetReceipt{
			ArtworkHash: assets.Artwork.AssetHash,
			GlyphHash:   assets.GlyphAtlas.AssetHash,
		},
	}
	return nil
}

// PreparePlaylistTimeline uploads the full immutable per-track timeline once so
// subsequent bounded batches can reference frames by frame_index instead of
// re-sending the per-frame spectrum/waveform/scroll controls. It never changes
// the normal CPU renderer route.
func (p *SidecarProcess) PreparePlaylistTimeline(ctx context.Context, epoch uint64, trackID, assetsHash string, timelineVersion uint64, chunk TimelineChunk) (err error) {
	p.mu.Lock()
	requestSent := false
	defer func() {
		if requestSent && err != nil {
			p.poisonLocked()
		}
		p.mu.Unlock()
	}()
	if p.closed {
		return ErrGPUUnavailable
	}
	if epoch == 0 {
		return errors.New("playlist GPU prepare-timeline epoch must be non-zero")
	}
	if p.playlistEpoch != epoch || p.playlistTrackID != trackID || p.playlistAssetsHash != assetsHash {
		return errors.New("playlist GPU prepare-timeline has no matching prepared track")
	}
	if timelineVersion == 0 {
		return errors.New("playlist GPU timeline version must be non-zero")
	}
	if len(chunk.Frames) == 0 {
		return errors.New("playlist GPU prepare-timeline requires at least one frame")
	}
	if err := chunk.ValidateTimeline(); err != nil {
		return fmt.Errorf("playlist GPU prepare-timeline: %w", err)
	}
	b, err := json.Marshal(struct {
		Type            string        `json:"type"`
		Epoch           uint64        `json:"epoch"`
		TrackID         string        `json:"track_id"`
		AssetsHash      string        `json:"assets_hash"`
		TimelineVersion uint64        `json:"timeline_version"`
		Timeline        TimelineChunk `json:"timeline"`
	}{"prepare_timeline", epoch, trackID, assetsHash, timelineVersion, chunk})
	if err != nil {
		return fmt.Errorf("encode playlist GPU prepare-timeline request: %w", err)
	}
	requestSent = true
	if _, err := fmt.Fprintln(p.in, string(b)); err != nil {
		return err
	}
	line, err := p.readSidecarLine(ctx)
	if err != nil {
		return err
	}
	var resp sidecarResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Code != "" {
		return fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
	}
	if resp.Type != "timeline_prepared" || resp.Epoch != epoch || resp.TimelineVersion != timelineVersion {
		return errors.New("playlist GPU timeline-prepared acknowledgement mismatch")
	}
	p.playlistTimelineVersion = timelineVersion
	p.playlistTimelinePrepared = true
	return nil
}

// RenderPlaylistTimelineBatch sends only bounded frame-indexed controls after
// PreparePlaylistTrack. It deliberately contains no PCM and no final image.
func (p *SidecarProcess) RenderPlaylistTimelineBatch(ctx context.Context, batchID, epoch uint64, assetsHash string, timelineVersion uint64, chunk TimelineChunk, transition *TransitionPlan) (frames []EncodedH264Frame, err error) {
	p.mu.Lock()
	requestSent := false
	defer func() {
		if requestSent && err != nil {
			p.poisonLocked()
		}
		p.mu.Unlock()
	}()
	if p.closed {
		return nil, ErrGPUUnavailable
	}
	if err := RequireGPUH264Output(p.outputs); err != nil {
		return nil, err
	}
	if p.playlistEpoch != epoch || p.playlistAssetsHash != assetsHash || p.playlistTrackID == "" {
		return nil, errors.New("playlist GPU timeline batch has no matching prepared track")
	}
	if timelineVersion == 0 {
		return nil, errors.New("playlist GPU timeline version must be non-zero")
	}
	if err := chunk.Validate(); err != nil {
		return nil, err
	}
	if p.playlistPendingBatch != nil {
		return nil, errors.New("playlist GPU timeline batch is already pending drain")
	}
	if err := p.playlistTimelineCursor.Validate(timelineVersion, batchID, chunk.Frames); err != nil {
		return nil, err
	}
	effectiveTransition := transition
	consumesPendingTransition := false
	if effectiveTransition == nil && p.playlistTransition != nil {
		plan := *p.playlistTransition
		effectiveTransition = &plan
		consumesPendingTransition = true
	}
	b, err := json.Marshal(struct {
		Type            string          `json:"type"`
		BatchID         uint64          `json:"batch_id"`
		Epoch           uint64          `json:"epoch"`
		AssetsHash      string          `json:"assets_hash"`
		TimelineVersion uint64          `json:"timeline_version"`
		Timeline        TimelineChunk   `json:"timeline"`
		Transition      *TransitionPlan `json:"transition,omitempty"`
		Output          string          `json:"output"`
	}{"render_timeline_batch", batchID, epoch, assetsHash, timelineVersion, chunk, effectiveTransition, string(GPUOutputH264Bitstream)})
	if err != nil {
		return nil, fmt.Errorf("encode playlist GPU timeline batch: %w", err)
	}
	requestSent = true
	if _, err := fmt.Fprintln(p.in, string(b)); err != nil {
		return nil, err
	}
	line, err := p.readSidecarLine(ctx)
	if err != nil {
		return nil, err
	}
	var resp sidecarResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "" {
		return nil, fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
	}
	if resp.Type != "encoded_timeline_batch" || resp.BatchID != batchID || resp.Epoch != epoch || resp.TimelineVersion != timelineVersion || len(resp.EncodedTimelineFrames) != len(chunk.Frames) {
		return nil, errors.New("playlist GPU encoded timeline batch acknowledgement mismatch")
	}
	for i, frame := range resp.EncodedTimelineFrames {
		expected := chunk.Frames[i]
		if frame.Sequence != expected.Sequence || frame.PTSNs != expected.PTSNs || frame.PixelReadbackBytes != 0 {
			return nil, fmt.Errorf("playlist GPU encoded timeline frame %d metadata mismatch", i)
		}
	}
	if err := p.playlistTimelineCursor.Commit(timelineVersion, batchID, chunk.Frames); err != nil {
		return nil, fmt.Errorf("commit playlist GPU timeline cursor: %w", err)
	}
	p.playlistTimelineVersion = timelineVersion
	if consumesPendingTransition {
		p.playlistTransition = nil
	}
	return resp.EncodedTimelineFrames, nil
}

// SubmitPlaylistTimelineBatch records one bounded playlist batch on the GPU
// sidecar without draining NVENC output. It is the explicit evaluation-only
// submit half of the submit/drain scheduler; the legacy atomic method above is
// intentionally unchanged.
func (p *SidecarProcess) SubmitPlaylistTimelineBatch(ctx context.Context, batchID, epoch uint64, assetsHash string, timelineVersion uint64, chunk TimelineChunk, transition *TransitionPlan) (err error) {
	if os.Getenv("IMAGEPAD_GPU_STAGE_TIMING") == "1" {
		started := time.Now()
		defer func() {
			fmt.Fprintf(os.Stderr, "[gpu-stage] go_submit_rpc_seconds=%.6f err=%v\n", time.Since(started).Seconds(), err)
		}()
	}
	p.mu.Lock()
	requestSent := false
	defer func() {
		if requestSent && err != nil {
			p.poisonLocked()
		}
		if requestSent {
			p.playlistPendingBatch = nil
		}
		p.mu.Unlock()
	}()
	if p.closed {
		return ErrGPUUnavailable
	}
	if p.playlistPendingBatch != nil {
		return errors.New("playlist GPU batch is already pending drain")
	}
	if err := RequireGPUH264Output(p.outputs); err != nil {
		return err
	}
	if p.playlistEpoch != epoch || p.playlistAssetsHash != assetsHash || p.playlistTrackID == "" {
		return errors.New("playlist GPU timeline batch has no matching prepared track")
	}
	if timelineVersion == 0 {
		return errors.New("playlist GPU timeline version must be non-zero")
	}
	if err := chunk.Validate(); err != nil {
		return err
	}
	if err := p.playlistTimelineCursor.Validate(timelineVersion, batchID, chunk.Frames); err != nil {
		return err
	}
	effectiveTransition := transition
	consumesPendingTransition := false
	if effectiveTransition == nil && p.playlistTransition != nil {
		plan := *p.playlistTransition
		effectiveTransition = &plan
		consumesPendingTransition = true
	}
	var timelinePtr *TimelineChunk
	var frameIndices []uint64
	if p.playlistTimelinePrepared && effectiveTransition == nil {
		frameIndices = make([]uint64, len(chunk.Frames))
		for i, frame := range chunk.Frames {
			frameIndices[i] = frame.FrameIndex
		}
	} else {
		timelinePtr = &chunk
	}
	// Zero-copy ring fast path: prepared timeline, no transition, and the ring
	// transport is enabled. The sidecar's ring worker performs the atomic
	// submit+drain; the drain below collects the raw H.264 from the response
	// ring. The JSONL control plane (transition/timeline upload) is untouched.
	if p.requestRing != nil && timelinePtr == nil && effectiveTransition == nil {
		req := &ringRenderRequest{
			batchID:         batchID,
			epoch:           epoch,
			timelineVersion: timelineVersion,
			frameIndices:    frameIndices,
		}
		if !p.requestRing.tryPush(encodeRingRenderRequest(req)) {
			return errors.New("playlist GPU request ring is full")
		}
		p.playlistPendingBatch = &playlistTimelinePendingBatch{
			batchID:         batchID,
			epoch:           epoch,
			timelineVersion: timelineVersion,
			frames:          append([]FrameDirective(nil), chunk.Frames...),
		}
		if consumesPendingTransition {
			p.playlistTransition = nil
		}
		return nil
	}
	payload, err := json.Marshal(struct {
		Type            string          `json:"type"`
		BatchID         uint64          `json:"batch_id"`
		Epoch           uint64          `json:"epoch"`
		AssetsHash      string          `json:"assets_hash"`
		TimelineVersion uint64          `json:"timeline_version"`
		Timeline        *TimelineChunk  `json:"timeline,omitempty"`
		FrameIndices    []uint64        `json:"frame_indices,omitempty"`
		Transition      *TransitionPlan `json:"transition,omitempty"`
		SubmitOnly      bool            `json:"submit_only"`
		Output          string          `json:"output"`
	}{"render_timeline_batch", batchID, epoch, assetsHash, timelineVersion, timelinePtr, frameIndices, effectiveTransition, true, string(GPUOutputH264Bitstream)})
	if err != nil {
		return fmt.Errorf("encode playlist GPU submit batch: %w", err)
	}
	requestSent = true
	if _, err := fmt.Fprintln(p.in, string(payload)); err != nil {
		return err
	}
	line, err := p.readSidecarLine(ctx)
	if err != nil {
		return err
	}
	var resp sidecarResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Code != "" {
		return fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
	}
	if resp.Type != "timeline_submitted" || resp.BatchID != batchID || resp.Epoch != epoch || resp.TimelineVersion != timelineVersion {
		return errors.New("playlist GPU timeline submit acknowledgement mismatch")
	}
	p.playlistPendingBatch = &playlistTimelinePendingBatch{
		batchID:         batchID,
		epoch:           epoch,
		timelineVersion: timelineVersion,
		frames:          append([]FrameDirective(nil), chunk.Frames...),
	}
	if consumesPendingTransition {
		p.playlistTransition = nil
	}
	requestSent = false
	return nil
}

// DrainPlaylistTimelineBatch collects the compressed H.264 output for the
// previously submitted bounded playlist batch and releases its pending state.
func (p *SidecarProcess) DrainPlaylistTimelineBatch(ctx context.Context, batchID, epoch, timelineVersion uint64) (frames []EncodedH264Frame, err error) {
	if os.Getenv("IMAGEPAD_GPU_STAGE_TIMING") == "1" {
		started := time.Now()
		defer func() {
			fmt.Fprintf(os.Stderr, "[gpu-stage] go_drain_rpc_seconds=%.6f err=%v\n", time.Since(started).Seconds(), err)
		}()
	}
	p.mu.Lock()
	requestSent := false
	defer func() {
		if requestSent && err != nil {
			p.poisonLocked()
		}
		p.mu.Unlock()
	}()
	if p.closed {
		return nil, ErrGPUUnavailable
	}
	pending := p.playlistPendingBatch
	if pending == nil || pending.batchID != batchID || pending.epoch != epoch || pending.timelineVersion != timelineVersion {
		return nil, errors.New("playlist GPU drain does not match pending batch")
	}
	// Zero-copy ring fast path: collect the raw H.264 batch written by the
	// sidecar ring worker and validate it against the pending frames.
	if p.responseRing != nil {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if p.closed {
				return nil, ErrGPUUnavailable
			}
			data := p.responseRing.tryPop()
			if data == nil {
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("playlist GPU ring drain timed out (sidecar: %s)", p.Diagnostics().Stderr)
				}
				runtime.Gosched()
				continue
			}
			batch, err := decodeRingEncodedBatch(data)
			if err != nil {
				return nil, fmt.Errorf("decode playlist GPU ring batch: %w", err)
			}
			if batch.batchID != batchID || batch.epoch != epoch || batch.timelineVersion != timelineVersion {
				return nil, errors.New("playlist GPU ring drain acknowledgement mismatch")
			}
			if len(batch.frames) != len(pending.frames) {
				return nil, errors.New("playlist GPU ring drain frame count mismatch")
			}
			frames := make([]EncodedH264Frame, 0, len(batch.frames))
			template := p.playlistFrameTemplate
			if template == nil {
				return nil, errors.New("playlist GPU frame template is not prepared")
			}
			for i, frame := range batch.frames {
				expected := pending.frames[i]
				if frame.sequence != expected.Sequence || frame.ptsNs != expected.PTSNs || frame.pixelReadbackBytes != 0 {
					return nil, fmt.Errorf("playlist GPU drained frame %d metadata mismatch", i)
				}
				frames = append(frames, EncodedH264Frame{
					Schema:             template.Schema,
					Sequence:           frame.sequence,
					PTSNs:              frame.ptsNs,
					Width:              template.Width,
					Height:             template.Height,
					Codec:              template.Codec,
					Profile:            template.Profile,
					Backend:            template.Backend,
					PixelReadbackBytes: frame.pixelReadbackBytes,
					AssetCacheReady:    template.AssetCacheReady,
					AssetReceipt:       template.AssetReceipt,
					Payload:            frame.payload,
				})
			}
			if err := p.playlistTimelineCursor.Commit(timelineVersion, batchID, pending.frames); err != nil {
				return nil, fmt.Errorf("commit playlist GPU timeline cursor: %w", err)
			}
			p.playlistTimelineVersion = timelineVersion
			p.playlistPendingBatch = nil
			return frames, nil
		}
	}
	payload, err := json.Marshal(struct {
		Type            string `json:"type"`
		BatchID         uint64 `json:"batch_id"`
		Epoch           uint64 `json:"epoch"`
		TimelineVersion uint64 `json:"timeline_version"`
	}{"drain_timeline_batch", batchID, epoch, timelineVersion})
	if err != nil {
		return nil, fmt.Errorf("encode playlist GPU drain batch: %w", err)
	}
	if _, err := fmt.Fprintln(p.in, string(payload)); err != nil {
		return nil, err
	}
	requestSent = true
	line, err := p.readSidecarLine(ctx)
	if err != nil {
		return nil, err
	}
	var resp sidecarResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "" {
		return nil, fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
	}
	if resp.Type != "encoded_timeline_batch" || resp.BatchID != batchID || resp.Epoch != epoch || resp.TimelineVersion != timelineVersion || len(resp.EncodedTimelineFrames) != len(pending.frames) {
		return nil, errors.New("playlist GPU drain acknowledgement mismatch")
	}
	for i, frame := range resp.EncodedTimelineFrames {
		expected := pending.frames[i]
		if frame.Sequence != expected.Sequence || frame.PTSNs != expected.PTSNs || frame.PixelReadbackBytes != 0 {
			return nil, fmt.Errorf("playlist GPU drained frame %d metadata mismatch", i)
		}
	}
	if err := p.playlistTimelineCursor.Commit(timelineVersion, batchID, pending.frames); err != nil {
		return nil, fmt.Errorf("commit playlist GPU timeline cursor: %w", err)
	}
	p.playlistTimelineVersion = timelineVersion
	p.playlistPendingBatch = nil
	return resp.EncodedTimelineFrames, nil
}

// RenderSceneH264Batch sends up to GPUH264MaxBatchFrames in one JSONL request
// and receives one atomic encoded_batch response. This reduces control-plane
// round trips; the Rust renderer may still submit individual frames internally
// until the per-slot surface/buffer ring is implemented.
func (p *SidecarProcess) RenderSceneH264Batch(ctx context.Context, width, height uint32, scenes []H264RenderBatchFrame) (frames []EncodedH264Frame, err error) {
	p.mu.Lock()
	requestSent := false
	defer func() {
		if requestSent && err != nil {
			p.poisonLocked()
		}
		p.mu.Unlock()
	}()
	if p.closed {
		return nil, ErrGPUUnavailable
	}
	if err := RequireGPUH264Output(p.outputs); err != nil {
		return nil, err
	}
	if len(scenes) == 0 || len(scenes) > GPUH264MaxBatchFrames {
		return nil, fmt.Errorf("direct H264 batch must contain 1..%d frames", GPUH264MaxBatchFrames)
	}
	for i, frame := range scenes {
		if frame.Scene == nil {
			return nil, fmt.Errorf("direct H264 batch frame %d has no scene", i)
		}
		if i > 0 {
			previous := scenes[i-1]
			if frame.Sequence != previous.Sequence+1 || frame.PTSNS <= previous.PTSNS {
				return nil, errors.New("direct H264 batch sequence and PTS must be strictly ordered")
			}
		}
	}

	type wireFrame struct {
		Sequence uint64             `json:"sequence"`
		PTSNS    int64              `json:"pts_ns"`
		Scene    *MusicScenePayload `json:"scene"`
	}
	wireFrames := make([]wireFrame, 0, len(scenes))
	assetsSent := p.h264StaticAssetsSent
	knownArtworkHash := p.h264ArtworkHash
	knownGlyphHash := p.h264GlyphHash
	for i, frame := range scenes {
		wireScene := frame.Scene
		if assetsSent {
			if frame.Scene.Artwork == nil || (knownArtworkHash != "" && frame.Scene.Artwork.AssetHash != knownArtworkHash) {
				return nil, fmt.Errorf("direct H264 artwork changed before batch frame %d", i)
			}
			compacted := compactH264Scene(frame.Scene)
			if frame.Scene.GlyphAtlas != nil && knownGlyphHash != "" && frame.Scene.GlyphAtlas.AssetHash != knownGlyphHash {
				glyph := *frame.Scene.GlyphAtlas
				compacted.GlyphAtlas = &glyph
			}
			wireScene = &compacted
		}
		wireFrames = append(wireFrames, wireFrame{Sequence: frame.Sequence, PTSNS: frame.PTSNS, Scene: wireScene})
		assetsSent = true
		if frame.Scene.Artwork != nil {
			knownArtworkHash = frame.Scene.Artwork.AssetHash
		}
		if frame.Scene.GlyphAtlas != nil {
			knownGlyphHash = frame.Scene.GlyphAtlas.AssetHash
		}
	}

	b, err := json.Marshal(struct {
		Type    string      `json:"type"`
		BatchID uint64      `json:"batch_id"`
		Width   uint32      `json:"width"`
		Height  uint32      `json:"height"`
		Output  string      `json:"output"`
		Frames  []wireFrame `json:"frames"`
	}{"render_batch", scenes[0].Sequence, width, height, string(GPUOutputH264Bitstream), wireFrames})
	if err != nil {
		return nil, fmt.Errorf("encode direct H264 batch request: %w", err)
	}
	if len(b) > GPUH264MaxBatchRequestBytes {
		return nil, fmt.Errorf("direct H264 batch request exceeds %d bytes", GPUH264MaxBatchRequestBytes)
	}
	rpcStarted := time.Now()
	requestSent = true
	if _, err := fmt.Fprintln(p.in, string(b)); err != nil {
		return nil, err
	}
	line, err := p.readSidecarLine(ctx)
	if err != nil {
		return nil, err
	}
	if len(line) > GPUH264MaxBatchResponseBytes {
		return nil, fmt.Errorf("direct H264 batch response exceeds %d bytes", GPUH264MaxBatchResponseBytes)
	}
	var resp sidecarResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.Code != "" {
		if resp.Type == "batch_error" && resp.BatchID != scenes[0].Sequence {
			return nil, errors.New("sidecar batch error id mismatch")
		}
		return nil, fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
	}
	if resp.Type != "encoded_batch" || len(resp.EncodedFrames) != len(scenes) {
		return nil, errors.New("sidecar encoded batch response count mismatch")
	}
	if resp.BatchID != scenes[0].Sequence {
		return nil, errors.New("sidecar encoded batch id mismatch")
	}
	for i := range resp.EncodedFrames {
		frame := &resp.EncodedFrames[i]
		expected := scenes[i]
		if frame.Sequence != expected.Sequence || frame.PTSNs != expected.PTSNS || frame.Width != width || frame.Height != height {
			return nil, fmt.Errorf("sidecar encoded batch frame %d metadata mismatch", i)
		}
		if !frame.AssetCacheReady {
			return nil, fmt.Errorf("sidecar encoded batch frame %d asset acknowledgement missing", i)
		}
		if expected.Scene != nil {
			if expected.Scene.Artwork == nil || expected.Scene.GlyphAtlas == nil {
				return nil, fmt.Errorf("direct H264 batch frame %d scene assets missing", i)
			}
			if frame.AssetReceipt.ArtworkHash != expected.Scene.Artwork.AssetHash || frame.AssetReceipt.GlyphHash != expected.Scene.GlyphAtlas.AssetHash {
				return nil, fmt.Errorf("sidecar encoded batch frame %d asset receipt mismatch", i)
			}
		}
	}
	p.h264StaticAssetsSent = true
	p.h264ArtworkHash = resp.EncodedFrames[len(resp.EncodedFrames)-1].AssetReceipt.ArtworkHash
	p.h264GlyphHash = resp.EncodedFrames[len(resp.EncodedFrames)-1].AssetReceipt.GlyphHash
	gpuStageLog("h264_batch first_sequence=%d frames=%d request_bytes=%d response_bytes=%d seconds=%.6f", scenes[0].Sequence, len(resp.EncodedFrames), len(b), len(line), time.Since(rpcStarted).Seconds())
	return resp.EncodedFrames, nil
}

func (p *SidecarProcess) readSidecarLine(ctx context.Context) ([]byte, error) {
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
		return nil, ctx.Err()
	case e := <-errs:
		return nil, e
	case b := <-line:
		return b, nil
	}
}

// RenderSceneH264 requests one compressed H.264 access unit from the direct
// D3D12/NVENC route. Negotiation and frame validation are mandatory; this
// method never accepts RGBA/YUV bytes and never performs a CPU conversion.
func (p *SidecarProcess) RenderSceneH264(ctx context.Context, width, height uint32, sequence uint64, ptsNS int64, scene *MusicScenePayload) (EncodedH264Frame, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return EncodedH264Frame{}, ErrGPUUnavailable
	}
	if err := RequireGPUH264Output(p.outputs); err != nil {
		return EncodedH264Frame{}, err
	}
	rpcStarted := time.Now()
	wireScene := scene
	if p.h264StaticAssetsSent && scene != nil {
		if scene.Artwork == nil || (p.h264ArtworkHash != "" && scene.Artwork.AssetHash != p.h264ArtworkHash) {
			return EncodedH264Frame{}, errors.New("direct H264 artwork changed during a sidecar session")
		}
		compacted := compactH264Scene(scene)
		if scene.GlyphAtlas != nil && p.h264GlyphHash != "" && scene.GlyphAtlas.AssetHash != p.h264GlyphHash {
			glyph := *scene.GlyphAtlas
			compacted.GlyphAtlas = &glyph
		}
		wireScene = &compacted
	}
	b, err := json.Marshal(struct {
		Type     string             `json:"type"`
		Output   string             `json:"output"`
		Width    uint32             `json:"width"`
		Height   uint32             `json:"height"`
		Sequence uint64             `json:"sequence"`
		PTSNS    int64              `json:"pts_ns"`
		Scene    *MusicScenePayload `json:"scene,omitempty"`
	}{"render", string(GPUOutputH264Bitstream), width, height, sequence, ptsNS, wireScene})
	if err != nil {
		return EncodedH264Frame{}, fmt.Errorf("encode direct H264 render request: %w", err)
	}
	requestBytes := len(b)
	if _, err := fmt.Fprintln(p.in, string(b)); err != nil {
		return EncodedH264Frame{}, err
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
		return EncodedH264Frame{}, ctx.Err()
	case e := <-errs:
		return EncodedH264Frame{}, e
	case b := <-line:
		var resp sidecarResponse
		if err := json.Unmarshal(b, &resp); err != nil {
			return EncodedH264Frame{}, err
		}
		if resp.Code != "" {
			return EncodedH264Frame{}, fmt.Errorf("sidecar %s: %s", resp.Code, resp.Message)
		}
		if resp.Frame != nil || resp.YuvFrame != nil {
			return EncodedH264Frame{}, errors.New("sidecar pixel frame received by h264 render")
		}
		if resp.Type != "encoded_frame" || resp.EncodedFrame == nil {
			return EncodedH264Frame{}, errors.New("sidecar encoded frame missing")
		}
		frame := *resp.EncodedFrame
		if frame.Sequence != sequence || frame.PTSNs != ptsNS || frame.Width != width || frame.Height != height {
			return EncodedH264Frame{}, errors.New("sidecar encoded frame metadata mismatch")
		}
		if !frame.AssetCacheReady {
			return EncodedH264Frame{}, errors.New("sidecar encoded frame asset cache acknowledgement missing")
		}
		if scene != nil {
			if scene.Artwork == nil || scene.GlyphAtlas == nil {
				return EncodedH264Frame{}, errors.New("direct H264 scene asset metadata missing")
			}
			if frame.AssetReceipt.ArtworkHash != scene.Artwork.AssetHash || frame.AssetReceipt.GlyphHash != scene.GlyphAtlas.AssetHash {
				return EncodedH264Frame{}, errors.New("sidecar encoded frame asset receipt mismatch")
			}
		}
		if sequence < 2 || sequence%100 == 0 {
			gpuStageLog("h264_rpc sequence=%d request_bytes=%d response_bytes=%d seconds=%.6f", sequence, requestBytes, len(b), time.Since(rpcStarted).Seconds())
		}
		p.h264StaticAssetsSent = true
		p.h264ArtworkHash = frame.AssetReceipt.ArtworkHash
		p.h264GlyphHash = frame.AssetReceipt.GlyphHash
		return frame, nil
	}
}

// compactH264Scene keeps the dynamic JSONL request small after the first
// direct-H264 frame. The Rust renderer owns the uploaded artwork/glyph
// resources for the session; hashes and dimensions remain on the wire so a
// changed asset is still detected and rejected/invalidated fail-closed.
func compactH264Scene(scene *MusicScenePayload) MusicScenePayload {
	compacted := *scene
	if scene.Artwork != nil {
		artwork := *scene.Artwork
		artwork.Payload = nil
		compacted.Artwork = &artwork
	}
	if scene.GlyphAtlas != nil {
		atlas := *scene.GlyphAtlas
		atlas.Payload = nil
		compacted.GlyphAtlas = &atlas
	}
	if scene.TextOverlay != nil {
		overlay := *scene.TextOverlay
		overlay.Payload = nil
		compacted.TextOverlay = &overlay
	}
	compacted.BaseTexture = nil
	compacted.WaveformTexture = nil
	compacted.LoudnessTexture = nil
	compacted.SpectrumTexture = nil
	return compacted
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
	cmd                      *exec.Cmd
	in                       io.WriteCloser
	out                      *bufio.Reader
	mu                       sync.Mutex
	wait                     chan error
	stderr                   *boundedBuffer
	startedAt                time.Time
	finishedAt               time.Time
	exitError                error
	closed                   bool
	fingerprint              SidecarFingerprint
	glyphReceipt             *GlyphAtlasReceipt
	outputs                  *GPUOutputCapabilities
	h264StaticAssetsSent     bool
	h264ArtworkHash          string
	h264GlyphHash            string
	playlistEpoch            uint64
	playlistTrackID          string
	playlistAssetsHash       string
	playlistTimelineVersion  uint64
	playlistTimelineCursor   playlistTimelineBatchCursor
	playlistTransition       *TransitionPlan
	playlistPendingBatch     *playlistTimelinePendingBatch
	playlistTimelinePrepared bool
	requestRing              *sharedRing
	responseRing             *sharedRing
	ringDir                  string
	playlistFrameTemplate    *EncodedH264Frame
}

type playlistTimelinePendingBatch struct {
	batchID         uint64
	epoch           uint64
	timelineVersion uint64
	frames          []FrameDirective
}

// playlistTimelineBatchCursor is the client-side mirror of the sidecar's
// immutable-timeline/multi-chunk cursor. timelineVersion identifies one
// prepared timeline, while batchID is a zero-based transport ordinal within
// that timeline. Validation never mutates the cursor; Commit is called only
// after the corresponding sidecar acknowledgement has been validated.
type playlistTimelineBatchCursor struct {
	initialized     bool
	timelineVersion uint64
	nextBatchID     uint64
	lastSequence    uint64
	lastFrameIndex  uint64
	lastPTSNs       int64
}

func (c playlistTimelineBatchCursor) validateBatchID(timelineVersion, batchID uint64) error {
	if timelineVersion == 0 {
		return errors.New("playlist GPU timeline version must be non-zero")
	}
	if batchID == ^uint64(0) {
		return errors.New("playlist GPU timeline batch id cannot wrap around")
	}
	if !c.initialized {
		if batchID != 0 {
			return errors.New("playlist GPU timeline batch id must start at zero")
		}
		return nil
	}
	if timelineVersion < c.timelineVersion {
		return errors.New("playlist GPU timeline version is stale")
	}
	if timelineVersion == c.timelineVersion && batchID != c.nextBatchID {
		return errors.New("playlist GPU timeline batch id is not contiguous")
	}
	if timelineVersion > c.timelineVersion && batchID != 0 {
		return errors.New("playlist GPU timeline revision must restart batch ids at zero")
	}
	return nil
}

func (c playlistTimelineBatchCursor) Validate(timelineVersion, batchID uint64, frames []FrameDirective) error {
	if err := c.validateBatchID(timelineVersion, batchID); err != nil {
		return err
	}
	if len(frames) == 0 || len(frames) > GPUH264MaxBatchFrames {
		return fmt.Errorf("playlist GPU timeline batch must contain 1..%d frames", GPUH264MaxBatchFrames)
	}
	for i := 1; i < len(frames); i++ {
		previous, current := frames[i-1], frames[i]
		if current.Sequence != previous.Sequence+1 || current.FrameIndex != previous.FrameIndex+1 || current.PTSNs <= previous.PTSNs {
			return errors.New("playlist GPU timeline batch sequence, frame index, and PTS must be strictly ordered")
		}
	}
	if !c.initialized {
		return nil
	}
	first := frames[0]
	if timelineVersion == c.timelineVersion {
		if c.lastSequence == ^uint64(0) || first.Sequence != c.lastSequence+1 || c.lastFrameIndex == ^uint64(0) || first.FrameIndex != c.lastFrameIndex+1 || first.PTSNs <= c.lastPTSNs {
			return errors.New("playlist GPU timeline frame cursor is not contiguous")
		}
		return nil
	}
	if first.Sequence <= c.lastSequence || first.PTSNs <= c.lastPTSNs {
		return errors.New("playlist GPU timeline revision must advance sequence and PTS")
	}
	return nil
}

func (c *playlistTimelineBatchCursor) Commit(timelineVersion, batchID uint64, frames []FrameDirective) error {
	if c == nil {
		return errors.New("playlist GPU timeline cursor is nil")
	}
	if err := c.Validate(timelineVersion, batchID, frames); err != nil {
		return err
	}
	last := frames[len(frames)-1]
	c.initialized = true
	c.timelineVersion = timelineVersion
	c.nextBatchID = batchID + 1
	c.lastSequence = last.Sequence
	c.lastFrameIndex = last.FrameIndex
	c.lastPTSNs = last.PTSNs
	return nil
}

// SetPlaylistTransitionPlan arms one explicit playlist GPU transition. It is
// consumed by the next successful RenderPlaylistTimelineBatch call when that
// call does not provide an inline transition. Normal CPU music rendering never
// reads this state.
func (p *SidecarProcess) SetPlaylistTransitionPlan(plan *TransitionPlan) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrGPUUnavailable
	}
	if plan == nil {
		p.playlistTransition = nil
		return nil
	}
	if err := plan.Validate(); err != nil {
		return fmt.Errorf("invalid playlist GPU transition plan: %w", err)
	}
	copy := *plan
	p.playlistTransition = &copy
	return nil
}

// ClearPlaylistTransitionPlan consumes the control-plane plan before a
// separately compiled fade-tail timeline is submitted. The fade-tail frames
// already carry the plan's sequence/PTS/alpha values and must not be validated
// as an overlapping ordinary timeline batch.
func (p *SidecarProcess) ClearPlaylistTransitionPlan() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.playlistTransition = nil
	p.mu.Unlock()
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
	stderr := &boundedBuffer{max: sidecarStderrCaptureLimit()}
	cmd.Stderr = stderr
	var requestRing, responseRing *sharedRing
	var ringDir string
	if os.Getenv("IMAGEPAD_PLAYLIST_RING") == "1" {
		requestRing, responseRing, ringDir, err = createSidecarRings(cmd)
		if err != nil {
			return nil, err
		}
	}
	startedAt := time.Now()
	if err = cmd.Start(); err != nil {
		cleanupSidecarRings(requestRing, responseRing, ringDir)
		return nil, err
	}
	p := &SidecarProcess{cmd: cmd, in: in, out: bufio.NewReader(stdout), wait: make(chan error, 1), stderr: stderr, startedAt: startedAt, requestRing: requestRing, responseRing: responseRing, ringDir: ringDir}
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.finishedAt, p.exitError = time.Now(), err
		p.mu.Unlock()
		p.wait <- err
	}()
	return p, nil
}

const playlistRingCapacity = 8
const playlistRingSlotBytes = 1 << 20

func createSidecarRings(cmd *exec.Cmd) (*sharedRing, *sharedRing, string, error) {
	dir, err := os.MkdirTemp("", "imagepad-ring-")
	if err != nil {
		return nil, nil, "", err
	}
	requestPath := filepath.Join(dir, "request.ring")
	responsePath := filepath.Join(dir, "response.ring")
	requestRing, err := createSharedRing(requestPath, playlistRingCapacity, playlistRingSlotBytes)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, "", err
	}
	responseRing, err := createSharedRing(responsePath, playlistRingCapacity, playlistRingSlotBytes)
	if err != nil {
		_ = requestRing.close()
		_ = os.RemoveAll(dir)
		return nil, nil, "", err
	}
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = append(base,
		"IMAGEPAD_PLAYLIST_RING_REQUEST="+requestPath,
		"IMAGEPAD_PLAYLIST_RING_RESPONSE="+responsePath,
		"IMAGEPAD_PLAYLIST_RING_CAPACITY=8",
		"IMAGEPAD_PLAYLIST_RING_SLOT_BYTES=1048576",
	)
	return requestRing, responseRing, dir, nil
}

func cleanupSidecarRings(requestRing, responseRing *sharedRing, ringDir string) {
	if requestRing != nil {
		_ = requestRing.close()
	}
	if responseRing != nil {
		_ = responseRing.close()
	}
	if ringDir != "" {
		_ = os.RemoveAll(ringDir)
	}
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
// RequireH264Output enforces the compressed GPU-surface handoff boundary.
// Callers must invoke this after Hello; no pixel transport or CPU encoder
// fallback is selected when the capability is absent.
func (p *SidecarProcess) RequireH264Output() error {
	return RequireGPUH264Output(p.OutputCapabilities())
}

func (p *SidecarProcess) FlushH264(ctx context.Context) error {
	return p.rpc(ctx, sidecarRequest{Type: "flush"})
}

// Closed reports whether the sidecar process has been closed or already exited.
// It is used by the persistent-process pool to decide whether a pooled sidecar
// can be reused for a new session.
func (p *SidecarProcess) Closed() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return true
	}
	select {
	case <-p.wait:
		return true
	default:
		return false
	}
}

// ResetSession clears the sidecar's per-session playlist state while keeping
// the D3D12 device, compiled pipeline, and NVENC encoder so the ~2.3s
// initialization is paid once per process instead of once per session. The
// next PreparePlaylistTrack re-uploads static textures and the next batch
// rebuilds frame slots + spectrum buffer.
func (p *SidecarProcess) ResetSession(ctx context.Context) error {
	if err := p.rpc(ctx, sidecarRequest{Type: "reset_session"}); err != nil {
		return err
	}
	p.mu.Lock()
	p.playlistEpoch = 0
	p.playlistTrackID = ""
	p.playlistAssetsHash = ""
	p.playlistTimelineVersion = 0
	p.playlistTimelineCursor = playlistTimelineBatchCursor{}
	p.playlistPendingBatch = nil
	p.playlistTimelinePrepared = false
	p.playlistFrameTemplate = nil
	p.playlistTransition = nil
	p.mu.Unlock()
	return nil
}

func (p *SidecarProcess) RequireYUV420Output() error {
	return RequireGPUYUV420Output(p.OutputCapabilities())
}

func (p *SidecarProcess) rpc(ctx context.Context, req sidecarRequest) (err error) {
	p.mu.Lock()
	requestSent := false
	defer func() {
		if requestSent && err != nil {
			p.poisonLocked()
		}
		p.mu.Unlock()
	}()
	if p.closed {
		return ErrGPUUnavailable
	}
	b, _ := json.Marshal(req)
	requestSent = true
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
		if req.Type == "flush" && r.Type != "flushed" {
			return fmt.Errorf("sidecar flush acknowledgement missing: type=%s", r.Type)
		}
		if req.Type == "reset_session" && r.Type != "session_reset" {
			return fmt.Errorf("sidecar reset_session acknowledgement missing: type=%s", r.Type)
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

// poisonLocked makes a JSONL session unusable after a request/response
// contract failure. The caller must hold p.mu. Continuing after a malformed,
// timed-out, or mismatched response could associate a later response with the
// wrong batch, so retry requires a fresh sidecar process and asset handshake.
func (p *SidecarProcess) poisonLocked() {
	if p.closed {
		return
	}
	p.closed = true
	if p.in != nil {
		_ = p.in.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *SidecarProcess) Health(ctx context.Context) error {
	return p.rpc(ctx, sidecarRequest{Type: "health"})
}
func (p *SidecarProcess) Hello(ctx context.Context, session string) error {
	return p.rpc(ctx, sidecarRequest{Type: "hello", Version: 1, Session: session})
}
func (p *SidecarProcess) Close() error {
	defer cleanupSidecarRings(p.requestRing, p.responseRing, p.ringDir)
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
		if gpuStageTimingEnabled() {
			gpuStageLog("sidecar_stderr=%q", p.Diagnostics().Stderr)
		}
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
