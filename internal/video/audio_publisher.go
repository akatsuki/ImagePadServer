package video

import (
	"math"
	"time"
)

// EnqueueAudioForID enqueues a generic audio visualizer job. The input's
// SourceKind (soundcloud, local_audio, remote_audio) is preserved inside the
// AudioRenderInput for later inspection; the queue Kind is always "audio".
func EnqueueAudioForID(input AudioRenderInput, outDir, id, title string, preset QualityPreset) string {
	return enqueueConversion(&queueJob{
		QueueItem: QueueItem{
			ID:        queueID(),
			MediaID:   id,
			Title:     fallbackTitle(title, ""),
			Kind:      "audio",
			Status:    "pending",
			Message:   "変換待ち",
			Quality:   preset.Effective,
			CreatedAt: time.Now(),
		},
		OutDir:       outDir,
		SourcePath:   input.SourcePath,
		Mode:         "audio",
		Preset:       preset,
		TotalSeconds: audioTotalSeconds(input.Analysis.Duration),
		Audio:        &input,
	})
}

func audioTotalSeconds(duration float64) int {
	if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return 0
	}
	return int(math.Ceil(duration))
}
