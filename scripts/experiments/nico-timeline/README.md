# Nico timeline reference capture

This directory documents the T0 baseline for the comment timeline compositor. The checked-in input catalog is [`internal/nicorender/testdata/timeline/fixtures.json`](../../../internal/nicorender/testdata/timeline/fixtures.json). Small, synthetic snapshots live in that catalog. The real `sm9` snapshot and six-second source video stay outside the repository; the catalog pins their SHA-256 values.

## Fixture coverage

The catalog covers ordinary `naka`, `ue`, and `shita` comments; identical text/style reuse; an actual `nico:opacity:0.5` A→B→A overlap; multiline, color, size, and long text; negative timestamps; pre-roll comments; 4:3 and square geometry; empty input; comments outside the six-second interval; and explicit unsupported-feature markers for Flash, dynamic reverse NicoScript, decorations, and comment limits.

Unsupported markers are classification inputs for the later eligibility gate. They do not claim that the current `Snapshot` API can express Flash playback, active NicoScript ranges, dynamic decoration, or renderer configuration. The overlap case does use the current comment command parser to set half opacity.

## Fast checks

These checks use only checked-in fixtures and do not start a browser or FFmpeg:

```powershell
rtk go test ./internal/nicorender -run '^TestTimelineReference(FixtureCatalog|Clock)$' -count=1
```

The clock vectors retain the renderer’s centisecond conversion, including floor for negative timestamps. `0, 33, 66, 100 ms` maps to `0, 3, 6, 10` at 30 fps; frame `-1` maps to `-4`. The rational `60000/1001` vectors are also checked.

## Capture all reference frames

The browser test is opt-in. Without `NICO_TIMELINE_INTEGRATION=1`, Go reports it as skipped. With opt-in set, every missing tool/input, hash mismatch, Canvas2D fallback, or short capture is a test failure. It requires Chrome or Edge with WebGL available and saves a lossless gzip stream of top-left premultiplied RGBA8 pixels plus a JSON manifest containing each frame’s vpos, actual renderer draw-call order, raw frame hash, and the environment hashes/versions.

The 12-second capture contains 360 frames. Its first 180 frames are also the exact six-second 1080p30 reference prefix. This avoids capturing the same initial interval twice. The capture does not include video decode or encode; FFmpeg and the source video are recorded and hashed for the later end-to-end comparison.

Example PowerShell setup (replace paths if the local copies move):

```powershell
$env:NICO_TIMELINE_INTEGRATION = '1'
$env:NICO_TIMELINE_SNAPSHOT = 'C:\Users\masah\AppData\Local\Temp\imagepad-nico-perf-20260916\snapshot.json'
$env:NICO_TIMELINE_SOURCE = 'C:\Users\masah\AppData\Local\Temp\imagepad-nico-perf-20260916\source-6s.mp4'
$env:NICO_TIMELINE_FFMPEG = 'C:\Users\masah\OneDrive\ドキュメント\GitHub\ImagePadServer\build\nico-native-integration\tools\ffmpeg.exe'
$env:NICO_TIMELINE_BROWSER = 'C:\Program Files\Google\Chrome\Application\chrome.exe'
$env:NICO_TIMELINE_ARTIFACTS = 'C:\Users\masah\AppData\Local\Temp\imagepad-nico-timeline-t0-<new-id>'
$env:IMAGEPAD_NICONICO_RENDER_GPU = 'enabled'
rtk go test ./internal/nicorender -run '^TestCaptureTimelineReferences$' -count=1 -v
```

The artifact directory must be outside the repository and use a fresh name. The test creates files exclusively, so it will not replace earlier evidence. It never copies the source or snapshot into the checkout. Expected input hashes are:

| Input | SHA-256 |
|---|---|
| `sm9` snapshot, 362 comments | `b7be86d86016175c99925f5f1b5651d43279eeffcc54deb1c22c5b2e6e15e946` |
| Six-second source video | `8aa46ff529686272ca233babfd24884f2b28ef25a24534f278e95560e755ae20` |
| Bundled niconicomments bytes | `d62f58ae0bd045eb86c2e116eefaec34e46e9b53c2f710656efae9e79252fee7` |

The capture uses the existing renderer page, binary transport, and browser session helpers. Its test-only observer wraps the existing `_drawComments`/comment `draw` methods to record invocation order; it adds no production API or instrumentation. It requires the WebGL2 canvas to report both `alpha` and `premultipliedAlpha` enabled. The captured image bytes come from the existing `gl.readPixels` path, vertically normalized by the existing transport.

The opt-in browser/WGPU test accepts `NICO_TIMELINE_REFERENCE_WIDTH`, `NICO_TIMELINE_REFERENCE_HEIGHT`, `NICO_TIMELINE_REFERENCE_FPS_NUM`, and `NICO_TIMELINE_REFERENCE_FPS_DEN` overrides for the T7 compatibility matrix. Dimensions are bounded by 3840×2160 and the frame rate by 60 fps. `NICO_TIMELINE_REFERENCE_DURATION_MS=6000` selects a six-second run. Use a fresh `NICO_TIMELINE_ARTIFACTS` directory for every case; fixed reference manifests cannot be combined with geometry, rate, or duration overrides.

The verified option cases are 1280×720 at 30/60 fps, 1920×1080 at 60 fps, 1920×1080 at 60000/1001 fps, 960×720, and 720×720. The default 1920×1080 at 30 fps remains the 360-frame reference case.

`convert_wall` for later performance work starts immediately before `EncodeNicoCommentedWithRenderer` and stops when it returns. `ready_wall` stops only when HLS is ready. `verification_wall` covers decode/NAL checks performed afterward. Keep those clocks separate from this browser-reference capture time.

## Compare browser and WGPU frame streams

`compare.py` reads raw or gzip RGBA8 streams and compares every frame. It reports hashes, the largest channel difference, pixels over tolerance, and per-frame counts. For comment-only transparent overlays, use `--require-transparent-exterior-exact`: a pixel is in the exterior when its alpha is zero in both streams, and every RGBA channel there must match exactly. Visible comment pixels are evaluated against the selected channel tolerance.

```powershell
rtk proxy python scripts/experiments/nico-timeline/compare.py `
  <browser.rgba.gz> <wgpu.rgba> `
  --width 1920 --height 1080 --frames 360 --tolerance 1 `
  --require-transparent-exterior-exact --report <fresh-report.json>
rtk pytest scripts/experiments/nico-timeline/test_compare.py -q
```

The report path must not already exist. A zero exit code means no pixel exceeded tolerance and the shared transparent exterior matched exactly. The exterior mask does not prove that visible comment pixels match the browser; inspect the full-frame channel-difference fields for that condition.

## Measure the real worker request

`TestNicoTimelineWorkerPerformance` sends one real protocol request to the built application worker and saves a `worker-run.json` in a unique child directory. The parent measures `worker_wall_seconds` from just before worker launch through the final result event; the worker measures `convert_wall_seconds` around `ExportNicoCommented`. The record includes input, helper, FFmpeg, worker, and output hashes, encoder/backend, fallback state, dimensions/FPS, and the worker CPU budget report. It does not download the video or comments.

PowerShell example with the six-second reference inputs:

```powershell
$env:NICO_TIMELINE_WORKER_PERF = '1'
$env:NICO_TIMELINE_WORKER_EXE = 'C:\path\to\imagepadserver-worker-current.exe'
$env:NICO_TIMELINE_WORKER_SOURCE = 'C:\Users\masah\AppData\Local\Temp\imagepad-nico-perf-20260916\source-6s.mp4'
$env:NICO_TIMELINE_WORKER_SNAPSHOT = 'C:\Users\masah\AppData\Local\Temp\imagepad-nico-perf-20260916\snapshot.json'
$env:NICO_TIMELINE_WORKER_FFMPEG = 'C:\path\to\ffmpeg.exe'
$env:NICO_TIMELINE_WORKER_HELPER = 'C:\path\to\nico-compositord.exe'
$env:NICO_TIMELINE_WORKER_OUTPUT_ROOT = Join-Path $env:TEMP 'nico-worker-perf-<fresh-id>'
$env:NICO_TIMELINE_WORKER_ENCODER = 'nvenc' # set to x264 for the CPU encoder cell
$env:NICO_TIMELINE_WORKER_GPU_BACKEND = 'vulkan'
rtk go test ./internal/server -run '^TestNicoTimelineWorkerPerformance$' -count=5 -v
```

Keep `NICO_TIMELINE_WORKER_OUTPUT_ROOT` outside the repository and use a fresh directory for each benchmark group. Each invocation gets a unique run directory. Do not include records with `timeline_fallback: true` in the WGPU-success median; retain them separately as fallback outcomes. For a CPU20% contention run, set `NICO_TIMELINE_WORKER_TEST_CPU_PERCENT=20` and run the test binary under `nico-budget-runner.exe --cpu-percent 20 --record <runner.json> --close-stdin`; the per-run file records this outer condition while `job_cpu_rate` records the worker's own production allowance. This test measures a staged source/snapshot only; network acquisition remains a separate measurement.

## Build a portable helper candidate

Build scripts target the current Rust host unless `--target` / `-Target` is supplied. They create `nico-compositord.bin`, a schema-1 NCT1 manifest with OS, architecture, SHA-256, Cargo target, and pinned WGPU 0.20.1, plus the locked Cargo manifest, lockfile, third-party license inventory, and license texts available in each locked Cargo package. They do not modify release workflows.

PowerShell:

```powershell
rtk proxy pwsh -NoProfile -File scripts/build-nico-timeline-compositor.ps1
rtk proxy pwsh -NoProfile -File scripts/build-nico-timeline-compositor.ps1 -ForGoEmbed
rtk go test -tags nico_timeline_embedded ./internal/nicorender -run '^TestTimelineRuntimeUsesEmbeddedHelper$' -count=1
```

Bash:

```bash
rtk proxy bash scripts/build-nico-timeline-compositor.sh
rtk proxy bash scripts/build-nico-timeline-compositor.sh --for-go-embed
```

The helper build tag is `nico_timeline_embedded` and is separate from `nico_native_embedded`. The Go runtime checks the manifest target and helper file format before extracting the helper to a job-owned temporary directory, then performs the same WGPU adapter and readback self-test used for an explicit helper path. Invalid/tampered payloads and builds without the helper fail with `ErrUnavailable`, which the export worker reports before falling back to CPU comment rendering. The embed integration test requires an actual supported GPU on the test machine; syntax and default-build tests do not count as hardware qualification.

Supported manifest targets are Windows amd64/arm64, macOS amd64/arm64, and Linux amd64/arm64. This target list records build artifacts only. It does not claim AMD, Intel, Apple Silicon, or Linux hardware success; each needs a real helper startup and output comparison on that hardware. Cross-target Cargo builds also require the corresponding Rust target and platform linker to be installed.
