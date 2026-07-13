# Playlist Streaming Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Eliminate the known data-loss, stream-continuity, readiness, configuration, recovery, and queue-cursor defects before enabling the hybrid overlay pipeline.

**Architecture:** Split playlist radio into four explicit contracts: immutable saved media, queue-owned runtime media, a frozen per-session delivery contract, and observable runtime readiness/error state. Pre-rendered track video uses one canonical asset format; delivery-specific GOP and latency behavior move to the persistent program encoder in the hybrid plan. The existing copy path remains available during remediation, but it may only concatenate assets with identical stream signatures.

**Tech Stack:** Go, FFmpeg/ffprobe, MediaMTX, existing playlist/server/obsrtmp/video packages.

**Subagent execution:** Use `docs/superpowers/plans/2026-07-11-playlist-subagent-execution.md` for model assignment, isolation, review, promotion, and integration gates.

## Global Constraints

- Never delete files under `playlist-media` from queue removal or queue replacement.
- A running MediaMTX path must never receive a track whose stream signature differs from the active session contract.
- HLS and RTSP URLs are published only after their own protocol-specific readiness checks pass.
- Desired settings and active session settings are separate state; a running session is never relabeled without restart.
- Canonical material resolution is an advanced setting with allowed values 360p, 720p, and 1080p; default is 720p. A change is loaded only after ImagePadServer restarts.
- FFmpeg/MediaMTX failures must be visible in `RadioStatus` and must not cause an unbounded restart loop.
- The hybrid implementation in `2026-07-11-playlist-hybrid-overlay.md` starts only after Tasks 1-7 pass.

---

### Task 1: Establish Media Ownership and Safe Playlist Materialization

**Problems solved:** Removing a loaded track deletes saved media; replacing queues leaks files; saved artwork paths are not self-contained.

**Files:**
- Modify: `internal/playlist/store.go`
- Modify: `internal/playlist/store_test.go`
- Modify: `internal/playlist/playlist.go`
- Modify: `internal/server/music_playlist.go`
- Modify: `internal/server/music_playlist_test.go`

**Interfaces:**
- Add `Store.Materialize(name, runtimeDir string) ([]Track, error)`.
- Add `Store.Save` support for copying `MediaPath`, `ThumbnailPath`, and the normalized source audio into the saved playlist directory.
- Add `Queue.ReplaceAll(next []Track) (previous []Track)` so the server can clean displaced queue-owned files.
- Materialized tracks receive fresh IDs and queue-owned paths under `playlist-runtime/<load-id>/`; saved paths are never returned directly to the queue.
- Retain the exact pre-render source audio after rendering so Task 2 can persist and regenerate it through the same recipe. Do not add a second loudness-normalization encode: loudness normalization remains a deterministic render filter. Task 1 does not implement manifests, fingerprints, compatibility audit, or regeneration decisions.
- Authorize thumbnails by canonical-path containment under the owned store root so `playlist-runtime/<load-id>` artwork remains readable without allowing traversal.
- Add a track-generation completion signal for cleanup. Queue track claim and generation publication must be one synchronized state transition. Expose exact `CancelGeneration(generation)` semantics so a stale handler cannot cancel a newer feeder. Do not poll `currentID` indefinitely after skip; Task 4 may extend the same signal for failure shutdown.
- Remove/replace handlers mutate the queue before capturing the active generation. After mutation, a newly claimed track cannot belong to the displaced set; cleanup may associate a generation only with a displaced track that was already active.

- [ ] Write failing tests: load then remove does not delete saved MP4; delete a saved playlist does not break an already materialized queue; artwork and normalized source audio survive original queue removal; replacing a queue removes only prior runtime-owned files; materialized thumbnails remain readable; traversal outside the store remains forbidden; cleanup completes after track-generation completion.
- [ ] Run `go test ./internal/playlist ./internal/server -run 'Test(StoreMaterialize|MusicPlaylistMediaOwnership)' -count=1` and confirm failure.
- [ ] Implement atomic materialization using a temporary directory followed by rename. Copy or hard-link media only within the same volume; fall back to copy when linking fails.
- [ ] Give every materialized track a fresh ID before deriving destination file names. Never reuse serialized saved-playlist IDs as runtime identity.
- [ ] After exact track-generation completion, asynchronously remove displaced runtime directories. Context cancellation alone is not completion: if feeder exit is not confirmed, terminate the waiter without deletion and leave cleanup to confirmed session completion or startup cleanup. Startup cleanup removes abandoned `playlist-runtime` directories older than 24 hours.
- [ ] Re-run the focused tests and confirm all pass.

#### Saved Playlist Manifest Contract

```json
{
  "schemaVersion": 1,
  "playlistId": "stable-playlist-id",
  "name": "My Playlist",
  "createdBy": {
    "appVersion": "v1.6.1-dev8",
    "buildVersion": "1.6.1.8"
  },
  "updatedBy": {
    "appVersion": "v1.6.1-dev8",
    "buildVersion": "1.6.1.8"
  },
  "encodingContract": {
    "videoCodec": "h264",
    "width": 1280,
    "height": 720,
    "frameRate": "30/1",
    "pixelFormat": "yuv420p",
    "gopFrames": 30,
    "bFrames": 0,
    "videoBitrate": 4000000,
    "videoFilters": ["scale=1280:720", "format=yuv420p"],
    "audioCodec": "aac",
    "sampleRate": 48000,
    "channels": 2,
    "audioBitrate": 192000,
    "audioFilters": ["loudnorm=..."]
  },
  "encodingFingerprint": "sha256-of-canonical-encoding-contract-json",
  "tracks": [
    {
      "id": "saved-track-id",
      "mediaFile": "radio-track-saved-track-id.mp4",
      "sourceAudioFile": "source-saved-track-id.m4a",
      "artworkFile": "artwork-saved-track-id.webp",
      "generatedBy": {
        "appVersion": "v1.6.1-dev8",
        "ffmpegVersion": "7.1",
        "encoderName": "h264_nvenc"
      },
      "encodingFingerprint": "sha256-of-canonical-encoding-contract-json",
      "observedEncoding": {
        "width": 1280,
        "height": 720,
        "frameRate": "30/1",
        "videoCodec": "h264",
        "profile": "High",
        "level": 31,
        "pixelFormat": "yuv420p",
        "audioCodec": "aac",
        "sampleRate": 48000,
        "channels": 2
      },
      "mediaSHA256": "sha256-of-media-file",
      "sourceSHA256": "sha256-of-source-audio",
      "mediaFingerprint": "sha256-of-observed-stream-contract",
      "needsRegeneration": false,
      "regenerationReason": ""
    }
  ]
}
```

Compatibility uses `schemaVersion`, the automatically generated `encodingContract` and `encodingFingerprint`, and each track's `observedEncoding`. `appVersion`, `buildVersion`, FFmpeg version, and encoder name are retained for diagnosis but do not force regeneration by themselves.

The render pipeline must not maintain JSON separately from the FFmpeg command. Typed semantic option structures emit both actual arguments and canonical persisted data. Canonical JSON serialization uses stable field ordering and normalized scalar forms, then SHA-256 fingerprints those bytes. Input/output paths, temporary paths, overwrite flags, logging, progress/reporting options, thread count, and equivalent hardware implementation identity are excluded. Stream compatibility includes codec, dimensions, frame rate, pixel format, GOP/B-frame policy, target bitrate/rate-control constraints, and audio format. Asset recipe semantics additionally include ordered filter/layout/waveform/ASS/fade/private-parameter rules without treating content-specific per-track values as a global compatibility mismatch. Tests must fail if an emitted semantic FFmpeg option changes without changing the appropriate fingerprint, and must fail if a fingerprint includes an option that is not emitted. Contract persistence happens only during explicit save; runtime comparisons are in-memory.

### Task 2: Define and Enforce a Canonical Track Asset Contract

**Problems solved:** Resolution, encoder, SPS/PPS, GOP, pixel format, and audio parameter changes between tracks/fallback.

**Files:**
- Modify: `internal/playlist/playlist.go`
- Modify: `internal/video/publisher.go`
- Modify: `internal/video/radio_render.go`
- Modify: `internal/video/radio_render_test.go`
- Modify: `internal/server/music_playlist.go`
- Modify: `internal/server/music_mode_test.go`

**Interfaces:**
- Own all `playlist.manifest.json`, fingerprint, `Store.AuditCanonical`, compatibility, and regeneration behavior; Task 1 owns only files and runtime ownership.
- Save playlists under stable playlist IDs with immutable versioned content directories. Commit media and manifest first, then atomically replace the central index pointer; display names never determine identity or directory ownership.
- Validate every persisted ID against the generated canonical format and derive all roots/deletions through a containment helper under `mediaDir`; reject traversal, symlink/junction escape, and different volumes before filesystem mutation.
- Store a canonical manifest digest in the central version pointer and verify it before trusting manifest fields. This detects manifest edits without rehashing media or running ffprobe during load.
- Save/load/rename/regenerate/delete use playlist ID. Names are mutable display metadata; creation/rename rejects duplicate display names. Legacy name-only calls resolve only one unambiguous legacy record.
- Add `video.RadioRenderRecipe` whose typed semantic fields generate both `FFmpegArgs(...)` and normalized persisted contracts. Maintain `StreamEncodingFingerprint` for receiver/copy compatibility and `AssetRenderRecipeFingerprint` for canonical rendering-rule changes. Neither uses a manually incremented material-rule version.
- Add `video.RadioAssetSpec` from the recipe's normalized contract with `Width`, `Height`, `FrameRate`, `VideoCodec`, `PixelFormat`, `AudioCodec`, `SampleRate`, `Channels`, `GOPFrames`, and `Fingerprint()`.
- Add `Settings.MusicPlaylistCanonicalHeight int` normalized to 360, 720, or 1080; zero/invalid values normalize to 720.
- Add `video.CanonicalRadioAssetPreset(height int)` using the configured 16:9 resolution, 30 fps, H.264, yuv420p, AAC 48 kHz stereo, no B-frames, GOP 30.
- Store `Track.MediaFingerprint`; ordinary freshly rendered queue tracks inherit it from their immutable recipe without probing. Saved legacy/unknown assets are marked for regeneration and are not ffprobed during load.
- Store `Track.SourcePath` for the queue-owned pre-render input so an asset can be regenerated after a canonical-resolution change. Saved playlists keep their own source-audio copy; regeneration reapplies the same recipe filters.
- Expose `needsRegeneration` on every saved playlist summary and saved track returned by the playlists API.

- [ ] Write failing tests proving 360p/720p/1080p normalization and that CPU/GPU renders, normal tracks, and fallback share the same externally visible `RadioAssetSpec` at each selected height.
- [ ] Add table-driven fingerprint tests for every receiver-visible FFmpeg option. Each option mutation must change canonical JSON and its SHA-256; changes to input/output paths, progress/logging flags, thread count, and equivalent CPU/GPU encoder implementation must not.
- [ ] Add manifest compatibility tests: same contract across a newer app version remains usable; changed resolution/audio/GOP/filter/codec settings are flagged; a future unsupported schema is blocked; a missing legacy manifest enters migration audit.
- [ ] Add hostile central-index ID tests for load/save/delete, manifest-digest tamper tests, duplicate/rename/ambiguous legacy-name tests, mixed source/no-source audit-state tests, and index-failure orphan-version cleanup tests.
- [ ] Add an ffprobe fixture test that rejects resolution, audio-rate, pixel-format, and GOP mismatches.
- [ ] Run `go test ./internal/video ./internal/server -run 'Test(CanonicalRadioAsset|RadioAssetCompatibility)' -count=1` and confirm failure.
- [ ] Load `MusicPlaylistCanonicalHeight` once during ImagePadServer startup and freeze it as `Server.activeCanonicalHeight`. A settings POST persists only the desired value and sets `canonicalRestartRequired`; it does not change the running process.
- [ ] Make `RenderRadioTrack` construct one immutable `RadioRenderRecipe` from `activeCanonicalHeight` and generate the executed FFmpeg arguments from it. Do not write a playlist manifest from the ordinary render path. Delivery profile still controls URL/HLS muxing; output-specific GOP moves to the hybrid program encoder.
- [ ] Keep the exact existing copy-path GOP matrix active until Task 3/#19 is integrated and accepted: `hls-high=120`, `hls=30`, `rtsp-low=60`, `rtsp-ultra=30`, `rtsp-realtime=15` frames. Build canonical program recipe and manifest code behind a program-output activation gate; do not feed a GOP-30 canonical asset into a legacy copy session that requires another GOP.
- [ ] On explicit playlist save only, serialize the recipe's normalized contract, calculate contract/media/source SHA-256 values, and probe the MP4 with ffprobe before committing it. Reject the temporary saved copy when observed dimensions, frame rate, codec, pixel format, GOP/B-frame policy, sample rate, or channels violate the generated contract; only then atomically publish the saved media and manifest together.
- [ ] Bound save/regeneration ffprobe with context timeout. Missing GOP evidence, fewer than two parseable keyframes, malformed timestamps, or variable cadence outside tolerance fail the commit; zero/unknown GOP never counts as compatible.
- [ ] Before each copy feed, compare `Track.MediaFingerprint` with `RadioStatus.AssetFingerprint`. On mismatch, mark the track `incompatible` and keep fallback active; never write it into the publisher sink.
- [ ] After ImagePadServer restarts with a new active height, compare every loaded/materialized asset fingerprint. If normalized source audio exists, move the track to `preparing` and regenerate it in the background. If source audio is unavailable for a legacy track, surface “解像度設定が変更されたため再追加が必要” without deleting the old asset.
- [ ] Keep the previous MP4 until replacement rendering succeeds, then atomically replace it. A failed regeneration leaves the old file intact but blocked from the new session contract.
- [ ] On startup, run `Store.AuditCanonical(activeSpec)` across all saved playlists. Set track-level flags for every mismatch and set playlist-level `needsRegeneration` when at least one track is flagged.
- [ ] During startup/load audit, verify manifest file names stay inside the owned playlist directory and compare the saved encoding fingerprint with the active in-memory recipe fingerprint. Do not recalculate media/source SHA-256 or run ffprobe during normal startup/load; those expensive checks belong to explicit save and regeneration completion. A missing, malformed, or mismatched manifest is marked `needsRegeneration` without probing it into compatibility.
- [ ] After successful regeneration from saved source audio, atomically replace the saved MP4, update its fingerprint, clear the track flag, and clear the playlist flag only when no flagged tracks remain. A partial failure keeps the remaining flags set.
- [ ] Persist track and playlist regeneration reasons/states in the manifest. Derive aggregate flags after all track audits: regeneration when a blocked track retains source; incompatible when a blocked track lacks source. Mixed playlists may expose both with separate counts, but one track has one coherent state.
- [ ] Re-run focused tests and a real two-track FFmpeg smoke test; verify no codec-parameter-change warning.

### Task 3: Replace Feeder-local PTS With a Session Program Clock

**Problems solved:** Every feeder restarts at PTS zero; `+genpts` cannot guarantee continuity; seek/pause/fallback handoffs reset source timestamps.

**Files:**
- Implement Tasks 3-6 from `docs/superpowers/plans/2026-07-11-playlist-hybrid-overlay.md`.
- Modify: `internal/obsrtmp/radio.go`
- Modify: `internal/video/radio_render.go`
- Modify: `internal/obsrtmp/radio_test.go`

**Interfaces:**
- `ProgramClock` owns 30 fps video PTS and 48 kHz audio sample PTS for the entire radio session.
- Replaceable track decoders translate source timestamps onto the current program clock.
- Fallback, black transition, track, seek, and pause all write through one persistent compositor/encoder output.

- [ ] Add a failing packet-timeline test covering fallback -> track A -> seek -> pause -> fallback -> track B.
- [ ] Require every emitted DTS/PTS to be greater than or equal to its predecessor and video cadence to remain 1/30 second.
- [ ] Remove feeder-level `output_ts_offset` responsibility after the program clock passes tests.
- [ ] Keep the current copy pipeline behind an explicit compatibility fallback until the program path passes the soak gate.
- [ ] Run `go test ./internal/obsrtmp ./internal/video -run 'Test(ProgramClock|RadioTimeline|RadioRecovery)' -count=1`.

### Task 4: Make Fallback and Publisher Failures Bounded and Observable

**Problems solved:** Fallback error is discarded; idle loop can spin; publisher start/exit errors disappear when status resets.

**Files:**
- Modify: `internal/obsrtmp/radio.go`
- Modify: `internal/obsrtmp/radio_fallback.go`
- Modify: `internal/obsrtmp/radio_test.go`
- Modify: `internal/obsrtmp/radio_fallback_test.go`
- Modify: `internal/server/music_playlist.go`

**Interfaces:**
- Extend `RadioStatus` with `Phase`, `LastError`, `RetryCount`, and `StoppedAt`.
- Add `RadioCallbacks.OnError(RadioError)` where `RadioError` contains stage (`publisher`, `fallback`, `track`, `mediamtx`) and recoverability.
- Extend `radioRuntime` with a process `done`/`wait` channel so unexpected MediaMTX exit is observable as stage `mediamtx`.
- MediaMTX process completion is broadcast-safe: multiple observers (`wait`, radio monitor, and `stop`) read one stored result behind a closed completion channel. No observer consumes the only exit value.
- Add a track-generation completion notification used by `SkipCurrentAndWait(ctx)` (or an equivalent generation-based API). Queue-owned runtime cleanup must terminate on completion, session stop, context cancellation, or fatal runtime exit.

- [ ] Write failing tests for publisher-start failure, publisher unexpected exit, fallback start failure, repeated fallback failure, and recovery after one transient failure.
- [ ] Add failing tests for unexpected MediaMTX exit, terminal status retention after child cleanup, successful `SkipCurrentAndWait`, and cancellation/fatal-exit release of cleanup waiters.
- [ ] Add failing tests where the radio monitor observes MediaMTX exit before `stop`, both MediaMTX and publisher exits are ready simultaneously (stage precedence is `mediamtx`), retry count remains nonzero until success, and every user-visible error path sanitizes URL userinfo/query, bearer authorization, and colon-separated credential fields.
- [ ] Propagate `runFallbackFeeder` errors. Retry recoverable fallback failures at 250 ms, 500 ms, 1 s, and 2 s; after four failures, stop the session with a retained error status.
- [ ] Treat `errPublisherDown` as fatal immediately. Do not retry feeders against a dead publisher.
- [ ] Preserve terminal error status after child cleanup; clear it only on a new successful `Start`.
- [ ] Add throttled server logging with stage and trimmed FFmpeg stderr, excluding credentials and private publish URLs.
- [ ] Run `go test ./internal/obsrtmp ./internal/server -run 'TestRadio(PublisherFailure|FallbackRetry|ErrorStatus)' -count=1`.

### Task 5: Introduce Protocol-aware Readiness and URL Publication

**Problems solved:** HLS URL is shown as soon as radio starts; standard fMP4 HLS is checked with LL-HLS tags; readiness can regress silently.

**Files:**
- Modify: `internal/obsrtmp/mediamtx.go`
- Modify: `internal/obsrtmp/mediamtx_test.go`
- Modify: `internal/obsrtmp/radio.go`
- Modify: `internal/server/music_playlist.go`
- Modify: `internal/server/music_playlist_test.go`
- Modify: `internal/server/ui_script_playlist_controller.go`

**Interfaces:**
- Consume the immutable active-session snapshot introduced by Task 6 phase A; never read mutable desired profile state while probing readiness.
- Replace `llhlsReady` with `hlsReady(ctx, profile)`.
- LL-HLS readiness requires master + media playlist + required LL-HLS tags.
- fMP4 HLS readiness requires master + media playlist + init-map availability + one readable media segment.
- `RadioStatus` exposes independent `RTSPReady` and `HLSReady` booleans.

- [ ] Write failing readiness tests for LL-HLS, standard fMP4 HLS, a playlist without a segment, transient 404, and readiness loss after publisher exit.
- [ ] Cache readiness in the radio monitor goroutine; do not perform blocking HTTP probes inside every playlist-state request.
- [ ] Publish `rtspUrl` only after MediaMTX path readiness and public mapping rules pass. Publish `hlsUrl`/`publicHlsUrl` only while `HLSReady` is true.
- [ ] Update UI placeholders to distinguish “配信準備中”, “公開経路準備中”, and stopped/error states.
- [ ] Run `go test ./internal/obsrtmp ./internal/server -run 'Test(RadioHLSReady|MusicPlaylistURLReadiness)' -count=1`.

### Task 6: Separate Desired Settings From the Active Session Contract

**Problems solved:** UI changes immediately while MediaMTX and active tracks retain old settings; mixed-profile assets can enter a running stream.

**Files:**
- Modify: `internal/settings/settings.go`
- Modify: `internal/obsrtmp/radio.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/music_playlist.go`
- Modify: `internal/server/ui_script_settings.go`
- Modify: `internal/server/ui_script_playlist_controller.go`
- Modify: `internal/server/music_mode_test.go`

**Interfaces:**
- `RadioStatus.ActiveContract` freezes delivery profile, HLS variant, canonical/quality height, asset fingerprint, encoder, frame rate, GOP, bitrate/rate-control policy, DVR/list size, and session ID at `Start`.
- API state returns `desiredDeliveryProfile`, `activeDeliveryProfile`, `desiredCanonicalHeight`, `activeCanonicalHeight`, `deliveryRestartRequired`, and `canonicalRestartRequired`.

**Execution split:** Phase A implements and tests the immutable active-session snapshot before Task 5/#16. Phase B adds desired/active API and UI after Task 5. Both phases remain under GitHub Issue #17 but use separate integration commits and reviews.

Phase A keeps OBS and playlist-radio contracts distinct. OBS snapshots per accepted media session inside `runOneWithEncoder` (listener start is not a media session) and freezes stream key/port, latency profile, quality preset, and selected encoder. Playlist radio snapshots once in `RadioManager.Start` and freezes delivery profile, latency/HLS settings, and fallback preset. `musicRadioPreset` and general `musicQualityPreset` are separate contracts.

- [ ] Write failing tests: profile change while stopped becomes active on start; change while running leaves active contract unchanged; canonical height changes remain pending until a new ImagePadServer process starts; UI reports the correct restart requirement; next track cannot adopt a different active contract.
- [ ] Snapshot settings once during `RadioManager.Start`. Never call mutable preset/profile functions from a running fallback or track transition.
- [ ] Replace running-session calls through `currentLatency()`, `currentPreset()`, `obsLatencyProfile()`, `videoQualityPreset()`, and `musicQualityPreset()` with the immutable active contract wherever they affect readiness, URL publication, fallback/track transitions, encoding, or shutdown.
- [ ] For OBS, snapshot desired callbacks once per accepted stream before FFmpeg/HLS/RTSP setup; `ProxyLLHLS`, readiness, endpoint publication, and status use that session contract. For playlist radio, `SetFallbackPreset` and `SetLatencyProfile` remain desired setters but are never invoked by a running session after `Start` captures them.
- [ ] Make the profile selector persist desired state only. Offer an explicit “配信を再起動して反映” command; do not auto-restart.
- [ ] Add a detailed-settings canonical-resolution select with 360p, 720p, and 1080p. Place visible text directly below it: “変更はImagePadServer再起動後に適用されます。既存素材は再生成されます。”
- [ ] In the saved-playlist menu, show “再生成が必要” beside flagged playlists and the number of affected tracks. Loading one starts queued regeneration instead of attempting playback with incompatible media.
- [ ] Do not offer an in-app hot-apply command for canonical resolution. Only process restart clears `canonicalRestartRequired` and updates `activeCanonicalHeight`.
- [ ] Keep video-quality and encoder API updates independent so a profile-only POST cannot reset quality to `auto`.
- [ ] Run `go test ./internal/server ./internal/obsrtmp -run 'Test(RadioActiveContract|MusicPlaylistDesiredSettings)' -count=1`.

### Task 7: Preserve Sequential Playback Cursor Across Idle Periods

**Problems solved:** Reaching the end clears `currentID`; a newly added track can restart playback from the first ready item.

**Files:**
- Modify: `internal/playlist/playlist.go`
- Modify: `internal/playlist/playlist_test.go`
- Modify: `internal/server/music_playlist.go`
- Modify: `internal/server/music_playlist_test.go`

**Interfaces:**
- Add `lastPlayedID` and playback `generation` independent of `currentID`. Derive the next sequential item from the last played logical track, not a bare integer cursor.
- Add `Queue.ResetPlaybackCycle()` for explicit user replay from the beginning.
- Automatic `Wake` after preparation continues from the cursor; an explicit Play after exhaustion calls reset.

- [ ] Write failing tests: A/B finish, C is added, automatic wake selects C; explicit replay selects A; reorder preserves the next logical track; remove before/after cursor remains valid; loop and shuffle behavior stays unchanged.
- [ ] Define reorder behavior in tests: if `lastPlayedID` still exists, next is its new successor; if removed, continue from the first unplayed track in the current generation; if none remains, the cycle is exhausted until explicit reset or a new track is appended.
- [ ] Implement cursor updates under the existing queue mutex. Do not infer position from an empty `currentID`.
- [ ] Make `handleMusicPlaylistPlay` distinguish explicit replay from automatic preparation wake.
- [ ] Run `go test ./internal/playlist ./internal/server -run 'Test(SequentialCursor|MusicPlaylistReplay)' -count=1`.

### Task 8: Strengthen Runtime Acceptance Tests

**Problems solved:** Current E2E is opt-in, checks HLS primarily, permits temporary 502/404, and never consumes RTSP media.

**Files:**
- Modify: `internal/server/music_radio_e2e_test.go`
- Create: `internal/server/music_radio_rtsp_acceptance_test.go`
- Create: `docs/PLAYLIST_STREAMING_SMOKE.md`

- [ ] Add a real RTSP reader using pinned ffprobe/FFmpeg. Require it to stay connected through fallback -> track -> pause -> resume -> next track.
- [ ] Land the RTSP reader and record the current copy-lane baseline before Task 3/#19. Complete the five-profile matrix and soak after #19.
- [ ] Gate binary acceptance with `IMAGEPAD_RTSP_E2E=1` plus absolute existing `IMAGEPAD_MEDIAMTX`, `IMAGEPAD_FFMPEG`, and `IMAGEPAD_FFPROBE` paths. The test must invoke those paths directly and must not call installer/downloader helpers.
- [ ] Keep the short binary acceptance bounded to 150 seconds. Put the 30-minute run behind the additional `IMAGEPAD_RTSP_SOAK=1` gate; it is scheduled/manual validation, not normal PR CI.
- [ ] Add deterministic fixture-based unit tests for stream-signature normalization, non-monotonic DTS, transition-discontinuity counting, codec-parameter change, reconnect detection, bounded reader timeouts, and pinned-tool-path validation.
- [ ] Add migration fixtures for manifest schema v1, a legacy playlist with no manifest, a future unsupported schema, and a playlist created by a different app version with an identical asset contract.
- [ ] Fail on non-monotonic DTS/PTS, codec-parameter changes, reconnects, 404/502 after initial readiness, and audio/video stream disappearance.
- [ ] Run the acceptance matrix for all five delivery profiles. HLS profiles validate fMP4 segments; RTSP profiles validate continuous RTSP/TCP playback.
- [ ] Make CI run unit/integration tests unconditionally and run binary acceptance when pinned helpers are available. A skipped acceptance test must be visible in release gating.
- [ ] Execute a 30-minute, three-transition soak and record hardware, encoder, CPU/GPU utilization, FFmpeg stderr, MediaMTX logs, and receiver behavior.

## Execution Order and Gates

1. Task 1 ownership only, then Task 4 failure monitoring and track-generation completion.
2. Task 2 contract code behind a non-active program-output gate; preserve legacy copy GOP behavior.
3. Task 6 phase A active-session snapshot, then Task 5 readiness, then Task 6 phase B API/UI.
4. Task 7 cursor, then the Task 8 RTSP reader baseline against the current copy lane.
5. Task 3 persistent program output, then the remaining Task 8 five-profile matrix and soak.

## Completion Criteria

- Removing or replacing queue entries never damages saved playlists.
- Every source written to a copy-mode session has one identical stream fingerprint.
- Program-mode PTS remains monotonic across every transition.
- No failure path can spawn FFmpeg in an unbounded loop.
- UI URLs and labels describe the active session, not merely desired settings.
- Canonical-resolution settings display both desired and active values and explicitly state that ImagePadServer restart is required.
- Saved playlists and tracks whose fingerprints differ from the active canonical specification remain flagged until atomic regeneration succeeds.
- App-version changes alone never force regeneration. The save-time manifest schema and automatically generated encoding fingerprint determine normal load compatibility; save/regeneration-time ffprobe and file hashes validate observed stream compatibility and integrity before atomic commit. No manually incremented material-contract number exists.
- Adding a track after exhaustion plays the new track automatically without replaying older entries.
- A real RTSP reader remains connected for the full acceptance scenario.
