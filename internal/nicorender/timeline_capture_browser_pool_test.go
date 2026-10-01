package nicorender

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

func TestTimelineCaptureNilPoolUsesFreshBrowserStarter(t *testing.T) {
	backend := newFakePoolCDP()
	backend.pageCall = timelineCaptureFakeCall(t)
	options := timelinePoolTestOptions()
	called := false
	scene, _, err := captureCommentTimelineWithCaptureLimitsAndStarter(
		context.Background(), timelinePoolTestSnapshot(), options, ptrUint32(77),
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax,
		func(_ context.Context, configured, pagePath string) (*browserSession, error) {
			called = true
			if configured != options.BrowserPath {
				t.Fatalf("fresh BrowserPath = %q, want %q", configured, options.BrowserPath)
			}
			if _, err := os.Stat(pagePath); err != nil {
				t.Fatalf("fresh page path unavailable: %v", err)
			}
			return &browserSession{pooledCall: backend.pageCall}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("nil pool did not use the fresh browser starter")
	}
	if len(scene.Draws) != 1 || len(scene.Assets) != 1 {
		t.Fatalf("fake fresh scene has draws/assets %d/%d, want 1/1", len(scene.Draws), len(scene.Assets))
	}
}

func TestTimelineCapturePooledSceneMatchesFreshAndReleasesBeforeTempPageRemoval(t *testing.T) {
	options := timelinePoolTestOptions()
	snapshot := timelinePoolTestSnapshot()
	seed := uint32(9127)

	var freshPage []byte
	freshScene, _, err := captureCommentTimelineWithCaptureLimitsAndStarter(
		context.Background(), snapshot, options, &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax,
		func(_ context.Context, _, pagePath string) (*browserSession, error) {
			data, err := os.ReadFile(pagePath)
			if err != nil {
				return nil, err
			}
			freshPage = data
			return &browserSession{pooledCall: timelineCaptureFakeCall(t)}, nil
		},
	)
	if err != nil {
		t.Fatalf("fresh capture: %v", err)
	}

	backend := newFakePoolCDP()
	backend.pageCall = timelineCaptureFakeCall(t)
	var pooledPage []byte
	backend.onPageClose = func(pagePath string) {
		if _, err := os.Stat(pagePath); err != nil {
			t.Errorf("temporary page was removed before lease release: %v", err)
			return
		}
		data, err := os.ReadFile(pagePath)
		if err != nil {
			t.Errorf("read pooled renderer page during release: %v", err)
			return
		}
		pooledPage = data
	}
	pool, err := newBrowserPool(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close fake pool: %v", err)
		}
	}()
	pooledScene, _, err := captureCommentTimelineWithCaptureLimits(
		context.Background(), snapshot, options.WithBrowserPool(pool), &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax,
	)
	if err != nil {
		t.Fatalf("pooled capture: %v", err)
	}
	bundleScript := regexp.MustCompile(`<script src="[^"]+"></script>`)
	if !bytes.Equal(bundleScript.ReplaceAll(freshPage, []byte(`<script src="<bundle>"></script>`)), bundleScript.ReplaceAll(pooledPage, []byte(`<script src="<bundle>"></script>`))) {
		t.Fatal("seeded fresh and pooled renderer page bytes differ after normalizing the per-run bundle path")
	}
	var freshBytes, pooledBytes bytes.Buffer
	if err := WriteCommentTimeline(&freshBytes, freshScene); err != nil {
		t.Fatal(err)
	}
	if err := WriteCommentTimeline(&pooledBytes, pooledScene); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(freshBytes.Bytes(), pooledBytes.Bytes()) {
		t.Fatal("same-input fresh and pooled timeline scene bytes differ")
	}
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("pooled context create/dispose = %v/%v", backend.created, backend.disposed)
	}
	if len(backend.pageSessions) != 1 || backend.pageSessions[0].closed != 1 {
		t.Fatalf("pooled page session close count = %+v, want exactly one", backend.pageSessions)
	}
}

func TestTimelineCapturePooledEmptyDenseEmptySkipsBrowserLeaseForEmptySnapshots(t *testing.T) {
	backend := newFakePoolCDP()
	backend.pageCall = timelineCaptureFakeCall(t)
	pool, err := newBrowserPool(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := pool.Close(); err != nil {
			t.Errorf("close fake pool: %v", err)
		}
	}()
	options := timelinePoolTestOptions().WithBrowserPool(pool)
	empty, err := niconico.NormalizeSnapshot(niconico.Snapshot{VideoID: "sm-empty-pool"})
	if err != nil {
		t.Fatal(err)
	}

	captureEmpty := func(label string) {
		t.Helper()
		scene, _, err := captureCommentTimelineWithCaptureLimits(
			context.Background(), empty, options, ptrUint32(77),
			timelineCaptureBatchDefault, timelineCaptureBatchTargetMax,
		)
		if err != nil {
			t.Fatalf("%s empty capture: %v", label, err)
		}
		if len(scene.Assets) != 0 || len(scene.Draws) != 0 {
			t.Fatalf("%s empty scene has %d assets/%d draws", label, len(scene.Assets), len(scene.Draws))
		}
		if len(backend.created) != 1 || len(backend.disposed) != 1 || len(backend.pageSessions) != 1 || backend.pageSessions[0].closed != 1 || pool.ActiveLease() != nil {
			t.Fatalf("%s empty capture changed browser lifecycle: created=%v disposed=%v pages=%+v active=%v", label, backend.created, backend.disposed, backend.pageSessions, pool.ActiveLease())
		}
	}

	// An empty snapshot takes the zero-browser fast path before and after a
	// dense capture, so it must not consume or perturb the persistent pool.
	firstEmpty, _, err := captureCommentTimelineWithCaptureLimits(
		context.Background(), empty, options, ptrUint32(77),
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax,
	)
	if err != nil {
		t.Fatalf("leading empty capture: %v", err)
	}
	if len(firstEmpty.Assets) != 0 || len(firstEmpty.Draws) != 0 || len(backend.created) != 0 || len(backend.disposed) != 0 || len(backend.pageSessions) != 0 {
		t.Fatalf("leading empty capture used the browser: scene=%d/%d contexts=%v/%v pages=%+v", len(firstEmpty.Assets), len(firstEmpty.Draws), backend.created, backend.disposed, backend.pageSessions)
	}

	denseScene, _, err := captureCommentTimelineWithCaptureLimits(
		context.Background(), timelinePoolTestSnapshot(), options, ptrUint32(77),
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax,
	)
	if err != nil {
		t.Fatalf("dense capture: %v", err)
	}
	if len(denseScene.Assets) != 1 || len(denseScene.Draws) != 1 {
		t.Fatalf("dense scene has %d assets/%d draws, want 1/1", len(denseScene.Assets), len(denseScene.Draws))
	}
	if len(backend.created) != 1 || len(backend.disposed) != 1 || len(backend.pageSessions) != 1 || backend.pageSessions[0].closed != 1 || pool.ActiveLease() != nil {
		t.Fatalf("dense capture lifecycle: created=%v disposed=%v pages=%+v active=%v", backend.created, backend.disposed, backend.pageSessions, pool.ActiveLease())
	}

	captureEmpty("trailing")
}

func TestTimelineCapturePooledErrorAndCancellationReleaseLease(t *testing.T) {
	t.Run("release failure is returned", func(t *testing.T) {
		backend := newFakePoolCDP()
		backend.pageCall = timelineCaptureFakeCall(t)
		backend.disposeErr = errors.New("fake dispose failure")
		pool, err := newBrowserPool(context.Background(), backend)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		_, _, err = captureCommentTimelineWithCaptureLimits(context.Background(), timelinePoolTestSnapshot(), timelinePoolTestOptions().WithBrowserPool(pool), ptrUint32(77), timelineCaptureBatchDefault, timelineCaptureBatchTargetMax)
		if err == nil || !strings.Contains(err.Error(), "fake dispose failure") {
			t.Fatalf("capture error = %v, want disposal failure surfaced", err)
		}
		if backend.disposeCalls != 1 || len(backend.pageSessions) != 1 || backend.pageSessions[0].closed != 1 || pool.ActiveLease() != nil {
			t.Fatalf("failed release lifecycle: dispose=%d pages=%+v active=%v", backend.disposeCalls, backend.pageSessions, pool.ActiveLease())
		}
	})

	t.Run("capture error", func(t *testing.T) {
		backend := newFakePoolCDP()
		backend.pageCall = func(_ context.Context, method string, params map[string]any) (json.RawMessage, error) {
			if expression, _ := params["expression"].(string); strings.Contains(expression, "ready:Boolean(window.__niconiReady)") {
				return timelineRuntimeValue(map[string]any{"ready": false, "error": "fake initialization failure"}), nil
			}
			return timelineCaptureFakeCall(t)(context.Background(), method, params)
		}
		assertTimelineCaptureReleasesLease(t, backend, context.Background(), errors.New("fake initialization failure"))
	})

	t.Run("job cancellation", func(t *testing.T) {
		backend := newFakePoolCDP()
		readyStarted := make(chan struct{})
		backend.pageCall = func(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
			if expression, _ := params["expression"].(string); strings.Contains(expression, "ready:Boolean(window.__niconiReady)") {
				close(readyStarted)
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return timelineCaptureFakeCall(t)(ctx, method, params)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			pool, err := newBrowserPool(context.Background(), backend)
			if err != nil {
				done <- err
				return
			}
			defer pool.Close()
			_, _, err = captureCommentTimelineWithCaptureLimits(ctx, timelinePoolTestSnapshot(), timelinePoolTestOptions().WithBrowserPool(pool), ptrUint32(77), timelineCaptureBatchDefault, timelineCaptureBatchTargetMax)
			done <- err
		}()
		select {
		case <-readyStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("capture did not reach the cancellable Runtime.evaluate call")
		}
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("capture error = %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("canceled capture did not settle")
		}
		assertFakePoolLeaseSettled(t, backend)
	})
}

func TestRenderOptionsBrowserPoolIsAbsentFromJSON(t *testing.T) {
	field, ok := reflect.TypeOf(RenderOptions{}).FieldByName("browserPool")
	if !ok {
		t.Fatal("RenderOptions has no private browserPool field")
	}
	if field.PkgPath == "" {
		t.Fatal("browserPool is exported and would appear in JSON")
	}
}

func assertTimelineCaptureReleasesLease(t *testing.T, backend *fakePoolCDP, ctx context.Context, wantErr error) {
	t.Helper()
	pool, err := newBrowserPool(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, _, err = captureCommentTimelineWithCaptureLimits(ctx, timelinePoolTestSnapshot(), timelinePoolTestOptions().WithBrowserPool(pool), ptrUint32(77), timelineCaptureBatchDefault, timelineCaptureBatchTargetMax)
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("capture error = %v, want %q", err, wantErr)
	}
	assertFakePoolLeaseSettled(t, backend)
}

func assertFakePoolLeaseSettled(t *testing.T, backend *fakePoolCDP) {
	t.Helper()
	if len(backend.created) != 1 || len(backend.disposed) != 1 || backend.created[0] != backend.disposed[0] {
		t.Fatalf("lease context create/dispose = %v/%v", backend.created, backend.disposed)
	}
	if backend.disposeCalls != 1 || len(backend.pageSessions) != 1 || backend.pageSessions[0].closed != 1 {
		t.Fatalf("lease cleanup counts: dispose=%d pageSessions=%+v", backend.disposeCalls, backend.pageSessions)
	}
}

func timelinePoolTestOptions() RenderOptions {
	return RenderOptions{Width: 64, Height: 36, DurationMs: 1000, FPSNum: 30, FPSDen: 1, BrowserPath: "fake-browser"}
}

func timelinePoolTestSnapshot() niconico.Snapshot {
	return niconico.Snapshot{
		SchemaVersion: 1, VideoID: "sm-timeline-pool", CommentStatus: niconico.CommentStatusReady, CommentCount: 1,
		Threads: []niconico.Thread{{Fork: "main", ID: "thread", Comments: []niconico.Comment{{ID: "comment", VposMs: 0, Body: "hello"}}}},
	}
}

func timelineCaptureFakeCall(t *testing.T) func(context.Context, string, map[string]any) (json.RawMessage, error) {
	t.Helper()
	texture := spriteJSONTexture{ID: 1, Width: 1, Height: 1, Encoding: "rgba", Data: base64.StdEncoding.EncodeToString([]byte{255, 0, 0, 255})}
	command := spriteJSONCommand{
		ID: 1, Rect: [4]float32{20, 20, 10, 10},
		Proj:  [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1},
		Color: [4]float32{1, 1, 1, 1},
	}
	description := timelineDescribeResult{
		OK:               true,
		Comments:         []timelineCommentMeta{{Index: 0, OwnerOrder: 0, Loc: "ue", StartVPos: 0, EndVPos: 100, X: 20, Y: 20, Width: 10, Height: 10}},
		ElementDrawCalls: 1,
	}
	batch := timelineCaptureBatchResult{Consumed: 1, Items: []timelineCaptureElementResult{{
		Index: 0, Samples: []timelineCaptureSample{{VPos: 48, X: 20, Y: 20, Commands: []spriteJSONCommand{command}, Textures: []spriteJSONTexture{texture}}},
		ElementDrawCalls: 1, SpriteMetrics: &browserSpriteCaptureMetrics{Mode: "sync", TextureCreations: 1},
	}}}
	return func(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if method != "Runtime.evaluate" {
			return nil, errors.New("unexpected pooled CDP method: " + method)
		}
		expression, _ := params["expression"].(string)
		switch {
		case strings.Contains(expression, "ready:Boolean(window.__niconiReady)"):
			return timelineRuntimeValue(map[string]any{"ready": true, "error": ""}), nil
		case strings.Contains(expression, "window.__niconi.comments ? window.__niconi.comments.length"):
			return timelineRuntimeValue(1), nil
		case strings.Contains(expression, "window.__nicoTimelineInstallError"):
			return timelineRuntimeValue(""), nil
		case strings.Contains(expression, "window.__nicoTimelineDescribe"):
			return timelineRuntimeValue(description), nil
		case strings.Contains(expression, "window.__nicoTimelineCaptureBatch"):
			return timelineRuntimeValue(batch), nil
		default:
			return timelineRuntimeValue(nil), nil
		}
	}
}

func timelineRuntimeValue(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	result, _ := json.Marshal(map[string]any{"result": map[string]any{"value": json.RawMessage(encoded)}})
	return result
}

func ptrUint32(value uint32) *uint32 { return &value }
