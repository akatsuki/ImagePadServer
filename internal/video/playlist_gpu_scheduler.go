package video

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const PlaylistGPUWindowSlots = GPUH264MaxBatchFrames

// PlaylistGPUBoundedScheduler is the explicit evaluation-only submit/drain
// boundary. It owns at most one in-flight window of up to eight frames because
// the native renderer's slot ring and NVENC output ownership are bounded to the
// same window. Submit and drain are intentionally separate operations.
type PlaylistGPUBoundedScheduler struct {
	mu                   sync.Mutex
	worker               *PlaylistGPUWorker
	pending              *playlistSchedulerPending
	transitionController *PlaylistGPUTransitionController
}

type playlistSchedulerPending struct {
	batchID         uint64
	epoch           uint64
	timelineVersion uint64
	frames          []FrameDirective
}

func NewPlaylistGPUBoundedScheduler(worker *PlaylistGPUWorker) (*PlaylistGPUBoundedScheduler, error) {
	if worker == nil {
		return nil, errors.New("playlist GPU scheduler requires a worker")
	}
	return &PlaylistGPUBoundedScheduler{worker: worker}, nil
}

func (s *PlaylistGPUBoundedScheduler) Submit(ctx context.Context, batchID, epoch uint64, assetsHash string, timelineVersion uint64, chunk TimelineChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker == nil {
		return ErrGPUUnavailable
	}
	if s.pending != nil {
		return errors.New("playlist GPU scheduler has an undrained pending window")
	}
	if s.transitionController != nil && s.transitionController.State() != PlaylistGPUStateRunning && s.transitionController.State() != PlaylistGPUStateNextTrack {
		return errors.New("playlist GPU scheduler normal submit is stopped for a transition")
	}
	if err := chunk.Validate(); err != nil {
		return fmt.Errorf("playlist GPU scheduler submit: %w", err)
	}
	if err := s.worker.submitTimelineBatchRaw(ctx, batchID, epoch, assetsHash, timelineVersion, chunk); err != nil {
		return err
	}
	s.pending = &playlistSchedulerPending{
		batchID:         batchID,
		epoch:           epoch,
		timelineVersion: timelineVersion,
		frames:          append([]FrameDirective(nil), chunk.Frames...),
	}
	return nil
}

// SubmitFadeTail is the transition-only submit boundary. Normal timeline
// submission remains stopped while the controller is in FadeTailRendering.
func (s *PlaylistGPUBoundedScheduler) SubmitFadeTail(ctx context.Context, batchID, epoch uint64, assetsHash string, timelineVersion uint64, chunk TimelineChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker == nil {
		return ErrGPUUnavailable
	}
	if s.pending != nil {
		return errors.New("playlist GPU scheduler has an undrained pending fade window")
	}
	if s.transitionController == nil || s.transitionController.State() != PlaylistGPUStateFadeTailRendering {
		return errors.New("playlist GPU fade submit requires fade tail rendering state")
	}
	if err := chunk.Validate(); err != nil {
		return fmt.Errorf("playlist GPU scheduler fade submit: %w", err)
	}
	if err := s.worker.submitTimelineBatchRaw(ctx, batchID, epoch, assetsHash, timelineVersion, chunk); err != nil {
		return err
	}
	s.pending = &playlistSchedulerPending{
		batchID:         batchID,
		epoch:           epoch,
		timelineVersion: timelineVersion,
		frames:          append([]FrameDirective(nil), chunk.Frames...),
	}
	return nil
}

func (s *PlaylistGPUBoundedScheduler) Drain(ctx context.Context, batchID, epoch, timelineVersion uint64) ([]EncodedH264Frame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker == nil {
		return nil, ErrGPUUnavailable
	}
	if s.pending == nil || s.pending.batchID != batchID || s.pending.epoch != epoch || s.pending.timelineVersion != timelineVersion {
		return nil, errors.New("playlist GPU scheduler drain does not match pending window")
	}
	frames, err := s.worker.drainTimelineBatchRaw(ctx, batchID, epoch, timelineVersion)
	if err != nil {
		return nil, err
	}
	if len(frames) != len(s.pending.frames) {
		return nil, errors.New("playlist GPU scheduler drained frame count mismatch")
	}
	for i, frame := range frames {
		expected := s.pending.frames[i]
		if frame.Sequence != expected.Sequence || frame.PTSNs != expected.PTSNs || frame.PixelReadbackBytes != 0 {
			return nil, fmt.Errorf("playlist GPU scheduler drained frame %d metadata mismatch", i)
		}
	}
	s.pending = nil
	return frames, nil
}

func (s *PlaylistGPUBoundedScheduler) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending != nil
}

// Flush closes the encoded output only after the bounded window has been
// drained. A pending window is never silently discarded or treated as a
// successful transition.
func (s *PlaylistGPUBoundedScheduler) Flush(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker == nil {
		return ErrGPUUnavailable
	}
	if s.pending != nil {
		return errors.New("playlist GPU scheduler cannot flush with a pending window")
	}
	if s.transitionController != nil && s.transitionController.State() != PlaylistGPUStateFlushOutput {
		return errors.New("playlist GPU scheduler Flush/EOS requires completed fade tail")
	}
	return s.worker.flushRaw(ctx)
}

func (s *PlaylistGPUBoundedScheduler) AttachTransitionController(controller *PlaylistGPUTransitionController) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if controller == nil {
		return errors.New("playlist GPU scheduler transition controller is required")
	}
	if s.pending != nil {
		return errors.New("playlist GPU scheduler cannot attach a controller with a pending window")
	}
	s.transitionController = controller
	return nil
}

func (s *PlaylistGPUBoundedScheduler) TransitionState() PlaylistGPUTransitionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return PlaylistGPUStateRunning
	}
	return s.transitionController.State()
}

func (s *PlaylistGPUBoundedScheduler) RequestTransition(plan TransitionPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.worker == nil || s.transitionController == nil {
		return ErrGPUUnavailable
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if err := s.worker.SetPlaylistTransitionPlan(&plan); err != nil {
		return err
	}
	return s.transitionController.Request(plan)
}

func (s *PlaylistGPUBoundedScheduler) StopNormalSubmit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.StopNormalSubmit()
}

func (s *PlaylistGPUBoundedScheduler) BeginDrainInFlight() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.BeginDrainInFlight()
}

func (s *PlaylistGPUBoundedScheduler) BeginFadeTailRendering() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.BeginFadeTailRendering()
}

func (s *PlaylistGPUBoundedScheduler) BeginNextTrackRendering(epoch uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.BeginNextTrackRendering(epoch)
}

func (s *PlaylistGPUBoundedScheduler) BeginFlushOutput() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.BeginFlushOutput()
}

func (s *PlaylistGPUBoundedScheduler) BeginFlushOutputAfterNextTrack() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.BeginFlushOutputAfterNextTrack()
}

func (s *PlaylistGPUBoundedScheduler) CompleteTransition(committedSequence uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.CompleteTransition(committedSequence)
}

// ResumeRunningAfterContinuousTrack reopens normal bounded submission on the
// same explicit GPU session after the target track has been flushed and its
// final sequence committed.
func (s *PlaylistGPUBoundedScheduler) ResumeRunningAfterContinuousTrack(epoch, committedSequence uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.ResumeRunningAfterContinuousTrack(epoch, committedSequence)
}

func (s *PlaylistGPUBoundedScheduler) ResumeRunningAfterStopTransition() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionController == nil {
		return ErrGPUUnavailable
	}
	return s.transitionController.ResumeRunningAfterStopTransition()
}
