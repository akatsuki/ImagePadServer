package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"imagepadserver/internal/video"
)

type programFailure struct {
	stage RadioErrorStage
	err   error
}

func (m *RadioManager) runProgramSession(ctx, sessionParent context.Context, done chan struct{}, publisher radioPublisher, encoder ProgramEncoder, contract RadioActiveSessionContract, sessionGeneration uint64) *RadioError {
	width, height := radioProgramOutputSize(contract.FallbackPreset)
	overlayRenderer := NewOverlayRenderer()
	pipeline := NewProgramPipeline(width, height, encoder, func(width, height int, elapsed time.Duration, snapshot OverlaySnapshot) ([]byte, error) {
		return overlayRenderer.RenderRGBA(width, height, elapsed, snapshot), nil
	})
	pipeline.onOverlayFail = func(err error) {
		m.recordProgramOverlayFailure(sessionGeneration, RadioErrorStageOverlay, err, 1)
	}

	failures := make(chan programFailure, 2)
	pumpCtx, cancelPump := context.WithCancel(ctx)
	pumpStopped := make(chan struct{})
	defer func() {
		cancelPump()
		select {
		case <-pumpStopped:
		case <-time.After(500 * time.Millisecond):
			encoder.Close()
			<-pumpStopped
		}
	}()
	go func() {
		defer close(pumpStopped)
		if err := pipeline.Run(pumpCtx); err != nil && pumpCtx.Err() == nil {
			failures <- programFailure{stage: RadioErrorStageEncoder, err: err}
		}
	}()
	go func() {
		_, err := io.Copy(publisher.sink(), encoder.Output())
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failures <- programFailure{stage: RadioErrorStagePublisher, err: fmt.Errorf("%w: %v", errPublisherDown, err)}
			return
		}
		failures <- programFailure{stage: RadioErrorStageEncoder, err: errors.New("program encoder output ended")}
	}()

	for {
		select {
		case failure := <-failures:
			radioErr := newRadioError(failure.stage, failure.err, false)
			return &radioErr
		default:
		}
		if ctx.Err() != nil {
			return nil
		}

		mediaPath, trackID, startSeconds, trackGeneration, pushCtx, cancelPush, ok := m.claimProgramTrack(ctx, sessionParent, done, sessionGeneration)
		if !ok {
			if cancelPush != nil {
				cancelPush()
			}
			if ctx.Err() != nil {
				return nil
			}
			if m.cb.OnIdle != nil {
				m.cb.OnIdle()
			}
			select {
			case <-ctx.Done():
				return nil
			case failure := <-failures:
				radioErr := newRadioError(failure.stage, failure.err, false)
				return &radioErr
			case <-m.wake:
				continue
			}
		}

		snapshot := m.overlaySnapshotAtBoundary()
		pipeline.SetOverlay(snapshot)
		if m.cb.OnTrackStart != nil {
			m.cb.OnTrackStart(trackID)
		}

		var trackErr error
		for attempt := 0; attempt < 2; attempt++ {
			frames := make(chan ProgramSourceFrame, programVideoFrameRate/2)
			pipelineFrames := make(chan ProgramSourceFrame, programVideoFrameRate/2)
			pipeline.SetSource(pipelineFrames)
			framesDrained := relayProgramFrames(pushCtx, frames, pipelineFrames)
			result := make(chan error, 1)
			go func() {
				err := m.runProgramFeeder(pushCtx, mediaPath, startSeconds, width, height, frames)
				close(frames)
				result <- err
			}()
			var canceled bool
			var failure *programFailure
			trackErr, canceled, failure = waitProgramTrackResult(ctx, result, framesDrained, failures, cancelPush)
			if canceled {
				pipeline.ClearSource()
				return nil
			}
			if failure != nil {
				cancelPush()
				pipeline.ClearSource()
				radioErr := newRadioError(failure.stage, failure.err, false)
				return &radioErr
			}
			pipeline.ClearSource()

			m.mu.Lock()
			skipped := m.skipped
			m.mu.Unlock()
			if skipped || trackErr == nil {
				trackErr = nil
				break
			}
			trackErr = SanitizeRadioError(trackErr)
			m.recordProgramOverlayFailure(sessionGeneration, RadioErrorStageDecoder, trackErr, attempt+1)
			if attempt == 0 {
				continue
			}
			pipeline.DisableOverlay()
		}

		cancelPush()
		m.mu.Lock()
		if !m.ownsActiveGenerationLocked(sessionGeneration) {
			m.mu.Unlock()
			return nil
		}
		m.skipPush = nil
		m.mu.Unlock()
		m.completeTrack(sessionGeneration, trackGeneration)
		if m.cb.OnTrackEnd != nil {
			m.cb.OnTrackEnd(trackID, trackErr)
		}
		if trackErr == nil {
			m.clearProgramOverlayError(sessionGeneration)
		}
	}
}

func waitProgramFramesDrained(framesDrained <-chan error, failures <-chan programFailure) *programFailure {
	timer := time.NewTimer(750 * time.Millisecond)
	defer timer.Stop()
	select {
	case failure := <-failures:
		return &failure
	case err := <-framesDrained:
		if err != nil {
			failure := programFailure{stage: RadioErrorStageEncoder, err: err}
			return &failure
		}
		return nil
	case <-timer.C:
		failure := programFailure{stage: RadioErrorStageDecoder, err: errors.New("program pipeline did not acknowledge final frame")}
		return &failure
	}
}

func waitProgramTrackResult(ctx context.Context, result <-chan error, framesDrained <-chan error, failures <-chan programFailure, cancelPush context.CancelFunc) (trackErr error, canceled bool, failure *programFailure) {
	if ctx.Err() != nil {
		cancelPush()
		return nil, true, waitProgramFramesDrained(framesDrained, failures)
	}
	select {
	case <-ctx.Done():
		cancelPush()
		return nil, true, waitProgramFramesDrained(framesDrained, failures)
	case failure := <-failures:
		return nil, false, &failure
	case trackErr := <-result:
		return trackErr, false, waitProgramFramesDrained(framesDrained, failures)
	}
}

func relayProgramFrames(ctx context.Context, source <-chan ProgramSourceFrame, sink chan<- ProgramSourceFrame) <-chan error {
	drained := make(chan error, 1)
	go func() {
		defer close(sink)
		for {
			select {
			case <-ctx.Done():
				drained <- nil
				return
			case frame, open := <-source:
				if !open {
					drained <- nil
					return
				}
				frame.written = make(chan error, 1)
				select {
				case <-ctx.Done():
					drained <- nil
					return
				case sink <- frame:
				}
				err := <-frame.written
				if err != nil {
					drained <- err
					return
				}
			}
		}
	}()
	return drained
}

func (m *RadioManager) claimProgramTrack(ctx, sessionParent context.Context, done chan struct{}, sessionGeneration uint64) (mediaPath, trackID string, startSeconds int, generation uint64, pushCtx context.Context, cancelPush context.CancelFunc, ok bool) {
	pushCtx, cancelPush = context.WithCancel(ctx)
	m.mu.Lock()
	if !m.ownsActiveGenerationLocked(sessionGeneration) {
		m.mu.Unlock()
		return "", "", 0, 0, pushCtx, cancelPush, false
	}
	mediaPath, trackID, startSeconds, ok = m.next()
	if !ok {
		m.status.CurrentTrackID = ""
		m.status.TrackStartedAt = time.Time{}
		m.status.BaseOffsetSeconds = 0
		m.mu.Unlock()
		return "", "", 0, 0, pushCtx, cancelPush, false
	}
	m.skipPush = cancelPush
	m.skipped = false
	m.trackGeneration++
	m.activeTrackDone = make(chan struct{})
	m.activeTrack = TrackGeneration{
		TrackID:         trackID,
		Generation:      m.trackGeneration,
		Completed:       m.activeTrackDone,
		SessionCanceled: sessionParent.Done(),
		SessionDone:     done,
	}
	m.status.CurrentTrackID = trackID
	m.status.TrackStartedAt = time.Now()
	m.status.BaseOffsetSeconds = startSeconds
	generation = m.trackGeneration
	m.mu.Unlock()
	return mediaPath, trackID, startSeconds, generation, pushCtx, cancelPush, true
}

func (m *RadioManager) overlaySnapshotAtBoundary() OverlaySnapshot {
	m.mu.Lock()
	source := m.overlaySource
	m.mu.Unlock()
	if source == nil {
		return OverlaySnapshot{Mode: OverlayModeOff}
	}
	return source()
}

func (m *RadioManager) recordProgramOverlayFailure(generation uint64, stage RadioErrorStage, err error, retryCount int) {
	radioErr := newRadioError(stage, err, true)
	m.mu.Lock()
	if !m.ownsActiveGenerationLocked(generation) {
		m.mu.Unlock()
		return
	}
	m.status.LastError = radioErr.Message
	m.status.OverlayError = radioErr.Message
	m.status.RetryCount = retryCount
	m.mu.Unlock()
	if m.cb.OnError != nil {
		m.cb.OnError(radioErr)
	}
}

func (m *RadioManager) clearProgramOverlayError(generation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsActiveGenerationLocked(generation) {
		return
	}
	m.status.OverlayError = ""
	m.status.RetryCount = 0
}

func radioProgramOutputSize(preset video.QualityPreset) (int, int) {
	height := preset.Height
	if height <= 0 {
		height = 720
	}
	if height%2 != 0 {
		height++
	}
	width := height * 16 / 9
	if width%2 != 0 {
		width++
	}
	return width, height
}
