# Audio Visualizer and Media Ingest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current SoundCloud-only prototype with a shared, deterministic audio ingest and HLS visualizer pipeline for SoundCloud, local audio, and direct audio-file URLs.

**Architecture:** Freeze the current dirty prototype first, then establish immutable shared Go contracts. Independent workers implement probing, metadata, artwork, size enforcement, fonts, acquisition, analysis, and rendering in isolated worktrees with exclusive file ownership. A single integration worker later connects those outputs to the queue, server, store, UI, runtime verification, documentation, and release gates.

**Tech Stack:** Go 1.25, FFmpeg/ffprobe, yt-dlp, HLS, HTML/CSS/JavaScript embedded in Go, Noto Sans CJK JP, PowerShell verification on Windows.

---

## Authoritative inputs

- Design specification: `docs/superpowers/specs/2026-06-19-soundcloud-visualizer-design.md`
- Dispatch contract: `docs/superpowers/plans/audio-visualizer/00-dispatch-contract.md`
- Foundation and ingest tickets: `docs/superpowers/plans/audio-visualizer/01-foundation-and-ingest-tickets.md`
- Analysis and rendering tickets: `docs/superpowers/plans/audio-visualizer/02-analysis-and-rendering-tickets.md`
- Integration and UI tickets: `docs/superpowers/plans/audio-visualizer/03-integration-and-ui-tickets.md`
- Runtime and release tickets: `docs/superpowers/plans/audio-visualizer/04-runtime-verification-tickets.md`
- Post-review correction tickets: `docs/superpowers/plans/audio-visualizer/05-review-correction-tickets.md`
- Status ledger: `docs/superpowers/plans/audio-visualizer/ticket-status.md`

If a ticket conflicts with the design specification, stop with `BLOCKED_SPEC_CONFLICT`. Do not reinterpret the design.

## Current-state gate

The worktree intentionally contains an uncommitted SoundCloud prototype in these files:

- `README.md`
- `internal/about/about.go`
- `internal/library/store.go`
- `internal/server/media_paths.go`
- `internal/server/server.go`
- `internal/server/server_test.go`
- `internal/video/publisher.go`
- `internal/video/publisher_test.go`
- `internal/video/soundcloud.go`
- `internal/video/soundcloud_test.go`
- `winres/winres.json`

No parallel worktree may be created until AV-000 reviews and commits that baseline. `internal/about/about.go` and `winres/winres.json` become protected after AV-000; only the final release ticket may modify them.

## Locked shared contracts

AV-001 creates `internal/video/audio_types.go`. After AV-001 is merged, only the active integration AI may modify it. Every worker must compile against these names rather than inventing alternatives.

```go
package video

const MaxMediaSourceBytes int64 = 1<<32 - 1

type SourceKind string

const (
	SourceSoundCloud SourceKind = "soundcloud"
	SourceLocalAudio SourceKind = "local_audio"
	SourceRemoteAudio SourceKind = "remote_audio"
)

type MediaClass string

const (
	MediaUnsupported MediaClass = "unsupported"
	MediaAudio MediaClass = "audio"
	MediaVideo MediaClass = "video"
)

type AudioMetadata struct {
	Title    string
	Artist   string
	Album    string
	Uploader string
}

type MediaStream struct {
	Index       int
	CodecType   string
	CodecName   string
	AttachedPic bool
	Width       int
	Height      int
	Tags        map[string]string
}

type MediaProbe struct {
	Streams    []MediaStream
	Duration   float64
	FormatTags map[string]string
}

type AudioFeatures struct {
	BPM               float64
	IntegratedLUFS    float64
	LowFrequencyRatio float64
	SpectralCentroid  float64
	Fingerprint64     [64]float64
	LoudnessEnvelope  [1000]float64
}

type AudioFrame struct {
	Spectrum24 [24]float64
}

type AudioAnalysis struct {
	FPS      int
	Duration float64
	Frames   []AudioFrame
	Features AudioFeatures
}

type FontSet struct {
	Regular400 string
	Medium500  string
	SemiBold600 string
}

type ArtworkCandidate struct {
	Path       string
	FrontCover bool
	Width      int
	Height     int
	Bytes      int64
}

type AcquiredAudio struct {
	SourcePath                string
	SourceName                string
	Kind                      SourceKind
	Probe                     MediaProbe
	EmbeddedMetadata          AudioMetadata
	SoundCloudMetadata        AudioMetadata
	EmbeddedArtwork           []ArtworkCandidate
	SoundCloudArtworkPath     string
	SoundCloudInformationPath string
}

type AudioRenderInput struct {
	SourcePath string
	Kind       SourceKind
	Metadata   AudioMetadata
	ArtworkPath string
	Analysis   AudioAnalysis
}
```

## File responsibility map

| File | Responsibility | Exclusive ticket |
| --- | --- | --- |
| `internal/video/audio_types.go` | Immutable shared contracts | AV-001 |
| `internal/video/toolchain.go` | ffmpeg/ffprobe/yt-dlp path resolution and installation | AV-100 |
| `internal/video/media_probe.go` | ffprobe execution, JSON parsing, audio/video classification | AV-101 |
| `internal/video/audio_metadata.go` | Unicode validation, strict CP932 repair, metadata precedence | AV-102 |
| `internal/video/media_limit.go` | 4 GiB - 1 byte streaming limit | AV-103 |
| `internal/video/font.go` | Pinned Noto font path and validation | AV-104 |
| `internal/video/audio_artwork.go` | Embedded art extraction and deterministic candidate selection | AV-201 |
| `internal/video/soundcloud_download.go` | SoundCloud yt-dlp manifest, JSON, artwork, cleanup | AV-202 |
| `internal/server/remote_media.go` | SSRF-safe generic direct media download | AV-203 |
| `internal/video/audio_analysis.go` | 24/64-band analysis, loudness envelope, mood features | AV-204 |
| `internal/server/media_classification.go` | Local upload classification adapter | AV-205 |
| `internal/video/fallback_artwork.go` | Mood gradient, fingerprint, centered note tile | AV-301 |
| `internal/video/visualizer_layout.go` | Canonical geometry, scrolling, timing, formatting | AV-302 |
| `internal/video/visualizer_background.go` | Crop, blur, rounded artwork, contrast mode | AV-303 |
| `internal/video/audio_visualizer.go` | Shared audio-to-HLS renderer and filter graph | AV-401 |
| `internal/video/publisher.go` | Queue integration; one owner only | AV-501 |
| `internal/server/server.go` | Publish/queue/history integration; one owner only | AV-502 |
| `internal/server/ui.go` | Enabled/disabled media UI copy and file input | AV-601 |
| `README.md` | Verified user documentation only | AV-603 |

Files may be smaller than the names above suggest, but responsibilities must not be recombined into `soundcloud.go`, `publisher.go`, or `server.go`.

## Dependency DAG

```text
AV-000 baseline checkpoint
  -> AV-001 shared contracts
      -> AV-100 ffprobe toolchain
      -> AV-101 media probe
      -> AV-102 metadata normalization
      -> AV-103 size enforcement
      -> AV-104 font bundle

AV-100 -> AV-101
AV-101 + AV-102 -> AV-201 embedded artwork
AV-100 + AV-101 + AV-102 + AV-103 -> AV-202 SoundCloud acquisition
AV-101 + AV-103 -> AV-203 direct media acquisition
AV-100 + AV-101 -> AV-204 audio analysis
AV-101 + AV-103 -> AV-205 local upload classification

AV-104 + AV-204 -> AV-301 fallback artwork
AV-100 + AV-102 + AV-104 -> AV-302 layout and text
AV-201 + AV-301 -> AV-303 artwork and background
AV-204 + AV-302 + AV-303 -> AV-401 audio visualizer HLS
AV-401 -> AV-501 publisher queue
AV-202 + AV-203 + AV-205 + AV-501 -> AV-502 server/store integration
AV-502 -> AV-601 UI
AV-502 -> AV-602 runtime fixtures
AV-502 -> AV-603 documentation
AV-601 + AV-602 + AV-603 -> AV-700 final QA
AV-700 -> AV-710 versioned Windows test build

Post-review correction wave:
AV-711 + AV-713 + AV-714 may start in parallel
AV-711 -> AV-712
AV-714 -> AV-715
AV-713 + AV-715 -> AV-716
AV-712 + AV-715 -> AV-717
AV-716 + AV-717 -> AV-718
AV-718 -> AV-719
AV-719 -> AV-700 re-verification -> AV-710
```

## Parallel execution waves

| Wave | Tickets allowed in parallel | Merge gate |
| --- | --- | --- |
| 0 | AV-000 only | Clean baseline commit and full test output |
| 1 | AV-001 only | Contract review; no undefined types |
| 2A | AV-100, AV-102, AV-103, AV-104 | Focused tests plus `go test ./internal/video` |
| 2B | AV-101 only after AV-100 merges | ffprobe path contract available |
| 3 | AV-201, AV-202, AV-203, AV-204, AV-205 as dependencies permit | Each worktree owns disjoint files |
| 4 | AV-301, AV-302, AV-303 | Golden/geometry tests reviewed before merge |
| 5 | AV-401 only | Real FFmpeg HLS and frame evidence |
| 6 | AV-501 only, then AV-502 only | Queue first; server second; never parallel |
| 7 | AV-601, AV-602, AV-603 | Docs cannot claim results before AV-602 evidence |
| 8 | AV-700 only | Full integration evidence |
| 9 | AV-710 only | Version and Windows artifact evidence |
| C1 | AV-711, AV-713, AV-714 | Local route, metadata propagation, and BPM tests |
| C2 | AV-712 and AV-715 after their dependencies; write sets are disjoint | Direct URL route and bounded analysis |
| C3 | AV-716 and AV-717 | Complete renderer and history replay |
| C4 | AV-718 only, then AV-719 only | Runtime evidence, ledger closure, and release-gate restoration |

The actual number of simultaneous agents is bounded by the current agent platform. When a wave contains more tickets than available slots, dispatch the first available disjoint tickets and continue the same wave as slots free up.

## Merge order

1. AV-000, AV-001.
2. AV-100, AV-101, AV-102, AV-103, AV-104 in numeric order.
3. AV-201, AV-202, AV-203, AV-204, AV-205 in numeric order.
4. AV-301, AV-302, AV-303.
5. AV-401.
6. AV-501.
7. AV-502.
8. AV-601, AV-602, AV-603.
9. AV-700.
10. AV-710.

The post-review correction merge order overrides steps 9-10 until closed:

1. AV-711, AV-713, AV-714.
2. AV-712.
3. AV-715.
4. AV-716 and AV-717.
5. AV-718.
6. AV-719.
7. Re-run AV-700 and only then execute AV-710.

After every merge, the active AI runs the package tests named in the ticket. After every wave, it runs:

```powershell
rtk go test ./internal/video ./internal/server ./internal/library -count=1
rtk git diff --check
```

If a merge changes a contract required by another open ticket, revert the merge or issue a superseding contract ticket. Do not ask workers to guess the new API.

## Integration constraints

- `publisher.go`, `server.go`, and `ui.go` each have one exclusive owner.
- `soundcloud.go` is replaced in phases: AV-202 owns acquisition removal; AV-401 owns renderer removal. No other ticket edits it.
- Local and generic remote audio must have no code path that performs SoundCloud lookup.
- A true video stream wins over audio; `attached_pic` never makes audio a video.
- The source byte limit is exactly `4,294,967,295` for audio and video.
- Network tests never run as ordinary unit tests. They require `IMAGEPAD_RUN_NETWORK_TESTS=1`.
- Copyrighted GUNPEI audio and JPEG remain temporary runtime evidence and are never committed.
- Noto is pinned to commit `f8d157532fbfaeda587e826d4cd5b21a49186f7c`:
  - Font URL: `https://raw.githubusercontent.com/notofonts/noto-cjk/f8d157532fbfaeda587e826d4cd5b21a49186f7c/Sans/Variable/OTF/NotoSansCJKjp-VF.otf`
  - Font SHA-256: `AB2728702F90D2AE900309F299DC3C2B075010888A1A8A67FBD5B4C6AFF713A0`
  - License URL: `https://raw.githubusercontent.com/notofonts/noto-cjk/f8d157532fbfaeda587e826d4cd5b21a49186f7c/Sans/LICENSE`
  - License SHA-256: `6A73F9541C2DE74158C0E7CF6B0A58EF774F5A780BF191F2D7EC9CC53EFE2BF2`

## Global verification gates

### Design-spec acceptance coverage

| Spec acceptance criteria | Implementing tickets | Final evidence |
| --- | --- | --- |
| 1-2 canonical/scaled layout | AV-302, AV-401 | AV-700 frame comparisons |
| 3-4 artwork/background | AV-201, AV-303 | AV-700 dark/light frames |
| 5-7 fonts, scrolling, antialiasing | AV-104, AV-302, AV-401 | AV-700 encoded frames |
| 8-12 spectrum, waveform, loudness, position | AV-204, AV-401 | AV-700 probe and timestamp frames |
| 13 fallback artwork | AV-301 | AV-301 golden plus AV-700 frame |
| 14 no browser visualizer | AV-401, AV-502 | code review and runtime HLS |
| 15-16 ffprobe classification/attached_pic | AV-101, AV-205 | focused tests |
| 17 embedded art before SoundCloud art | AV-201, AV-202 | focused and GUNPEI tests |
| 18 no SoundCloud lookup for local/remote | AV-102, AV-203, AV-502 | call-counter tests |
| 19 SoundCloud user/album fallback | AV-102, AV-202 | GUNPEI metadata test |
| 20 enabled/disabled UI | AV-601 | HTML assertions and runtime UI |
| 21 4 GiB - 1 byte limit | AV-103, AV-203, AV-502 | boundary tests |
| 22 direct audio URL path | AV-203, AV-502 | direct-URL runtime test |
| 23-24 manifest and sidecar isolation | AV-202 | manifest regression tests |
| 25-27 Unicode/CP932 normalization | AV-102 | table tests and GUNPEI fixture |
| 28 verified GUNPEI artwork | AV-201, AV-202, AV-602 | live 715x706 evidence |

No acceptance row may be marked complete from a plan or argument-string test alone when the final-evidence column requires runtime output.

The review at commit `5c5b872` invalidated the previous AV-700 completion claim. Criteria 3-4, 8-13, 18-22, and 28 require correction-wave evidence from AV-711 through AV-719 before AV-700 can return to `VERIFIED`.

The feature is not complete until AV-700 records fresh evidence for all commands:

```powershell
rtk go test ./... -count=1
rtk go vet ./...
rtk git diff --check
```

`go vet` may still report the known pre-existing Windows `unsafe.Pointer` warning. The active AI must compare it with the baseline and reject any new warning.

Runtime output must also pass:

```powershell
rtk proxy "$env:IMAGEPAD_FFPROBE" -v error -show_streams -show_format -of json .\current.m3u8
rtk proxy "$env:IMAGEPAD_FFMPEG" -v error -i .\current.m3u8 -f null NUL
```

Expected encoded output: H.264, `yuv420p`, 30 fps, even 16:9 dimensions, AAC stereo 48 kHz, complete decode with exit code 0.

## Execution handoff

Use subagent-driven execution. One fresh worker receives exactly one ticket and one worktree. The active AI reviews the diff and evidence before updating `ticket-status.md` or merging. No worker may self-mark `VERIFIED` or `MERGED`.
