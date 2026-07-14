package video

import (
	"testing"
	"time"
)

func TestAudioTotalSecondsRoundsUpPartialSeconds(t *testing.T) {
	tests := []struct {
		duration float64
		want     int
	}{
		{-1, 0},
		{0, 0},
		{0.1, 1},
		{60, 60},
		{60.01, 61},
	}
	for _, tc := range tests {
		if got := audioTotalSeconds(tc.duration); got != tc.want {
			t.Errorf("audioTotalSeconds(%v) = %d, want %d", tc.duration, got, tc.want)
		}
	}
}

func TestEnqueueAudioForID_CreatesAudioJob(t *testing.T) {
	outDir := t.TempDir()
	preset := ResolveQuality("720", 0)

	input := AudioRenderInput{
		SourcePath: "test.m4a",
		Kind:       SourceLocalAudio,
		Metadata:   AudioMetadata{Title: "My Song", Artist: "Artist"},
		Analysis:   AudioAnalysis{Duration: 240, FPS: 30},
	}

	jobID := EnqueueAudioForID(input, outDir, "media-1", "My Song", preset)
	t.Cleanup(func() {
		CancelQueue(outDir)
		queues.Delete(outDir)
	})

	if jobID == "" {
		t.Fatal("EnqueueAudioForID returned empty job ID")
	}

	state := queueFor(outDir)
	state.mu.Lock()
	var found *queueJob
	for _, job := range state.items {
		if job.ID == jobID {
			found = job
			break
		}
	}
	state.mu.Unlock()

	if found == nil {
		t.Fatal("audio job not found in queue")
	}
	if found.Mode != "audio" {
		t.Errorf("Mode = %q, want %q", found.Mode, "audio")
	}
	if found.Kind != "audio" {
		t.Errorf("Kind = %q, want %q", found.Kind, "audio")
	}
	if found.Audio == nil {
		t.Fatal("Audio field is nil, want non-nil *AudioRenderInput")
	}
	if found.SourcePath != "test.m4a" {
		t.Errorf("SourcePath = %q, want %q", found.SourcePath, "test.m4a")
	}
	if found.MediaID != "media-1" {
		t.Errorf("MediaID = %q, want %q", found.MediaID, "media-1")
	}
	if found.Title != "My Song" {
		t.Errorf("Title = %q, want %q", found.Title, "My Song")
	}
	if found.Status != "pending" && found.Status != "running" {
		t.Errorf("Status = %q, want pending or running", found.Status)
	}
	if found.TotalSeconds != 240 {
		t.Errorf("TotalSeconds = %d, want %d", found.TotalSeconds, 240)
	}
}

func TestEnqueueAudioForID_SourceKindPreserved(t *testing.T) {
	outDir := t.TempDir()
	preset := ResolveQuality("720", 0)

	tests := []struct {
		name string
		kind SourceKind
	}{
		{"soundcloud", SourceSoundCloud},
		{"local_audio", SourceLocalAudio},
		{"remote_audio", SourceRemoteAudio},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := AudioRenderInput{
				SourcePath: "test.m4a",
				Kind:       tt.kind,
				Metadata:   AudioMetadata{Title: "Test"},
				Analysis:   AudioAnalysis{Duration: 30},
			}

			jobID := EnqueueAudioForID(input, outDir, "id-"+tt.name, "Test", preset)
			t.Cleanup(func() {
				CancelQueue(outDir)
				queues.Delete(outDir)
			})

			state := queueFor(outDir)
			state.mu.Lock()
			var found *queueJob
			for _, job := range state.items {
				if job.ID == jobID {
					found = job
					break
				}
			}
			state.mu.Unlock()

			if found == nil {
				t.Fatal("job not found in queue")
			}
			if found.Mode != "audio" {
				t.Errorf("Mode = %q, want %q", found.Mode, "audio")
			}
			if found.Audio == nil {
				t.Fatal("Audio field is nil")
			}
			if found.Audio.Kind != tt.kind {
				t.Errorf("Audio.Kind = %q, want %q", found.Audio.Kind, tt.kind)
			}
		})
	}
}

func TestEnqueueAudioForID_ZeroDuration(t *testing.T) {
	outDir := t.TempDir()
	preset := ResolveQuality("720", 0)

	input := AudioRenderInput{
		SourcePath: "test.m4a",
		Kind:       SourceLocalAudio,
		Metadata:   AudioMetadata{Title: "No Duration"},
		Analysis:   AudioAnalysis{Duration: 0},
	}

	jobID := EnqueueAudioForID(input, outDir, "zero-dur", "No Duration", preset)
	t.Cleanup(func() {
		CancelQueue(outDir)
		queues.Delete(outDir)
	})

	state := queueFor(outDir)
	state.mu.Lock()
	var found *queueJob
	for _, job := range state.items {
		if job.ID == jobID {
			found = job
			break
		}
	}
	state.mu.Unlock()

	if found == nil {
		t.Fatal("audio job not found in queue")
	}
	if found.TotalSeconds != 0 {
		t.Errorf("TotalSeconds = %d, want 0 for zero-duration input", found.TotalSeconds)
	}
	if found.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero time")
	}
	if !found.CreatedAt.Before(time.Now().Add(time.Second)) {
		t.Error("CreatedAt seems incorrect")
	}
}
