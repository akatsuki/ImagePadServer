# Audio Visualizer Review-Correction Tickets

These tickets correct defects found in the post-implementation review at commit `5c5b872`. They supersede any earlier completion claim for AV-700. Every worker must follow `00-dispatch-contract.md` and must capture a behavior-specific RED result before implementation.

## Correction dependency graph

```text
AV-711 local upload routing ───────┐
AV-713 SoundCloud metadata ────────┼─> AV-716 complete renderer ─┐
AV-714 BPM units -> AV-715 streaming analysis ┘                  ├─> AV-718 runtime QA -> AV-719 closure -> AV-710
AV-711 -> AV-712 direct URL routing ─────────────────────────────┤
AV-712 + AV-715 -> AV-717 history re-analysis ───────────────────┘
```

AV-711, AV-713, and AV-714 have disjoint write sets and may run in parallel. AV-712 starts only after AV-711 because both integrate HTTP routing. AV-715 starts after AV-714 because both own `audio_analysis.go`. AV-716 and AV-717 may run in parallel after their dependencies merge.

### AV-711: Route local audio uploads into the shared audio pipeline

**Dependencies:** review at `5c5b872`. **Parallel wave:** C1. **Exclusive owner:** `internal/server/server.go`.

**Files:**
- Create: `internal/server/audio_upload.go`
- Create: `internal/server/audio_upload_test.go`
- Modify exclusively: `internal/server/server.go`
- Modify: `internal/server/audio_ingest.go`

Required helper:

```go
func (s *Server) acquireUploadedAudio(ctx context.Context, reader io.Reader, name string) (video.AcquiredAudio, error)
```

- [ ] Add `TestProcessAndPublishLocalAudioUsesSharedPipeline` and `TestProcessAndQueueLocalAudioUsesSharedPipeline`. Upload valid generated WAV bytes with a misleading or empty content type; assert `SourceKind == "local_audio"`, stored metadata, and an audio queue job.
- [ ] Add `TestLocalAudioNeverUsesSoundCloudMetadata`. Inject empty embedded tags and assert filename fallback without a SoundCloud call or value.
- [ ] Run `rtk go test ./internal/server -run '^Test(ProcessAndPublishLocalAudio|ProcessAndQueueLocalAudio|LocalAudioNever)' -count=1 -v`; expected RED because `processAndPublish` currently sends non-video audio to `imageproc.Process`.
- [ ] Save the multipart body to a unique temporary source with `video.CopyMediaWithLimit`, resolve `EnsureFFprobe`, call `video.ProbeMedia`, and dispatch `MediaAudio` to `processAudioFileAndPublish` or `processAudioFileAndQueue`.
- [ ] Populate `AcquiredAudio.Probe`, embedded tags from the probe, and candidates from `ExtractEmbeddedArtwork`; set `Kind: video.SourceLocalAudio`. Do not call yt-dlp or any SoundCloud helper.
- [ ] Preserve the existing image/RAW fast path and existing video path. Delete a temporary upload on every error before ownership transfers to the store.
- [ ] Run the focused command, `rtk go test ./internal/server ./internal/video -count=1`, and `rtk git diff --check`.
- [ ] Commit `fix: route local audio uploads through visualizer`.

### AV-712: Connect direct audio URLs to the SSRF-safe remote pipeline

**Dependencies:** AV-711. **Parallel:** prohibited with any `server.go` ticket.

**Files:**
- Modify exclusively: `internal/server/server.go`
- Modify: `internal/server/remote_media.go`
- Modify: `internal/server/remote_media_test.go`
- Create: `internal/server/remote_audio_route_test.go`

- [ ] Add publish and queue handler tests using an `httptest.Server` that returns audio bytes after one allowed redirect. Assert `SourceKind == "remote_audio"` and that the audio queue path is selected.
- [ ] Add handler tests for private-network redirect rejection, `Content-Length > MaxMediaSourceBytes`, streamed overflow, audio without a filename extension, and unchanged remote video routing.
- [ ] Run `rtk go test ./internal/server -run '^TestHandleUploadURL.*(RemoteAudio|Redirect|Limit|RemoteVideo)' -count=1 -v`; expected RED because non-SoundCloud URLs currently always call `downloadVideoURL` and become `Kind == "video"`.
- [ ] In `handleUploadURL` and `handleUploadURLQueue`, preserve SoundCloud-page detection first. For other HTTP(S) URLs, call `downloadRemoteMedia` with `video.ProbeMedia`; dispatch `MediaAudio` as `SourceRemoteAudio`, `MediaVideo` to the existing video path, and reject `MediaUnsupported`.
- [ ] Extract embedded metadata and artwork for remote audio exactly as for local audio. Never call SoundCloud metadata or artwork discovery.
- [ ] Ensure every redirect is revalidated and partial files are removed on all failures.
- [ ] Run focused tests, `rtk go test ./internal/server -count=1`, and `rtk git diff --check`.
- [ ] Commit `fix: route direct audio URLs through remote ingest`.

### AV-713: Preserve SoundCloud metadata and sidecar binding through dispatch

**Dependencies:** review at `5c5b872`. **Parallel wave:** C1.

**Files:**
- Modify exclusively: `internal/video/soundcloud.go`
- Modify: `internal/video/soundcloud_test.go`
- Modify: `internal/video/soundcloud_download_test.go`
- Modify: `internal/server/audio_ingest_test.go`

Required result fields:

```go
type DownloadedMedia struct {
	SourcePath string
	Name string
	Kind string
	ArtworkPath string
	Metadata AudioMetadata
	InformationPath string
}
```

- [ ] Add `TestDownloadMediaURLPreservesSoundCloudMetadata` with a fake download result containing title, artist, album, and uploader. Assert all fields and the matching `.info.json` path survive the wrapper.
- [ ] Add a server-facing test proving the GUNPEI fallback values become `Title=GUNPEI`, `Artist=藤子名人`, and `Album=濃度` when embedded fields are empty.
- [ ] Run `rtk go test ./internal/video ./internal/server -run 'Test.*SoundCloud.*Metadata' -count=1 -v`; expected RED because `DownloadMediaURL` currently returns only source, name, kind, and artwork.
- [ ] Copy `SoundCloudMetadata` and `SoundCloudInformationPath` from `AcquiredAudio` into `DownloadedMedia`; pass them back into `AcquiredAudio` at the HTTP dispatch boundary without re-reading unrelated sidecars.
- [ ] Keep embedded metadata precedence in `ResolveAudioMetadata` and keep missing/invalid info JSON nonfatal.
- [ ] Run focused tests, package tests, and `rtk git diff --check`.
- [ ] Commit `fix: preserve SoundCloud metadata through ingest`.

### AV-714: Correct onset-flux timing and BPM autocorrelation units

**Dependencies:** review at `5c5b872`. **Parallel wave:** C1. **Exclusive owner:** `internal/video/audio_analysis.go`.

**Files:**
- Modify exclusively: `internal/video/audio_analysis.go`
- Modify: `internal/video/audio_analysis_test.go`

Use a fixed onset rate:

```go
const onsetHopSamples = 480 // 10 ms at 48 kHz
const onsetRate = sampleRate / onsetHopSamples // 100 Hz
```

- [ ] Add deterministic click-track tests for 60, 90, 120, and 180 BPM; require absolute error `<= 2 BPM`. Add silence and sub-two-second inputs that must return zero without panic.
- [ ] Add `TestComputeBPMUsesOnsetFrameUnits` proving the autocorrelation lag for 120 BPM is approximately 50 onset frames, not 24,000 PCM samples.
- [ ] Run `rtk go test ./internal/video -run '^TestComputeBPM' -count=1 -v`; expected RED with BPM zero or outside tolerance.
- [ ] Build one onset value per `onsetHopSamples`, compute lag bounds as `onsetRate*60/maxBPM` through `onsetRate*60/minBPM`, and return `60*onsetRate/bestLag`.
- [ ] Keep the accepted search range 60-200 BPM and deterministic tie-breaking toward the lower lag only when correlation is strictly greater.
- [ ] Run focused tests, `rtk go test ./internal/video -count=1`, and `rtk git diff --check`.
- [ ] Commit `fix: calculate BPM in onset-frame units`.

### AV-715: Stream PCM analysis with bounded memory

**Dependencies:** AV-714. **Parallel:** prohibited with AV-714 or AV-716. **Exclusive owner:** `internal/video/audio_analysis.go`.

**Files:**
- Modify exclusively: `internal/video/audio_analysis.go`
- Modify: `internal/video/audio_analysis_test.go`
- Create: `internal/video/audio_analysis_stream_test.go`

Required internal seam:

```go
type pcmAnalyzer interface {
	ConsumeStereo(samples []int16) error
	Finish() (AudioAnalysis, error)
}
```

- [ ] Add a bounded-reader test that feeds at least ten minutes of generated PCM in small chunks and asserts peak retained sample capacity stays below 20 seconds of stereo PCM.
- [ ] Add equivalence tests comparing streamed and short-buffer results for duration, 30 fps frame count, envelope length, fingerprint length, BPM tolerance, and finite normalized spectrum values.
- [ ] Add cancellation and malformed odd-byte tests; both must terminate FFmpeg and return a typed error without leaking a goroutine.
- [ ] Run `rtk go test ./internal/video -run '^Test(Stream|AnalyzeAudioBounded|AnalyzeAudioCancellation)' -count=1 -v`; expected RED because `decodeToPCM` currently buffers FFmpeg stdout completely.
- [ ] Replace `bytes.Buffer` stdout capture with `StdoutPipe`; decode little-endian stereo chunks, retain only overlap/windows required by FFT and BPM analysis, and aggregate the 1000-bin envelope/fingerprint incrementally.
- [ ] Bound stderr capture, call `Wait` exactly once, close pipes on cancellation, and reject an incomplete final sample.
- [ ] Run focused tests, `rtk go test ./internal/video -count=1`, and `rtk git diff --check`.
- [ ] Commit `fix: bound memory during audio analysis`.

### AV-716: Compose the complete deterministic visualizer frame

**Dependencies:** AV-713, AV-715. **Parallel wave:** C3; may run with AV-717. **Exclusive owner:** renderer files.

**Files:**
- Modify exclusively: `internal/video/audio_visualizer.go`
- Modify: `internal/video/audio_visualizer_test.go`
- Modify: `internal/video/visualizer_background.go`
- Modify: `internal/video/visualizer_background_test.go`
- Modify: `internal/video/fallback_artwork.go`
- Modify: `internal/video/fallback_artwork_test.go`
- Modify: `internal/video/visualizer_ass.go`
- Modify: `internal/video/visualizer_ass_test.go`

- [ ] Add an integration-level frame test with a known red artwork tile. Decode frames at 0 s and 4 s and assert the artwork rectangle, blurred/cropped background, rounded corners, readability overlay, 24 spectrum bars, waveform region, 1000-bin loudness envelope, decorative progress marker, and time text occupy the canonical layout rectangles.
- [ ] Add a no-art frame test asserting mood-derived fallback colors, 64-band fingerprint, centered music note, and the same fallback tile as the blurred background source.
- [ ] Add dark/light artwork tests asserting a single foreground mode reaches 4.5:1 in metadata and graph regions. Add a golden test proving the bottom of every spectrum bar fades to alpha zero.
- [ ] Run `rtk go test ./internal/video -run '^Test(VisualizerFrame|VisualizerFallback|VisualizerContrast|SpectrumBottomFade)' -count=1 -v`; expected RED because frames currently use a solid `{20,20,30}` background and ignore `ArtworkPath` and `LoudnessEnvelope`.
- [ ] Call `PrepareVisualizerBase` once per job, load the resulting base image, and copy it into each RGBA frame before drawing dynamic elements. Build fallback artwork only when `ArtworkPath` is absent or invalid.
- [ ] Draw loudness envelope, progress decoration, and spectrum using `LayoutForSize`; remove hard-coded coordinate subtraction. Keep FFmpeg `showwaves` responsible only for the existing real-time waveform layer.
- [ ] Use the selected `ForegroundMode` consistently for text, graphs, bars, and overlays. Preserve native FFmpeg/libass antialiasing and text scrolling rules.
- [ ] Run focused tests and a real FFmpeg HLS test. Probe H.264/yuv420p/30 fps/AAC/48 kHz/stereo and decode the whole playlist with exit code zero.
- [ ] Run `rtk go test ./internal/video -count=1` and `rtk git diff --check`.
- [ ] Commit `fix: render complete artwork-driven visualizer`.

### AV-717: Re-analyze restored audio before enqueue

**Dependencies:** AV-712, AV-715. **Parallel wave:** C3; may run with AV-716. **Exclusive owner:** history audio adapter.

**Files:**
- Create: `internal/server/audio_history.go`
- Create: `internal/server/audio_history_test.go`
- Modify exclusively: `internal/server/server.go`

Required helper:

```go
func (s *Server) audioRenderInputForStored(ctx context.Context, path string, item library.CurrentImage) (video.AudioRenderInput, error)
```

- [ ] Add tests for current-history selection and queued-history replay with `Converted == false`; assert non-empty analysis frames before `EnqueueAudioForID`.
- [ ] Add cancellation/error tests proving a failed re-analysis returns an HTTP error or queue failure and never enqueues an empty `AudioRenderInput`.
- [ ] Add a regression test that an already converted history item reuses its completed HLS without analysis.
- [ ] Run `rtk go test ./internal/server -run '^Test(AudioHistory|HistorySelectAudio)' -count=1 -v`; expected RED with `no analysis frames to render` or an empty analysis assertion.
- [ ] Move stored-audio input construction into `audio_history.go`; call `EnsureFFmpeg` and `AnalyzeAudio` before enqueueing unconverted audio. Reuse persisted title, artist, album, source kind, and thumbnail.
- [ ] Use the request context for selection and a queue-owned cancellable context for background replay; do not use `context.Background()` inside the HTTP acquisition path.
- [ ] Run focused tests, `rtk go test ./internal/server ./internal/video -count=1`, and `rtk git diff --check`.
- [ ] Commit `fix: analyze restored audio before conversion`.

### AV-718: Run correction integration and encoded-frame verification

**Dependencies:** AV-716, AV-717. **Parallel:** prohibited. **Owner:** active AI.

**Files:**
- Modify: `internal/video/audio_runtime_test.go`
- Modify: `scripts/verify-audio-visualizer.ps1`
- Create runtime evidence only under an ignored temporary directory; do not commit downloaded audio, artwork, HLS segments, or logs.

- [ ] Change `TestIntegrationGUNPEI` to call the generic `RunAudioVisualizerHLS` path. The legacy `runSoundCloudHLS` path is not valid evidence.
- [ ] Add end-to-end HTTP tests for local no-art audio, local attached-art audio, direct audio URL, SoundCloud GUNPEI, history replay, and unchanged video upload.
- [ ] Make the verifier resolve tools in this order: explicit environment variable, `%APPDATA%\ImagePadServer\bin`, then PATH. Missing required filters or tools must exit nonzero.
- [ ] Run `rtk go test ./... -count=3`; expected 0 failures. Run `rtk go test -race ./internal/video ./internal/server ./internal/library -count=1` or record a concrete Windows toolchain blocker.
- [ ] With `IMAGEPAD_RUN_NETWORK_TESTS=1`, run the GUNPEI test and record metadata, 715x706 artwork selection, HLS path, ffprobe JSON, and full decode exit code.
- [ ] Extract frames at 0 s, 3 s, 4 s, 50%, and one second before end for 360p, 720p, and 1080p. Check every design acceptance criterion rather than only file existence.
- [ ] Run `rtk go vet ./...`; compare the two known Windows `unsafe.Pointer` warnings and reject any new warning.
- [ ] Do not mark the ticket complete if any temporary-directory cleanup failure recurs.

### AV-719: Close correction ledger and reopen the release gate

**Dependencies:** AV-718. **Parallel:** prohibited. **Owner:** active AI.

**Files:**
- Modify only: `docs/superpowers/plans/audio-visualizer/ticket-status.md`
- Remove only confirmed generated scratch files such as `mock-args.txt`; never remove an unknown user file without inspecting it first.

- [ ] Verify every correction commit touches only its allowlist and every RED/GREEN/runtime evidence field is populated.
- [ ] Run `rtk git status --short`; inspect every untracked file and remove only proven test scratch output.
- [ ] Run `rtk go test ./... -count=3`, `rtk go build ./...`, `rtk go vet ./...`, and `rtk git diff --check` fresh.
- [ ] Mark AV-700 `VERIFIED` again only after AV-718 evidence covers all 28 design acceptance criteria.
- [ ] Mark AV-710 `READY` only after AV-700 is re-verified; otherwise leave it `WAITING_DEPENDENCY`.
- [ ] Commit only the ledger update as `docs: close audio visualizer correction wave`.

## Correction-wave stop rules

- No worker may edit `ticket-status.md`; only the active AI records transitions.
- AV-711, AV-712, and AV-717 serialize all `server.go` changes.
- AV-714 and AV-715 serialize all `audio_analysis.go` changes.
- AV-716 must not modify acquisition or server routing.
- AV-718 must test the generic visualizer, not the obsolete SoundCloud-only renderer.
- AV-710 remains blocked until AV-719 explicitly restores its gate.
