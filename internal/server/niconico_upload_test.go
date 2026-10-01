package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/nicoexportworker"
	"imagepadserver/internal/niconico"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

func TestWaitForNiconicoIngestQueuesAndHonorsCancellation(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	if !srv.tryBeginIngest(ingestDownloading, "first") {
		t.Fatal("failed to occupy ingest slot")
	}
	release := time.AfterFunc(45*time.Millisecond, srv.clearIngest)
	waited, err := srv.waitForNiconicoIngest(context.Background(), "second")
	release.Stop()
	if err != nil {
		t.Fatal(err)
	}
	if waited < 30*time.Millisecond {
		t.Fatalf("waited = %s, want queued wait", waited)
	}
	srv.clearIngest()

	if !srv.tryBeginIngest(ingestDownloading, "first-again") {
		t.Fatal("failed to occupy ingest slot for cancellation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	_, err = srv.waitForNiconicoIngest(ctx, "canceled")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline", err)
	}
	srv.clearIngest()
}

func TestNiconicoTargetGeometryPreservesAspectAndEvenDimensions(t *testing.T) {
	width, height := niconicoTargetGeometry(video.MediaProbe{Streams: []video.MediaStream{{CodecType: "video", Width: 1920, Height: 1080}}}, 720)
	if width != 1280 || height != 720 {
		t.Fatalf("geometry = %dx%d, want 1280x720", width, height)
	}
	width, height = niconicoTargetGeometry(video.MediaProbe{Streams: []video.MediaStream{{CodecType: "video", Width: 1080, Height: 1920}}}, 360)
	if width%2 != 0 || height%2 != 0 || width <= 0 || height != 360 {
		t.Fatalf("vertical geometry = %dx%d", width, height)
	}
}

func TestNicoEncoderForMode(t *testing.T) {
	for mode, want := range map[string]string{
		"cpu":    "x264",
		"gpu":    "nvenc",
		"auto":   "nvenc",
		"broken": "nvenc",
	} {
		if got := nicoEncoderForMode(mode); got != want {
			t.Errorf("nicoEncoderForMode(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestIsNicoNVENCFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nvenc device unavailable", err: errors.New("h264_nvenc: No capable devices found"), want: true},
		{name: "missing driver library", err: errors.New("Cannot load libnvidia-encode.so.1"), want: true},
		{name: "compositor failure", err: errors.New("timeline compositor exited"), want: false},
		{name: "nil", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNicoNVENCFailure(tc.err); got != tc.want {
				t.Fatalf("isNicoNVENCFailure(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestWriteNicoSnapshotUsesPrivateSidecarName(t *testing.T) {
	dir := t.TempDir()
	snapshot := niconico.Snapshot{SchemaVersion: 1, VideoID: "sm9", CommentStatus: niconico.CommentStatusEmpty}
	if err := writeNicoSnapshot(dir, "media-1", snapshot); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "niconico-snapshot-media-1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got niconico.Snapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.VideoID != "sm9" || !strings.HasSuffix(path, ".json") {
		t.Fatalf("snapshot = %#v path=%q", got, path)
	}
}

func TestWriteNicoSnapshotPathRemovesTemporaryFileWhenFinalRenameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snapshot.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := niconico.Snapshot{SchemaVersion: 1, VideoID: "sm9", CommentStatus: niconico.CommentStatusEmpty}
	if err := writeNicoSnapshotPath(path, snapshot); err == nil {
		t.Fatal("snapshot write unexpectedly succeeded")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary snapshot survived failed rename: %v", err)
	}
}

func TestProcessPreparedNicoVideoKeepsRenderedFileAfterPublish(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	source := filepath.Join(store.Dir(), "niconico-commented-rendered.mp4")
	thumbnail := filepath.Join(store.Dir(), "niconico-thumb.jpg")
	if err := os.WriteFile(source, []byte("rendered-mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbnail, []byte("thumbnail"), 0600); err != nil {
		t.Fatal(err)
	}
	previousEnqueue := enqueueNiconicoCommentedVideo
	enqueueNiconicoCommentedVideo = func(string, string, string, string, int) string { return "" }
	t.Cleanup(func() { enqueueNiconicoCommentedVideo = previousEnqueue })

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/upload-url", nil)
	snapshot := niconico.Snapshot{SchemaVersion: 1, VideoID: "sm9", CommentStatus: niconico.CommentStatusEmpty}
	if _, err := srv.processPreparedNicoVideo(req, source, "sm9.mp4", thumbnail, snapshot, false); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	if current == nil || current.FileName != filepath.Base(source) {
		t.Fatalf("current = %#v", current)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), current.FileName)); err != nil {
		t.Fatalf("rendered file was removed during publish: %v", err)
	}
}

func TestHandleUploadURLNiconicoCommentsPublishesPreparedMedia(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	// This handler test injects a one-shot worker function, so select that route explicitly.
	t.Setenv("IMAGEPAD_NICO_WORKER_SESSION", "0")
	t.Setenv("IMAGEPAD_NICO_ENCODER", "")
	appSettings, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	appSettings.EncoderMode = "gpu"
	if err := settings.Save(appSettings); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_NICO_OUTPUT_MODE", "tee")
	t.Setenv("IMAGEPAD_NICO_RENDERER", "auto")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_ENABLED", "true")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_COMPOSITOR", "timeline-helper")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS", "2")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_GPU_BACKEND", "vulkan")

	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "source.mp4")
	if err := os.WriteFile(source, []byte("downloaded-source"), 0600); err != nil {
		t.Fatal(err)
	}

	oldFetch := fetchNiconicoSnapshot
	oldDownload := pageMediaDownloader
	oldEnsureFFmpeg := ensureFFmpeg
	oldEnsureFFprobe := niconicoEnsureFFprobe
	oldProbe := niconicoProbeMedia
	oldWorker := niconicoWorkerRunner
	t.Cleanup(func() {
		fetchNiconicoSnapshot = oldFetch
		pageMediaDownloader = oldDownload
		ensureFFmpeg = oldEnsureFFmpeg
		niconicoEnsureFFprobe = oldEnsureFFprobe
		niconicoProbeMedia = oldProbe
		niconicoWorkerRunner = oldWorker
	})
	fetchNiconicoSnapshot = func(context.Context, string) (niconico.Snapshot, error) {
		return niconico.Snapshot{SchemaVersion: 1, VideoID: "sm9", CommentStatus: niconico.CommentStatusReady, CommentCount: 1}, nil
	}
	pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
		return video.DownloadedMedia{SourcePath: source, Name: "source.mp4"}, nil
	}
	ensureFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	niconicoEnsureFFprobe = func() (string, error) { return "fake-ffprobe", nil }
	niconicoProbeMedia = func(context.Context, string, string) (video.MediaProbe, error) {
		return video.MediaProbe{
			Duration: 1,
			Streams:  []video.MediaStream{{CodecType: "video", Width: 640, Height: 360}},
		}, nil
	}
	workerCalls := 0
	workerOutputMode := ""
	var workerRequest nicoexportworker.Request
	var workerRequests []nicoexportworker.Request
	niconicoWorkerRunner = func(_ context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
		workerCalls++
		workerRequest = request
		workerRequests = append(workerRequests, request)
		workerOutputMode = request.OutputMode
		if request.Encoder == "nvenc" {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, errors.New("ffmpeg h264_nvenc: No capable devices found")
		}
		if err := os.WriteFile(request.OutputPath, []byte("rendered-mp4"), 0600); err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
		}
		if err := os.MkdirAll(request.HLSStagingDir, 0700); err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
		}
		segment := filepath.Join(request.HLSStagingDir, "segment-00000.ts")
		playlist := filepath.Join(request.HLSStagingDir, "playlist.m3u8")
		if err := os.WriteFile(segment, []byte("segment"), 0600); err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
		}
		if err := os.WriteFile(playlist, []byte("#EXTM3U\n#EXTINF:1.000,\nsegment-00000.ts\n#EXT-X-ENDLIST\n"), 0600); err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
		}
		return nicoexportworker.Event{
			Version: nicoexportworker.ProtocolVersion, Type: "result", OK: true,
			RunID: request.RunID, MediaID: request.MediaID, Output: request.OutputPath, Playlist: playlist,
			FallbackNotice: "WGPUコメント描画に失敗したため、CPU描画へ切り替えました",
		}, nicoexportbudget.Report{Verified: true, LogicalCPUs: 8, JobCPURate: 2000}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.nicovideo.jp/watch/sm9","niconicoComments":{"enabled":true}}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if workerCalls != 2 {
		t.Fatalf("worker calls = %d, want NVENC attempt plus CPU fallback", workerCalls)
	}
	if workerOutputMode != "tee" {
		t.Fatalf("worker output mode = %q, want tee", workerOutputMode)
	}
	if workerRequest.Backend != "auto" || !workerRequest.TimelineEnabled || workerRequest.TimelineCompositor != "timeline-helper" || workerRequest.TimelineReadbackSlots != 2 || workerRequest.TimelineGPUBackend != "vulkan" {
		t.Fatalf("timeline settings were not sent to worker: %+v", workerRequest)
	}
	if len(workerRequests) != 2 {
		t.Fatalf("worker requests = %d, want NVENC attempt plus CPU fallback", len(workerRequests))
	}
	if workerRequests[0].Encoder != "nvenc" || workerRequests[1].Encoder != "x264" {
		t.Fatalf("worker encoders = [%s %s], want [nvenc x264]", workerRequests[0].Encoder, workerRequests[1].Encoder)
	}
	if !strings.Contains(rec.Body.String(), "NVENCに失敗したためCPU/libx264へ切り替えました") {
		t.Fatalf("fallback notification missing from response: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "WGPUコメント描画に失敗したため、CPU描画へ切り替えました") {
		t.Fatalf("renderer fallback notification missing from response: %q", rec.Body.String())
	}
	current := srv.store.Current()
	if current == nil || current.Kind != "video" || current.ID == "" {
		t.Fatalf("current = %#v", current)
	}
	if current.PublicName != "current-video.mp4" || !current.Converted || !current.Published {
		t.Fatalf("current metadata = %#v", current)
	}
	if _, err := os.Stat(filepath.Join(srv.store.Dir(), current.FileName)); err != nil {
		t.Fatalf("published MP4 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srv.store.Dir(), "current-"+current.ID+".m3u8")); err != nil {
		t.Fatalf("published HLS playlist missing: %v", err)
	}
	if _, _, ok := srv.store.ConvertedPath(current.ID); !ok {
		t.Fatalf("converted HLS tree missing for %s", current.ID)
	}
	for _, item := range srv.store.History() {
		if item.ID == current.ID {
			return
		}
	}
	t.Fatalf("current %s was not registered in history", current.ID)
}

func TestHandleUploadURLNiconicoCommentsWorkerFailureKeepsPreviousPublication(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	// This handler test injects a one-shot worker function, so select that route explicitly.
	t.Setenv("IMAGEPAD_NICO_WORKER_SESSION", "0")

	previousPath := filepath.Join(srv.store.Dir(), "previous-current.mp4")
	previousData := []byte("previous-current-publication")
	if err := os.WriteFile(previousPath, previousData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetCurrentInfo(library.CurrentImage{
		Kind: "video", FileName: filepath.Base(previousPath), PublicName: "current-video.mp4",
		ContentType: "video/mp4", OriginalName: "previous.mp4",
	}); err != nil {
		t.Fatal(err)
	}
	previous := srv.store.Current()
	if previous == nil || previous.ID == "" {
		t.Fatal("failed to establish previous publication")
	}

	source := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(source, []byte("downloaded-source"), 0600); err != nil {
		t.Fatal(err)
	}
	oldFetch := fetchNiconicoSnapshot
	oldDownload := pageMediaDownloader
	oldEnsureFFmpeg := ensureFFmpeg
	oldEnsureFFprobe := niconicoEnsureFFprobe
	oldProbe := niconicoProbeMedia
	oldWorker := niconicoWorkerRunner
	t.Cleanup(func() {
		fetchNiconicoSnapshot = oldFetch
		pageMediaDownloader = oldDownload
		ensureFFmpeg = oldEnsureFFmpeg
		niconicoEnsureFFprobe = oldEnsureFFprobe
		niconicoProbeMedia = oldProbe
		niconicoWorkerRunner = oldWorker
	})
	fetchNiconicoSnapshot = func(context.Context, string) (niconico.Snapshot, error) {
		return niconico.Snapshot{SchemaVersion: 1, VideoID: "sm9", CommentStatus: niconico.CommentStatusReady, CommentCount: 1}, nil
	}
	pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
		return video.DownloadedMedia{SourcePath: source, Name: "source.mp4"}, nil
	}
	ensureFFmpeg = func() (string, error) { return "fake-ffmpeg", nil }
	niconicoEnsureFFprobe = func() (string, error) { return "fake-ffprobe", nil }
	niconicoProbeMedia = func(context.Context, string, string) (video.MediaProbe, error) {
		return video.MediaProbe{Duration: 1, Streams: []video.MediaStream{{CodecType: "video", Width: 640, Height: 360}}}, nil
	}
	niconicoWorkerRunner = func(_ context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
		if err := os.WriteFile(request.OutputPath, []byte("partial-mp4"), 0600); err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
		}
		if err := os.MkdirAll(request.HLSStagingDir, 0700); err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
		}
		if err := os.WriteFile(filepath.Join(request.HLSStagingDir, "partial.ts"), []byte("partial"), 0600); err != nil {
			return nicoexportworker.Event{}, nicoexportbudget.Report{}, err
		}
		return nicoexportworker.Event{}, nicoexportbudget.Report{Verified: true}, errors.New("injected worker failure")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.nicovideo.jp/watch/sm9","niconicoComments":{"enabled":true}}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "injected worker failure") {
		t.Fatalf("failed worker response = status %d body %q", rec.Code, rec.Body.String())
	}
	current := srv.store.Current()
	if current == nil || current.ID != previous.ID || current.FileName != previous.FileName {
		t.Fatalf("current changed after worker failure: before=%#v after=%#v", previous, current)
	}
	currentData, err := os.ReadFile(filepath.Join(srv.store.Dir(), current.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(currentData) != string(previousData) {
		t.Fatalf("previous publication changed: %q", currentData)
	}
	entries, err := os.ReadDir(srv.store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".niconico-export-") {
			t.Fatalf("staging survived worker failure: %s", entry.Name())
		}
	}
}

func TestHandleUploadURLNiconicoCommentsWithRealWorker(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICO_HTTP_WORKER_TEST=1 for real worker HTTP E2E")
	}
	workerExe := os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_EXE")
	if workerExe == "" {
		workerExe = filepath.Join("..", "..", "build", "nico-cpu20-tools", "imagepadserver-worker-current.exe")
	}
	if _, err := os.Stat(workerExe); err != nil {
		t.Skipf("real worker executable is unavailable: %v", err)
	}
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		t.Skip("IMAGEPAD_FFMPEG is not set")
	}
	source, snapshot := makeNicoHTTPWorkerFixture(t, ffmpeg)

	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	oldFetch := fetchNiconicoSnapshot
	oldDownload := pageMediaDownloader
	oldEnsureFFmpeg := ensureFFmpeg
	oldEnsureFFprobe := niconicoEnsureFFprobe
	oldProbe := niconicoProbeMedia
	oldWorker := niconicoWorkerRunner
	oldExecutable := nicoWorkerExecutable
	t.Cleanup(func() {
		fetchNiconicoSnapshot = oldFetch
		pageMediaDownloader = oldDownload
		ensureFFmpeg = oldEnsureFFmpeg
		niconicoEnsureFFprobe = oldEnsureFFprobe
		niconicoProbeMedia = oldProbe
		niconicoWorkerRunner = oldWorker
		nicoWorkerExecutable = oldExecutable
	})
	fetchNiconicoSnapshot = func(context.Context, string) (niconico.Snapshot, error) { return snapshot, nil }
	pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
		return video.DownloadedMedia{SourcePath: source, Name: "fixture.mp4"}, nil
	}
	ensureFFmpeg = func() (string, error) { return ffmpeg, nil }
	niconicoEnsureFFprobe = func() (string, error) { return "unused-ffprobe", nil }
	niconicoProbeMedia = func(context.Context, string, string) (video.MediaProbe, error) {
		return video.MediaProbe{Duration: 1, Streams: []video.MediaStream{{CodecType: "video", Width: 640, Height: 360}}}, nil
	}
	workerBackend := os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_BACKEND")
	missingCompositor := filepath.Join(t.TempDir(), "missing-nico-compositor.exe")
	var workerReport nicoexportbudget.Report
	var workerEvent nicoexportworker.Event
	var workerRequest nicoexportworker.Request
	niconicoWorkerRunner = func(ctx context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
		if workerBackend == "auto-fallback" {
			request.Backend = "auto"
			request.Compositor = missingCompositor
		} else if workerBackend == "late-nct2-failure" {
			request.Backend = "auto"
			request.Compositor = os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_COMPOSITOR")
		}
		workerRequest = request
		result, report, err := runNicoWorkerWithBudget(ctx, request)
		workerEvent = result
		workerReport = report
		return result, report, err
	}
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.nicovideo.jp/watch/sm9","niconicoComments":{"enabled":true}}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if workerBackend == "auto-fallback" || workerBackend == "late-nct2-failure" {
		const fallbackNotice = "WGPUコメント描画に失敗したため、CPU描画へ切り替えました"
		if workerRequest.Backend != "auto" || !workerRequest.TimelineEnabled || workerRequest.TimelineReadbackSlots != 2 || workerRequest.TimelineGPUBackend != "vulkan" || workerRequest.TimelineCompositor == "" {
			t.Fatalf("real worker did not receive timeline auto-fallback options: %+v", workerRequest)
		}
		if !workerEvent.TimelineFallback || workerEvent.FallbackNotice != fallbackNotice {
			t.Fatalf("real worker terminal event fallback = (%v, %q), want timeline fallback and %q", workerEvent.TimelineFallback, workerEvent.FallbackNotice, fallbackNotice)
		}
		if got := strings.Count(rec.Body.String(), fallbackNotice); got != 1 {
			t.Fatalf("HTTP fallback notice count = %d, want 1; body=%q", got, rec.Body.String())
		}
	}
	if workerBackend == "late-nct2-failure" {
		attempt := workerEvent.TimelineAttempt
		if attempt == nil || attempt.Protocol != "NCT2" || attempt.StreamEnd != nil || attempt.FirstAssetReady == nil {
			t.Fatalf("late helper failure attempt = %+v, want NCT2 asset-ready without stream-end", attempt)
		}
		if want := os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_COMPOSITOR"); workerRequest.TimelineCompositor != want {
			t.Fatalf("worker compositor = %q, want fault-injection helper %q", workerRequest.TimelineCompositor, want)
		}
	}
	if !workerReport.Verified || workerReport.JobCPURate != 10000 {
		t.Fatalf("production worker CPU allowance = %+v, want verified 100%% machine capacity", workerReport)
	}
	t.Logf("production worker CPU allowance verified: rate=%d cpu_seconds=%.3f", workerReport.JobCPURate, workerReport.CPUSeconds)
	current := srv.store.Current()
	if current == nil || current.Kind != "video" || current.ID == "" || !current.Converted || !current.Published {
		t.Fatalf("real worker current = %#v", current)
	}
	for _, path := range []string{
		filepath.Join(srv.store.Dir(), current.FileName),
		filepath.Join(srv.store.Dir(), "current-"+current.ID+".m3u8"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("real worker publication missing %s: %v", path, err)
		}
	}
}

func TestHandleUploadURLNiconicoCommentsWithRealWorkerAutoFallback(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICO_HTTP_WORKER_TEST=1 for real worker HTTP fallback E2E")
	}
	t.Setenv("IMAGEPAD_NICO_HTTP_WORKER_TEST", "1")
	t.Setenv("IMAGEPAD_NICO_HTTP_WORKER_BACKEND", "auto-fallback")
	t.Setenv("IMAGEPAD_NICO_RENDERER", "auto")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_ENABLED", "true")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS", "2")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_GPU_BACKEND", "vulkan")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_COMPOSITOR", filepath.Join(t.TempDir(), "missing-timeline-compositor.exe"))
	TestHandleUploadURLNiconicoCommentsWithRealWorker(t)
}

func TestHandleUploadURLNiconicoCommentsWithRealWorkerLateNCT2Failure(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICO_HTTP_WORKER_TEST=1 for real worker late NCT2 failure E2E")
	}
	t.Setenv("IMAGEPAD_NICO_HTTP_WORKER_TEST", "1")
	t.Setenv("IMAGEPAD_NICO_HTTP_WORKER_BACKEND", "late-nct2-failure")
	t.Setenv("IMAGEPAD_NICO_RENDERER", "auto")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_ENABLED", "true")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_READBACK_SLOTS", "2")
	t.Setenv("IMAGEPAD_NICO_TIMELINE_GPU_BACKEND", "vulkan")
	compositor := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_COMPOSITOR"))
	if compositor == "" {
		t.Fatal("IMAGEPAD_NICO_HTTP_WORKER_COMPOSITOR is required for late NCT2 failure E2E")
	}
	t.Setenv("IMAGEPAD_NICO_TIMELINE_COMPOSITOR", compositor)
	TestHandleUploadURLNiconicoCommentsWithRealWorker(t)
}

func TestHandleUploadURLNiconicoCommentsCancellationRemovesStagingAndKeepsCurrent(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICO_HTTP_WORKER_TEST=1 for real worker HTTP cancellation E2E")
	}
	workerExe := os.Getenv("IMAGEPAD_NICO_HTTP_WORKER_EXE")
	if workerExe == "" {
		workerExe = filepath.Join("..", "..", "build", "nico-cpu20-tools", "imagepadserver-worker-current.exe")
	}
	if _, err := os.Stat(workerExe); err != nil {
		t.Skipf("real worker executable is unavailable: %v", err)
	}
	workerExe = copyNicoHTTPWorkerExecutable(t, workerExe)
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		t.Skip("IMAGEPAD_FFMPEG is not set")
	}
	source, snapshot := makeNicoHTTPWorkerFixture(t, ffmpeg)

	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	oldCurrentPath := filepath.Join(srv.store.Dir(), "old-current.mp4")
	oldCurrentData := []byte("previous-current-publication")
	if err := os.WriteFile(oldCurrentPath, oldCurrentData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetCurrentInfo(library.CurrentImage{
		Kind: "video", FileName: filepath.Base(oldCurrentPath), PublicName: "current-video.mp4",
		ContentType: "video/mp4", OriginalName: "previous.mp4",
	}); err != nil {
		t.Fatal(err)
	}
	oldCurrent := srv.store.Current()
	if oldCurrent == nil || oldCurrent.ID == "" {
		t.Fatal("failed to establish previous current publication")
	}

	oldFetch := fetchNiconicoSnapshot
	oldDownload := pageMediaDownloader
	oldEnsureFFmpeg := ensureFFmpeg
	oldEnsureFFprobe := niconicoEnsureFFprobe
	oldProbe := niconicoProbeMedia
	oldWorker := niconicoWorkerRunner
	oldExecutable := nicoWorkerExecutable
	t.Cleanup(func() {
		fetchNiconicoSnapshot = oldFetch
		pageMediaDownloader = oldDownload
		ensureFFmpeg = oldEnsureFFmpeg
		niconicoEnsureFFprobe = oldEnsureFFprobe
		niconicoProbeMedia = oldProbe
		niconicoWorkerRunner = oldWorker
		nicoWorkerExecutable = oldExecutable
	})
	fetchNiconicoSnapshot = func(context.Context, string) (niconico.Snapshot, error) { return snapshot, nil }
	pageMediaDownloader = func(string, string) (video.DownloadedMedia, error) {
		return video.DownloadedMedia{SourcePath: source, Name: "fixture.mp4"}, nil
	}
	ensureFFmpeg = func() (string, error) { return ffmpeg, nil }
	niconicoEnsureFFprobe = func() (string, error) { return "unused-ffprobe", nil }
	niconicoProbeMedia = func(context.Context, string, string) (video.MediaProbe, error) {
		return video.MediaProbe{Duration: 60, Streams: []video.MediaStream{{CodecType: "video", Width: 640, Height: 360}}}, nil
	}
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }
	workerStarted := make(chan struct{})
	niconicoWorkerRunner = func(ctx context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
		close(workerStarted)
		return runNicoWorkerWithBudget(ctx, request)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.nicovideo.jp/watch/sm9","niconicoComments":{"enabled":true}}`)).WithContext(ctx)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- adminJSON(t, mux, req)
	}()
	select {
	case <-workerStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("real worker did not start before cancellation")
	}
	workerPID := waitForNicoHTTPWorkerProcess(t, workerExe, 10*time.Second)
	t.Logf("observed real worker child pid=%d before cancellation", workerPID)
	cancel()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-response:
	case <-time.After(15 * time.Second):
		t.Fatal("HTTP request did not finish after cancellation")
	}
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "context canceled") {
		t.Fatalf("canceled request = status %d body %q", rec.Code, rec.Body.String())
	}
	current := srv.store.Current()
	if current == nil || current.ID != oldCurrent.ID || current.FileName != oldCurrent.FileName {
		t.Fatalf("current changed after cancellation: before=%#v after=%#v", oldCurrent, current)
	}
	currentData, err := os.ReadFile(filepath.Join(srv.store.Dir(), current.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(currentData) != string(oldCurrentData) {
		t.Fatalf("previous current publication changed: %q", currentData)
	}
	entries, err := os.ReadDir(srv.store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".niconico-export-") || strings.HasPrefix(entry.Name(), ".niconico-prepared-") {
			t.Fatalf("temporary Nico export artifact survived cancellation: %s", entry.Name())
		}
	}
}

func makeNicoHTTPWorkerFixture(t *testing.T, ffmpeg string) (string, niconico.Snapshot) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"color=c=black:s=640x360:r=30:d=10", "-c:v", "libx264", "-preset", "ultrafast",
		"-pix_fmt", "yuv420p", "-movflags", "+faststart", "-y", source,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate temporary real-worker source: %v\n%s", err, output)
	}
	snapshot := niconico.Snapshot{
		SchemaVersion: 1,
		VideoID:       "sm9",
		CommentStatus: niconico.CommentStatusReady,
		CommentCount:  1,
		SelectedForks: []string{"main"},
		Threads: []niconico.Thread{{
			ID:   "thread-main",
			Fork: "main",
			Comments: []niconico.Comment{{
				ID: "t7-7-temp-comment", VposMs: 500, Body: "temporary worker comment",
				Commands: []string{"big", "red"}, PostedAt: "2026-01-01T00:00:00Z",
			}},
		}},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal temporary real-worker snapshot: %v", err)
	}
	snapshotPath := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(snapshotPath, data, 0600); err != nil {
		t.Fatalf("write temporary real-worker snapshot: %v", err)
	}
	decodedData, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read temporary real-worker snapshot: %v", err)
	}
	var decoded niconico.Snapshot
	if err := json.Unmarshal(decodedData, &decoded); err != nil {
		t.Fatalf("decode temporary real-worker snapshot: %v", err)
	}
	return source, decoded
}

func waitForNicoHTTPWorkerProcess(t *testing.T, workerExe string, timeout time.Duration) int {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("real worker child observation currently uses Windows tasklist.exe")
	}
	imageName := filepath.Base(workerExe)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("tasklist.exe", "/FI", "IMAGENAME eq "+imageName, "/FO", "CSV", "/NH")
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("query worker child process: %v", err)
		}
		rows, err := csv.NewReader(strings.NewReader(string(output))).ReadAll()
		if err != nil {
			t.Fatalf("parse worker process listing: %v: %s", err, output)
		}
		for _, row := range rows {
			if len(row) < 2 || !strings.EqualFold(row[0], imageName) {
				continue
			}
			pid, err := strconv.Atoi(strings.ReplaceAll(row[1], ",", ""))
			if err != nil {
				t.Fatalf("parse worker PID %q: %v", row[1], err)
			}
			return pid
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("worker child %q did not appear within %s; cancellation was not issued early", imageName, timeout)
	return 0
}

func copyNicoHTTPWorkerExecutable(t *testing.T, sourcePath string) string {
	t.Helper()
	dir := t.TempDir()
	destination := filepath.Join(dir, "imagepadserver-worker-"+filepath.Base(dir)+".exe")
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatalf("open worker executable for unique process identity: %v", err)
	}
	defer source.Close()
	destinationFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatalf("create uniquely named worker executable: %v", err)
	}
	if _, err := io.Copy(destinationFile, source); err != nil {
		destinationFile.Close()
		t.Fatalf("copy worker executable: %v", err)
	}
	if err := destinationFile.Close(); err != nil {
		t.Fatalf("close copied worker executable: %v", err)
	}
	return destination
}
