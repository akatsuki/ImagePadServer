package video

import (
	"strings"
	"testing"
	"time"
)

// TestCancelConversionDiscardsPreemptedJob verifies that replacing the published
// media stops the old conversion permanently: a running-but-preempted job for the
// replaced id must become "canceled" (not "pending"), so the queue cannot resume
// it and resurface the old video after the new one has taken over.
func TestCancelConversionDiscardsPreemptedJob(t *testing.T) {
	outDir := t.TempDir()

	canceled := false
	oldRunning := &queueJob{
		QueueItem: QueueItem{ID: "job-old", MediaID: "old", Status: "running"},
		Preempted: true, // a newer publish had preempted it back toward "pending"
		Cancel:    func() { canceled = true },
	}
	oldPending := &queueJob{
		QueueItem: QueueItem{ID: "job-old-2", MediaID: "old", Status: "pending"},
	}
	keep := &queueJob{
		QueueItem: QueueItem{ID: "job-new", MediaID: "new", Status: "pending"},
	}

	state := &queueState{items: []*queueJob{keep, oldRunning, oldPending}}
	queues.Store(outDir, state)
	t.Cleanup(func() { queues.Delete(outDir) })

	CancelConversion(outDir, "old")

	if !canceled {
		t.Fatal("running job for replaced media id was not canceled")
	}
	if oldRunning.Status != "canceled" || oldRunning.Preempted {
		t.Fatalf("running job = %q preempted=%v, want canceled and not preempted", oldRunning.Status, oldRunning.Preempted)
	}
	if oldPending.Status != "canceled" {
		t.Fatalf("pending job = %q, want canceled", oldPending.Status)
	}
	if keep.Status != "pending" {
		t.Fatalf("unrelated job = %q, want pending (untouched)", keep.Status)
	}

	// The replaced media must not be resumable by the queue runner.
	if next := state.nextPending(); next != nil && next.MediaID == "old" {
		t.Fatalf("nextPending resurfaced replaced media: %q", next.ID)
	}
}

func TestEnqueueSoundCloudForID_CreatesSoundCloudJob(t *testing.T) {
	outDir := t.TempDir()
	preset := ResolveQuality("720", 0)
	jobID := EnqueueSoundCloudForID("audio.m4a", "art.jpg", outDir, "media-1", "My Track", preset, 300)
	t.Cleanup(func() {
		CancelQueue(outDir)
		queues.Delete(outDir)
	})

	if jobID == "" {
		t.Fatal("EnqueueSoundCloudForID returned empty job ID")
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
		t.Fatal("SoundCloud job not found in queue")
	}
	if found.Mode != "soundcloud" {
		t.Errorf("Mode = %q, want %q", found.Mode, "soundcloud")
	}
	if found.Kind != "soundcloud" {
		t.Errorf("Kind = %q, want %q", found.Kind, "soundcloud")
	}
	if found.SourcePath != "audio.m4a" {
		t.Errorf("SourcePath = %q, want %q", found.SourcePath, "audio.m4a")
	}
	if found.ArtworkPath != "art.jpg" {
		t.Errorf("ArtworkPath = %q, want %q", found.ArtworkPath, "art.jpg")
	}
	if found.MediaID != "media-1" {
		t.Errorf("MediaID = %q, want %q", found.MediaID, "media-1")
	}
	if found.Title != "My Track" {
		t.Errorf("Title = %q, want %q", found.Title, "My Track")
	}
	if found.Status != "pending" {
		t.Errorf("Status = %q, want %q", found.Status, "pending")
	}
}

func TestEnqueueSoundCloudForID_FallbackTitle(t *testing.T) {
	outDir := t.TempDir()
	preset := ResolveQuality("720", 0)
	jobID := EnqueueSoundCloudForID("audio.m4a", "", outDir, "media-2", "", preset, 0)
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
		t.Fatal("SoundCloud job not found in queue")
	}
	if found.Title != "SoundCloud" {
		t.Errorf("fallback Title = %q, want %q", found.Title, "SoundCloud")
	}
}

func TestRunQueueJob_SoundCloudMode_CreatesCorrectJob(t *testing.T) {
	outDir := t.TempDir()
	preset := ResolveQuality("720", 0)

	jobID := EnqueueSoundCloudForID("audio.m4a", "art.jpg", outDir, "sc-media", "SC Track", preset, 300)
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
		t.Fatal("SoundCloud job not found in queue")
	}
	if found.Mode != "soundcloud" {
		t.Errorf("Mode = %q, want %q", found.Mode, "soundcloud")
	}
	if found.ArtworkPath != "art.jpg" {
		t.Errorf("ArtworkPath = %q, want %q", found.ArtworkPath, "art.jpg")
	}

	// Verify the queue item appears in QueueStatus
	items := QueueStatus(outDir)
	var foundItem bool
	for _, item := range items {
		if item.ID == jobID {
			foundItem = true
			if item.Kind != "soundcloud" {
				t.Errorf("QueueItem.Kind = %q, want %q", item.Kind, "soundcloud")
			}
			if !strings.Contains(item.Title, "SC Track") {
				t.Errorf("QueueItem.Title = %q, want to contain 'SC Track'", item.Title)
			}
			break
		}
	}
	if !foundItem {
		t.Error("SoundCloud job not found in QueueStatus")
	}
}

func TestEnqueueSoundCloudForID_TimestampsAndZeroTotalSeconds(t *testing.T) {
	outDir := t.TempDir()
	preset := ResolveQuality("720", 0)
	before := time.Now()

	jobID := EnqueueSoundCloudForID("audio.m4a", "", outDir, "ts-test", "Timestamps", preset, 0)
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
		t.Fatal("SoundCloud job not found")
	}
	if found.CreatedAt.Before(before) {
		t.Error("CreatedAt before test start")
	}
	if found.TotalSeconds != 0 {
		t.Errorf("TotalSeconds = %d, want 0", found.TotalSeconds)
	}
}
