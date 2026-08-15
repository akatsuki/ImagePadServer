package video

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PlaylistGPUWorker owns one long-lived explicit playlist-compositord session.
// It is evaluation-only: normal CPU music rendering never constructs one.
// The worker deliberately exposes only playlist asset/timeline operations and
// never accepts PCM or final CPU-rendered pixels.
type PlaylistGPUWorker struct {
	process   *SidecarProcess
	cancel    context.CancelFunc
	scheduler *PlaylistGPUBoundedScheduler
	width     uint32
	height    uint32
	fps       uint32
	pooled    bool
}

const (
	playlistGPUSurfaceWidth  uint32 = 640
	playlistGPUSurfaceHeight uint32 = 360
	playlistGPUFPS           uint32 = 30
)

var newPlaylistGPUPlaybackRunner = NewPlaylistGPUPlaybackRunner

// playlistGPUSurfaceDimensions resolves the playlist render surface size. The
// default is 640x360; the diagnostic env IMAGEPAD_PLAYLIST_SURFACE_HEIGHT
// overrides the height (width follows the 16:9 ratio) for explicit GPU
// evaluation at other resolutions such as 1080p. Normal CPU music rendering
// never consults this.
func playlistGPUSurfaceDimensions() (width, height uint32) {
	width, height = playlistGPUSurfaceWidth, playlistGPUSurfaceHeight
	if text := strings.TrimSpace(os.Getenv("IMAGEPAD_PLAYLIST_SURFACE_HEIGHT")); text != "" {
		if v, err := strconv.ParseUint(text, 10, 32); err == nil && v > 0 && v%2 == 0 {
			height = uint32(v)
			width = height * 16 / 9
		}
	}
	return width, height
}

var (
	playlistSidecarPoolMu  sync.Mutex
	playlistSidecarPool    *SidecarProcess
	playlistSidecarPoolExe string
)

// acquirePlaylistSidecar returns a compositord process, reusing a pooled one
// when IMAGEPAD_PLAYLIST_REUSE=1 so the D3D12 device, compiled pipeline, and
// NVENC encoder survive across sessions (the ~2.3s init is paid once per
// process). The pooled process is spawned with context.Background() so it is
// not killed by a worker's Close; ClosePlaylistSidecarPool retires it.
func acquirePlaylistSidecar(ctx context.Context, executable, session string) (*SidecarProcess, bool, error) {
	if os.Getenv("IMAGEPAD_PLAYLIST_REUSE") != "1" {
		p, err := StartSidecar(ctx, executable, session)
		return p, false, err
	}
	playlistSidecarPoolMu.Lock()
	defer playlistSidecarPoolMu.Unlock()
	if playlistSidecarPool != nil && playlistSidecarPoolExe == executable && !playlistSidecarPool.Closed() {
		resetCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := playlistSidecarPool.ResetSession(resetCtx)
		cancel()
		if err == nil {
			return playlistSidecarPool, true, nil
		}
		_ = playlistSidecarPool.Close()
		playlistSidecarPool = nil
	}
	p, err := StartSidecar(context.Background(), executable, session)
	if err != nil {
		return nil, false, err
	}
	playlistSidecarPool = p
	playlistSidecarPoolExe = executable
	// pooled=false: this is a freshly spawned process that still needs Hello.
	return p, false, nil
}

// ClosePlaylistSidecarPool retires the persistent compositord process, if any.
func ClosePlaylistSidecarPool() {
	playlistSidecarPoolMu.Lock()
	defer playlistSidecarPoolMu.Unlock()
	if playlistSidecarPool != nil {
		_ = playlistSidecarPool.Close()
		playlistSidecarPool = nil
	}
}

// StartPlaylistGPUWorker starts and handshakes one long-lived playlist sidecar.
// The caller owns the session and must Close it when the playlist session ends.
func StartPlaylistGPUWorker(parent context.Context, executable, session string) (*PlaylistGPUWorker, error) {
	if parent == nil {
		parent = context.Background()
	}
	if strings.TrimSpace(executable) == "" {
		return nil, errors.New("playlist GPU worker executable is required")
	}
	if strings.TrimSpace(session) == "" {
		return nil, errors.New("playlist GPU worker session is required")
	}
	workerCtx, cancel := context.WithCancel(parent)
	process, reused, err := acquirePlaylistSidecar(workerCtx, executable, session)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start playlist GPU worker sidecar: %w", err)
	}
	width, height := playlistGPUSurfaceDimensions()
	worker := &PlaylistGPUWorker{
		process: process,
		cancel:  cancel,
		width:   width,
		height:  height,
		fps:     playlistGPUFPS,
		// pooled means the process belongs to the persistent pool and must not
		// be killed by Close; reused means it was already handshaken.
		pooled: os.Getenv("IMAGEPAD_PLAYLIST_REUSE") == "1",
	}
	scheduler, err := NewPlaylistGPUBoundedScheduler(worker)
	if err != nil {
		_ = worker.Close()
		return nil, fmt.Errorf("create playlist GPU scheduler: %w", err)
	}
	worker.scheduler = scheduler
	if !reused {
		helloCtx, helloCancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := process.Hello(helloCtx, session); err != nil {
			helloCancel()
			_ = worker.Close()
			return nil, fmt.Errorf("playlist GPU worker hello: %w", err)
		}
		helloCancel()
		if err := process.RequireH264Output(); err != nil {
			_ = worker.Close()
			return nil, fmt.Errorf("playlist GPU worker H264 capability: %w", err)
		}
	}
	return worker, nil
}

func (w *PlaylistGPUWorker) SetPlaylistTransitionPlan(plan *TransitionPlan) error {
	if w == nil || w.process == nil {
		return ErrGPUUnavailable
	}
	return w.process.SetPlaylistTransitionPlan(plan)
}

func (w *PlaylistGPUWorker) PrepareTrack(ctx context.Context, epoch uint64, assets TrackAssets) error {
	if w == nil || w.process == nil {
		return ErrGPUUnavailable
	}
	return w.process.PreparePlaylistTrack(ctx, epoch, assets, w.width, w.height, w.fps)
}

// PrepareTimeline uploads the full immutable timeline once before the bounded
// window loop, so each window references frames by index instead of re-sending
// the per-frame controls.
func (w *PlaylistGPUWorker) PrepareTimeline(ctx context.Context, epoch uint64, assets TrackAssets, timeline TrackTimeline) error {
	if w == nil || w.process == nil {
		return ErrGPUUnavailable
	}
	if err := assets.Validate(); err != nil {
		return err
	}
	if timeline.TrackID != assets.TrackID {
		return errors.New("playlist GPU prepared asset/timeline track mismatch")
	}
	chunk := TimelineChunk{
		ScrollProfiles: append([]ScrollProfile(nil), timeline.ScrollProfiles...),
		Frames:         append([]FrameDirective(nil), timeline.Frames...),
	}
	return w.process.PreparePlaylistTimeline(ctx, epoch, assets.TrackID, assets.AssetsHash, timeline.TimelineVersion, chunk)
}

// playlistGPUOverlayLayout converts the canonical 1280x720 scene layout to
// the actual screen-space dimensions of the CPU-rasterized overlay. TrackAssets
// retain canonical coordinates for the GPU shader; libass instead receives the
// output-sized layout as its PlayRes coordinate system.
func playlistGPUOverlayLayout(width, height uint32) (VisualizerLayout, error) {
	if width == 0 || height == 0 {
		return VisualizerLayout{}, errors.New("playlist GPU overlay dimensions must be non-zero")
	}
	return LayoutForSize(int(width), int(height))
}

func applyPlaylistGPUForegroundPalette(assets *TrackAssets, mode ForegroundMode) {
	if assets == nil {
		return
	}
	assets.Palette.Primary = [4]uint8{mode.PrimaryColor.R, mode.PrimaryColor.G, mode.PrimaryColor.B, mode.PrimaryColor.A}
	assets.Palette.Accent = [4]uint8{mode.AccentColor.R, mode.AccentColor.G, mode.AccentColor.B, mode.AccentColor.A}
	assets.Palette.Overlay = [4]uint8{mode.Overlay.R, mode.Overlay.G, mode.Overlay.B, mode.Overlay.A}
}

func preparePlaylistGPUBaseTexture(ctx context.Context, ffmpeg string, input AudioRenderInput, trackID string, width, height uint32) (*BaseTextureMetadata, ForegroundMode, error) {
	layout, err := playlistGPUOverlayLayout(width, height)
	if err != nil {
		return nil, ForegroundMode{}, err
	}
	fonts, err := VisualizerFonts()
	if err != nil {
		return nil, ForegroundMode{}, fmt.Errorf("playlist base fonts: %w", err)
	}
	var fallback *image.RGBA
	var fallbackRenderer func(color.RGBA) (*image.RGBA, error)
	if input.ArtworkPath == "" {
		fallback, err = RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, color.RGBA{255, 255, 255, 224}, layout.Artwork.W)
		if err != nil {
			return nil, ForegroundMode{}, fmt.Errorf("playlist base fallback artwork: %w", err)
		}
		fallbackRenderer = func(accent color.RGBA) (*image.RGBA, error) {
			return RenderFallbackArtwork(ctx, ffmpeg, fonts, input.Analysis.Features, accent, layout.Artwork.W)
		}
	}
	base, mode, err := RenderVisualizerBaseCPUWithFallback(ctx, ffmpeg, input.ArtworkPath, fallback, fallbackRenderer, layout)
	if err != nil {
		return nil, ForegroundMode{}, fmt.Errorf("playlist base raster: %w", err)
	}
	meta, err := NewBaseTextureMetadata("playlist-base-"+trackID, base, ColorSRGB)
	if err != nil {
		return nil, ForegroundMode{}, fmt.Errorf("playlist base metadata: %w", err)
	}
	return &meta, mode, nil
}

func preparePlaylistGPULoudnessTexture(trackID string, input AudioRenderInput, assets TrackAssets, width, height uint32) (*BaseTextureMetadata, error) {
	layout, err := playlistGPUOverlayLayout(width, height)
	if err != nil {
		return nil, fmt.Errorf("playlist loudness layout: %w", err)
	}
	mode := ForegroundMode{
		PrimaryColor: color.RGBA{R: assets.Palette.Primary[0], G: assets.Palette.Primary[1], B: assets.Palette.Primary[2], A: assets.Palette.Primary[3]},
		AccentColor:  color.RGBA{R: assets.Palette.Accent[0], G: assets.Palette.Accent[1], B: assets.Palette.Accent[2], A: assets.Palette.Accent[3]},
		Overlay:      color.RGBA{R: assets.Palette.Overlay[0], G: assets.Palette.Overlay[1], B: assets.Palette.Overlay[2], A: assets.Palette.Overlay[3]},
	}
	envelope := normalizeRelativeLoudness(input.Analysis.Features.LoudnessEnvelope)
	trend := SmoothLoudnessTrend(envelope, input.Analysis.Duration)
	layer := renderLoudnessLayer(envelope, trend, mode, layout, int(width), int(height))
	meta, err := NewBaseTextureMetadata("playlist-loudness-layer-"+trackID, layer, ColorSRGB)
	if err != nil {
		return nil, fmt.Errorf("playlist loudness layer metadata: %w", err)
	}
	return &meta, nil
}

// CompilePreparedTrackAssets keeps CPU timeline compilation and GPU asset
// preparation separate from sidecar ownership. The returned assets/timeline
// are immutable; no PCM or final CPU pixels are sent to the worker.
func (w *PlaylistGPUWorker) CompilePreparedTrackAssets(ctx context.Context, input AudioRenderInput, trackID string) (TrackAssets, TrackTimeline, error) {
	assets, timeline, err := CompilePlaylistGPUTrack(input, trackID)
	if err != nil {
		return TrackAssets{}, TrackTimeline{}, err
	}
	if assets.BaseTexture == nil {
		ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
		if ffmpeg == "" {
			ffmpeg, err = exec.LookPath("ffmpeg")
			if err != nil {
				return TrackAssets{}, TrackTimeline{}, fmt.Errorf("playlist CPU base raster requires ffmpeg: %w", err)
			}
		}
		var baseMode ForegroundMode
		assets.BaseTexture, baseMode, err = preparePlaylistGPUBaseTexture(ctx, ffmpeg, input, trackID, w.width, w.height)
		if err != nil {
			return TrackAssets{}, TrackTimeline{}, err
		}
		applyPlaylistGPUForegroundPalette(&assets, baseMode)
		assets.AssetsHash, err = playlistTrackAssetsHash(assets)
		if err != nil {
			return TrackAssets{}, TrackTimeline{}, fmt.Errorf("playlist assets hash after base raster: %w", err)
		}
	}
	assets.LoudnessTexture, err = preparePlaylistGPULoudnessTexture(trackID, input, assets, w.width, w.height)
	if err != nil {
		return TrackAssets{}, TrackTimeline{}, err
	}
	assets.AssetsHash, err = playlistTrackAssetsHash(assets)
	if err != nil {
		return TrackAssets{}, TrackTimeline{}, fmt.Errorf("playlist assets hash after loudness raster: %w", err)
	}
	if assets.TextOverlay == nil || assets.TextOverlay.Kind != "screen_rgba" {
		ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
		if ffmpeg == "" {
			ffmpeg, err = exec.LookPath("ffmpeg")
			if err != nil {
				return TrackAssets{}, TrackTimeline{}, fmt.Errorf("playlist CPU text raster requires ffmpeg: %w", err)
			}
		}
		overlayLayout, layoutErr := playlistGPUOverlayLayout(w.width, w.height)
		if layoutErr != nil {
			return TrackAssets{}, TrackTimeline{}, fmt.Errorf("playlist CPU text layout: %w", layoutErr)
		}
		overlay, overlayErr := RenderCanonicalASSOverlay(
			ctx,
			ffmpeg,
			input.Metadata,
			input.Analysis.Duration,
			overlayLayout,
			gpuArtworkForegroundMode(input),
			int(w.width),
			int(w.height),
		)
		if overlayErr != nil {
			return TrackAssets{}, TrackTimeline{}, fmt.Errorf("playlist CPU text raster: %w", overlayErr)
		}
		assets.TextOverlay = overlay
		assets.AssetsHash, err = playlistTrackAssetsHash(assets)
		if err != nil {
			return TrackAssets{}, TrackTimeline{}, fmt.Errorf("playlist assets hash after text raster: %w", err)
		}
	}
	return assets, timeline, nil
}

// PrepareCompiledTrack compiles immutable CPU-owned assets/timeline and then
// publishes that track identity to the long-lived playlist sidecar.
func (w *PlaylistGPUWorker) PrepareCompiledTrack(ctx context.Context, epoch uint64, input AudioRenderInput, trackID string) (TrackAssets, TrackTimeline, error) {
	assets, timeline, err := w.CompilePreparedTrackAssets(ctx, input, trackID)
	if err != nil {
		return TrackAssets{}, TrackTimeline{}, err
	}
	if err := w.PrepareTrack(ctx, epoch, assets); err != nil {
		return TrackAssets{}, TrackTimeline{}, err
	}
	return assets, timeline, nil
}

// RenderPreparedTimelineChunk submits and drains one bounded timeline slice.
// It validates the immutable asset/timeline contract before transport and never
// accepts PCM or a CPU-rendered frame as input.
func (w *PlaylistGPUWorker) RenderPreparedTimelineChunk(ctx context.Context, batchID, epoch uint64, assets TrackAssets, timeline TrackTimeline, start, count int) ([]EncodedH264Frame, error) {
	frames, _, _, err := w.RenderPreparedTimelineChunkTimed(ctx, batchID, epoch, assets, timeline, start, count)
	return frames, err
}

// RenderPreparedTimelineChunkTimed is RenderPreparedTimelineChunk with the
// submit and drain wall-clock durations exposed separately for the throughput
// probe. The drain is ~pure RPC (the Rust-side drain is microsecond-scale after
// the spin-wait fix), so drain - submit isolates the per-frame encode cost from
// the machine-load-sensitive round-trip latency.
func (w *PlaylistGPUWorker) RenderPreparedTimelineChunkTimed(ctx context.Context, batchID, epoch uint64, assets TrackAssets, timeline TrackTimeline, start, count int) (frames []EncodedH264Frame, submitSeconds, drainSeconds float64, err error) {
	if err := assets.Validate(); err != nil {
		return nil, 0, 0, err
	}
	if err := timeline.ValidateChunk(start, count); err != nil {
		return nil, 0, 0, err
	}
	if timeline.TrackID != assets.TrackID {
		return nil, 0, 0, errors.New("playlist GPU prepared asset/timeline track mismatch")
	}
	chunk := TimelineChunk{
		ScrollProfiles: append([]ScrollProfile(nil), timeline.ScrollProfiles...),
		Frames:         append([]FrameDirective(nil), timeline.Frames[start:start+count]...),
	}
	submitStarted := time.Now()
	if err := w.SubmitTimelineBatch(ctx, batchID, epoch, assets.AssetsHash, timeline.TimelineVersion, chunk); err != nil {
		return nil, 0, 0, err
	}
	submitSeconds = time.Since(submitStarted).Seconds()
	drainStarted := time.Now()
	frames, err = w.DrainTimelineBatch(ctx, batchID, epoch, timeline.TimelineVersion)
	drainSeconds = time.Since(drainStarted).Seconds()
	return frames, submitSeconds, drainSeconds, err
}

// RenderPreparedTimeline evaluates an immutable timeline in bounded chunks and
// forwards only ordered compressed frames to the supplied mux sink. It never
// buffers the full track and refuses to finalize partial output.
func (w *PlaylistGPUWorker) RenderPreparedTimeline(ctx context.Context, epoch uint64, assets TrackAssets, timeline TrackTimeline, sink func(EncodedH264Frame) error) error {
	runner, err := newPlaylistGPUPlaybackRunner(w, epoch, assets, timeline, sink)
	if err != nil {
		return err
	}
	if err := w.PrepareTimeline(ctx, epoch, assets, timeline); err != nil {
		return err
	}
	return runner.RenderAll(ctx)
}

// RenderPlaylistGPUFadeTail compiles the TransitionPlan-owned fade tail and
// sends it through the same bounded submit/drain and ordered-output path as a
// normal prepared timeline. It is explicit evaluation-only and never carries
// PCM or CPU-rendered pixels.
func (w *PlaylistGPUWorker) RenderPlaylistGPUFadeTail(ctx context.Context, epoch uint64, assets TrackAssets, plan TransitionPlan, source TrackTimeline, sink func(EncodedH264Frame) error) error {
	tail, err := CompilePlaylistGPUFadeTail(plan, source)
	if err != nil {
		return err
	}
	if w == nil || w.process == nil || w.scheduler == nil {
		return ErrGPUUnavailable
	}
	if err := assets.Validate(); err != nil {
		return err
	}
	mux, err := NewPlaylistGPUOrderedMux(tail.Frames[0].Sequence, tail.Frames[0].PTSNs, tail.FrameCount, sink)
	if err != nil {
		return err
	}
	for start := 0; start < len(tail.Frames); start += GPUH264MaxBatchFrames {
		count := GPUH264MaxBatchFrames
		if remaining := len(tail.Frames) - start; remaining < count {
			count = remaining
		}
		chunk := TimelineChunk{
			ScrollProfiles: append([]ScrollProfile(nil), tail.ScrollProfiles...),
			Frames:         append([]FrameDirective(nil), tail.Frames[start:start+count]...),
		}
		batchID := uint64(start / GPUH264MaxBatchFrames)
		if err := w.scheduler.SubmitFadeTail(ctx, batchID, epoch, assets.AssetsHash, tail.TimelineVersion, chunk); err != nil {
			return err
		}
		frames, err := w.scheduler.Drain(ctx, batchID, epoch, tail.TimelineVersion)
		if err != nil {
			return err
		}
		if err := mux.PushBatch(frames); err != nil {
			return err
		}
	}
	return mux.Finalize()
}

// ExecutePlaylistGPUTransition is the explicit evaluation-only lifecycle
// executor. Each phase is ordered and any contract, pending-window, sidecar,
// or sink failure aborts without fallback or partial success.
func (w *PlaylistGPUWorker) ExecutePlaylistGPUTransition(ctx context.Context, assets TrackAssets, source TrackTimeline, plan TransitionPlan, sink func(EncodedH264Frame) error) error {
	if w == nil || w.process == nil || w.scheduler == nil {
		return ErrGPUUnavailable
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := assets.Validate(); err != nil {
		return err
	}
	if err := source.Validate(); err != nil {
		return err
	}
	if source.TrackID != assets.TrackID || source.TrackID != plan.SourceTrackID {
		return errors.New("playlist GPU transition asset/source/plan track mismatch")
	}
	if err := w.RequestTransition(plan); err != nil {
		return err
	}
	if err := w.scheduler.StopNormalSubmit(); err != nil {
		return err
	}
	if w.scheduler.Pending() {
		return errors.New("playlist GPU transition has an undrained in-flight window")
	}
	if err := w.scheduler.BeginDrainInFlight(); err != nil {
		return err
	}
	if err := w.scheduler.BeginFadeTailRendering(); err != nil {
		return err
	}
	w.clearPlaylistTransitionPlan()
	if err := w.RenderPlaylistGPUFadeTail(ctx, plan.Epoch, assets, plan, source, sink); err != nil {
		return err
	}
	if err := w.scheduler.BeginFlushOutput(); err != nil {
		return err
	}
	if err := w.Flush(ctx); err != nil {
		return err
	}
	return w.scheduler.CompleteTransition(plan.FadeEndSequence)
}

// ExecutePlaylistGPUContinuousTrack renders the source fade tail and the
// complete target track through one sidecar session and one final Flush/EOS.
// It never sends PCM or CPU-composited pixels to the worker.
func (w *PlaylistGPUWorker) ExecutePlaylistGPUContinuousTrack(ctx context.Context, sourceAssets TrackAssets, source TrackTimeline, targetAssets TrackAssets, target TrackTimeline, plan TransitionPlan, targetEpoch uint64, sink func(EncodedH264Frame) error) error {
	if w == nil || w.process == nil || w.scheduler == nil {
		return ErrGPUUnavailable
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.Reason != PlaylistTransitionTrackChange || plan.TargetTrackID == "" {
		return errors.New("continuous playlist GPU execution requires a track-change plan")
	}
	if err := sourceAssets.Validate(); err != nil {
		return err
	}
	if err := source.Validate(); err != nil {
		return err
	}
	if err := targetAssets.Validate(); err != nil {
		return err
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if source.TrackID != sourceAssets.TrackID || source.TrackID != plan.SourceTrackID || target.TrackID != targetAssets.TrackID || target.TrackID != plan.TargetTrackID {
		return errors.New("continuous playlist GPU source/target asset or plan mismatch")
	}
	if targetEpoch <= plan.Epoch {
		return errors.New("continuous playlist GPU target epoch is not newer than transition epoch")
	}
	if err := w.RequestTransition(plan); err != nil {
		return err
	}
	if err := w.scheduler.StopNormalSubmit(); err != nil {
		return err
	}
	if w.scheduler.Pending() {
		return errors.New("continuous playlist GPU transition has an undrained in-flight window")
	}
	if err := w.scheduler.BeginDrainInFlight(); err != nil {
		return err
	}
	if err := w.scheduler.BeginFadeTailRendering(); err != nil {
		return err
	}
	w.clearPlaylistTransitionPlan()
	if err := w.RenderPlaylistGPUFadeTail(ctx, plan.Epoch, sourceAssets, plan, source, sink); err != nil {
		return err
	}
	if err := w.PrepareTrack(ctx, targetEpoch, targetAssets); err != nil {
		return err
	}
	if err := w.scheduler.BeginNextTrackRendering(targetEpoch); err != nil {
		return err
	}
	frameDurationNS := int64(1_000_000_000 / target.FPS)
	if frameDurationNS <= 0 || plan.FadeEndPTSNs > int64(^uint64(0)>>1)-frameDurationNS {
		return errors.New("continuous playlist GPU target timing cannot follow fade tail")
	}
	baseSequence := plan.FadeEndSequence + 1
	if baseSequence == 0 {
		return errors.New("continuous playlist GPU target sequence overflows after fade tail")
	}
	rebasedTarget, err := RebasePlaylistTimeline(target, baseSequence, plan.FadeEndPTSNs+frameDurationNS)
	if err != nil {
		return fmt.Errorf("rebase playlist GPU target sequence/PTS: %w", err)
	}
	rebasedTarget, err = RebasePlaylistTimelineFrameIndex(rebasedTarget, plan.DurationFrames)
	if err != nil {
		return fmt.Errorf("rebase playlist GPU target frame index: %w", err)
	}
	if err := w.RenderPreparedTimeline(ctx, targetEpoch, targetAssets, rebasedTarget, sink); err != nil {
		return err
	}
	if err := w.scheduler.BeginFlushOutputAfterNextTrack(); err != nil {
		return err
	}
	if err := w.Flush(ctx); err != nil {
		return err
	}
	lastSequence := rebasedTarget.Frames[len(rebasedTarget.Frames)-1].Sequence
	if err := w.scheduler.CompleteTransition(lastSequence); err != nil {
		return err
	}
	return w.scheduler.ResumeRunningAfterContinuousTrack(targetEpoch, lastSequence)
}

func (w *PlaylistGPUWorker) RenderTimelineBatch(ctx context.Context, batchID, epoch uint64, assetsHash string, timelineVersion uint64, chunk TimelineChunk) ([]EncodedH264Frame, error) {
	if w == nil || w.process == nil {
		return nil, ErrGPUUnavailable
	}
	return w.process.RenderPlaylistTimelineBatch(ctx, batchID, epoch, assetsHash, timelineVersion, chunk, nil)
}

func (w *PlaylistGPUWorker) SubmitTimelineBatch(ctx context.Context, batchID, epoch uint64, assetsHash string, timelineVersion uint64, chunk TimelineChunk) error {
	if w == nil || w.process == nil {
		return ErrGPUUnavailable
	}
	if w.scheduler == nil {
		return errors.New("playlist GPU worker scheduler is not initialized")
	}
	return w.scheduler.Submit(ctx, batchID, epoch, assetsHash, timelineVersion, chunk)
}

func (w *PlaylistGPUWorker) DrainTimelineBatch(ctx context.Context, batchID, epoch, timelineVersion uint64) ([]EncodedH264Frame, error) {
	if w == nil || w.process == nil {
		return nil, ErrGPUUnavailable
	}
	if w.scheduler == nil {
		return nil, errors.New("playlist GPU worker scheduler is not initialized")
	}
	return w.scheduler.Drain(ctx, batchID, epoch, timelineVersion)
}

func (w *PlaylistGPUWorker) Flush(ctx context.Context) error {
	if w == nil || w.process == nil {
		return ErrGPUUnavailable
	}
	if w.scheduler == nil {
		return errors.New("playlist GPU worker scheduler is not initialized")
	}
	return w.scheduler.Flush(ctx)
}

func (w *PlaylistGPUWorker) AttachTransitionController(controller *PlaylistGPUTransitionController) error {
	if w == nil || w.process == nil || w.scheduler == nil {
		return ErrGPUUnavailable
	}
	return w.scheduler.AttachTransitionController(controller)
}

func (w *PlaylistGPUWorker) RequestTransition(plan TransitionPlan) error {
	if w == nil || w.process == nil || w.scheduler == nil {
		return ErrGPUUnavailable
	}
	return w.scheduler.RequestTransition(plan)
}

func (w *PlaylistGPUWorker) clearPlaylistTransitionPlan() {
	if w != nil && w.process != nil {
		w.process.ClearPlaylistTransitionPlan()
	}
}

func (w *PlaylistGPUWorker) TransitionState() PlaylistGPUTransitionState {
	if w == nil || w.scheduler == nil {
		return PlaylistGPUStateRunning
	}
	return w.scheduler.TransitionState()
}

// ResumeRunningAfterStopTransition reopens the bounded scheduler after a
// stop/pause transition has completed Flush/EOS. The worker remains owned by
// the explicit GPU session; only the final session teardown closes it.
func (w *PlaylistGPUWorker) ResumeRunningAfterStopTransition() error {
	if w == nil {
		return ErrGPUUnavailable
	}
	if w.scheduler == nil {
		// Lightweight unit fixtures may carry only ownership identity. A real
		// worker always has a scheduler from StartPlaylistGPUWorker.
		return nil
	}
	return w.scheduler.ResumeRunningAfterStopTransition()
}

func (w *PlaylistGPUWorker) submitTimelineBatchRaw(ctx context.Context, batchID, epoch uint64, assetsHash string, timelineVersion uint64, chunk TimelineChunk) error {
	if w == nil || w.process == nil {
		return ErrGPUUnavailable
	}
	return w.process.SubmitPlaylistTimelineBatch(ctx, batchID, epoch, assetsHash, timelineVersion, chunk, nil)
}

func (w *PlaylistGPUWorker) drainTimelineBatchRaw(ctx context.Context, batchID, epoch, timelineVersion uint64) ([]EncodedH264Frame, error) {
	if w == nil || w.process == nil {
		return nil, ErrGPUUnavailable
	}
	return w.process.DrainPlaylistTimelineBatch(ctx, batchID, epoch, timelineVersion)
}

func (w *PlaylistGPUWorker) flushRaw(ctx context.Context) error {
	if w == nil || w.process == nil {
		return ErrGPUUnavailable
	}
	return w.process.FlushH264(ctx)
}

func (w *PlaylistGPUWorker) Close() error {
	if w == nil {
		return nil
	}
	process := w.process
	w.process = nil
	var closeErr error
	if process != nil && !w.pooled {
		// Let the sidecar receive its graceful shutdown RPC before cancelling
		// the worker context. Cancelling first races cmd.Wait and turns a clean
		// shutdown into an opaque exit status 1.
		closeErr = process.Close()
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	return closeErr
}
