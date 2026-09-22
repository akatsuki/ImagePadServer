package video

import "time"

// NicoStageTiming is an optional diagnostic measurement for one section of the
// Nico encode path. It is wall time, not exclusive CPU time; concurrent
// subprocess sections may overlap.
type NicoStageTiming struct {
	Name       string        `json:"name"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Elapsed    time.Duration `json:"elapsed"`
	Bytes      int64         `json:"bytes,omitempty"`
}

type nicoStageTimer struct {
	timings  []NicoStageTiming
	observer func(NicoStageTiming)
}

func newNicoStageTimer(observer func(NicoStageTiming)) *nicoStageTimer {
	return &nicoStageTimer{observer: observer}
}

func (t *nicoStageTimer) mark(name string, start time.Time) {
	t.markBytes(name, start, 0)
}

func (t *nicoStageTimer) markBytes(name string, start time.Time, bytes int64) {
	if t == nil || name == "" {
		return
	}
	finished := time.Now()
	stage := NicoStageTiming{
		Name:       name,
		StartedAt:  start,
		FinishedAt: finished,
		Elapsed:    finished.Sub(start),
		Bytes:      bytes,
	}
	t.timings = append(t.timings, stage)
	if t.observer != nil {
		t.observer(stage)
	}
}

func (t *nicoStageTimer) record(stage NicoStageTiming) {
	if t == nil || stage.Name == "" {
		return
	}
	t.timings = append(t.timings, stage)
	if t.observer != nil {
		t.observer(stage)
	}
}

func (t *nicoStageTimer) snapshot() []NicoStageTiming {
	if t == nil || len(t.timings) == 0 {
		return nil
	}
	return append([]NicoStageTiming(nil), t.timings...)
}
