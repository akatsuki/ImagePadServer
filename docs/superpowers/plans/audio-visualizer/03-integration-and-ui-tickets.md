# Integration and UI Tickets

These tickets touch shared, high-collision files. AV-501 and AV-502 are sequential and single-owner.

### AV-501: Replace the SoundCloud queue mode with generic audio

**Dependencies:** AV-401. **Parallel:** prohibited.

**Files:**
- Create: `internal/video/audio_publisher.go`
- Create: `internal/video/audio_publisher_test.go`
- Modify exclusively: `internal/video/publisher.go`
- Modify exclusively: `internal/video/publisher_test.go`

Required API:

```go
func EnqueueAudioForID(input AudioRenderInput, outDir, id, title string, preset QualityPreset) string
```

Required queue fields:

```go
type queueJob struct {
	QueueItem
	OutDir string
	SourcePath string
	Mode string
	Audio *AudioRenderInput
	Preset QualityPreset
	Cancel context.CancelFunc
	Done chan struct{}
	TotalSeconds int
	Preempted bool
}
```

- [ ] Add failing tests proving SoundCloud, local, and remote inputs all enqueue `Mode == "audio"` and keep their `SourceKind` inside `AudioRenderInput`.
- [ ] Add cancellation, preemption, progress, and completed-file tests for audio jobs.
- [ ] Add a regression test that uploaded video still runs `runUploadedHLS` and never `RunAudioVisualizerHLS`.
- [ ] Run focused tests; expected RED.
- [ ] Implement the generic audio queue path and delete `EnqueueSoundCloudForID` only after all callers compile through the new API.
- [ ] Keep queue status `Kind` user-facing as `audio`; source kind remains metadata, not a separate queue mode.
- [ ] Run `rtk go test ./internal/video -run 'Test(Audio|Queue|Cancel)' -count=1 -v` and package tests.
- [ ] Commit `feat: queue shared audio visualizer jobs`.

### AV-502: Connect local, direct, and SoundCloud audio to publish/queue/history

**Dependencies:** AV-202, AV-203, AV-205, AV-501. **Parallel:** prohibited.

**Files:**
- Create: `internal/server/audio_ingest.go`
- Create: `internal/server/audio_ingest_test.go`
- Modify exclusively: `internal/server/server.go`
- Modify exclusively: `internal/server/server_test.go`
- Modify exclusively: `internal/server/media_paths.go`
- Modify exclusively: `internal/library/store.go`
- Modify exclusively: `internal/library/store_test.go`

Required server entry point:

```go
func (s *Server) processAudioFileAndPublish(r *http.Request, acquired video.AcquiredAudio) (map[string]interface{}, error)
func (s *Server) processAudioFileAndQueue(r *http.Request, acquired video.AcquiredAudio) (map[string]interface{}, error)
```

Required persistent fields:

```go
type CurrentImage struct {
	// existing fields remain
	SourceKind string `json:"sourceKind,omitempty"`
	Title string `json:"title,omitempty"`
	Artist string `json:"artist,omitempty"`
	Album string `json:"album,omitempty"`
}
```

- [ ] Add failing tests for local publish/queue, remote publish/queue, SoundCloud publish/queue, history restore, and cancellation of replaced current audio.
- [ ] Add explicit spies proving local and remote paths invoke zero SoundCloud lookups.
- [ ] Add size-limit tests for both audio and video upload paths using injected small limits; separately assert the production constant.
- [ ] Run focused tests; expected RED.
- [ ] Move audio-specific server methods into `audio_ingest.go`; keep HTTP routing and dispatch in `server.go`.
- [ ] Persist resolved metadata and source kind through history and current-state restoration.
- [ ] Route images before ffprobe, true video to the existing video path, and audio to the shared audio path.
- [ ] Keep video-player-disabled behavior image/RAW-only.
- [ ] Run `rtk go test ./internal/server ./internal/library -count=1` and `rtk go test ./internal/video -count=1`.
- [ ] Commit `feat: integrate audio ingest with server and history`.

### AV-601: Update the enabled media UI

**Dependencies:** AV-502. **Parallel wave:** 7.

**Files:**
- Modify exclusively: `internal/server/ui.go`
- Create: `internal/server/ui_media_test.go`

- [ ] Add failing HTML assertions.

```go
func TestVideoPlayerEnabledMediaCopy(t *testing.T) {
	for _, want := range []string{"画像/音声/動画", "メディアアップロード", "画像、RAW、音声、動画"} {
		if !strings.Contains(indexHTML, want) { t.Fatalf("missing %q", want) }
	}
}
```

- [ ] Add a test that enabled mode removes restrictive `accept` filtering and disabled mode restores the existing image/RAW accept list.
- [ ] Run focused tests; expected RED.
- [ ] Change enabled labels and drop hints; describe the link field as a media URL.
- [ ] Keep the existing image-only strings and `imageAccept` behavior when disabled.
- [ ] Do not redesign unrelated controls or OBS UI.
- [ ] Run `rtk go test ./internal/server -run '^Test.*Media.*UI|^TestVideoPlayerEnabledMediaCopy$' -count=1 -v` and package tests.
- [ ] Commit `feat: expose image audio video uploads in UI`.

### AV-602: Add deterministic and live integration fixtures

**Dependencies:** AV-502. **Parallel wave:** 7.

**Files:**
- Create: `internal/video/testdata/audio/README.txt`
- Create: `internal/video/testdata/soundcloud/gunpei.info.json`
- Create: `internal/video/audio_runtime_test.go`
- Create: `scripts/verify-audio-visualizer.ps1`

- [ ] Add a fixture generator inside the test that uses FFmpeg to create tiny no-art M4A, attached-art M4A, multiple-art FLAC, extensionless audio, and real video fixtures under `t.TempDir()`.
- [ ] Store only a minimal non-copyrighted GUNPEI metadata JSON fixture; do not commit downloaded audio or JPEG.
- [ ] Add `TestIntegrationGUNPEI` guarded by `IMAGEPAD_RUN_NETWORK_TESTS=1`.
- [ ] Require manifest one-line path, playable M4A, no attached image, 715x706 JPEG, title `GUNPEI`, artist `藤子名人`, album `濃度`, and completed HLS.
- [ ] Run ordinary unit tests without network and confirm the live test skips.
- [ ] Run the PowerShell verifier with the network flag and record temp artifact paths plus ffprobe JSON.
- [ ] Commit `test: add audio visualizer runtime verification`.

The script must locate tools through `IMAGEPAD_FFMPEG`, `IMAGEPAD_FFPROBE`, and `IMAGEPAD_YTDLP`, falling back to the app bin directory. It must exit nonzero for any missing tool or failed probe.

### AV-603: Document only verified behavior

**Dependencies:** AV-502; runtime claims also require AV-602 evidence. **Parallel wave:** 7.

**Files:**
- Modify exclusively: `README.md`

- [ ] Add documentation for local audio, generic direct audio URLs, SoundCloud, artwork precedence, metadata fallback, 4 GiB - 1 byte limit, queue/history, and `画像/音声/動画` UI.
- [ ] State that supported audio formats are those recognized by the bundled FFmpeg build; do not publish a false fixed extension list.
- [ ] Include the exact network verification command only after AV-602 records success.
- [ ] Remove obsolete wording that describes only a simple waveform or 2 GB limit.
- [ ] Run `rtk grep "2 GB|2G|画像/動画" README.md` and explain every remaining match or remove it.
- [ ] Run `rtk git diff --check`.
- [ ] Commit `docs: document shared audio visualizer support`.

## Shared-file stop rules

- AV-501 stops if another unmerged branch edits `publisher.go`.
- AV-502 starts only after AV-501 is merged and stops if another unmerged branch edits `server.go` or `store.go`.
- AV-601 does not edit server handlers.
- AV-603 does not alter version numbers or release notes.
