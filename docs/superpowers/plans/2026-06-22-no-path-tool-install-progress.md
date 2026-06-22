# Bundled-only Tooling + Safe Install with Grayout Progress — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop resolving ffmpeg/ffprobe/yt-dlp from system PATH, make tool acquisition robust (mirrors + retries + startup validation), and replace the install freeze with a grayout overlay + progress bar driven by `/api/state`.

**Architecture:** `internal/video` owns bundled-only resolution, a process-wide install-progress tracker, progress-reporting downloads, mirror/retry acquisition, and startup integrity validation. `internal/server` exposes `toolInstall` in `/api/state`, makes the video-player toggle async (revert on failure, self-heal while active), and renders a grayout overlay in `ui.go`. `internal/app` warms/validates tools at startup.

**Tech Stack:** Go (stdlib `net/http`, `os/exec`, `archive/zip`, `sync`), vanilla JS/CSS in the served HTML (`internal/server/ui.go`).

**Spec:** `docs/superpowers/specs/2026-06-22-no-path-tool-install-progress-design.md`

**Commit convention:** Stage with `git add`, then commit via the h5i provenance tool (`mcp__h5i__h5i_commit` with `agent: "claude-code"`, `model: "claude-opus-4-8"`, and the driving prompt). Raw `git commit` works but skips provenance. Note: in this repo, `git` invoked through the Bash tool fails under the Japanese OneDrive path — use the PowerShell tool or the h5i commit tool.

---

## File Structure

- `internal/video/toolchain.go` (modify) — remove `exec.LookPath` fallbacks; wire progress into downloads; mirror/retry; `ToolsReady`, `ValidateInstalledTools`.
- `internal/video/install_progress.go` (create) — `ToolInstallStatus` type, thread-safe tracker, accessor, `progressWriter`.
- `internal/video/install_progress_test.go` (create) — tracker + progressWriter tests.
- `internal/video/toolchain_test.go` (modify) — assert PATH is never used; mirror fallback; `ValidateInstalledTools`; `ToolsReady`.
- `internal/video/tool_sources.go` (create) — per-tool ordered source lists + retry helper.
- `internal/server/server.go` (modify) — `toolInstall` in state; async `handleVideoPlayer` with revert + self-heal loop; `startVideoToolInstall`.
- `internal/server/tool_install.go` (create) — server-side async orchestration (goroutine, dedupe, self-heal-while-active).
- `internal/server/tool_install_test.go` (create) — toggle stays OFF on failure, enables on success, no double-goroutine.
- `internal/server/ui.go` (modify) — grayout overlay HTML/CSS + `toolInstall` rendering in `applyState`.
- `internal/server/ui_media_test.go` (modify) — assert overlay element + `toolInstall` branch are served.
- `internal/app/app.go` (modify) — startup `ValidateInstalledTools` + warm when video player persisted-enabled.

---

## Task 1: Remove system PATH from tool resolution

**Files:**
- Modify: `internal/video/toolchain.go` (`ffmpegPath` ~175, `ffprobePath` ~52, `usableFFprobePath` ~83, `ytdlpPath` ~188)
- Test: `internal/video/toolchain_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/video/toolchain_test.go`:

```go
func TestResolversNeverUsePATH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	t.Setenv("IMAGEPAD_FFMPEG", "")
	t.Setenv("IMAGEPAD_FFPROBE", "")
	t.Setenv("IMAGEPAD_YTDLP", "")

	// Put a fake ffmpeg/ffprobe/yt-dlp on PATH only.
	pathDir := t.TempDir()
	for _, base := range []string{"ffmpeg", "ffprobe", "yt-dlp"} {
		writeFakeExe(t, filepath.Join(pathDir, executableName(base)))
	}
	t.Setenv("PATH", pathDir)

	if got, err := ffmpegPath(); err == nil {
		t.Fatalf("ffmpegPath() resolved %q from PATH; want error", got)
	}
	if got, err := ffprobePath(); err == nil {
		t.Fatalf("ffprobePath() resolved %q from PATH; want error", got)
	}
	if got, err := ytdlpPath(); err == nil {
		t.Fatalf("ytdlpPath() resolved %q from PATH; want error", got)
	}
	if got := usableFFprobePath(); got != "" {
		t.Fatalf("usableFFprobePath() = %q from PATH; want empty", got)
	}
}

// writeFakeExe writes a non-empty file marked executable so fileExists passes.
func writeFakeExe(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho fake\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}
```

Ensure `os` and `path/filepath` are imported in the test file (they already are if other tests use them; add if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestResolversNeverUsePATH -count=1`
Expected: FAIL — resolvers currently return the PATH binary.

- [ ] **Step 3: Remove the PATH fallbacks**

In `ffmpegPath` replace the final `return exec.LookPath("ffmpeg")` with:

```go
	return "", fmt.Errorf("ffmpeg not found in bundle. %s You can also set IMAGEPAD_FFMPEG.", toolInstallHint("ffmpeg"))
```

In `ytdlpPath` replace `return exec.LookPath("yt-dlp")` with:

```go
	return "", fmt.Errorf("yt-dlp not found in bundle. %s You can also set IMAGEPAD_YTDLP.", toolInstallHint("yt-dlp"))
```

In `ffprobePath` replace `return exec.LookPath("ffprobe")` with:

```go
	return "", fmt.Errorf("ffprobe not found in bundle. %s You can also set IMAGEPAD_FFPROBE.", toolInstallHint("ffmpeg"))
```

In `usableFFprobePath` delete this block entirely:

```go
	if path, err := exec.LookPath("ffprobe"); err == nil {
		candidates = append(candidates, path)
	}
```

If `os/exec` becomes unused in the file, keep it — `exec.Command` is still used by `validateExecutable`/`run`. (It is; no import change needed.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run 'TestResolversNeverUsePATH|TestEnsureFFprobe' -count=1`
Expected: PASS (existing `TestEnsureFFprobe*` still pass — they use temp bins, not PATH).

- [ ] **Step 5: Build the whole module**

Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Commit**

Stage `internal/video/toolchain.go` and `internal/video/toolchain_test.go`, then commit:
`feat(video): resolve ffmpeg/ffprobe/yt-dlp from bundle only, never PATH`

---

## Task 2: Install-progress tracker + progressWriter

**Files:**
- Create: `internal/video/install_progress.go`
- Test: `internal/video/install_progress_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/video/install_progress_test.go`:

```go
package video

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

func TestInstallTrackerLifecycle(t *testing.T) {
	resetInstallProgress()
	if s := ToolInstallStatus(); s.Active {
		t.Fatalf("fresh tracker Active=true")
	}
	installBegin("ffmpeg")
	installPhase("download")
	installPercent(42)
	s := ToolInstallStatus()
	if !s.Active || s.Tool != "ffmpeg" || s.Phase != "download" || s.Percent != 42 {
		t.Fatalf("unexpected status: %+v", s)
	}
	installDone()
	if s := ToolInstallStatus(); s.Active || s.Failed || s.Percent != 100 {
		t.Fatalf("after done: %+v", s)
	}
	installBegin("yt-dlp")
	installFail("boom")
	if s := ToolInstallStatus(); s.Active || !s.Failed || s.Message != "boom" {
		t.Fatalf("after fail: %+v", s)
	}
}

func TestProgressWriterReportsMonotonicPercent(t *testing.T) {
	resetInstallProgress()
	var seen []int
	pw := &progressWriter{total: 100, onProgress: func(p int) { seen = append(seen, p) }}
	src := bytes.NewReader(make([]byte, 100))
	var dst bytes.Buffer
	if _, err := io.Copy(&dst, io.TeeReader(src, pw)); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 || seen[len(seen)-1] != 100 {
		t.Fatalf("expected final 100, got %v", seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("non-monotonic percent: %v", seen)
		}
	}
}

func TestInstallTrackerConcurrentSnapshot(t *testing.T) {
	resetInstallProgress()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			installBegin("ffmpeg")
			installPercent(10)
			_ = ToolInstallStatus()
		}()
	}
	wg.Wait()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run 'TestInstallTracker|TestProgressWriter' -count=1`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement the tracker**

Create `internal/video/install_progress.go`:

```go
package video

import "sync"

// ToolInstallStatus is an immutable snapshot of the current tool acquisition.
type ToolInstallStatus struct {
	Active  bool   `json:"active"`
	Tool    string `json:"tool"`    // "ffmpeg" | "ffprobe" | "yt-dlp"
	Phase   string `json:"phase"`   // "download" | "extract" | "validate" | ""
	Percent int    `json:"percent"` // 0-100; download is byte-driven, others 0
	Attempt int    `json:"attempt"` // 1-based attempt across sources/retries
	Failed  bool   `json:"failed"`
	Message string `json:"message"`
}

type installTracker struct {
	mu     sync.Mutex
	status ToolInstallStatus
}

var installProgress = &installTracker{}

// ToolInstallStatus returns a copy of the current tracker state.
func ToolInstallStatus() ToolInstallStatus {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	return installProgress.status
}

func resetInstallProgress() {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status = ToolInstallStatus{}
}

func installBegin(tool string) {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status = ToolInstallStatus{Active: true, Tool: tool, Attempt: 1}
}

func installPhase(phase string) {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status.Phase = phase
	if phase != "download" {
		installProgress.status.Percent = 0
	}
}

func installPercent(p int) {
	if p < 0 {
		p = 0
	} else if p > 100 {
		p = 100
	}
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status.Percent = p
}

func installAttempt(n int) {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status.Attempt = n
}

func installDone() {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status = ToolInstallStatus{Active: false, Failed: false, Percent: 100}
}

func installFail(msg string) {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status.Active = false
	installProgress.status.Failed = true
	installProgress.status.Message = msg
}

// progressWriter counts bytes and reports an integer percent via onProgress.
type progressWriter struct {
	total      int64
	written    int64
	lastPct    int
	onProgress func(int)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.written += int64(n)
	if w.onProgress != nil && w.total > 0 {
		pct := int(w.written * 100 / w.total)
		if pct > 100 {
			pct = 100
		}
		if pct != w.lastPct {
			w.lastPct = pct
			w.onProgress(pct)
		}
	}
	return n, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run 'TestInstallTracker|TestProgressWriter' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

Stage both files, commit: `feat(video): add install-progress tracker and progressWriter`

---

## Task 3: Report download progress into the tracker

**Files:**
- Modify: `internal/video/toolchain.go` (`downloadFile` ~457, `downloadFileAllowMissingChecksum` ~509)
- Test: `internal/video/install_progress_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Add to `internal/video/install_progress_test.go`:

```go
func TestDownloadFileReportsProgress(t *testing.T) {
	resetInstallProgress()
	installBegin("yt-dlp")
	payload := make([]byte, 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	sum := sha256.Sum256(payload)
	dst := filepath.Join(t.TempDir(), "out.bin")
	if err := downloadFile(dst, srv.URL, 1<<20, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if s := ToolInstallStatus(); s.Phase != "download" || s.Percent != 100 {
		t.Fatalf("expected download phase at 100%%, got %+v", s)
	}
}
```

Add imports to the test file: `crypto/sha256`, `encoding/hex`, `net/http`, `net/http/httptest`, `path/filepath`, `strconv`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestDownloadFileReportsProgress -count=1`
Expected: FAIL — phase/percent not set.

- [ ] **Step 3: Wire progress into the copy**

In `downloadFile`, replace:

```go
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, maxBytes+1))
```

with:

```go
	installPhase("download")
	pw := &progressWriter{total: resp.ContentLength, onProgress: installPercent}
	written, copyErr := io.Copy(out, io.TeeReader(io.LimitReader(resp.Body, maxBytes+1), pw))
```

Apply the identical change in `downloadFileAllowMissingChecksum`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run TestDownloadFileReportsProgress -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

Stage `toolchain.go` and the test, commit: `feat(video): report download byte progress to tracker`

---

## Task 4: Mirror sources + bounded retry for acquisition

**Files:**
- Create: `internal/video/tool_sources.go`
- Modify: `internal/video/toolchain.go` (`downloadFFmpeg` ~288, `downloadYTDLPWithChecksum` ~329)
- Test: `internal/video/toolchain_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/video/toolchain_test.go`:

```go
func TestAcquireFromSourcesFallsBackToMirror(t *testing.T) {
	var calls []string
	attempt := func(s toolSource) error {
		calls = append(calls, s.url)
		if s.url == "primary" {
			return errors.New("primary down")
		}
		return nil
	}
	sources := []toolSource{{url: "primary"}, {url: "mirror"}}
	if err := acquireFromSources("ffmpeg", sources, 1, attempt); err != nil {
		t.Fatalf("acquireFromSources: %v", err)
	}
	if len(calls) != 2 || calls[1] != "mirror" {
		t.Fatalf("expected fallback to mirror, calls=%v", calls)
	}
}

func TestAcquireFromSourcesExhaustionFails(t *testing.T) {
	attempt := func(s toolSource) error { return errors.New("nope") }
	err := acquireFromSources("ffmpeg", []toolSource{{url: "a"}}, 2, attempt)
	if err == nil {
		t.Fatal("expected error when all sources/retries fail")
	}
}
```

Ensure `errors` is imported in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestAcquireFromSources -count=1`
Expected: FAIL — undefined `toolSource`, `acquireFromSources`.

- [ ] **Step 3: Implement sources + retry helper**

Create `internal/video/tool_sources.go`:

```go
package video

import (
	"fmt"
	"runtime"
	"time"
)

// toolSource is one place to fetch a tool. checksumURL may be empty, in which
// case the binary is trusted only after a successful -version validation.
type toolSource struct {
	url         string
	checksumURL string
}

// ffmpegWindowsSources lists the Windows FFmpeg archive download locations in
// priority order.
func ffmpegWindowsSources() []toolSource {
	return []toolSource{
		{url: ffmpegDownloadURL, checksumURL: ffmpegSHA256URL},
		// Mirror: GitHub release of the same Gyan build (no sidecar .sha256;
		// validated by -version after extraction).
		{url: "https://github.com/GyanD/codexffmpeg/releases/latest/download/ffmpeg-release-essentials.zip"},
	}
}

// ytdlpSources lists yt-dlp executable download locations in priority order.
func ytdlpSources() []toolSource {
	if runtime.GOOS == "darwin" {
		return []toolSource{
			{url: ytdlpMacOSURL},
			{url: "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/yt-dlp_macos"},
		}
	}
	return []toolSource{
		{url: ytdlpDownloadURL},
		{url: "https://github.com/yt-dlp/yt-dlp-nightly-builds/releases/latest/download/yt-dlp.exe"},
	}
}

// acquireFromSources tries each source in order; each source is retried up to
// retries times with exponential backoff before advancing to the next. The
// current attempt number is reported to the install tracker.
func acquireFromSources(tool string, sources []toolSource, retries int, attempt func(toolSource) error) error {
	if retries < 1 {
		retries = 1
	}
	var lastErr error
	n := 0
	for _, src := range sources {
		for try := 0; try < retries; try++ {
			n++
			installAttempt(n)
			if err := attempt(src); err != nil {
				lastErr = err
				time.Sleep(backoffDelay(try))
				continue
			}
			return nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no download sources configured for %s", tool)
	}
	return fmt.Errorf("failed to acquire %s after exhausting %d source(s): %w", tool, len(sources), lastErr)
}

func backoffDelay(try int) time.Duration {
	d := time.Second << uint(try) // 1s, 2s, 4s, ...
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run TestAcquireFromSources -count=1`
Expected: PASS.

- [ ] **Step 5: Route Windows ffmpeg + yt-dlp downloads through the sources**

In `downloadFFmpeg` (Windows branch, after `target := localFFmpegPath()` and the `MkdirAll`), replace the single-source download/extract body (the block from `checksum := strings.TrimSpace(...)` through `return target, nil`) with a per-source attempt:

```go
	installBegin("ffmpeg")
	zipPath := filepath.Join(settings.Dir(), "bin", "ffmpeg-release-essentials.zip")
	envChecksum := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG_SHA256"))
	if envChecksum == "" {
		envChecksum = ffmpegDownloadSHA256
	}
	attempt := func(src toolSource) error {
		checksum := envChecksum
		if checksum == "" && src.checksumURL != "" {
			c, err := remoteTextSHA256(src.checksumURL)
			if err != nil {
				return fmt.Errorf("failed to resolve FFmpeg checksum: %w", err)
			}
			checksum = c
		}
		if checksum != "" {
			if err := downloadFile(zipPath, src.url, 160<<20, checksum); err != nil {
				return fmt.Errorf("failed to download FFmpeg: %w", err)
			}
		} else {
			if err := downloadFileAllowMissingChecksum(zipPath, src.url, 160<<20, ""); err != nil {
				return fmt.Errorf("failed to download FFmpeg: %w", err)
			}
		}
		defer os.Remove(zipPath)
		installPhase("extract")
		if err := extractFFmpegZip(zipPath, target); err != nil {
			return fmt.Errorf("failed to install FFmpeg: %w", err)
		}
		installPhase("validate")
		if err := validateExecutable(target, "-version"); err != nil {
			_ = os.Remove(target)
			return err
		}
		return nil
	}
	if err := acquireFromSources("ffmpeg", ffmpegWindowsSources(), 2, attempt); err != nil {
		installFail(err.Error())
		return "", err
	}
	installDone()
	return target, nil
```

In `downloadYTDLPWithChecksum` (Windows branch, after `target := localYTDLPPath()` and `MkdirAll`), replace the checksum-resolution + `downloadFile` body (from `checksum = strings.TrimSpace(checksum)` through `return target, nil`) with:

```go
	installBegin("yt-dlp")
	envChecksum := strings.TrimSpace(checksum)
	if envChecksum == "" {
		envChecksum = strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP_SHA256"))
	}
	if envChecksum == "" {
		envChecksum = ytdlpDownloadSHA256
	}
	attempt := func(src toolSource) error {
		sum := envChecksum
		if sum == "" {
			c, err := remoteSHA256For("yt-dlp.exe")
			if err == nil {
				sum = c
			}
		}
		if sum != "" {
			if err := downloadFile(target, src.url, 50<<20, sum); err != nil {
				return fmt.Errorf("failed to download yt-dlp: %w", err)
			}
		} else {
			if err := downloadFileAllowMissingChecksum(target, src.url, 50<<20, ""); err != nil {
				return fmt.Errorf("failed to download yt-dlp: %w", err)
			}
		}
		installPhase("validate")
		if err := validateExecutable(target, "--version"); err != nil {
			_ = os.Remove(target)
			return err
		}
		return nil
	}
	if err := acquireFromSources("yt-dlp", ytdlpSources(), 2, attempt); err != nil {
		installFail(err.Error())
		return "", err
	}
	installDone()
	return target, nil
```

- [ ] **Step 6: Run video tests + build**

Run: `go test ./internal/video -count=1` then `go build ./...`
Expected: PASS / success.

- [ ] **Step 7: Commit**

Stage `tool_sources.go`, `toolchain.go`, `toolchain_test.go`, commit:
`feat(video): mirror sources + bounded retry for ffmpeg/yt-dlp acquisition`

---

## Task 5: Startup integrity validation + ToolsReady

**Files:**
- Modify: `internal/video/toolchain.go`
- Test: `internal/video/toolchain_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/video/toolchain_test.go`:

```go
func TestValidateInstalledToolsRepairsCorruptFFmpeg(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", dir)
	t.Setenv("IMAGEPAD_FFMPEG", "")
	t.Setenv("IMAGEPAD_FFPROBE", "")
	t.Setenv("PATH", t.TempDir()) // empty PATH

	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Corrupt ffmpeg: present but fails -version.
	if err := os.WriteFile(localFFmpegPath(), []byte("garbage"), 0o755); err != nil {
		t.Fatal(err)
	}

	prev := ffprobeBundleInstaller
	t.Cleanup(func() { ffprobeBundleInstaller = prev })
	installed := false
	ffprobeBundleInstaller = func() (string, error) {
		installed = true
		writeValidFixture(t, localFFmpegPath())
		writeValidFixture(t, localFFprobePath())
		return localFFmpegPath(), nil
	}

	ValidateInstalledTools()
	if !installed {
		t.Fatal("expected corrupt ffmpeg to trigger reinstall")
	}
}
```

Add a `writeValidFixture` helper next to the test (writes a script whose `-version` exits 0). On Windows, `validateExecutable` runs the file directly — reuse the existing fixture pattern from `TestEnsureFFprobeRepairsStaleConfiguredPath`; if that test already defines an equivalent helper, call it instead of redefining.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/video -run TestValidateInstalledToolsRepairs -count=1`
Expected: FAIL — `ValidateInstalledTools` undefined.

- [ ] **Step 3: Implement ToolsReady + ValidateInstalledTools**

Add to `internal/video/toolchain.go`:

```go
// ToolsReady reports whether ffmpeg and ffprobe both resolve to a bundled (or
// IMAGEPAD_*) binary that passes -version, without downloading anything.
func ToolsReady() bool {
	ffmpeg, err := ffmpegPath()
	if err != nil || validateExecutable(ffmpeg, "-version") != nil {
		return false
	}
	return usableFFprobePath() != ""
}

// ValidateInstalledTools checks the bundled binaries at startup and re-acquires
// any that are missing or fail validation. It is best-effort: errors are
// surfaced only through the install tracker, never returned, so startup never
// blocks on a tool problem it cannot fix.
func ValidateInstalledTools() {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return
	}
	if !ToolsReady() {
		if _, err := EnsureFFmpeg(); err != nil {
			installFail(err.Error())
			return
		}
		if _, err := EnsureFFprobe(); err != nil {
			installFail(err.Error())
			return
		}
	}
}
```

Note: `EnsureFFmpeg` already re-validates a corrupt local binary only if `ffmpegPath` fails. To make a *corrupt-but-present* ffmpeg trigger repair, change the first check in `EnsureFFmpeg` from `if ffmpeg, err := ffmpegPath(); err == nil {` to also require validation:

```go
	if ffmpeg, err := ffmpegPath(); err == nil && validateExecutable(ffmpeg, "-version") == nil {
		return ffmpeg, nil
	}
```

Apply the same validation gate to the second check inside the mutex.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/video -run 'TestValidateInstalledToolsRepairs|TestEnsureFFmpeg|TestEnsureFFprobe' -count=1`
Expected: PASS.

- [ ] **Step 5: Build + full video tests**

Run: `go build ./...` then `go test ./internal/video -count=1`
Expected: success / PASS.

- [ ] **Step 6: Commit**

Stage `toolchain.go`, `toolchain_test.go`, commit:
`feat(video): startup integrity validation + ToolsReady, repair corrupt binaries`

---

## Task 6: Server-side async orchestration with revert + self-heal

**Files:**
- Create: `internal/server/tool_install.go`
- Modify: `internal/server/server.go` (add fields to the `Server` struct; reference helpers)
- Test: `internal/server/tool_install_test.go`

- [ ] **Step 1: Locate the Server struct and add coordination fields**

Find the `type Server struct {` definition in `internal/server/server.go` and add:

```go
	toolInstallMu  sync.Mutex
	toolInstalling bool
```

(`sync` is already imported in the server package; confirm with `grep '"sync"' internal/server/server.go`.)

- [ ] **Step 2: Write the failing test**

Create `internal/server/tool_install_test.go`:

```go
package server

import (
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestStartVideoToolInstallEnablesOnSuccess(t *testing.T) {
	s := newTestServer(t) // existing helper used by other server tests
	prevReady, prevEnsure := videoToolsReady, ensureVideoTools
	t.Cleanup(func() { videoToolsReady = prevReady; ensureVideoTools = prevEnsure })

	videoToolsReady = func() bool { return false }
	ensureVideoTools = func() error { return nil }

	s.startVideoToolInstall()
	waitFor(t, 2*time.Second, func() bool { return s.videoPlayerEnabled() })

	if video.ToolInstallStatus().Failed {
		t.Fatal("tracker should not be failed on success")
	}
}

func TestStartVideoToolInstallRevertsOnFailure(t *testing.T) {
	s := newTestServer(t)
	prevReady, prevEnsure := videoToolsReady, ensureVideoTools
	t.Cleanup(func() { videoToolsReady = prevReady; ensureVideoTools = prevEnsure })

	videoToolsReady = func() bool { return false }
	ensureVideoTools = func() error { return errFakeInstall }

	s.startVideoToolInstall()
	waitFor(t, 2*time.Second, func() bool { return !s.toolInstallingNow() })

	if s.videoPlayerEnabled() {
		t.Fatal("video player must stay OFF after install failure")
	}
}
```

Add near the test: `var errFakeInstall = errors.New("install failed")` (import `errors`), and a small `waitFor` helper:

```go
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
```

If `newTestServer` does not exist, use the same construction other tests in `internal/server` use (search for `server.New(` / `New(cfg,` in `_test.go` files and mirror it).

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/server -run TestStartVideoToolInstall -count=1`
Expected: FAIL — undefined `startVideoToolInstall`, `videoToolsReady`, `ensureVideoTools`, `toolInstallingNow`.

- [ ] **Step 4: Implement the orchestration**

Create `internal/server/tool_install.go`:

```go
package server

import (
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

// Seams for tests.
var (
	videoToolsReady = video.ToolsReady
	ensureVideoTools = func() error {
		if _, err := video.EnsureFFmpeg(); err != nil {
			return err
		}
		_, err := video.EnsureFFprobe()
		return err
	}
)

func (s *Server) toolInstallingNow() bool {
	s.toolInstallMu.Lock()
	defer s.toolInstallMu.Unlock()
	return s.toolInstalling
}

// startVideoToolInstall ensures video tools in the background, then enables
// video player mode. On failure it leaves video player mode OFF. While video
// player mode is intended-on but tools are missing, it keeps retrying with
// backoff so transient failures self-heal. Idempotent: a second call while one
// is running is a no-op.
func (s *Server) startVideoToolInstall() {
	s.toolInstallMu.Lock()
	if s.toolInstalling {
		s.toolInstallMu.Unlock()
		return
	}
	s.toolInstalling = true
	s.toolInstallMu.Unlock()

	go func() {
		defer func() {
			s.toolInstallMu.Lock()
			s.toolInstalling = false
			s.toolInstallMu.Unlock()
		}()

		const maxRounds = 4
		for round := 0; round < maxRounds; round++ {
			if videoToolsReady() {
				s.commitVideoPlayerEnabled()
				return
			}
			if err := ensureVideoTools(); err == nil {
				s.commitVideoPlayerEnabled()
				return
			}
			time.Sleep(time.Duration(round+1) * 2 * time.Second)
		}
		// Exhausted: ensure the toggle is reverted to OFF.
		_ = settings.Update(func(a *settings.Settings) error {
			a.VideoPlayerEnabled = false
			a.MusicModeEnabled = false
			return nil
		})
	}()
}

// commitVideoPlayerEnabled persists video-player ON and runs the same
// side-effects the synchronous toggle did.
func (s *Server) commitVideoPlayerEnabled() {
	_ = settings.Update(func(a *settings.Settings) error {
		a.VideoPlayerEnabled = true
		return nil
	})
	if imagePath, current, ok := s.store.CurrentPath(); ok {
		s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
	}
	s.SyncOBSReceiver()
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/server -run TestStartVideoToolInstall -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

Stage `tool_install.go`, `server.go`, `tool_install_test.go`, commit:
`feat(server): async video-tool install with revert + self-heal`

---

## Task 7: Make the video-player toggle async + expose toolInstall in state

**Files:**
- Modify: `internal/server/server.go` (`handleVideoPlayer` ~1412, `state` ~1841, `stateWithMedia` ~1958)
- Test: `internal/server/tool_install_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Add to `internal/server/tool_install_test.go`:

```go
func TestVideoPlayerEnableAsyncWhenToolsMissing(t *testing.T) {
	s := newTestServer(t)
	prevReady, prevEnsure := videoToolsReady, ensureVideoTools
	t.Cleanup(func() { videoToolsReady = prevReady; ensureVideoTools = prevEnsure })
	videoToolsReady = func() bool { return false }
	blocked := make(chan struct{})
	ensureVideoTools = func() error { <-blocked; return nil }

	req := httptest.NewRequest(http.MethodPost, "/api/video-player", strings.NewReader(`{"enabled":true}`))
	rec := httptest.NewRecorder()
	s.handleVideoPlayer(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (async accepted)", rec.Code)
	}
	if s.videoPlayerEnabled() {
		t.Fatal("must not be enabled until install completes")
	}
	close(blocked)
}

func TestStateIncludesToolInstall(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	st := s.state(req)
	if _, ok := st["toolInstall"]; !ok {
		t.Fatal("state missing toolInstall")
	}
}
```

Add imports if missing: `net/http`, `net/http/httptest`, `strings`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server -run 'TestVideoPlayerEnableAsync|TestStateIncludesToolInstall' -count=1`
Expected: FAIL — handler still synchronous; `toolInstall` absent.

- [ ] **Step 3: Rewrite the enable branch of handleVideoPlayer**

Replace this block in `handleVideoPlayer`:

```go
		if req.Enabled {
			if _, err := video.EnsureFFmpeg(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.VideoPlayerEnabled = req.Enabled
			if !req.Enabled {
				appSettings.MusicModeEnabled = false
			}
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		if req.Enabled {
			if imagePath, current, ok := s.store.CurrentPath(); ok {
				s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
			}
		}
		s.SyncOBSReceiver()
		writeJSON(w, s.videoPlayerState())
```

with:

```go
		if req.Enabled && !videoToolsReady() {
			// Tools not ready: install in the background and return now.
			// The toggle stays OFF until install succeeds (or reverts on
			// failure). The UI shows progress via state.toolInstall.
			s.startVideoToolInstall()
			writeJSON(w, s.videoPlayerState())
			return
		}
		if err := settings.Update(func(appSettings *settings.Settings) error {
			appSettings.VideoPlayerEnabled = req.Enabled
			if !req.Enabled {
				appSettings.MusicModeEnabled = false
			}
			return nil
		}); err != nil {
			http.Error(w, "failed to save settings", http.StatusInternalServerError)
			return
		}
		if req.Enabled {
			if imagePath, current, ok := s.store.CurrentPath(); ok {
				s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
			}
		}
		s.SyncOBSReceiver()
		writeJSON(w, s.videoPlayerState())
```

- [ ] **Step 4: Add toolInstall to both state builders**

In `state(...)`, add to the returned map (next to `"ingest": s.ingestState(),`):

```go
		"toolInstall":     video.ToolInstallStatus(),
```

Add the identical entry to `stateWithMedia(...)`'s returned map.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/server -run 'TestVideoPlayer|TestState|TestMusicMode' -count=1`
Expected: PASS (existing music-mode/video-player tests still pass; the
already-ready path is unchanged).

- [ ] **Step 6: Build + server tests**

Run: `go build ./...` then `go test ./internal/server -count=1`
Expected: success / PASS.

- [ ] **Step 7: Commit**

Stage `server.go`, `tool_install_test.go`, commit:
`feat(server): async video-player toggle + toolInstall in /api/state`

---

## Task 8: Grayout overlay + progress bar in the UI

**Files:**
- Modify: `internal/server/ui.go` (CSS after `.progress-detail` ~436; HTML after `#dragDropOverlay` ~1011; JS in `applyState` after `updateMobileProgress(data);` ~1180)
- Test: `internal/server/ui_media_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/server/ui_media_test.go` (mirror the existing `fetch('/api/music-mode'` assertion style — it checks the served HTML string contains a substring):

```go
func TestUIContainsToolInstallOverlay(t *testing.T) {
	html := indexHTML // the served page constant/string used by other ui_*_test.go
	for _, want := range []string{`id="toolInstallOverlay"`, "data.toolInstall", "toolInstallFill"} {
		if !strings.Contains(html, want) {
			t.Fatalf("served HTML missing %q", want)
		}
	}
}
```

Use whatever identifier the other `ui_*_test.go` files use for the page HTML (search for `fetch('/api/music-mode'` to find the variable/const they assert against) and match it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server -run TestUIContainsToolInstallOverlay -count=1`
Expected: FAIL — markers absent.

- [ ] **Step 3: Add the overlay CSS**

In `ui.go`, immediately after the `.progress-detail { ... }` rule (~436), add:

```css
    .tool-install-overlay {
      position: fixed;
      inset: 0;
      z-index: 1000;
      display: none;
      align-items: center;
      justify-content: center;
      background: rgba(15, 28, 25, 0.55);
      backdrop-filter: blur(2px);
    }
    .tool-install-overlay.open { display: flex; }
    .tool-install-card {
      width: min(90%, 420px);
      display: grid;
      gap: 14px;
      padding: 24px;
      border-radius: 16px;
      background: #fff;
      box-shadow: 0 18px 50px rgba(0,0,0,0.25);
      text-align: center;
    }
    .tool-install-title { font-weight: 800; color: #21443d; }
    .tool-install-detail { color: var(--muted); font-size: 13px; font-weight: 700; }
    .tool-install-card.failed .tool-install-title { color: #b3261e; }
```

- [ ] **Step 4: Add the overlay HTML**

Immediately after the `#dragDropOverlay` closing `</div>` (~1011), add:

```html
  <div class="tool-install-overlay" id="toolInstallOverlay" role="status" aria-live="polite" aria-hidden="true">
    <div class="tool-install-card" id="toolInstallCard">
      <div class="tool-install-title" id="toolInstallTitle">必要なツールを準備しています…</div>
      <div class="progress-track" aria-label="インストール進捗">
        <div class="progress-fill" id="toolInstallFill" style="width:6%"></div>
      </div>
      <div class="tool-install-detail" id="toolInstallDetail"></div>
    </div>
  </div>
```

- [ ] **Step 5: Add the rendering function and call it**

In `applyState`, immediately after `updateMobileProgress(data);` add:

```js
      updateToolInstall(data.toolInstall);
```

Then add this function next to `updateMobileProgress` (search for `function updateMobileProgress(`):

```js
    function updateToolInstall(info) {
      const overlay = document.getElementById('toolInstallOverlay');
      const card = document.getElementById('toolInstallCard');
      const title = document.getElementById('toolInstallTitle');
      const fill = document.getElementById('toolInstallFill');
      const detail = document.getElementById('toolInstallDetail');
      if (!overlay) return;
      const active = !!(info && info.active);
      const failed = !!(info && info.failed);
      if (!active && !failed) {
        overlay.classList.remove('open');
        overlay.setAttribute('aria-hidden', 'true');
        card.classList.remove('failed');
        fill.classList.remove('indeterminate');
        return;
      }
      overlay.classList.add('open');
      overlay.setAttribute('aria-hidden', 'false');
      const toolLabel = { ffmpeg: 'FFmpeg', ffprobe: 'ffprobe', 'yt-dlp': 'yt-dlp' }[info.tool] || 'ツール';
      if (failed) {
        card.classList.add('failed');
        title.textContent = 'ツールの準備に失敗しました';
        detail.textContent = info.message || '時間をおいて再度お試しください。';
        fill.classList.remove('indeterminate');
        fill.style.width = '100%';
        return;
      }
      card.classList.remove('failed');
      const phaseLabel = { download: 'ダウンロード中', extract: '展開中', validate: '検証中' }[info.phase] || '準備中';
      title.textContent = toolLabel + ' を' + phaseLabel + '…';
      const pct = Math.max(0, Math.min(100, Number(info.percent || 0)));
      if (info.phase === 'download' && pct > 0) {
        fill.classList.remove('indeterminate');
        fill.style.width = Math.max(6, pct) + '%';
        detail.textContent = pct + '%';
      } else {
        fill.classList.add('indeterminate');
        detail.textContent = info.attempt > 1 ? ('再試行 ' + info.attempt + '回目') : '';
      }
    }
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./internal/server -run TestUIContainsToolInstallOverlay -count=1`
Expected: PASS.

- [ ] **Step 7: Build + full server tests**

Run: `go build ./...` then `go test ./internal/server -count=1`
Expected: success / PASS.

- [ ] **Step 8: Commit**

Stage `ui.go`, `ui_media_test.go`, commit:
`feat(ui): grayout overlay + progress bar for tool installation`

---

## Task 9: Startup validation + warm wiring

**Files:**
- Modify: `internal/app/app.go` (`run` ~58, near `go updateYTDLPOnStartup()` ~74)

- [ ] **Step 1: Add startup hooks**

In `run(...)`, immediately after `go updateYTDLPOnStartup()`, add:

```go
	go func() {
		video.ValidateInstalledTools()
		if appSettings, err := settings.Load(); err == nil && appSettings.VideoPlayerEnabled {
			if _, err := video.EnsureFFmpeg(); err != nil {
				log.Printf("startup ffmpeg warm failed: %v", err)
			}
			if _, err := video.EnsureFFprobe(); err != nil {
				log.Printf("startup ffprobe warm failed: %v", err)
			}
		}
	}()
```

Confirm `imagepadserver/internal/settings` is already imported in `app.go` (it is, used by `measureNetworkOnce`).

- [ ] **Step 2: Build + vet**

Run: `go build ./...` then `go vet ./internal/app/...`
Expected: success.

- [ ] **Step 3: Full test suite**

Run: `go test ./... -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

Stage `app.go`, commit: `feat(app): validate + warm video tools at startup`

---

## Task 10: Manual end-to-end install verification (safe, reversible)

**Goal:** Confirm a from-scratch install works through the new UI without touching system PATH binaries.

- [ ] **Step 1: Build the app**

Run: `go build -o dist/manual/imagepadserver.exe ./cmd/imagepadserver`
Expected: success.

- [ ] **Step 2: Remove ONLY the bundled binaries (reversible)**

Delete the contents of `%APPDATA%\ImagePadServer\bin\` (ffmpeg.exe, ffprobe.exe, yt-dlp.exe). Do NOT touch any system PATH ffmpeg. Reinstall re-downloads them.

PowerShell:
```powershell
Remove-Item "$env:APPDATA\ImagePadServer\bin\ffmpeg.exe","$env:APPDATA\ImagePadServer\bin\ffprobe.exe","$env:APPDATA\ImagePadServer\bin\yt-dlp.exe" -ErrorAction SilentlyContinue
```

- [ ] **Step 3: Launch and trigger install**

Run the built exe, open the UI, toggle video player mode ON.
Expected: the grayout overlay appears with FFmpeg download percent, then extract/validate, then the overlay disappears and video player mode is ON. `%APPDATA%\ImagePadServer\bin\` now contains ffmpeg.exe + ffprobe.exe.

- [ ] **Step 4: Verify PATH is ignored**

With a system `ffmpeg` on PATH but `%APPDATA%\ImagePadServer\bin\` emptied again, confirm the app still downloads its own copy (does not use the PATH one). The overlay must appear.

- [ ] **Step 5: Verify failure revert**

Temporarily set `IMAGEPAD_FFMPEG_SHA256` to a wrong value (forces checksum failure on the primary source) OR block network, toggle ON, and confirm: after retries/sources exhaust, the overlay shows the failure message and the video-player toggle returns to OFF (not stuck).

---

## Self-Review notes

- **Spec coverage:** A→Task1, B→Task2, C→Tasks3/4/5, D→Task6, E→Task7, F→Task8, G→Task9, verification→Task10. All spec sections mapped.
- **Type consistency:** `ToolInstallStatus` fields (Active/Tool/Phase/Percent/Attempt/Failed/Message) used identically in tracker (Task2), JSON state (Task7), and UI (`data.toolInstall.*`, Task8). Seams `videoToolsReady`/`ensureVideoTools` defined in Task6 and reused in Task7. `acquireFromSources(tool, sources, retries, attempt)` signature consistent between Task4 definition and tests.
- **Bundled-only invariant:** Task1 removes all `exec.LookPath`; Task10 step 4 verifies PATH is ignored at runtime.
