# Ingest Phase Progress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show the current processing phase (downloading / analyzing) in the UI for all ingest paths (music, page-video, direct media, uploads), so a long synchronous download/analyze is distinguishable from a freeze.

**Architecture:** The ingest handlers run synchronously in their own request goroutine, while the UI polls `/api/state` on a separate goroutine. So no async refactor is needed: handlers write a shared, mutex-guarded "ingest phase" before each blocking step, `/api/state` exposes it, and the existing adaptive poll renders it live. Render progress is already shown via `data.video`, so this only covers the previously-invisible download/analyze gap.

**Tech Stack:** Go, net/http, embedded HTML/JS, Go tests.

---

### Task 1: Ingest phase store + `/api/state` field

**Files:**
- Create: `internal/server/ingest_status.go`
- Modify: `internal/server/server.go:1923` (add `"ingest"` to the state map)
- Test: `internal/server/ingest_status_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/server/ingest_status_test.go`:

```go
package server

import "testing"

func TestIngestPhaseLifecycle(t *testing.T) {
	s := &Server{}

	if got := s.ingestState(); got["active"] != false || got["phase"] != "" {
		t.Fatalf("initial: got %#v, want inactive empty", got)
	}

	s.setIngest(ingestDownloading, "My Track")
	got := s.ingestState()
	if got["active"] != true || got["phase"] != "downloading" || got["title"] != "My Track" {
		t.Fatalf("after set: got %#v", got)
	}

	s.setIngest(ingestAnalyzing, "My Track")
	if s.ingestState()["phase"] != "analyzing" {
		t.Fatalf("after bump: %#v", s.ingestState())
	}

	s.clearIngest()
	if got := s.ingestState(); got["active"] != false || got["phase"] != "" {
		t.Fatalf("after clear: got %#v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server -run TestIngestPhaseLifecycle -v`
Expected: FAIL — `ingestState`, `setIngest`, `clearIngest`, `ingestDownloading`, `ingestAnalyzing` undefined.

- [ ] **Step 3: Implement the store**

Create `internal/server/ingest_status.go`:

```go
package server

import "sync"

// Ingest phase identifiers surfaced to the UI for the synchronous
// download/analyze portion of media ingest (render progress is reported
// separately via the video player state).
const (
	ingestDownloading = "downloading"
	ingestAnalyzing   = "analyzing"
	ingestProcessing  = "processing"
)

type ingestStatus struct {
	mu     sync.Mutex
	active bool
	phase  string
	title  string
}

func (s *Server) setIngest(phase, title string) {
	s.ingest.mu.Lock()
	s.ingest.active = true
	s.ingest.phase = phase
	s.ingest.title = title
	s.ingest.mu.Unlock()
}

func (s *Server) clearIngest() {
	s.ingest.mu.Lock()
	s.ingest.active = false
	s.ingest.phase = ""
	s.ingest.title = ""
	s.ingest.mu.Unlock()
}

func (s *Server) ingestState() map[string]interface{} {
	s.ingest.mu.Lock()
	defer s.ingest.mu.Unlock()
	return map[string]interface{}{
		"active": s.ingest.active,
		"phase":  s.ingest.phase,
		"title":  s.ingest.title,
	}
}
```

Add the field to the `Server` struct in `internal/server/server.go` (after `relayNonces`):

```go
	relayNonces     map[string]time.Time
	ingest          ingestStatus
```

Add the key to the state map in `stateWithMedia` (`internal/server/server.go:1923`), alongside `"videoQueue"`:

```go
		"videoQueue":      s.videoQueueState(),
		"ingest":          s.ingestState(),
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server -run TestIngestPhaseLifecycle -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/ingest_status.go internal/server/ingest_status_test.go internal/server/server.go
git commit -m "feat: add ingest phase status to server state"
```

---

### Task 2: Report phases from the ingest handlers

**Files:**
- Modify: `internal/server/server.go` (the `/api/upload-url` and `/api/upload-url-queue` handlers — music, page-video, and direct-media branches)
- Modify: `internal/server/audio_ingest.go` (`processAudioFileAndPublish`, `processAudioFileAndQueue` — set analyzing before `AnalyzeAudioForKind`)
- Test: `internal/server/music_mode_test.go`

The handler goroutine sets `downloading` before the blocking download and clears on return; the shared audio funcs bump to `analyzing` right before analysis.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/music_mode_test.go` a test that the music acquirer path records a phase. Use the existing injectable `musicURLAcquirer` to block mid-download and assert `/api/state` reports `downloading` while it runs:

```go
func TestUploadURLReportsDownloadingPhase(t *testing.T) {
	_, mux := testServer(t, true)
	if err := settings.Update(func(s *settings.Settings) error {
		s.MusicModeEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	oldMusic := musicURLAcquirer
	defer func() { musicURLAcquirer = oldMusic }()
	release := make(chan struct{})
	reached := make(chan struct{})
	musicURLAcquirer = func(context.Context, *Server, string) (video.AcquiredAudio, error) {
		close(reached)
		<-release
		return video.AcquiredAudio{}, errors.New("stop here")
	}

	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.youtube.com/watch?v=test"}`))
		adminJSON(t, mux, req)
	}()

	<-reached
	stateReq := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := adminJSON(t, mux, stateReq)
	close(release)

	var st map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	ingest, _ := st["ingest"].(map[string]interface{})
	if ingest == nil || ingest["active"] != true || ingest["phase"] != "downloading" {
		t.Fatalf("ingest phase = %#v, want downloading/active", ingest)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server -run TestUploadURLReportsDownloadingPhase -v`
Expected: FAIL — `ingest.active` is false because no handler sets the phase.

- [ ] **Step 3: Set phases in the handlers**

In `internal/server/server.go`, in the `/api/upload-url` handler, immediately after the request URL is parsed and before the routing branches (music / page / direct), add:

```go
		s.setIngest(ingestDownloading, req.URL)
		defer s.clearIngest()
```

Apply the same two lines at the same point in the `/api/upload-url-queue` handler.

In `internal/server/audio_ingest.go`, set the analyzing phase right before each analysis call. In `processAudioFileAndPublish` change:

```go
	analysis, err := video.AnalyzeAudioForKind(r.Context(), ffmpegPath, acquired.SourcePath, acquired.Kind)
```

to:

```go
	s.setIngest(ingestAnalyzing, meta.Title)
	analysis, err := video.AnalyzeAudioForKind(r.Context(), ffmpegPath, acquired.SourcePath, acquired.Kind)
```

Make the identical change in `processAudioFileAndQueue` (before its `AnalyzeAudioForKind` call). `meta` is already in scope in both functions.

(The `defer s.clearIngest()` in the handler clears the phase once the synchronous work returns; the background render continues to report through `data.video`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server -run TestUploadURLReportsDownloadingPhase -v`
Expected: PASS

- [ ] **Step 5: Run the focused suite**

Run: `go test ./internal/server -run 'Music|UploadURL|Ingest' -v`
Expected: PASS (existing routing tests unaffected).

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go internal/server/audio_ingest.go internal/server/music_mode_test.go
git commit -m "feat: report downloading/analyzing phases during ingest"
```

---

### Task 3: Show the phase in the UI

**Files:**
- Modify: `internal/server/ui.go` (`applyState` rendering near line 1135; the adaptive poll at line 1169)
- Test: `internal/server/ui_media_test.go`

- [ ] **Step 1: Write the failing test**

`ui_media_test.go` asserts on static UI HTML/JS strings. Add a test requiring the ingest phase labels and element to exist in the served page:

```go
func TestUIRendersIngestPhase(t *testing.T) {
	page := renderUIForTest(t) // use the same helper other ui_media_test.go tests use
	for _, want := range []string{"ingestPhase", "ダウンロード中", "解析中"} {
		if !strings.Contains(page, want) {
			t.Errorf("UI page missing %q", want)
		}
	}
}
```

If `ui_media_test.go` uses a different helper to obtain the page HTML, mirror that existing pattern instead of `renderUIForTest`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server -run TestUIRendersIngestPhase -v`
Expected: FAIL — the strings are not in the page yet.

- [ ] **Step 3: Add the UI element + rendering**

In `internal/server/ui.go`, add a status line element near the upload progress block (after the `mobileProgressText` block around line 938):

```html
<div id="ingestPhase" class="ingest-phase" hidden></div>
```

In `applyState` (around line 1135), add rendering logic:

```javascript
      const ingest = data.ingest || {};
      const ingestEl = document.getElementById('ingestPhase');
      if (ingestEl) {
        const labels = { downloading: 'ダウンロード中…', analyzing: '解析中…', processing: '処理中…' };
        if (ingest.active && labels[ingest.phase]) {
          ingestEl.textContent = labels[ingest.phase] + (ingest.title ? ' — ' + ingest.title : '');
          ingestEl.hidden = false;
        } else {
          ingestEl.hidden = true;
        }
      }
```

Update the adaptive poll interval (line 1169) so ingest activity also triggers fast polling. Change:

```javascript
scheduleRefresh((data.video && data.video.active) || (data.obs && data.obs.connected) ? <fast> : <slow>);
```

to also check `(data.ingest && data.ingest.active)`:

```javascript
scheduleRefresh((data.ingest && data.ingest.active) || (data.video && data.video.active) || (data.obs && data.obs.connected) ? <fast> : <slow>);
```

(Keep the existing `<fast>`/`<slow>` literal values that are already in that line.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server -run TestUIRendersIngestPhase -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/ui.go internal/server/ui_media_test.go
git commit -m "feat: show ingest phase (downloading/analyzing) in the UI"
```

---

### Task 4: Verify

**Files:** none

- [ ] **Step 1: Full suites + build**

Run: `go test ./internal/server ./internal/video && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 2: Manual smoke**

Publish a long music URL; confirm the UI shows "ダウンロード中…" then "解析中…" during the previously-frozen gap, then the existing "変換中" render progress, then completion.

---

### Self-review

- **Spec coverage:** phase status store + state field (Task 1); phases reported for URL ingest paths + analysis (Task 2); UI display + fast poll (Task 3); verification (Task 4). "Non-music too" is covered because `setIngest(downloading)` is in the shared upload-url/queue handlers (all URL paths) and analyzing is in the shared audio funcs; uploads/page-video flow through the same handlers.
- **Placeholder scan:** the UI test helper name (`renderUIForTest`) is flagged to match the existing `ui_media_test.go` pattern; the poll `<fast>`/`<slow>` literals are intentionally preserved from the existing line rather than invented.
- **Type consistency:** `setIngest`/`clearIngest`/`ingestState` and constants `ingestDownloading`/`ingestAnalyzing`/`ingestProcessing` are used identically across tasks; the state key is `"ingest"` everywhere; the JS reads `data.ingest.{active,phase,title}` matching the Go map keys.
- **Risk:** no async refactor and no change to `watchConversion`; the only behavioral change is shared-status writes guarded by a dedicated mutex (no `s.mu` contention).
