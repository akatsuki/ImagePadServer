# GUI Controller Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Split ImagePadServer's rebuilt management GUI into controller-sized JavaScript units, starting with PreviewController, without removing existing services or changing public API contracts.

**Architecture:** Keep the current Go template string composition, but move each major GUI region into a controller object with explicit `init`, `render`, and `reset` responsibilities. Start with preview because HLS.js lifetime, video playback, OBS preview, and hover behavior have produced the most regressions.

**Tech Stack:** Go template string constants, vanilla JavaScript, HLS.js, existing ImagePadServer HTTP APIs, Playwright/manual browser verification.

---

## Files and Responsibilities

- Create: `internal/server/ui_script_preview_controller.go`
  - Owns `#preview`, image/video/OBS/empty/progress rendering, HLS.js lifetime, same-origin preview URL normalization, and the custom video play button.
- Modify: `internal/server/ui_script_preview.go`
  - Keeps share URL display helpers only. Removes preview rendering/HLS resource code after controller migration.
- Modify: `internal/server/ui_scripts.go`
  - Adds `dashboardScriptPreviewController` after DOM refs and before state sync rendering uses it.
- Modify: `internal/server/ui_script_statesync.go`
  - Calls `PreviewController.render(data, context)` instead of `renderPreview(data, nextCurrentID)`.
- Modify: `internal/server/ui_css_components.go`
  - Keeps preview visual rules and `.preview-play-button` rules. Ensures hover transform cannot override centering.
- Modify: `internal/server/ui_media_test.go`
  - Adds regression checks for PreviewController, HLS.js priority, same-origin preview URL, play button, and no per-refresh HLS destruction.
- Modify later: `internal/server/ui_script_uploadevents.go`
  - In Phase 2, move upload event behavior into UploadController.
- Modify later: `internal/server/ui_script_historyqueue.go`
  - In Phase 3, move history/favorites/queue behavior into HistoryController.
- Modify later: `internal/server/ui_script_settings.go`
  - In Phase 4, move settings/QR/theme behavior into SettingsController.

## Phase 1: PreviewController

### Task 1: Add PreviewController Shell

**Files:**
- Create: `internal/server/ui_script_preview_controller.go`
- Modify: `internal/server/ui_scripts.go`
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Write the failing test**

Add this to `internal/server/ui_media_test.go`:

```go
func TestUIPreviewControllerIsWired(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`const PreviewController = (() => {`,
		`function initPreviewController(deps)`,
		`function renderPreviewController(data, context)`,
		`PreviewController.init({`,
		`PreviewController.render(data, {`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("PreviewController wiring missing %q", want)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/server -run TestUIPreviewControllerIsWired -timeout 120s
```

Expected: FAIL with `PreviewController wiring missing`.

- [x] **Step 3: Create controller shell**

Create `internal/server/ui_script_preview_controller.go`:

```go
package server

const dashboardScriptPreviewController = `
    const PreviewController = (() => {
      let deps = {};
      let mode = '';
      let mediaID = '';
      let mediaURL = '';

      function initPreviewController(nextDeps) {
        deps = nextDeps || {};
      }

      function renderPreviewController(data, context) {
        data = data || {};
        context = context || {};
        if (typeof renderPreview === 'function') {
          renderPreview(data, context.nextCurrentID || (data.current && data.current.id) || '');
        }
      }

      function resetPreviewController() {
        mode = '';
        mediaID = '';
        mediaURL = '';
      }

      return {
        init: initPreviewController,
        render: renderPreviewController,
        reset: resetPreviewController
      };
    })();
`
```

- [x] **Step 4: Wire the script aggregator**

Modify `internal/server/ui_scripts.go` so the preview controller string is included after `dashboardScriptPreview` and before scripts that may call it:

```go
const dashboardScripts = dashboardScriptBoot +
	dashboardScriptToast +
	dashboardScriptDOMRefs +
	dashboardScriptState +
	dashboardScriptPreview +
	dashboardScriptPreviewController +
	dashboardScriptHistoryQueue +
	dashboardScriptProgressStatus +
	dashboardScriptUploadState +
	dashboardScriptOBS +
	dashboardScriptUploadEvents +
	dashboardScriptSettings +
	dashboardScriptStateSync +
	dashboardScriptLiveSync
```

If the current order differs, keep all existing entries and insert only `dashboardScriptPreviewController` immediately after `dashboardScriptPreview`.

- [x] **Step 5: Initialize the controller**

Modify the boot/init area where DOM refs are already available. Add:

```js
    PreviewController.init({
      preview,
      showToast,
      scheduleRefresh
    });
```

If no central init block exists, add it near the existing startup calls in `ui_script_boot.go`.

- [x] **Step 6: Route state sync through the controller**

In `internal/server/ui_script_statesync.go`, replace:

```js
      renderPreview(data, nextCurrentID);
```

with:

```js
      PreviewController.render(data, {
        uploadMode,
        mediaIntent,
        localUploadActive,
        nextCurrentID
      });
```

- [x] **Step 7: Run test to verify it passes**

Run:

```powershell
go test ./internal/server -run TestUIPreviewControllerIsWired -timeout 120s
```

Expected: PASS.

### Task 2: Move HLS Resource Ownership Into PreviewController

**Files:**
- Modify: `internal/server/ui_script_preview_controller.go`
- Modify: `internal/server/ui_script_preview.go`
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Write the failing test**

Add this to `internal/server/ui_media_test.go`:

```go
func TestUIPreviewControllerOwnsHLSLifecycle(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`let previewHLS = null`,
		`function destroyPreviewHLS()`,
		`window.Hls && window.Hls.isSupported()`,
		`if (video.canPlayType('application/vnd.apple.mpegurl'))`,
		`PreviewController.render(data, {`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("PreviewController HLS lifecycle missing %q", want)
		}
	}
	hlsIndex := strings.Index(html, `window.Hls && window.Hls.isSupported()`)
	nativeIndex := strings.Index(html, `video.canPlayType('application/vnd.apple.mpegurl')`)
	if hlsIndex < 0 || nativeIndex < 0 || hlsIndex > nativeIndex {
		t.Fatal("HLS.js must be preferred before native HLS sniffing")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/server -run TestUIPreviewControllerOwnsHLSLifecycle -timeout 120s
```

Expected: FAIL until lifecycle functions are moved.

- [x] **Step 3: Add HLS lifecycle functions to controller**

In `ui_script_preview_controller.go`, replace the shell with:

```go
package server

const dashboardScriptPreviewController = `
    const PreviewController = (() => {
      let deps = {};
      let mode = '';
      let mediaID = '';
      let mediaURL = '';
      let previewHLS = null;

      function initPreviewController(nextDeps) {
        deps = nextDeps || {};
      }

      function destroyPreviewHLS() {
        if (!previewHLS) return;
        try {
          previewHLS.destroy();
        } catch (error) {
        }
        previewHLS = null;
      }

      function attachPreviewHLS(video, src) {
        destroyPreviewHLS();
        if (window.Hls && window.Hls.isSupported()) {
          previewHLS = new window.Hls({
            lowLatencyMode: true,
            backBufferLength: 30
          });
          previewHLS.loadSource(src);
          previewHLS.attachMedia(video);
          return true;
        }
        if (video.canPlayType('application/vnd.apple.mpegurl')) {
          video.src = src;
          return true;
        }
        return false;
      }

      function releaseIfLeavingVideo(nextMode) {
        if (nextMode !== mode) {
          destroyPreviewHLS();
        }
      }

      function renderPreviewController(data, context) {
        data = data || {};
        context = context || {};
        if (typeof renderPreview === 'function') {
          renderPreview(data, context.nextCurrentID || (data.current && data.current.id) || '');
        }
      }

      function resetPreviewController() {
        destroyPreviewHLS();
        mode = '';
        mediaID = '';
        mediaURL = '';
      }

      return {
        init: initPreviewController,
        render: renderPreviewController,
        reset: resetPreviewController
      };
    })();
`
```

- [x] **Step 4: Remove old duplicated HLS ownership from preview script**

In `internal/server/ui_script_preview.go`, keep share URL helpers. Move or delete old `destroyOBSPreviewHLS`, `attachHLSPreview`, and HLS state that belongs to normal preview only. Keep OBS-specific functions until Task 4 if they are still needed by OBS preview.

- [x] **Step 5: Run target test**

Run:

```powershell
go test ./internal/server -run TestUIPreviewControllerOwnsHLSLifecycle -timeout 120s
```

Expected: PASS.

### Task 3: Move Normal Video Rendering Into PreviewController

**Files:**
- Modify: `internal/server/ui_script_preview_controller.go`
- Modify: `internal/server/ui_script_preview.go`
- Modify: `internal/server/ui_css_components.go`
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Write the failing test**

Add this to `internal/server/ui_media_test.go`:

```go
func TestUIPreviewControllerRendersPlayableVideo(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function sameOriginPreviewURL(value)`,
		`function mediaPreviewURL(data)`,
		`function addVideoPlayButton(video)`,
		`await video.play()`,
		`preview-play-button`,
		`state.previewVideoURL`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("playable video preview missing %q", want)
		}
	}
	if strings.Contains(html, `video.muted = true;
          video.preload = 'metadata';`) {
		t.Fatal("regular video preview must not force mute before user playback")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/server -run TestUIPreviewControllerRendersPlayableVideo -timeout 120s
```

Expected: FAIL until video rendering is moved.

- [x] **Step 3: Implement same-origin URL helper**

Add to controller:

```js
      function sameOriginPreviewURL(value) {
        if (!value) return '';
        try {
          const parsed = new URL(value, window.location.href);
          const params = new URLSearchParams(parsed.search);
          const pageToken = new URLSearchParams(window.location.search).get('token');
          if (pageToken && !params.has('token')) params.set('token', pageToken);
          const query = params.toString();
          return parsed.pathname + (query ? '?' + query : '');
        } catch (error) {
          return value;
        }
      }

      function mediaPreviewURL(data) {
        return sameOriginPreviewURL(data.hlsURL || data.publicHLSURL || data.videoURL || data.publicVideoURL || '');
      }
```

- [x] **Step 4: Implement play button helper**

Add to controller:

```js
      function videoPlayIcon(paused) {
        if (paused) {
          return '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M8 5v14l11-7Z"/></svg>';
        }
        return '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M7 5h4v14H7Zm6 0h4v14h-4Z"/></svg>';
      }

      function addVideoPlayButton(video) {
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'preview-play-button';
        button.setAttribute('aria-label', '動画を再生');
        const sync = () => {
          const paused = video.paused;
          button.classList.toggle('playing', !paused);
          button.setAttribute('aria-label', paused ? '動画を再生' : '動画を一時停止');
          button.innerHTML = videoPlayIcon(paused);
        };
        button.addEventListener('click', async (event) => {
          event.preventDefault();
          event.stopPropagation();
          try {
            if (video.paused) {
              await video.play();
            } else {
              video.pause();
            }
          } catch (error) {
            if (deps.showToast) {
              deps.showToast('動画を再生できませんでした: ' + (error && error.message ? error.message : error), { error: true });
            }
          }
          sync();
        });
        video.addEventListener('play', sync);
        video.addEventListener('pause', sync);
        video.addEventListener('ended', sync);
        sync();
        deps.preview.appendChild(button);
      }
```

- [x] **Step 5: Implement video render path**

Add to controller:

```js
      function renderVideo(data, nextCurrentID) {
        const preview = deps.preview;
        const videoPreviewURL = mediaPreviewURL(data);
        const existingVideo = preview.querySelector('video');
        if (videoPreviewURL && (mode !== 'video' || nextCurrentID !== mediaID || mediaURL !== videoPreviewURL || !existingVideo)) {
          preview.classList.remove('obs-preview');
          preview.innerHTML = '';
          const video = document.createElement('video');
          video.controls = true;
          video.playsInline = true;
          video.preload = 'metadata';
          let videoReady = false;
          if (String(videoPreviewURL).includes('.m3u8')) {
            videoReady = attachPreviewHLS(video, videoPreviewURL);
          } else {
            video.src = videoPreviewURL;
            videoReady = true;
          }
          if (videoReady) {
            preview.appendChild(video);
            addVideoPlayButton(video);
            mode = 'video';
            mediaID = nextCurrentID;
            mediaURL = videoPreviewURL;
            state.previewVideoURL = videoPreviewURL;
          } else {
            preview.innerHTML = '<div class="empty">HLSプレビューを準備中です</div>';
            mode = 'video-waiting';
            mediaURL = '';
            state.previewVideoURL = '';
            if (deps.scheduleRefresh) deps.scheduleRefresh(500);
          }
        } else if (!videoPreviewURL && mode !== 'video-empty') {
          destroyPreviewHLS();
          preview.innerHTML = '<div class="empty">動画URLを準備中です</div>';
          mode = 'video-empty';
          mediaURL = '';
          state.previewVideoURL = '';
        }
      }
```

- [x] **Step 6: Update CSS for stable play button**

Ensure `internal/server/ui_css_components.go` contains:

```css
    .preview {
      position: relative;
    }
    .preview-play-button {
      position: absolute;
      left: 50%;
      top: 50%;
      z-index: 2;
      width: 58px;
      min-width: 58px;
      height: 58px;
      min-height: 58px;
      padding: 0;
      border-radius: 50%;
      background: rgba(238, 238, 238, .72);
      color: rgba(0, 0, 0, .82);
      border: 1px solid rgba(255, 255, 255, .46);
      box-shadow: 0 8px 22px rgba(0, 0, 0, .16);
      transform: translate(-50%, -50%);
      backdrop-filter: blur(8px);
      transition: background-color .12s ease, opacity .12s ease;
    }
    .preview-play-button.playing,
    .preview-play-button.playing:hover,
    .preview-play-button:hover,
    .preview-play-button:focus-visible {
      transform: translate(-50%, -50%);
    }
```

- [x] **Step 7: Run target test**

Run:

```powershell
go test ./internal/server -run 'TestUIPreviewControllerRendersPlayableVideo|TestUIRendersVideoPreviewElement' -timeout 120s
```

Expected: PASS.

### Task 4: Move Image, Empty, Progress, and OBS Preview Rendering

**Files:**
- Modify: `internal/server/ui_script_preview_controller.go`
- Modify: `internal/server/ui_script_preview.go`
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Write the failing test**

Add this to `internal/server/ui_media_test.go`:

```go
func TestUIPreviewControllerOwnsAllPreviewModes(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function renderImage(data, nextCurrentID)`,
		`function renderEmpty()`,
		`function renderIngestProgress(data)`,
		`function renderVideoProgress(data)`,
		`function renderOBSPreview(data, context)`,
		`preview.classList.add('obs-preview')`,
		`preview.classList.remove('obs-preview')`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("PreviewController mode rendering missing %q", want)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run:

```powershell
go test ./internal/server -run TestUIPreviewControllerOwnsAllPreviewModes -timeout 120s
```

Expected: FAIL until all mode functions exist.

- [x] **Step 3: Add mode routing**

Replace `renderPreviewController` with:

```js
      function renderPreviewController(data, context) {
        data = data || {};
        context = context || {};
        const nextCurrentID = context.nextCurrentID || (data.current && data.current.id) || '';
        if (context.uploadMode === 'obs' && data.obs && (data.obs.connected || data.obs.publishing)) {
          renderOBSPreview(data, context);
          return;
        }
        deps.preview.classList.remove('obs-preview');
        if (context.localUploadActive) {
          releaseIfLeavingVideo('local-upload');
          return;
        }
        if (data.ingest && data.ingest.active) {
          renderIngestProgress(data);
          return;
        }
        if (!data.current) {
          renderEmpty();
          return;
        }
        if (data.video && data.video.active) {
          renderVideoProgress(data);
          return;
        }
        if (data.current.kind === 'video') {
          renderVideo(data, nextCurrentID);
          return;
        }
        renderImage(data, nextCurrentID);
      }
```

- [x] **Step 4: Move existing rendering bodies**

Move the existing logic from `renderPreview` and `renderIngestPreview` into these controller functions:

```js
      function renderEmpty() {
        releaseIfLeavingVideo('empty');
        if (mode !== 'empty') {
          deps.preview.innerHTML = '<div class="empty">まだ画像が選択されていません</div>';
          mode = 'empty';
        }
      }
```

```js
      function renderImage(data, nextCurrentID) {
        releaseIfLeavingVideo('image');
        const existingImage = deps.preview.querySelector('img');
        if (mode !== 'image' || nextCurrentID !== mediaID || !existingImage) {
          deps.preview.innerHTML = '';
          const img = document.createElement('img');
          const previewImageURL = nextCurrentID ? '/image/current?v=' + encodeURIComponent(nextCurrentID) : data.previewImageURL;
          img.src = previewImageURL + (previewImageURL.includes('?') ? '&' : '?') + 'preview=1';
          img.alt = '現在公開中の画像';
          deps.preview.appendChild(img);
          mode = 'image';
          mediaID = nextCurrentID;
        }
      }
```

For progress and OBS, copy the exact current HTML strings from `ui_script_preview.go` so visible text stays unchanged.

- [x] **Step 5: Leave compatibility wrapper**

Keep this in `ui_script_preview.go` temporarily:

```js
    function renderPreview(data, nextCurrentID) {
      PreviewController.render(data, {
        uploadMode,
        mediaIntent,
        localUploadActive,
        nextCurrentID
      });
    }
```

- [x] **Step 6: Run target test**

Run:

```powershell
go test ./internal/server -run 'TestUIPreviewControllerOwnsAllPreviewModes|TestUIRendersIngestPhase|TestOBSConnectionDetailsUIAndUnifiedRTSPURL' -timeout 120s
```

Expected: PASS.

### Task 5: Browser Verify PreviewController

**Files:**
- No source edits unless verification fails.

- [x] **Step 1: Build**

Run:

```powershell
go build -o (Join-Path $env:TEMP 'imagepadserver-codex-gui-latest.exe') ./cmd/imagepadserver
```

Expected: command exits 0.

- [x] **Step 2: Restart local app**

Run:

```powershell
$ports = Get-NetTCPConnection -LocalPort 8819 -State Listen -ErrorAction SilentlyContinue
foreach ($p in $ports) { Stop-Process -Id $p.OwningProcess -Force -ErrorAction SilentlyContinue }
Start-Sleep -Milliseconds 500
$env:IMAGEPAD_PORT = "8819"
$out = Join-Path $env:TEMP "imagepadserver-codex-gui-latest.exe"
Start-Process -FilePath $out -WorkingDirectory (Get-Location) -WindowStyle Hidden
Start-Sleep -Seconds 5
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8819/healthz -TimeoutSec 5
```

Expected: response content contains `ok`.

- [x] **Step 3: Verify video playback in browser**

Use Playwright or the in-app browser:

```js
await page.goto('http://127.0.0.1:8819/', { waitUntil: 'domcontentloaded' });
```

Then publish a saved video from history and evaluate:

```js
const result = await page.evaluate(async () => {
  const video = document.querySelector('#preview video');
  const button = document.querySelector('#preview .preview-play-button');
  if (!video || !button) return { ok: false, reason: 'missing video or play button' };
  const before = video.currentTime;
  button.click();
  await new Promise(resolve => setTimeout(resolve, 3000));
  return {
    ok: !video.paused && video.currentTime > before + 1,
    paused: video.paused,
    before,
    after: video.currentTime,
    readyState: video.readyState,
    error: video.error ? { code: video.error.code, message: video.error.message } : null
  };
});
```

Expected: `ok: true`, `readyState >= 3`, `error: null`.

- [x] **Step 4: Verify hover position stability**

Evaluate:

```js
const before = await page.evaluate(() => {
  const button = document.querySelector('#preview .preview-play-button');
  const r = button.getBoundingClientRect();
  return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) };
});
await page.mouse.move(before.x + before.w / 2, before.y + before.h / 2);
await page.waitForTimeout(300);
const after = await page.evaluate(() => {
  const button = document.querySelector('#preview .preview-play-button');
  const r = button.getBoundingClientRect();
  return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) };
});
```

Expected: `before` and `after` are identical.

## Phase 2: UploadController

### Task 6: Introduce UploadController Shell

**Files:**
- Create: `internal/server/ui_script_upload_controller.go`
- Modify: `internal/server/ui_scripts.go`
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Write failing test**

```go
func TestUIUploadControllerIsWired(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`const UploadController = (() => {`,
		`function initUploadController(deps)`,
		`function setUploadModeController(mode)`,
		`function setMediaIntentController(intent)`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("UploadController wiring missing %q", want)
		}
	}
}
```

- [x] **Step 2: Create controller shell**

Create:

```go
package server

const dashboardScriptUploadController = `
    const UploadController = (() => {
      let deps = {};
      function initUploadController(nextDeps) {
        deps = nextDeps || {};
      }
      function setUploadModeController(mode) {
        setUploadMode(mode);
      }
      function setMediaIntentController(intent) {
        setMediaIntent(intent);
      }
      return {
        init: initUploadController,
        setUploadMode: setUploadModeController,
        setMediaIntent: setMediaIntentController
      };
    })();
`
```

- [x] **Step 3: Wire aggregator and init**

Insert `dashboardScriptUploadController` after `dashboardScriptUploadState` in `ui_scripts.go`, then initialize with file input, upload button, and mode controls.

- [x] **Step 4: Run test**

```powershell
go test ./internal/server -run TestUIUploadControllerIsWired -timeout 120s
```

Expected: PASS.

### Task 7: Move Upload Progress Rendering

**Files:**
- Modify: `internal/server/ui_script_upload_controller.go`
- Modify: `internal/server/ui_script_uploadevents.go`
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Write failing test**

```go
func TestUIUploadControllerOwnsUploadProgress(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function beginLocalUploadProgress(file)`,
		`function updateLocalUploadProgress(loaded, total)`,
		`function finishLocalUploadProgress()`,
		`localUploadActive = true`,
		`localUploadActive = false`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("UploadController progress behavior missing %q", want)
		}
	}
}
```

- [x] **Step 2: Move progress state into UploadController**

Move the current XHR progress and flicker guards from `ui_script_uploadevents.go` into UploadController functions. Keep the public wrappers with existing names if other files call them.

- [x] **Step 3: Verify no preview flicker**

Run:

```powershell
go test ./internal/server -run 'TestUIUploadControllerOwnsUploadProgress|TestUIRendersIngestPhase' -timeout 120s
```

Expected: PASS.

## Phase 3: HistoryController

### Task 8: Introduce HistoryController

**Files:**
- Create: `internal/server/ui_script_history_controller.go`
- Modify: `internal/server/ui_scripts.go`
- Modify: `internal/server/ui_script_historyqueue.go`
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Write failing test**

```go
func TestUIHistoryControllerIsWired(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`const HistoryController = (() => {`,
		`function renderHistoryController(data)`,
		`function publishHistoryItem(id)`,
		`function toggleFavoriteHistoryItem(id)`,
		`function queueHistoryItem(id)`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("HistoryController wiring missing %q", want)
		}
	}
}
```

- [x] **Step 2: Create controller shell and wrappers**

Create a controller that calls existing `renderHistory`, `selectHistory`, favorite, and queue functions first. Then migrate function bodies in small follow-up steps.

- [x] **Step 3: Preserve OBS recording behavior**

Keep this regression in `ui_media_test.go`:

```go
if strings.Contains(html, `if (mode === 'obs') {
        setUploadMode('obs');`) {
	t.Fatal("history publish must not switch saved OBS recordings back into live OBS mode")
}
```

- [x] **Step 4: Run test**

```powershell
go test ./internal/server -run 'TestUIHistoryControllerIsWired|TestUIHistoryPublishNavigatesToSourceMode' -timeout 120s
```

Expected: PASS.

## Phase 4: SettingsController

### Task 9: Introduce SettingsController

**Files:**
- Create: `internal/server/ui_script_settings_controller.go`
- Modify: `internal/server/ui_scripts.go`
- Modify: `internal/server/ui_script_settings.go`
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Write failing test**

```go
func TestUISettingsControllerIsWired(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`const SettingsController = (() => {`,
		`function openSettingsController()`,
		`function openPhoneConnectController()`,
		`function applyThemePreferenceController`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("SettingsController wiring missing %q", want)
		}
	}
}
```

- [x] **Step 2: Create controller shell**

Create a controller that wraps existing settings/QR/theme functions without changing behavior.

- [x] **Step 3: Run test**

```powershell
go test ./internal/server -run 'TestUISettingsControllerIsWired|TestUIThemePreferenceSupportsSystemLightDark' -timeout 120s
```

Expected: PASS.

## Phase 5: Cleanup and Verification

### Task 10: Remove Compatibility Wrappers That Are No Longer Used

**Files:**
- Modify: all `internal/server/ui_script_*controller.go`
- Modify: legacy source files after call sites are migrated
- Test: `internal/server/ui_media_test.go`

- [x] **Step 1: Search for wrapper usage**

Run:

```powershell
rg -n "renderPreview\\(|setUploadMode\\(|renderHistory\\(|openSettings" internal/server
```

Expected: only intended wrappers remain.

- [x] **Step 2: Remove wrappers one by one**

Delete a wrapper only when no call site needs it. After each deletion run the targeted test that covers that controller.

- [x] **Step 3: Full tests**

Run:

```powershell
go test ./internal/server -timeout 120s
go test ./internal/app ./internal/obsrtmp ./internal/video -timeout 120s
```

Expected: PASS.

- [x] **Step 4: Browser smoke**

Verify these flows:

- Static image publish shows image preview
- Saved video publish shows video and custom play button
- Video play button advances time
- Play button hover does not move
- OBS mode still shows OBS connection state and preview when available
- File upload progress appears below the step 3 publish buttons during upload, not inside the preview box
- Settings modal opens, theme changes, and closes
- Phone QR modal opens and closes

## Completion Criteria

- Controller files exist and are included in `ui_scripts.go`
- PreviewController owns all `#preview` rendering and HLS.js lifetime
- UploadController shell exists and upload progress is isolated
- HistoryController shell exists and history publish regression remains covered
- SettingsController shell exists and theme/QR behavior remains covered
- `go test ./internal/server -timeout 120s` passes
- `go test ./internal/app ./internal/obsrtmp ./internal/video -timeout 120s` passes
- Browser smoke confirms video playback and hover stability

