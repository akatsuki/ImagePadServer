package video

import (
	"context"
	"errors"
	"math"
	"sync"
)

// PlaylistGPUPlaybackRunner is the incremental explicit-evaluation boundary.
// It advances one bounded timeline window at a time and never stores the full
// encoded track in memory.
type PlaylistGPUPlaybackRunner struct {
	mu        sync.Mutex
	worker    *PlaylistGPUWorker
	epoch     uint64
	assets    TrackAssets
	timeline  TrackTimeline
	mux       *PlaylistGPUOrderedMux
	next      int
	nextBatch uint64
	batchSize int
	probed    bool
	warmups   int
	inFlight  bool
	stopped   bool
	failed    error
}

func NewPlaylistGPUPlaybackRunner(worker *PlaylistGPUWorker, epoch uint64, assets TrackAssets, timeline TrackTimeline, sink func(EncodedH264Frame) error) (*PlaylistGPUPlaybackRunner, error) {
	if worker == nil {
		return nil, ErrGPUUnavailable
	}
	if epoch == 0 {
		return nil, errors.New("playlist GPU playback runner requires an epoch")
	}
	if err := assets.Validate(); err != nil {
		return nil, err
	}
	if err := timeline.Validate(); err != nil {
		return nil, err
	}
	if timeline.TrackID != assets.TrackID {
		return nil, errors.New("playlist GPU playback runner asset/timeline track mismatch")
	}
	mux, err := NewPlaylistGPUOrderedMux(timeline.Frames[0].Sequence, timeline.Frames[0].PTSNs, timeline.FrameCount, sink)
	if err != nil {
		return nil, err
	}
	return &PlaylistGPUPlaybackRunner{
		worker:    worker,
		epoch:     epoch,
		assets:    assets,
		timeline:  timeline,
		mux:       mux,
		batchSize: PlaylistGPUProbeBatchFrames,
		warmups:   PlaylistGPUWarmupWindows,
	}, nil
}

// RenderNextWindow submits and drains exactly one 1..8 frame window. The
// cursor advances only after the entire window passes ordered output checks.
func (r *PlaylistGPUPlaybackRunner) RenderNextWindow(ctx context.Context) error {
	if r == nil {
		return ErrGPUUnavailable
	}
	r.mu.Lock()
	if r.failed != nil {
		err := r.failed
		r.mu.Unlock()
		return err
	}
	if r.stopped {
		r.mu.Unlock()
		return errors.New("playlist GPU playback runner normal submit is stopped")
	}
	if r.inFlight {
		r.mu.Unlock()
		return errors.New("playlist GPU playback runner has an in-flight window")
	}
	if r.next >= len(r.timeline.Frames) {
		r.mu.Unlock()
		return errors.New("playlist GPU playback runner has no remaining frames")
	}
	start := r.next
	count := r.batchSize
	if count <= 0 {
		count = PlaylistGPUProbeBatchFrames
	}
	if remaining := len(r.timeline.Frames) - start; remaining < count {
		count = remaining
	}
	batchID := r.nextBatch
	r.nextBatch++
	r.inFlight = true
	r.mu.Unlock()

	frames, submitSeconds, drainSeconds, err := r.worker.RenderPreparedTimelineChunkTimed(ctx, batchID, r.epoch, r.assets, r.timeline, start, count)
	if err == nil {
		err = r.mux.PushBatch(frames)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight = false
	if err != nil {
		r.failed = err
		return err
	}
	r.next += count
	if r.warmups > 0 {
		r.warmups--
	} else if !r.probed {
		r.probed = true
		r.batchSize = computePlaylistBatchSize(submitSeconds, drainSeconds, count)
		if playlistRealTimeBudgetExceeded(submitSeconds, drainSeconds, count, r.timeline.FPS) {
			r.failed = errors.New("playlist GPU render cannot sustain the real-time frame budget under the shared GPU load")
			return r.failed
		}
	}
	return nil
}

// playlistRealTimeBudgetExceeded reports whether the shared GPU is too busy for
// the render to sustain the target frame rate even at the largest window. The
// fixed submit+drain round-trip is amortizable across frames, but the per-frame
// encode cost is not; when it pushes the best-case wall-clock past one frame
// interval, the render must fail closed rather than silently fall behind or
// starve the foreground workload (games such as VRChat).
func playlistRealTimeBudgetExceeded(submitSeconds, drainSeconds float64, probeFrames int, fps uint32) bool {
	if probeFrames <= 0 || fps == 0 || submitSeconds <= 0 || drainSeconds <= 0 {
		return false
	}
	encodeSeconds := submitSeconds - drainSeconds
	if encodeSeconds <= 0 {
		return false
	}
	perFrameEncode := encodeSeconds / float64(probeFrames)
	bestCase := perFrameEncode + drainSeconds/float64(GPUH264MaxBatchFrames)
	return bestCase > 1.0/float64(fps)
}

// computePlaylistBatchSize derives the concurrent-frame window size from the
// probe window's measured submit and drain durations. The drain is ~pure RPC
// (microsecond-scale Rust-side work), so submit - drain isolates the per-frame
// encode cost, and the drain itself is the round-trip the window amortizes.
func computePlaylistBatchSize(submitSeconds, drainSeconds float64, probeFrames int) int {
	if probeFrames <= 0 || drainSeconds <= 0 {
		return PlaylistGPUProbeBatchFrames
	}
	// With the zero-copy ring, submit is ~0 (a bare ring push) and drain carries
	// the render+encode. When submit rounds to zero the round-trip is already
	// eliminated, so there is nothing left to amortize: use the maximum window.
	if submitSeconds <= 0 {
		return GPUH264MaxBatchFrames
	}
	encodeSeconds := submitSeconds - drainSeconds
	if encodeSeconds <= 0 {
		return GPUH264MaxBatchFrames
	}
	perFrameEncode := encodeSeconds / float64(probeFrames)
	if perFrameEncode <= 0 {
		return GPUH264MaxBatchFrames
	}
	batch := int(math.Round(drainSeconds / perFrameEncode))
	if batch < PlaylistGPUProbeBatchFrames {
		batch = PlaylistGPUProbeBatchFrames
	}
	if batch > GPUH264MaxBatchFrames {
		batch = GPUH264MaxBatchFrames
	}
	return batch
}

// RenderAll advances the complete immutable timeline through bounded windows
// and finalizes the ordered mux only after every window has drained. A failed
// window poisons the runner and cannot be converted into partial success.
func (r *PlaylistGPUPlaybackRunner) RenderAll(ctx context.Context) error {
	if r == nil {
		return ErrGPUUnavailable
	}
	for {
		r.mu.Lock()
		failed := r.failed
		next := r.next
		complete := next == len(r.timeline.Frames)
		r.mu.Unlock()
		if failed != nil {
			return failed
		}
		if complete {
			return r.Complete()
		}
		if err := r.RenderNextWindow(ctx); err != nil {
			return err
		}
	}
}

// StopNormalSubmit prevents any new window while allowing an already submitted
// window to finish. The caller must drain that window before calling Complete.
func (r *PlaylistGPUPlaybackRunner) StopNormalSubmit() error {
	if r == nil {
		return ErrGPUUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed != nil {
		return r.failed
	}
	r.stopped = true
	return nil
}

func (r *PlaylistGPUPlaybackRunner) Complete() error {
	if r == nil {
		return ErrGPUUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed != nil {
		return r.failed
	}
	if r.inFlight || r.next != len(r.timeline.Frames) {
		return errors.New("playlist GPU playback runner cannot complete a partial stream")
	}
	return r.mux.Finalize()
}

func (r *PlaylistGPUPlaybackRunner) NextFrame() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.next
}

func (r *PlaylistGPUPlaybackRunner) Stopped() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopped
}
