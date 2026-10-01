package nicorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

func TestSpritePaletteEarlyExitRealBrowserParity(t *testing.T) {
	if os.Getenv("NICO_SPRITE_PALETTE_PARITY") != "1" {
		t.Skip("set NICO_SPRITE_PALETTE_PARITY=1 to compare real-browser NCT1 output")
	}
	variant := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_SPRITE_PALETTE_PARITY_VARIANT")))
	if variant != "legacy" && variant != "candidate" {
		t.Fatal("NICO_SPRITE_PALETTE_PARITY_VARIANT must be legacy or candidate")
	}
	hasEarlyExit := strings.Contains(spriteCaptureScript, "!paletteComplete && !isGray")
	if (variant == "legacy" && hasEarlyExit) || (variant == "candidate" && !hasEarlyExit) {
		t.Fatalf("embedded sprite script does not match requested %s variant", variant)
	}
	outputDir := strings.TrimSpace(os.Getenv("NICO_SPRITE_PARITY_OUTPUT_DIR"))
	if outputDir == "" || !filepath.IsAbs(outputDir) {
		t.Fatal("NICO_SPRITE_PARITY_OUTPUT_DIR must be an absolute owned output directory")
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("create parity output directory: %v", err)
	}
	fixtures := []struct {
		name         string
		width        int
		height       int
		commentCount int
	}{
		{name: "ordinary-naka", width: 640, height: 360, commentCount: 1},
		{name: "sm9-362", width: 1920, height: 1080, commentCount: 362},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			snapshot := loadTimelineFixture(t, fixture.name)
			if snapshot.CommentCount != fixture.commentCount {
				t.Fatalf("fixture comments=%d, want %d", snapshot.CommentCount, fixture.commentCount)
			}
			if fixture.name == "ordinary-naka" {
				snapshot.Threads[0].Comments[0].PostedAt = "2020-01-01T00:00:00Z"
			}
			options := RenderOptions{
				Width: fixture.width, Height: fixture.height, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
				Transport: "binary", BrowserPath: strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER")),
			}
			t.Setenv("NICO_TIMELINE_ASSET_READBACK", "sync")
			seed := uint32(0x4e49434f)
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			scene, report, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, options, &seed, timelineCaptureBatchDefault)
			if err != nil {
				t.Fatalf("capture %s comment timeline: %v", fixture.name, err)
			}
			if report.EligibleComments == 0 || len(scene.Assets) == 0 || len(scene.Draws) == 0 {
				t.Fatalf("fixture produced no browser-rendered comment assets: report=%+v", report)
			}
			if err := ValidateCommentTimeline(scene); err != nil {
				t.Fatalf("captured comment timeline is invalid: %v", err)
			}
			var nct1 bytes.Buffer
			if err := WriteCommentTimeline(&nct1, scene); err != nil {
				t.Fatalf("write captured NCT1: %v", err)
			}
			if nct1.Len() < 4 || string(nct1.Bytes()[:4]) != "NCT1" {
				t.Fatalf("captured timeline has invalid NCT1 magic: %q", nct1.Bytes()[:min(nct1.Len(), 4)])
			}
			path := filepath.Join(outputDir, fixture.name+".nct")
			if err := os.WriteFile(path, nct1.Bytes(), 0o600); err != nil {
				t.Fatalf("write parity NCT1 %s: %v", path, err)
			}
			rgbaHash := sha256.New()
			var rgbaBytes uint64
			for _, asset := range scene.Assets {
				_, _ = rgbaHash.Write(asset.RGBA)
				rgbaBytes += uint64(len(asset.RGBA))
			}
			if report.SpriteCaptureMetrics == nil || !report.SpriteCaptureMetrics.Valid || report.SpriteCaptureMetrics.Mode != "sync" {
				t.Fatalf("sync sprite capture metrics invalid: %+v", report.SpriteCaptureMetrics)
			}
			nct1Hash := sha256.Sum256(nct1.Bytes())
			t.Logf("variant=%s fixture=%s comments=%d assets=%d draws=%d nct1_bytes=%d nct1_sha256=%x rgba_bytes=%d rgba_sha256=%x pack_wall_ms=%.3f browser_payload_bytes=%d output=%s",
				variant, fixture.name, report.EligibleComments, len(scene.Assets), len(scene.Draws), nct1.Len(), nct1Hash,
				rgbaBytes, rgbaHash.Sum(nil), report.SpriteCaptureMetrics.PackWallMs, report.BrowserAssetPayloadBytes, path)
		})
	}
}

func TestTimelineCaptureIntegration(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_INTEGRATION") != "1" {
		t.Skip("set NICO_TIMELINE_INTEGRATION=1 to run browser comment capture")
	}
	options := RenderOptions{
		Width: 640, Height: 360, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		BrowserPath: strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER")),
	}

	t.Run("pinned-default-plugin-state", func(t *testing.T) {
		snapshot := normalizeTimelineFixture(t, "ordinary-naka", loadTimelineFixture(t, "ordinary-naka"), -1)
		snapshot.Threads[0].Comments[0].PostedAt = "2020-01-01T00:00:00Z"
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
			t.Fatalf("start pinned bundle browser: %v", err)
		}
		defer session.close()
		if err := session.waitReady(ctx, false); err != nil {
			t.Fatalf("wait for pinned bundle browser: %v", err)
		}
		var state struct {
			CommentCount       int      `json:"commentCount"`
			CommentTypes       []string `json:"commentTypes"`
			RuntimePlugins     []string `json:"runtimePlugins"`
			ConfigPlugins      []string `json:"configPlugins"`
			CommentPlugins     []string `json:"commentPlugins"`
			DefaultFlashClass  string   `json:"defaultFlashClass"`
			DefaultFlashFilter string   `json:"defaultFlashFilter"`
		}
		expression := `(() => { const n = window.__niconi, flash = NiconiComments.FlashComment; return {
  commentCount: n.comments.length,
  commentTypes: n.comments.map(c => c && c.constructor && c.constructor.name || ""),
  runtimePlugins: (n.plugins || []).map(p => p && p.constructor && p.constructor.name || typeof p),
  configPlugins: (n.ctx.config.plugins || []).map(p => p && p.name || typeof p),
  commentPlugins: (n.ctx.config.commentPlugins || []).map(p => (p && p.class && p.class.name || "") + "/" + (p && p.condition && p.condition.name || "")),
  defaultFlashClass: flash && flash.class && flash.class.name || "",
  defaultFlashFilter: flash && flash.condition && flash.condition.name || ""
}; })()`
		if err := spriteEvaluate(ctx, session, expression, &state); err != nil {
			t.Fatalf("read pinned bundle plugin state: %v", err)
		}
		if state.CommentCount != 1 || len(state.CommentTypes) != 1 || state.CommentTypes[0] != "HTML5Comment" {
			t.Fatalf("ordinary comment was not instantiated as built-in HTML5: %+v", state)
		}
		if len(state.RuntimePlugins) != 0 || len(state.ConfigPlugins) != 0 {
			t.Fatalf("pinned default unexpectedly activates custom rendering plugins: %+v", state)
		}
		if len(state.CommentPlugins) != 1 || state.CommentPlugins[0] != "FlashComment/isFlashComment" ||
			state.DefaultFlashClass != "FlashComment" || state.DefaultFlashFilter != "isFlashComment" {
			t.Fatalf("pinned default comment factory changed: %+v", state)
		}
		if err := spriteEvaluate(ctx, session, "window.__niconi.ctx.config.commentPlugins = [{class: function CustomComment(){}, condition: function customCondition(){ return false; }}]", nil); err != nil {
			t.Fatalf("inject test-only custom comment factory: %v", err)
		}
		if err := spriteEvaluate(ctx, session, timelineCaptureScript, nil); err != nil {
			t.Fatalf("evaluate bridge with custom comment factory: %v", err)
		}
		var installError string
		if err := spriteEvaluate(ctx, session, "window.__nicoTimelineInstallError || ''", &installError); err != nil {
			t.Fatalf("read custom comment factory decision: %v", err)
		}
		if !strings.Contains(installError, "custom or active plugin rendering") {
			t.Fatalf("custom comment factory was not rejected: %q", installError)
		}
		if err := spriteEvaluate(ctx, session, "window.__niconi.ctx.config.commentPlugins = [NiconiComments.FlashComment]; window.__niconi.plugins.push({});", nil); err != nil {
			t.Fatalf("inject test-only runtime plugin: %v", err)
		}
		if err := spriteEvaluate(ctx, session, timelineCaptureScript, nil); err != nil {
			t.Fatalf("evaluate bridge with runtime plugin: %v", err)
		}
		if err := spriteEvaluate(ctx, session, "window.__nicoTimelineInstallError || ''", &installError); err != nil {
			t.Fatalf("read runtime plugin decision: %v", err)
		}
		if !strings.Contains(installError, "custom or active plugin rendering") {
			t.Fatalf("runtime plugin was not rejected: %q", installError)
		}
		if err := spriteEvaluate(ctx, session, "window.__niconi.plugins = []; window.__niconi.ctx.config.plugins.push(function CustomRendererPlugin(){});", nil); err != nil {
			t.Fatalf("inject test-only renderer plugin: %v", err)
		}
		if err := spriteEvaluate(ctx, session, timelineCaptureScript, nil); err != nil {
			t.Fatalf("evaluate bridge with renderer plugin: %v", err)
		}
		if err := spriteEvaluate(ctx, session, "window.__nicoTimelineInstallError || ''", &installError); err != nil {
			t.Fatalf("read renderer plugin decision: %v", err)
		}
		if !strings.Contains(installError, "custom or active plugin rendering") {
			t.Fatalf("renderer plugin was not rejected: %q", installError)
		}
	})

	t.Run("ordinary", func(t *testing.T) {
		snapshot := normalizeTimelineFixture(t, "ordinary-naka", loadTimelineFixture(t, "ordinary-naka"), -1)
		snapshot.Threads[0].Comments[0].PostedAt = "2020-01-01T00:00:00Z"
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		scene, report, err := CaptureCommentTimeline(ctx, snapshot, options)
		if err != nil {
			t.Fatalf("capture ordinary comment: %v", err)
		}
		if report.EligibleComments != 1 || report.DrawCanvasCalls != 0 || report.ElementDrawCalls != report.EligibleComments {
			t.Fatalf("capture counts violate contract: %+v", report)
		}
		metrics := report.SpriteCaptureMetrics
		if metrics == nil || !metrics.Valid || metrics.Mode != "sync" {
			t.Fatalf("sprite capture metrics missing or invalid: %+v", metrics)
		}
		if metrics.TextureCreations == 0 || metrics.ReadbackCalls == 0 || metrics.ReadbackBytes == 0 || metrics.CaptureWallSeconds <= 0 {
			t.Fatalf("sprite capture metrics omitted actual texture work: %+v", metrics)
		}
		if len(scene.Draws) == 0 || len(scene.Assets) == 0 {
			t.Fatalf("ordinary comment produced empty timeline: %+v", report)
		}
		if err := ValidateCommentTimeline(scene); err != nil {
			t.Fatalf("captured scene invalid: %v", err)
		}
	})

	t.Run("analytic-speed-matches-pinned-renderer", func(t *testing.T) {
		snapshot := normalizeTimelineFixture(t, "ordinary-naka", loadTimelineFixture(t, "ordinary-naka"), -1)
		snapshot.Threads[0].Comments[0].PostedAt = "2020-01-01T00:00:00Z"
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
			t.Fatalf("start speed-formula browser: %v", err)
		}
		defer session.close()
		if err := session.waitReady(ctx, false); err != nil {
			t.Fatalf("wait for speed-formula browser: %v", err)
		}
		if err := spriteEvaluate(ctx, session, spriteCaptureScript, nil); err != nil {
			t.Fatalf("install sprite capture for speed check: %v", err)
		}
		if err := spriteEvaluate(ctx, session, timelineCaptureScript, nil); err != nil {
			t.Fatalf("install timeline bridge for speed check: %v", err)
		}
		var description timelineDescribeResult
		if err := spriteEvaluate(ctx, session, "window.__nicoTimelineDescribe(0,600)", &description); err != nil {
			t.Fatalf("describe comment speed: %v", err)
		}
		if len(description.Comments) != 1 || description.Comments[0].SpeedX == nil {
			t.Fatalf("missing analytic comment speed: %+v", description)
		}
		meta := description.Comments[0]
		anchor := timelineProbeVPos(meta.StartVPos, meta.EndVPos)[0]
		other := anchor + 1
		if other >= meta.EndVPos {
			other = anchor - 1
		}
		if other < meta.StartVPos {
			t.Fatalf("fixture interval [%d,%d) has no distinct speed check position", meta.StartVPos, meta.EndVPos)
		}
		expression := fmt.Sprintf(`(() => {
  const comment = window.__niconi.comments[%d];
  comment.draw(%d, false, undefined);
  const x = comment.pos.x;
  comment.draw(%d, false, undefined);
  return comment.pos.x - x;
})()`, meta.Index, anchor, other)
		var measuredSpeed float64
		if err := spriteEvaluate(ctx, session, expression, &measuredSpeed); err != nil {
			t.Fatalf("measure pinned comment speed: %v", err)
		}
		measuredSpeed /= float64(other - anchor)
		delta := measuredSpeed - *meta.SpeedX
		if delta < -1e-9 || delta > 1e-9 {
			t.Fatalf("analytic speed=%0.12f, pinned position delta=%0.12f", *meta.SpeedX, measuredSpeed)
		}
	})

	t.Run("sprite-deflate-is-lossless", func(t *testing.T) {
		snapshot := normalizeTimelineFixture(t, "ordinary-naka", loadTimelineFixture(t, "ordinary-naka"), -1)
		snapshot.Threads[0].Comments[0].PostedAt = "2020-01-01T00:00:00Z"
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		seed := uint32(0x4e49434f)
		rawOptions := options
		rawOptions.SpriteCompression = "none"
		rawScene, rawReport, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, rawOptions, &seed, timelineCaptureBatchDefault)
		if err != nil {
			t.Fatalf("capture without deflate: %v", err)
		}
		seed = 0x4e49434f
		deflateOptions := options
		deflateOptions.SpriteCompression = "deflate"
		deflateScene, deflateReport, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, deflateOptions, &seed, timelineCaptureBatchDefault)
		if err != nil {
			t.Fatalf("capture with deflate: %v", err)
		}
		if !reflect.DeepEqual(rawScene, deflateScene) {
			t.Fatal("lossless deflate changed the captured comment timeline")
		}
		if rawReport.BrowserAssetPayloadBytes <= 0 || deflateReport.BrowserAssetPayloadBytes <= 0 {
			t.Fatalf("missing browser asset payload counters: raw=%+v deflate=%+v", rawReport, deflateReport)
		}
	})

	t.Run("capture-batches-preserve-all-comments", func(t *testing.T) {
		snapshot := normalizeTimelineFixture(t, "ordinary-naka", loadTimelineFixture(t, "ordinary-naka"), -1)
		template := snapshot.Threads[0].Comments[0]
		comments := make([]niconico.Comment, 0, 35)
		for i := 0; i < 35; i++ {
			comment := template
			comment.ID = fmt.Sprintf("capture-batch-%02d", i)
			comment.No = int64(i + 1)
			comment.VposMs = int64(1000 + i*100)
			comment.Body = fmt.Sprintf("batch %02d", i)
			comment.PostedAt = "2020-01-01T00:00:00Z"
			comments = append(comments, comment)
		}
		snapshot.Threads[0].Comments = comments
		snapshot, err := niconico.NormalizeSnapshot(snapshot)
		if err != nil {
			t.Fatalf("normalize multi-comment snapshot: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		scene, report, err := CaptureCommentTimeline(ctx, snapshot, options)
		if err != nil {
			t.Fatalf("capture multi-comment timeline: %v", err)
		}
		if report.EligibleComments != len(comments) {
			t.Fatalf("eligible comments=%d, want %d", report.EligibleComments, len(comments))
		}
		wantCaptureBatchCalls := (len(comments) + timelineCaptureBatchDefault - 1) / timelineCaptureBatchDefault
		if report.CaptureBatchCalls != wantCaptureBatchCalls {
			t.Fatalf("capture batch calls=%d, want %d for %d comments", report.CaptureBatchCalls, wantCaptureBatchCalls, len(comments))
		}
		if report.ElementDrawCalls == 0 || report.ElementDrawCalls > report.EligibleComments {
			t.Fatalf("capture draw calls violate per-comment bounds: %+v", report)
		}
		if err := ValidateCommentTimeline(scene); err != nil {
			t.Fatalf("batched scene is invalid: %v", err)
		}
	})

	t.Run("batch-response-budget-preserves-pending-item", func(t *testing.T) {
		snapshot := normalizeTimelineFixture(t, "ordinary-naka", loadTimelineFixture(t, "ordinary-naka"), -1)
		snapshot.Threads[0].Comments[0].PostedAt = "2020-01-01T00:00:00Z"
		second := snapshot.Threads[0].Comments[0]
		second.ID = "capture-pending-item"
		second.No = 2
		second.VposMs = 1500
		second.Body = "pending item"
		second.PostedAt = "2020-01-01T00:00:00Z"
		snapshot.Threads[0].Comments = append(snapshot.Threads[0].Comments, second)
		snapshot, err := niconico.NormalizeSnapshot(snapshot)
		if err != nil {
			t.Fatalf("normalize pending-item snapshot: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		seed := uint32(0x4e49434f)
		wholeScene, wholeReport, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, options, &seed, timelineCaptureBatchDefault)
		if err != nil {
			t.Fatalf("capture with normal response budget: %v", err)
		}
		seed = 0x4e49434f
		budgetScene, budgetReport, err := captureCommentTimelineWithCaptureLimits(ctx, snapshot, options, &seed, timelineCaptureBatchDefault, 1)
		if err != nil {
			t.Fatalf("capture with one-byte batch target: %v", err)
		}
		if !reflect.DeepEqual(wholeScene, budgetScene) {
			t.Fatal("response-size split changed or dropped a captured comment")
		}
		if wholeReport.CaptureBatchCalls != 1 || budgetReport.CaptureBatchCalls != 2 {
			t.Fatalf("capture batch calls with whole/budgeted responses=%d/%d, want 1/2", wholeReport.CaptureBatchCalls, budgetReport.CaptureBatchCalls)
		}
	})

	t.Run("no-visible-comments", func(t *testing.T) {
		snapshot := loadTimelineFixture(t, "no-visible-comments")
		snapshot.Threads[0].Comments[0].PostedAt = "2020-01-01T00:00:00Z"
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		scene, report, err := CaptureCommentTimeline(ctx, snapshot, options)
		if err != nil {
			t.Fatalf("capture zero-visible snapshot: %v", err)
		}
		if report.EligibleComments != 0 || report.ElementDrawCalls != 0 || len(scene.Draws) != 0 || len(scene.Assets) != 0 {
			t.Fatalf("zero-visible result contains events: report=%+v assets=%d draws=%d", report, len(scene.Assets), len(scene.Draws))
		}
		metrics := report.SpriteCaptureMetrics
		if metrics == nil || !metrics.Valid || metrics.TextureCreations != 0 || metrics.ReadbackCalls != 0 || metrics.ReadbackBytes != 0 {
			t.Fatalf("zero-visible capture metrics should record known zero sprite work: %+v", metrics)
		}
	})

	t.Run("owner-reverse-script", func(t *testing.T) {
		snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{
			VideoID: "sm9", SelectedForks: []string{"owner"},
			Threads: []niconico.Thread{{Fork: "owner", Comments: []niconico.Comment{{
				ID: "owner-reverse-script", VposMs: 1000, Body: "@逆 全", Commands: []string{"white"}, PostedAt: "2020-01-01T00:00:00Z",
			}}}},
		})
		if err != nil {
			t.Fatal(err)
		}
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
		browserCtx, browserCancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer browserCancel()
		browser, err := startBrowser(browserCtx, options.BrowserPath, pagePath)
		if err != nil {
			t.Fatalf("start owner NicoScript browser: %v", err)
		}
		defer browser.close()
		if err := browser.waitReady(browserCtx, false); err != nil {
			t.Fatalf("wait for owner NicoScript browser: %v", err)
		}
		var reverseCount int
		if err := spriteEvaluate(browserCtx, browser, "window.__niconi.ctx.nicoScripts.reverse.length", &reverseCount); err != nil {
			t.Fatalf("inspect browser NicoScript reverse state: %v", err)
		}
		if reverseCount == 0 {
			t.Fatal("owner @逆 全 did not create browser nicoScripts.reverse entries")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		_, report, err := CaptureCommentTimeline(ctx, snapshot, options)
		if !errors.Is(err, ErrTimelineUnsupported) {
			t.Fatalf("got %v, want typed unsupported result", err)
		}
		if report.UnsupportedComments == 0 {
			t.Fatalf("owner NicoScript was not counted unsupported: %+v", report)
		}
	})
}

func TestSpriteBase64NativeBrowserAndSeededPBOSyncParity(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_SPRITE_BASE64_PBO_PARITY") != "1" {
		t.Skip("set NICO_TIMELINE_SPRITE_BASE64_PBO_PARITY=1 to verify native Base64 and seeded sync/PBO parity in Chromium")
	}
	snapshot := loadTimelineFixture(t, "sm9-362")
	browserPath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER"))
	if browserPath == "" {
		var err error
		browserPath, err = findBrowser()
		if err != nil {
			t.Fatalf("find Chromium for native Base64 parity: %v", err)
		}
	}

	probeSnapshot := normalizeTimelineFixture(t, "ordinary-naka", loadTimelineFixture(t, "ordinary-naka"), -1)
	bundlePath, cleanupBundle, err := materializeBundle("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupBundle()
	pagePath, err := writeRendererPage(640, 360, bundlePath, probeSnapshot.RendererThreads(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(pagePath)
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer probeCancel()
	probeBrowser, err := startBrowser(probeCtx, browserPath, pagePath)
	if err != nil {
		t.Fatalf("start Chromium for native Base64 parity: %v", err)
	}
	defer probeBrowser.close()
	if err := probeBrowser.waitReady(probeCtx, false); err != nil {
		t.Fatalf("wait for native Base64 parity browser: %v", err)
	}
	if err := spriteEvaluate(probeCtx, probeBrowser, spriteCaptureScript, nil); err != nil {
		t.Fatalf("install browser sprite capture: %v", err)
	}
	var probe struct {
		NativeAPI            bool   `json:"nativeApi"`
		FixtureCount         int    `json:"fixtureCount"`
		EqualFixtures        int    `json:"equalFixtures"`
		NativeCalls          int    `json:"nativeCalls"`
		BtoaCalls            int    `json:"btoaCalls"`
		FromCharCodeCalls    int    `json:"fromCharCodeCalls"`
		ExpectedLegacyChunks int    `json:"expectedLegacyChunks"`
		UserAgent            string `json:"userAgent"`
	}
	expression := `(() => {
  const nativeMethod = Uint8Array.prototype.toBase64;
  if (typeof nativeMethod !== 'function') return { nativeApi: false, userAgent: navigator.userAgent };
  let nativeCalls = 0, btoaCalls = 0, fromCharCodeCalls = 0;
  const originalBtoa = window.btoa;
  const originalFromCharCode = String.fromCharCode;
  Uint8Array.prototype.toBase64 = function (...args) {
    nativeCalls++;
    return Reflect.apply(nativeMethod, this, args);
  };
  window.btoa = function (...args) {
    btoaCalls++;
    return Reflect.apply(originalBtoa, this, args);
  };
  String.fromCharCode = function (...args) {
    fromCharCodeCalls++;
    return Reflect.apply(originalFromCharCode, this, args);
  };
  try {
    const pattern = (length, multiplier, offset) => {
      const bytes = new Uint8Array(length);
      for (let i = 0; i < length; i++) bytes[i] = (i * multiplier + offset) & 255;
      return bytes;
    };
    const cases = [
      new Uint8Array(0), new Uint8Array([0]), new Uint8Array([0, 255]),
      new Uint8Array([0, 255, 127]), Uint8Array.from({ length: 256 }, (_, i) => i),
      new Uint8Array([238, 0, 255, 127, 128, 221]).subarray(1, 5),
      pattern(16383, 131, 17), pattern(16384, 131, 17), pattern(16385, 131, 17),
      pattern(1920 * 1080 * 4, 31, 9)
    ];
    const expectedChunks = cases.reduce((sum, bytes) => sum + Math.ceil(bytes.byteLength / 16384), 0);
    const equal = cases.map(bytes => {
      const automatic = window.__nicoSpritesEncodeBytesForTest(bytes);
      const forcedNative = window.__nicoSpritesEncodeBytesForTest(bytes, 'native');
      const legacy = window.__nicoSpritesEncodeBytesForTest(bytes, 'legacy');
      return automatic === forcedNative && forcedNative === legacy;
    });
    return {
      nativeApi: true, fixtureCount: cases.length,
      equalFixtures: equal.filter(Boolean).length,
      nativeCalls, btoaCalls, fromCharCodeCalls,
      expectedLegacyChunks: expectedChunks, userAgent: navigator.userAgent
    };
  } finally {
    Uint8Array.prototype.toBase64 = nativeMethod;
    window.btoa = originalBtoa;
    String.fromCharCode = originalFromCharCode;
  }
})()`
	if err := spriteEvaluate(probeCtx, probeBrowser, expression, &probe); err != nil {
		t.Fatalf("execute browser native Base64 parity: %v", err)
	}
	if !probe.NativeAPI {
		t.Fatalf("browser lacks Uint8Array.toBase64; native path was not tested: %s", probe.UserAgent)
	}
	if probe.FixtureCount != 10 || probe.EqualFixtures != probe.FixtureCount ||
		probe.NativeCalls != 2*probe.FixtureCount || probe.BtoaCalls != probe.FixtureCount ||
		probe.FromCharCodeCalls != probe.ExpectedLegacyChunks {
		t.Fatalf("browser native/legacy Base64 parity failed: %+v", probe)
	}
	t.Logf("browser Base64 native parity PASS: fixtures=%d nativeCalls=%d btoaCalls=%d legacyChunks=%d userAgent=%q",
		probe.FixtureCount, probe.NativeCalls, probe.BtoaCalls, probe.FromCharCodeCalls, probe.UserAgent)

	options := RenderOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		Transport: "binary", BrowserPath: browserPath,
	}
	capture := func(mode string) (CommentTimeline, TimelineCaptureReport) {
		t.Helper()
		t.Setenv("NICO_TIMELINE_ASSET_READBACK", mode)
		seed := uint32(0x4e49434f)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		scene, report, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, options, &seed, timelineCaptureBatchDefault)
		if err != nil {
			t.Fatalf("capture seeded %s timeline: %v", mode, err)
		}
		return scene, report
	}
	syncScene, syncReport := capture("sync")
	pboScene, pboReport := capture("pbo")
	if !reflect.DeepEqual(syncScene, pboScene) {
		t.Fatal("seeded PBO scene differs from sync scene")
	}
	syncBytes, err := json.Marshal(syncScene)
	if err != nil {
		t.Fatalf("marshal sync scene: %v", err)
	}
	pboBytes, err := json.Marshal(pboScene)
	if err != nil {
		t.Fatalf("marshal PBO scene: %v", err)
	}
	if string(syncBytes) != string(pboBytes) {
		t.Fatal("seeded PBO scene JSON bytes differ from sync scene")
	}
	syncMetrics, pboMetrics := syncReport.SpriteCaptureMetrics, pboReport.SpriteCaptureMetrics
	if syncMetrics == nil || !syncMetrics.Valid || syncMetrics.Mode != "sync" {
		t.Fatalf("sync capture metrics are invalid: %+v", syncMetrics)
	}
	if pboMetrics == nil || !pboMetrics.Valid || pboMetrics.RequestedMode != "pbo" || pboMetrics.Mode != "pbo" || pboMetrics.PBOTextures == 0 || pboMetrics.PBOBytes == 0 {
		t.Fatalf("PBO capture did not execute PBO readbacks: %+v", pboMetrics)
	}
	if pboMetrics.PendingPixelPeakBytes > 64*1024*1024 {
		t.Fatalf("PBO pending pixels exceeded 64 MiB: %+v", pboMetrics)
	}
	digest := sha256.Sum256(syncBytes)
	t.Logf("seeded scene sync/PBO exact parity PASS: bytes=%d sha256=%s sync_capture=%.3fs pbo_capture=%.3fs pbo_bytes=%d pending_peak=%d",
		len(syncBytes), hex.EncodeToString(digest[:]), syncMetrics.CaptureWallSeconds, pboMetrics.CaptureWallSeconds,
		pboMetrics.PBOBytes, pboMetrics.PendingPixelPeakBytes)
}

func TestTimelineCaptureBatchPerformanceAB(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_CAPTURE_BATCH_PERF") != "1" {
		t.Skip("set NICO_TIMELINE_CAPTURE_BATCH_PERF=1 to compare real browser capture batch sizes")
	}
	snapshot := loadTimelineFixture(t, "sm9-362")
	options := RenderOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		BrowserPath: strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER")),
	}
	if os.Getenv("NICO_TIMELINE_CAPTURE_BATCH_HIGH_DENSITY") == "1" {
		var err error
		snapshot, err = spreadTimelineCaptureBatchComments(snapshot, options.DurationMs)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("capture_batch_profile=high-density comments=%d duration_ms=%d", snapshot.CommentCount, options.DurationMs)
	}
	limits := []int{1, 2, 4, 8, 16, 32}
	if configured := strings.TrimSpace(os.Getenv("NICO_TIMELINE_CAPTURE_BATCH_LIMITS")); configured != "" {
		limits = nil
		seen := make(map[int]struct{})
		for _, field := range strings.Split(configured, ",") {
			limit, err := strconv.Atoi(strings.TrimSpace(field))
			if err != nil || limit < 1 || limit > timelineCaptureBatchMaxItems {
				t.Fatalf("invalid NICO_TIMELINE_CAPTURE_BATCH_LIMITS value %q", configured)
			}
			if _, ok := seen[limit]; ok {
				t.Fatalf("duplicate capture batch limit %d", limit)
			}
			seen[limit] = struct{}{}
			limits = append(limits, limit)
		}
		if len(limits) < 2 {
			t.Fatalf("NICO_TIMELINE_CAPTURE_BATCH_LIMITS needs at least two comma-separated values")
		}
	}
	repeats := 3
	if configured := strings.TrimSpace(os.Getenv("NICO_TIMELINE_CAPTURE_BATCH_REPEATS")); configured != "" {
		parsed, err := strconv.Atoi(configured)
		if err != nil || parsed < 2 || parsed > 100 {
			t.Fatalf("invalid NICO_TIMELINE_CAPTURE_BATCH_REPEATS value %q", configured)
		}
		repeats = parsed
	}
	elapsedByLimit := make(map[int][]time.Duration, len(limits))
	callsByLimit := make(map[int][]int, len(limits))
	drawCallsByLimit := make(map[int][]int, len(limits))
	pairedDeltas := make([]time.Duration, 0, repeats)
	var reference CommentTimeline
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	for repeat := 0; repeat < repeats; repeat++ {
		order := append([]int(nil), limits...)
		if repeat%2 == 1 {
			for left, right := 0, len(order)-1; left < right; left, right = left+1, right-1 {
				order[left], order[right] = order[right], order[left]
			}
		}
		elapsedByPair := make(map[int]time.Duration, len(order))
		for _, limit := range order {
			seed := uint32(0x4e49434f)
			started := time.Now()
			scene, report, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, options, &seed, limit)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("capture with batch limit %d: %v", limit, err)
			}
			if report.CaptureBatchCalls == 0 || report.EligibleComments == 0 {
				t.Fatalf("batch limit %d captured no eligible comments: %+v", limit, report)
			}
			if report.ElementDrawCalls > report.EligibleComments {
				t.Fatalf("batch limit %d made too many direct draws: %+v", limit, report)
			}
			if reference.Header.FrameCount == 0 {
				reference = scene
			} else if !reflect.DeepEqual(reference, scene) {
				t.Fatalf("batch limit %d changed the seeded timeline scene", limit)
			}
			elapsedByLimit[limit] = append(elapsedByLimit[limit], elapsed)
			elapsedByPair[limit] = elapsed
			callsByLimit[limit] = append(callsByLimit[limit], report.CaptureBatchCalls)
			drawCallsByLimit[limit] = append(drawCallsByLimit[limit], report.ElementDrawCalls)
			t.Logf("batch_limit=%d capture_calls=%d element_draw_calls=%d eligible=%d elapsed=%s", limit, report.CaptureBatchCalls, report.ElementDrawCalls, report.EligibleComments, elapsed)
		}
		if len(limits) == 2 {
			delta := elapsedByPair[limits[0]] - elapsedByPair[limits[1]]
			pairedDeltas = append(pairedDeltas, delta)
			t.Logf("batch_pair=%02d batch_%d_minus_batch_%d_ms=%.3f", repeat+1, limits[0], limits[1], float64(delta)/float64(time.Millisecond))
		}
	}
	median := func(values []time.Duration) time.Duration {
		sorted := append([]time.Duration(nil), values...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		return sorted[len(sorted)/2]
	}
	if oneAtATime := elapsedByLimit[1]; len(oneAtATime) > 0 {
		for _, limit := range limits {
			if limit != 1 && median(elapsedByLimit[limit]) < median(oneAtATime) {
				t.Logf("batch_limit=%d median_capture=%s vs one-at-a-time=%s", limit, median(elapsedByLimit[limit]), median(oneAtATime))
			}
		}
	}
	for _, limit := range limits {
		t.Logf("batch_limit=%d median_capture=%s median_calls=%d median_element_draw_calls=%d", limit, median(elapsedByLimit[limit]), medianInt(callsByLimit[limit]), medianInt(drawCallsByLimit[limit]))
	}
	if len(pairedDeltas) > 0 {
		t.Logf("paired_batch_delta_median_ms=%.3f", float64(median(pairedDeltas))/float64(time.Millisecond))
	}
	compressionElapsed := map[string][]time.Duration{"none": {}, "deflate": {}}
	compressionPayload := map[string][]int64{"none": {}, "deflate": {}}
	compressionOrder := []string{"none", "deflate"}
	for repeat := 0; repeat < 3; repeat++ {
		order := append([]string(nil), compressionOrder...)
		if repeat%2 == 1 {
			order[0], order[1] = order[1], order[0]
		}
		for _, compression := range order {
			seed := uint32(0x4e49434f)
			compressionOptions := options
			compressionOptions.SpriteCompression = compression
			started := time.Now()
			scene, report, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, compressionOptions, &seed, timelineCaptureBatchDefault)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("capture with sprite compression %q: %v", compression, err)
			}
			if !reflect.DeepEqual(reference, scene) {
				t.Fatalf("sprite compression %q changed the seeded timeline scene", compression)
			}
			compressionElapsed[compression] = append(compressionElapsed[compression], elapsed)
			compressionPayload[compression] = append(compressionPayload[compression], report.BrowserAssetPayloadBytes)
			t.Logf("sprite_compression=%s capture_calls=%d payload_bytes=%d elapsed=%s", compression, report.CaptureBatchCalls, report.BrowserAssetPayloadBytes, elapsed)
		}
	}
	for _, compression := range compressionOrder {
		payloadValues := append([]int64(nil), compressionPayload[compression]...)
		sort.Slice(payloadValues, func(i, j int) bool { return payloadValues[i] < payloadValues[j] })
		t.Logf("sprite_compression=%s median_capture=%s median_payload_bytes=%d", compression, median(compressionElapsed[compression]), payloadValues[len(payloadValues)/2])
	}
}

func medianInt(values []int) int {
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

func spreadTimelineCaptureBatchComments(snapshot niconico.Snapshot, durationMs int64) (niconico.Snapshot, error) {
	count := 0
	for _, thread := range snapshot.Threads {
		count += len(thread.Comments)
	}
	if durationMs < 2 || count < 2 {
		return niconico.Snapshot{}, fmt.Errorf("high-density capture profile needs at least two comments and a duration of at least 2ms")
	}
	index := 0
	for threadIndex := range snapshot.Threads {
		for commentIndex := range snapshot.Threads[threadIndex].Comments {
			snapshot.Threads[threadIndex].Comments[commentIndex].VposMs = int64(index) * (durationMs - 1) / int64(count-1)
			index++
		}
	}
	return snapshot, nil
}
