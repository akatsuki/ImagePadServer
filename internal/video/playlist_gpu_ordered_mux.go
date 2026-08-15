package video

import "errors"

// PlaylistGPUOutputTelemetry is the bounded correctness receipt for the
// compressed playlist GPU output boundary.
type PlaylistGPUOutputTelemetry struct {
	ExpectedFrames      uint64 `json:"expected_frames"`
	AcceptedFrames      uint64 `json:"accepted_frames"`
	DuplicateCount      uint64 `json:"duplicate_count"`
	ReorderCount        uint64 `json:"reorder_count"`
	PTSViolationCount   uint64 `json:"pts_violation_count"`
	PixelReadbackBytes  uint64 `json:"pixel_readback_bytes"`
	PartialSuccessCount uint64 `json:"partial_success_count"`
}

func (t PlaylistGPUOutputTelemetry) Validate() error {
	if t.ExpectedFrames == 0 || t.AcceptedFrames != t.ExpectedFrames {
		return errors.New("playlist GPU output telemetry has an incomplete frame count")
	}
	if t.DuplicateCount != 0 || t.ReorderCount != 0 || t.PTSViolationCount != 0 {
		return errors.New("playlist GPU output telemetry has ordering violations")
	}
	if t.PixelReadbackBytes != 0 {
		return errors.New("playlist GPU output telemetry contains pixel readback")
	}
	if t.PartialSuccessCount != 0 {
		return errors.New("playlist GPU output telemetry contains partial success")
	}
	return nil
}

// PlaylistGPUOrderedMux validates compressed GPU output before passing it to
// the final mux sink. It keeps only counters and ordering state, not an
// unbounded frame queue.
type PlaylistGPUOrderedMux struct {
	sink          func(EncodedH264Frame) error
	nextSequence  uint64
	nextPTSNs     int64
	expectedCount uint64
	count         uint64
	telemetry     PlaylistGPUOutputTelemetry
}

func NewPlaylistGPUOrderedMux(startSequence uint64, startPTSNs int64, expectedCount uint64, sink func(EncodedH264Frame) error) (*PlaylistGPUOrderedMux, error) {
	if expectedCount == 0 || sink == nil || startPTSNs < 0 {
		return nil, errors.New("playlist GPU ordered mux requires a bounded expected stream and sink")
	}
	return &PlaylistGPUOrderedMux{
		sink:          sink,
		nextSequence:  startSequence,
		nextPTSNs:     startPTSNs,
		expectedCount: expectedCount,
		telemetry:     PlaylistGPUOutputTelemetry{ExpectedFrames: expectedCount},
	}, nil
}

func (m *PlaylistGPUOrderedMux) PushBatch(frames []EncodedH264Frame) error {
	if m == nil || m.sink == nil {
		return errors.New("playlist GPU ordered mux is unavailable")
	}
	if len(frames) == 0 || uint64(len(frames))+m.count > m.expectedCount {
		m.telemetry.PartialSuccessCount++
		return errors.New("playlist GPU ordered mux received an invalid or partial batch")
	}
	for i, frame := range frames {
		if err := frame.Validate(); err != nil {
			if frame.PixelReadbackBytes > 0 {
				m.telemetry.PixelReadbackBytes += frame.PixelReadbackBytes
			}
			return err
		}
		expectedSequence := m.nextSequence + uint64(i)
		if frame.Sequence != expectedSequence {
			if frame.Sequence < expectedSequence {
				m.telemetry.DuplicateCount++
			} else {
				m.telemetry.ReorderCount++
			}
			return errors.New("playlist GPU ordered mux detected sequence disorder")
		}
		if i == 0 {
			if m.count == 0 && frame.PTSNs != m.nextPTSNs {
				m.telemetry.PTSViolationCount++
				return errors.New("playlist GPU ordered mux detected invalid initial PTS")
			}
			if m.count > 0 && frame.PTSNs <= m.nextPTSNs {
				m.telemetry.PTSViolationCount++
				return errors.New("playlist GPU ordered mux detected non-increasing PTS")
			}
			continue
		}
		if frame.PTSNs <= frames[i-1].PTSNs {
			m.telemetry.PTSViolationCount++
			return errors.New("playlist GPU ordered mux detected non-increasing batch PTS")
		}
	}
	for _, frame := range frames {
		if err := m.sink(frame); err != nil {
			m.telemetry.PartialSuccessCount++
			return err
		}
		m.nextSequence++
		m.nextPTSNs = frame.PTSNs
		m.count++
		m.telemetry.AcceptedFrames++
	}
	return nil
}

func (m *PlaylistGPUOrderedMux) Finalize() error {
	if m == nil || m.count != m.expectedCount {
		if m != nil {
			m.telemetry.PartialSuccessCount++
		}
		return errors.New("playlist GPU ordered mux finalized with an incomplete stream")
	}
	return nil
}

func (m *PlaylistGPUOrderedMux) Count() uint64 {
	if m == nil {
		return 0
	}
	return m.count
}

func (m *PlaylistGPUOrderedMux) Telemetry() PlaylistGPUOutputTelemetry {
	if m == nil {
		return PlaylistGPUOutputTelemetry{}
	}
	return m.telemetry
}
