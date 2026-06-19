# Foundation and Ingest Tickets

Execute each ticket under `00-dispatch-contract.md`. Commands are PowerShell commands run from the ticket worktree.

### AV-000: Checkpoint the accepted prototype

**Dependencies:** none. **Parallel:** prohibited. **Owner:** active AI.

**Files:** Existing dirty files only; do not modify file contents in this ticket.

- [ ] Run `rtk git diff --check` and record the result.
- [ ] Run `rtk go test ./... -count=1` and record package/test totals.
- [ ] Review `rtk git diff` plus untracked `internal/video/soundcloud.go`, `soundcloud_test.go`, and `publisher_test.go`; verify they are the accepted prototype described in the design spec.
- [ ] Stage exactly the current prototype files listed in the master plan.
- [ ] Commit with `git commit -m "feat: checkpoint SoundCloud audio prototype"`.
- [ ] Record the commit as the only base allowed for AV-001.

Expected evidence: one baseline hash, no unstaged prototype file, and a complete test result. If existing tests fail, create a baseline-failure ledger entry; do not hide the failure.

### AV-001: Lock shared audio contracts

**Dependencies:** AV-000. **Parallel:** prohibited.

**Files:**
- Create: `internal/video/audio_types.go`
- Create: `internal/video/audio_types_test.go`

- [ ] **Step 1: Write the failing contract test**

```go
func TestAudioContracts(t *testing.T) {
	if MaxMediaSourceBytes != 4294967295 {
		t.Fatalf("MaxMediaSourceBytes = %d", MaxMediaSourceBytes)
	}
	if SourceSoundCloud != "soundcloud" || SourceLocalAudio != "local_audio" || SourceRemoteAudio != "remote_audio" {
		t.Fatalf("unexpected source kinds")
	}
	var frame AudioFrame
	var features AudioFeatures
	if len(frame.Spectrum24) != 24 || len(features.Fingerprint64) != 64 || len(features.LoudnessEnvelope) != 1000 {
		t.Fatalf("fixed analysis dimensions changed")
	}
}
```

- [ ] Run `rtk go test ./internal/video -run '^TestAudioContracts$' -count=1 -v`; expected RED because the contract names do not exist.
- [ ] Add the exact contract block from the master plan to `audio_types.go`.
- [ ] Run the focused test; expected PASS.
- [ ] Run `rtk go test ./internal/video -count=1`.
- [ ] Commit `feat: define shared audio contracts`.

After merge, this file is frozen.

### AV-100: Install and resolve ffprobe with FFmpeg

**Dependencies:** AV-001. **Parallel wave:** 2.

**Files:**
- Create: `internal/video/toolchain.go`
- Create: `internal/video/toolchain_test.go`
- Modify: `internal/video/publisher.go` only to delegate existing ffmpeg/yt-dlp resolution into `toolchain.go`; no queue edits.

- [ ] Add failing tests named `TestFFprobePathUsesConfiguredPath`, `TestFFprobePathUsesSiblingOfFFmpeg`, `TestFFmpegArchiveInstallRequiresFFprobe`, and `TestVisualizerFFmpegRequiresSubtitlesFilter`.

```go
func TestFFprobePathUsesSiblingOfFFmpeg(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, executableName("ffmpeg"))
	ffprobe := filepath.Join(dir, executableName("ffprobe"))
	mustWriteExecutable(t, ffmpeg)
	mustWriteExecutable(t, ffprobe)
	t.Setenv("IMAGEPAD_FFMPEG", ffmpeg)
	t.Setenv("IMAGEPAD_FFPROBE", "")
	got, err := ffprobePath()
	if err != nil || got != ffprobe { t.Fatalf("got %q, %v", got, err) }
}

func executableName(base string) string {
	if runtime.GOOS == "windows" { return base + ".exe" }
	return base
}

func mustWriteExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), 0755); err != nil { t.Fatal(err) }
}
```

- [ ] Run `rtk go test ./internal/video -run '^TestFFprobePath' -count=1 -v`; expected RED because `ffprobePath` does not exist.
- [ ] Implement `ffprobePath() (string, error)` with priority: `IMAGEPAD_FFPROBE`, sibling of resolved FFmpeg, app bin directory, PATH.
- [ ] Update the FFmpeg archive install check so success requires both `ffmpeg.exe` and `ffprobe.exe`; return `ffprobe not found after FFmpeg installation` when absent.
- [ ] Implement `verifyVisualizerFFmpeg(ffmpeg string) error` by running `ffmpeg -hide_banner -filters` and requiring `subtitles`, `drawtext`, `showwaves`, `gblur`, and `ebur128`; return an explicit missing-filter error naming the missing filter.
- [ ] Run focused tests and `rtk go test ./internal/video -count=1`.
- [ ] Commit `feat: install and resolve ffprobe`.

Do not download tools in unit tests.

### AV-101: Parse ffprobe output and classify streams

**Dependencies:** AV-001, AV-100. **Parallel wave:** 2 after AV-100 merge.

**Files:**
- Create: `internal/video/media_probe.go`
- Create: `internal/video/media_probe_test.go`

Required API:

```go
func ProbeMedia(ctx context.Context, ffprobe, path string) (MediaProbe, error)
func ParseMediaProbeJSON(data []byte) (MediaProbe, error)
func ClassifyMediaProbe(probe MediaProbe) MediaClass
```

- [ ] Add table tests for audio-only, audio plus `attached_pic`, real video plus audio, and no playable stream.

```go
func TestClassifyMediaProbeAttachedPictureIsAudio(t *testing.T) {
	p := MediaProbe{Streams: []MediaStream{
		{Index: 0, CodecType: "audio", CodecName: "aac"},
		{Index: 1, CodecType: "video", CodecName: "mjpeg", AttachedPic: true},
	}}
	if got := ClassifyMediaProbe(p); got != MediaAudio { t.Fatalf("got %q", got) }
}
```

- [ ] Run focused tests; expected RED because APIs are absent.
- [ ] Implement JSON structs matching `-show_streams -show_format -of json` and invoke ffprobe with `-v error`.
- [ ] Require a finite positive duration for audio rendering, but keep classification independent from duration validation.
- [ ] Run `rtk go test ./internal/video -run 'Test(Parse|Classify|Probe)Media' -count=1 -v` and the package test.
- [ ] Commit `feat: classify media with ffprobe`.

### AV-102: Normalize legacy tags and resolve metadata

**Dependencies:** AV-001. **Parallel wave:** 2.

**Files:**
- Create: `internal/video/audio_metadata.go`
- Create: `internal/video/audio_metadata_test.go`

Required API:

```go
func NormalizeEmbeddedTag(raw string) string
func ResolveAudioMetadata(kind SourceKind, sourceName string, embedded, soundCloud AudioMetadata) AudioMetadata
```

- [ ] Add pass-through tests for ASCII, `日本語`, `简体中文`, `한국어`, and `café`.
- [ ] Add the exact GUNPEI repair test.

```go
func TestNormalizeEmbeddedTagRepairsGunpeiCP932(t *testing.T) {
	artist := string([]rune{0x93, 0xA1, 0x8E, 0x71, 0x96, 0xBC, 0x90, 0x6C})
	album := string([]rune{0x94, 0x5A, 0x93, 0x78})
	if got := NormalizeEmbeddedTag(artist); got != "藤子名人" { t.Fatalf("artist=%q", got) }
	if got := NormalizeEmbeddedTag(album); got != "濃度" { t.Fatalf("album=%q", got) }
}
```

- [ ] Run focused tests; expected RED.
- [ ] Implement the strict acceptance algorithm from spec section 4.8. Do not convert strings that already pass the Unicode checks.
- [ ] Import the already-present `golang.org/x/text/encoding/japanese` module without running `go mod tidy`; AV-204 is the only parallel-wave owner of `go.mod` and `go.sum`.
- [ ] Implement source precedence from spec section 4.4; local/remote never consume SoundCloud metadata.
- [ ] Run focused and package tests.
- [ ] Commit `feat: normalize and resolve audio metadata`.

### AV-103: Enforce the FAT32 media-size ceiling

**Dependencies:** AV-001. **Parallel wave:** 2.

**Files:**
- Create: `internal/video/media_limit.go`
- Create: `internal/video/media_limit_test.go`

Required API:

```go
var ErrMediaTooLarge = errors.New("media exceeds 4294967295 byte limit")
func CopyMediaWithLimit(dst io.Writer, src io.Reader) (int64, error)
func copyWithLimit(dst io.Writer, src io.Reader, limit int64) (int64, error)
func ValidateMediaContentLength(length int64) error
```

- [ ] Add tests using a 32-byte injected limit; never allocate a 4 GB fixture.

```go
func TestCopyWithLimitRejectsLimitPlusOne(t *testing.T) {
	var dst bytes.Buffer
	n, err := copyWithLimit(&dst, bytes.NewReader(make([]byte, 33)), 32)
	if !errors.Is(err, ErrMediaTooLarge) || n != 33 { t.Fatalf("n=%d err=%v", n, err) }
}
```

- [ ] Run focused tests; expected RED.
- [ ] Implement streaming with `io.LimitReader(src, limit+1)` and reject after reading byte `limit+1`.
- [ ] Assert `MaxMediaSourceBytes == 4294967295` in a separate test.
- [ ] Run package tests and commit `feat: enforce media source size limit`.

### AV-104: Build and validate exact Noto Sans CJK JP weights

**Dependencies:** AV-001. **Parallel wave:** 2.

**Files:**
- Create: `assets/fonts/NotoSansCJKjp-Regular.otf`
- Create: `assets/fonts/NotoSansCJKjp-Medium.otf`
- Create: `assets/fonts/NotoSansCJKjp-SemiBold.otf`
- Create: `assets/fonts/OFL.txt`
- Create: `tools/fonts/build_noto_instances.py`
- Create: `internal/video/font.go`
- Create: `internal/video/font_test.go`

- [ ] Add a failing test that calls `VisualizerFontPath()` and verifies a non-empty regular file plus OpenType signature.
- [ ] The build script must install/use `fonttools==4.63.0`, download the pinned variable source to a temporary directory, verify its hash, instantiate `wght=400`, `wght=500`, and `wght=600`, and write the three static outputs above. Do not commit the variable source.
- [ ] Implement the script around these exact operations:

```python
from pathlib import Path
from urllib.request import urlopen
import hashlib, subprocess, sys, tempfile

SOURCE_URL = "https://raw.githubusercontent.com/notofonts/noto-cjk/f8d157532fbfaeda587e826d4cd5b21a49186f7c/Sans/Variable/OTF/NotoSansCJKjp-VF.otf"
SOURCE_SHA256 = "AB2728702F90D2AE900309F299DC3C2B075010888A1A8A67FBD5B4C6AFF713A0"
LICENSE_URL = "https://raw.githubusercontent.com/notofonts/noto-cjk/f8d157532fbfaeda587e826d4cd5b21a49186f7c/Sans/LICENSE"
LICENSE_SHA256 = "6A73F9541C2DE74158C0E7CF6B0A58EF774F5A780BF191F2D7EC9CC53EFE2BF2"
OUTPUTS = {400: "NotoSansCJKjp-Regular.otf", 500: "NotoSansCJKjp-Medium.otf", 600: "NotoSansCJKjp-SemiBold.otf"}

def main() -> None:
    output_dir = Path(__file__).resolve().parents[2] / "assets" / "fonts"
    output_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="imagepad-noto-") as temp:
        source = Path(temp) / "NotoSansCJKjp-VF.otf"
        source.write_bytes(urlopen(SOURCE_URL, timeout=60).read())
        actual = hashlib.sha256(source.read_bytes()).hexdigest().upper()
        if actual != SOURCE_SHA256:
            raise SystemExit(f"font hash mismatch: {actual}")
        for weight, name in OUTPUTS.items():
            subprocess.run([sys.executable, "-m", "fontTools.varLib.instancer", str(source), f"wght={weight}", "--update-name-table", f"--output={output_dir / name}"], check=True)
        license_bytes = urlopen(LICENSE_URL, timeout=60).read()
        if hashlib.sha256(license_bytes).hexdigest().upper() != LICENSE_SHA256:
            raise SystemExit("license hash mismatch")
        (output_dir / "OFL.txt").write_bytes(license_bytes)

if __name__ == "__main__":
    main()
```

- [ ] Run it from a temporary venv containing exactly `fonttools==4.63.0`; do not commit the venv.
- [ ] Download the source font and license only from the pinned URLs in the master plan.
- [ ] Verify source SHA-256 exactly:

```text
NotoSansCJKjp-VF.otf AB2728702F90D2AE900309F299DC3C2B075010888A1A8A67FBD5B4C6AFF713A0
OFL.txt              6A73F9541C2DE74158C0E7CF6B0A58EF774F5A780BF191F2D7EC9CC53EFE2BF2
```

- [ ] Use `fontTools.ttLib.TTFont` in the script to assert each output's `OS/2.usWeightClass` is exactly 400, 500, or 600.
- [ ] Implement `VisualizerFonts() (FontSet, error)` using explicit bundled paths and an error containing `Noto Sans CJK JP font not found`.
- [ ] Do not use OS font fallback.
- [ ] Run `rtk go test ./internal/video -run '^TestVisualizerFont' -count=1 -v` and package tests.
- [ ] Commit `build: bundle Noto Sans CJK JP`.

### AV-201: Extract and select embedded artwork

**Dependencies:** AV-101, AV-102. **Parallel wave:** 3.

**Files:**
- Create: `internal/video/audio_artwork.go`
- Create: `internal/video/audio_artwork_test.go`

Required API:

```go
func ExtractEmbeddedArtwork(ctx context.Context, ffmpeg, sourcePath, outDir string, probe MediaProbe) ([]ArtworkCandidate, error)
func SelectArtwork(embedded []ArtworkCandidate, soundCloudPath string, kind SourceKind) (string, error)
```

- [ ] Add tests: front cover wins; otherwise largest area; then largest bytes; corrupt image skipped; SoundCloud art only for `SourceSoundCloud`.
- [ ] Run focused tests; expected RED.
- [ ] Extract each `attached_pic` stream to a unique PNG/JPEG path and validate it with `image.DecodeConfig`.
- [ ] Return empty selection, not an error, when no artwork exists.
- [ ] Add a call-counter test proving local and remote kinds never consult SoundCloud art.
- [ ] Run package tests and commit `feat: resolve embedded audio artwork`.

### AV-202: Bind SoundCloud outputs through a manifest

**Dependencies:** AV-100, AV-101, AV-102, AV-103. **Parallel wave:** 3.

**Files:**
- Create: `internal/video/soundcloud_download.go`
- Create: `internal/video/soundcloud_download_test.go`
- Create: `internal/video/soundcloud_info.go`
- Create: `internal/video/soundcloud_info_test.go`
- Modify: `internal/video/soundcloud.go` only to remove replaced acquisition functions.
- Modify: `internal/video/soundcloud_test.go` only to remove obsolete largest-file and 2G tests.

Required API:

```go
func DownloadSoundCloud(ctx context.Context, ytdlp, rawURL, outDir string) (AcquiredAudio, error)
func ParseSoundCloudInfoJSON(data []byte) (AudioMetadata, error)
func ReadSinglePathManifest(manifest, root string) (string, error)
```

- [ ] Add argument tests requiring `--max-filesize 4294967295`, `--write-thumbnail`, `--write-info-json`, and `--print-to-file after_move:filepath`.
- [ ] Add manifest tests for zero lines, two lines, missing file, outside-root path, and valid audio.
- [ ] Add a regression test where `.info.json` is larger than audio and manifest-listed audio still wins.
- [ ] Run focused tests; expected RED.
- [ ] Implement unique job prefixes and exact sidecar isolation from spec section 4.7.
- [ ] Parse uploader as artist fallback and album only when present.
- [ ] Do not commit live GUNPEI audio or JPEG.
- [ ] Run package tests and commit `feat: acquire SoundCloud audio by manifest`.

### AV-203: Download generic direct media safely

**Dependencies:** AV-101, AV-103. **Parallel wave:** 3.

**Files:**
- Create: `internal/server/remote_media.go`
- Create: `internal/server/remote_media_test.go`
- Modify: `internal/server/remote_upload.go` only to reuse existing SSRF validation helpers.

Required API:

```go
type downloadedRemoteMedia struct { Path, Name string; Class video.MediaClass }
func downloadRemoteMedia(ctx context.Context, rawURL, outDir string, probe func(context.Context, string) (video.MediaProbe, error)) (downloadedRemoteMedia, error)
```

- [ ] Add `httptest.Server` cases for Content-Length rejection before body read, chunked limit+1, redirect to blocked IP, Content-Disposition name, URL basename fallback, audio classification, and video preservation.
- [ ] Run focused tests; expected RED.
- [ ] Revalidate every redirect with existing public-URL rules.
- [ ] Use `video.ValidateMediaContentLength` and `video.CopyMediaWithLimit`.
- [ ] Set kind `remote_audio` only after ffprobe identifies audio without real video.
- [ ] Run `rtk go test ./internal/server -run '^TestDownloadRemoteMedia' -count=1 -v` and package tests.
- [ ] Commit `feat: download direct remote media safely`.

### AV-204: Analyze audio deterministically

**Dependencies:** AV-100, AV-101. **Parallel wave:** 3.

**Files:**
- Create: `internal/video/audio_analysis.go`
- Create: `internal/video/audio_analysis_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

Required API:

```go
func AnalyzeAudio(ctx context.Context, ffmpeg, sourcePath string) (AudioAnalysis, error)
func SelectMoodPalette(features AudioFeatures) (startHex, endHex string)
```

- [ ] Add `gonum.org/v1/gonum@v0.17.0` and use `dsp/fourier` rather than inventing an FFT implementation.
- [ ] Add tests for `ceil(duration*30)` frames, exactly 24 spectrum values per frame, 64 fingerprint values, 1000 envelope values, deterministic repeat output, and every first-match palette boundary.
- [ ] Run focused tests; expected RED.
- [ ] Decode with FFmpeg to stereo `f32le` at 48 kHz. Use a 2048-sample Hann FFT window and a 1600-sample frame advance to align analysis to 30 fps.
- [ ] Map FFT magnitudes to 24 logarithmic bands over 20 Hz..20 kHz. Store exactly one `AudioFrame` per 30 fps output frame.
- [ ] Smooth each band with attack `0.65` and release `0.18`: when current exceeds previous use `previous + 0.65*(current-previous)`, otherwise use `previous + 0.18*(current-previous)`.
- [ ] Compute 64 whole-track logarithmic band averages, 1000 RMS envelope samples, spectral centroid, and low-frequency ratio. Obtain integrated LUFS through FFmpeg `ebur128`/metadata output and parse it deterministically.
- [ ] Compute BPM from a 100 Hz positive spectral-flux onset envelope: autocorrelate lags representing 60..200 BPM, choose the highest correlation, and set BPM to `6000 / lag`. If every correlation is non-positive, set BPM to zero so palette selection falls through to the other measured features.
- [ ] Implement analysis as a pre-render step and clamp all normalized outputs to `0..1`.
- [ ] Fail analysis when the whole-track loudness envelope cannot be produced; do not return a flat fake graph.
- [ ] Run package tests and commit `feat: analyze audio for visualizer`.

### AV-205: Classify local uploads with ffprobe

**Dependencies:** AV-101, AV-103. **Parallel wave:** 3.

**Files:**
- Create: `internal/server/media_classification.go`
- Create: `internal/server/media_classification_test.go`

Required API:

```go
func classifyUploadedMedia(ctx context.Context, enabled bool, name, contentType, path string, probe func(context.Context, string) (video.MediaProbe, error)) (video.MediaClass, error)
```

- [ ] Add cases for existing image/RAW, extensionless audio, audio with attached cover, true video, unsupported file, and video-player-disabled audio rejection.
- [ ] Run focused tests; expected RED.
- [ ] Preserve image/RAW detection before ffprobe.
- [ ] Ignore MIME and extension as authoritative audio evidence.
- [ ] Run server package tests and commit `feat: classify local media uploads`.
