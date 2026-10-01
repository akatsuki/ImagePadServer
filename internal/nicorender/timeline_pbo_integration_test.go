package nicorender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

var timelinePBOParitySequence atomic.Uint32

func TestTimelineCapturePBOParity(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_PBO_PARITY") != "1" {
		t.Skip("set NICO_TIMELINE_PBO_PARITY=1 to compare sync and PBO browser captures")
	}
	snapshotPath := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT"))
	baselinePath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_BASELINE_SCENE_REFERENCE"))
	if snapshotPath == "" || baselinePath == "" {
		t.Fatal("IMAGEPAD_NICO_PERF_SNAPSHOT and NICO_TIMELINE_BASELINE_SCENE_REFERENCE are required")
	}
	snapshotBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotBytes, &snapshot); err != nil {
		t.Fatalf("decode parity snapshot: %v", err)
	}
	snapshot, err = niconico.NormalizeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("normalize parity snapshot: %v", err)
	}
	wantScene, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatalf("read T0 sync scene: %v", err)
	}
	var baseline timelineCaptureBaselineManifest
	manifestBytes, err := os.ReadFile(baselinePath + ".manifest.json")
	if err != nil {
		t.Fatalf("read T0 sync scene manifest: %v", err)
	}
	if err := json.Unmarshal(manifestBytes, &baseline); err != nil {
		t.Fatalf("decode T0 sync scene manifest: %v", err)
	}
	snapshotDigest := sha256.Sum256(snapshotBytes)
	if baseline.SnapshotSHA256 != hex.EncodeToString(snapshotDigest[:]) || baseline.Seed != 0x4e49434f ||
		baseline.Width != 1920 || baseline.Height != 1080 || baseline.DurationMs != 6000 || baseline.FPSNum != 30 || baseline.FPSDen != 1 {
		t.Fatalf("T0 baseline inputs do not match this test: %+v", baseline)
	}
	baselineDigest := sha256.Sum256(wantScene)
	if baseline.SceneSHA256 != hex.EncodeToString(baselineDigest[:]) {
		t.Fatal("T0 sync scene hash does not match its manifest")
	}
	options := RenderOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		Transport: "binary", BrowserPath: strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER")),
	}
	capture := func(mode string) ([]byte, TimelineCaptureReport) {
		t.Helper()
		t.Setenv("NICO_TIMELINE_ASSET_READBACK", mode)
		seed := uint32(0x4e49434f)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		scene, report, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, options, &seed, timelineCaptureBatchDefault)
		if err != nil {
			t.Fatalf("capture %s timeline: %v", mode, err)
		}
		data, err := json.Marshal(scene)
		if err != nil {
			t.Fatalf("marshal %s scene: %v", mode, err)
		}
		return data, report
	}
	order := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_PBO_ORDER")))
	if order == "" {
		if timelinePBOParitySequence.Add(1)%2 == 0 {
			order = "pbo-first"
		} else {
			order = "sync-first"
		}
	}
	if order != "sync-first" && order != "pbo-first" {
		t.Fatalf("unsupported NICO_TIMELINE_PBO_ORDER %q", order)
	}
	var syncScene, pboScene []byte
	var syncReport, pboReport TimelineCaptureReport
	if order == "sync-first" {
		syncScene, syncReport = capture("sync")
		pboScene, pboReport = capture("pbo")
	} else {
		pboScene, pboReport = capture("pbo")
		syncScene, syncReport = capture("sync")
	}
	if string(syncScene) != string(wantScene) {
		t.Fatalf("current sync scene no longer matches T0 baseline: got %x want %s", sha256.Sum256(syncScene), baseline.SceneSHA256)
	}
	if string(pboScene) != string(syncScene) {
		t.Fatalf("PBO scene differs from sync scene: pbo=%x sync=%x", sha256.Sum256(pboScene), sha256.Sum256(syncScene))
	}
	metrics := pboReport.SpriteCaptureMetrics
	if metrics == nil || !metrics.Valid || metrics.RequestedMode != "pbo" || metrics.Mode != "pbo" || metrics.PBOTextures == 0 || metrics.PBOBytes == 0 {
		t.Fatalf("PBO was requested but did not perform PBO readbacks: %+v", metrics)
	}
	if metrics.PendingPixelPeakBytes > 64*1024*1024 {
		t.Fatalf("pending browser pixel backing exceeded 64 MiB: %+v", metrics)
	}
	t.Logf("sync capture=%.3fs readback=%.3fms; pbo capture=%.3fs readback=%.3fms, enqueue=%.3fms, drain=%.3fms, fence wait=%.3fms, copy=%.3fms, bytes=%d, fallbacks=%d; exact scene parity PASS",
		syncReport.SpriteCaptureMetrics.CaptureWallSeconds,
		syncReport.SpriteCaptureMetrics.ReadbackWallMs,
		metrics.CaptureWallSeconds,
		metrics.ReadbackWallMs,
		metrics.PBOEnqueueWallMs,
		metrics.PBODrainWallMs,
		metrics.PBOFenceWaitWallMs,
		metrics.PBOCopyWallMs,
		metrics.PBOBytes,
		metrics.PBOFallbacks)
}

func TestTimelinePBOReadbackState(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_PBO_PARITY") != "1" {
		t.Skip("set NICO_TIMELINE_PBO_PARITY=1 to verify PBO readback in a real browser")
	}
	snapshot := normalizeTimelineFixture(t, "ordinary-naka", loadTimelineFixture(t, "ordinary-naka"), -1)
	options := RenderOptions{Width: 640, Height: 360, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		BrowserPath: strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER"))}
	bundlePath, cleanupBundle, err := materializeBundle("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupBundle()
	pagePath, err := writeRendererPage(options.Width, options.Height, bundlePath, snapshot.RendererThreads(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(pagePath)
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	defer cancel()
	session, err := startBrowser(ctx, options.BrowserPath, pagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if err := session.waitReady(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := spriteEvaluate(ctx, session, timelinePBOCaptureScript, nil); err != nil {
		t.Fatalf("install PBO manager: %v", err)
	}
	var result struct {
		Available bool  `json:"available"`
		Pixels    []int `json:"pixels"`
		Restored  bool  `json:"restored"`
	}
	expression := `(async () => {
  const gl = window.__niconi.renderer.gl, manager = window.__nicoTimelinePBO;
  if (!manager || !manager.available) return { available: false };
  const foreign = gl.createBuffer(), texture = gl.createTexture();
  gl.bindBuffer(gl.PIXEL_PACK_BUFFER, foreign);
  gl.pixelStorei(gl.PACK_ALIGNMENT, 8);
  gl.pixelStorei(gl.PACK_ROW_LENGTH, 13);
  gl.pixelStorei(gl.PACK_SKIP_PIXELS, 2);
  gl.pixelStorei(gl.PACK_SKIP_ROWS, 3);
  const before = {
    framebuffer: gl.getParameter(gl.READ_FRAMEBUFFER_BINDING),
    readBuffer: gl.getParameter(gl.READ_BUFFER),
    pbo: gl.getParameter(gl.PIXEL_PACK_BUFFER_BINDING),
    alignment: gl.getParameter(gl.PACK_ALIGNMENT), rowLength: gl.getParameter(gl.PACK_ROW_LENGTH),
    skipPixels: gl.getParameter(gl.PACK_SKIP_PIXELS), skipRows: gl.getParameter(gl.PACK_SKIP_ROWS)
  };
  gl.pixelStorei(gl.UNPACK_PREMULTIPLY_ALPHA_WEBGL, false);
  gl.bindTexture(gl.TEXTURE_2D, texture);
  gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, 2, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array([11,22,33,44,55,66,77,88]));
  const descriptor = manager.enqueue(texture, 2, 1, 1);
  if (!descriptor) throw Error('PBO enqueue unexpectedly fell back: ' + JSON.stringify(manager.getStats()));
  await manager.drain();
  const after = {
    framebuffer: gl.getParameter(gl.READ_FRAMEBUFFER_BINDING),
    readBuffer: gl.getParameter(gl.READ_BUFFER),
    pbo: gl.getParameter(gl.PIXEL_PACK_BUFFER_BINDING),
    alignment: gl.getParameter(gl.PACK_ALIGNMENT), rowLength: gl.getParameter(gl.PACK_ROW_LENGTH),
    skipPixels: gl.getParameter(gl.PACK_SKIP_PIXELS), skipRows: gl.getParameter(gl.PACK_SKIP_ROWS)
  };
  const pixels = Array.from(descriptor.pixels);
  const restored = before.framebuffer === after.framebuffer && before.readBuffer === after.readBuffer && before.pbo === after.pbo &&
    before.alignment === after.alignment && before.rowLength === after.rowLength &&
    before.skipPixels === after.skipPixels && before.skipRows === after.skipRows;
  manager.dispose();
  gl.deleteBuffer(foreign);
  gl.deleteTexture(texture);
  return { available: true, pixels, restored };
})()`
	if err := timelineEvaluate(ctx, session, expression, 4096, &result); err != nil {
		t.Fatalf("execute browser PBO readback: %v", err)
	}
	if !result.Available {
		t.Fatal("real browser does not expose WebGL2 PBO readback APIs")
	}
	want := []int{11, 22, 33, 44, 55, 66, 77, 88}
	if len(result.Pixels) != len(want) {
		t.Fatalf("readback pixels=%v, want %v", result.Pixels, want)
	}
	for i := range want {
		if result.Pixels[i] != want[i] {
			t.Fatalf("readback pixels=%v, want %v", result.Pixels, want)
		}
	}
	if !result.Restored {
		t.Fatal("PBO readback did not restore caller GL state")
	}
}

func TestTimelineCapturePBOScriptIsEmbedded(t *testing.T) {
	if strings.TrimSpace(timelinePBOCaptureScript) == "" || !strings.Contains(timelinePBOCaptureScript, "__nicoTimelinePBO") {
		t.Fatal("timeline PBO capture script is not embedded")
	}
}
