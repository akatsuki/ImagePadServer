package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/nicoexportbudget"
	"imagepadserver/internal/nicoexportworker"
	"imagepadserver/internal/niconico"
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
	t.Setenv("IMAGEPAD_NICO_OUTPUT_MODE", "tee")

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
	niconicoWorkerRunner = func(_ context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
		workerCalls++
		workerOutputMode = request.OutputMode
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
		}, nicoexportbudget.Report{Verified: true, LogicalCPUs: 8, JobCPURate: 2000}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.nicovideo.jp/watch/sm9","niconicoComments":{"enabled":true}}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if workerCalls != 1 {
		t.Fatalf("worker calls = %d, want one direct worker call", workerCalls)
	}
	if workerOutputMode != "tee" {
		t.Fatalf("worker output mode = %q, want tee", workerOutputMode)
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
	repoRoot := filepath.Join("..", "..")
	fixtureSource := filepath.Join(repoRoot, "build", "nico-cpu20", "t11-fixtures-20260921", "source-high-density-10s.mp4")
	fixtureSnapshot := filepath.Join(repoRoot, "build", "nico-cpu20", "t11-fixtures-20260921", "snapshot-high-density.json")
	sourceData, err := os.ReadFile(fixtureSource)
	if err != nil {
		t.Skipf("real worker source fixture is unavailable: %v", err)
	}
	snapshotData, err := os.ReadFile(fixtureSnapshot)
	if err != nil {
		t.Skipf("real worker snapshot fixture is unavailable: %v", err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "downloaded-source.mp4")
	if err := os.WriteFile(source, sourceData, 0600); err != nil {
		t.Fatal(err)
	}

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
	niconicoWorkerRunner = func(ctx context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
		if workerBackend == "auto-fallback" {
			request.Backend = "auto"
			request.Compositor = missingCompositor
		}
		return runNicoWorkerWithBudget(ctx, request)
	}
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.nicovideo.jp/watch/sm9","niconicoComments":{"enabled":true}}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
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
	ffmpeg := os.Getenv("IMAGEPAD_FFMPEG")
	if ffmpeg == "" {
		t.Skip("IMAGEPAD_FFMPEG is not set")
	}
	repoRoot := filepath.Join("..", "..")
	fixtureSource := filepath.Join(repoRoot, "build", "nico-cpu20", "t11-fixtures-20260921", "source-high-density-10s.mp4")
	fixtureSnapshot := filepath.Join(repoRoot, "build", "nico-cpu20", "t11-fixtures-20260921", "snapshot-high-density.json")
	sourceData, err := os.ReadFile(fixtureSource)
	if err != nil {
		t.Skipf("real worker source fixture is unavailable: %v", err)
	}
	snapshotData, err := os.ReadFile(fixtureSnapshot)
	if err != nil {
		t.Skipf("real worker snapshot fixture is unavailable: %v", err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "downloaded-source.mp4")
	if err := os.WriteFile(source, sourceData, 0600); err != nil {
		t.Fatal(err)
	}

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
		return video.MediaProbe{Duration: 8, Streams: []video.MediaStream{{CodecType: "video", Width: 640, Height: 360}}}, nil
	}
	nicoWorkerExecutable = func() (string, error) { return workerExe, nil }
	workerStarted := make(chan struct{})
	niconicoWorkerRunner = func(ctx context.Context, request nicoexportworker.Request) (nicoexportworker.Event, nicoexportbudget.Report, error) {
		close(workerStarted)
		return runNicoWorkerWithBudget(ctx, request)
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://www.nicovideo.jp/watch/sm9","niconicoComments":{"enabled":true}}`)).WithContext(ctx)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- adminJSON(t, mux, req)
	}()
	select {
	case <-workerStarted:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("real worker did not start before cancellation")
	}
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
