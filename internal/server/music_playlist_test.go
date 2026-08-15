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
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/playlist"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

func enableMusicMode(t *testing.T) {
	t.Helper()
	if err := settings.Update(func(s *settings.Settings) error {
		s.MusicModeEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// fakeRadioPipeline replaces the ffmpeg-heavy steps of prepareRadioTrack.
func fakeRadioPipeline(t *testing.T) {
	t.Helper()
	oldEnsure := ensureFFmpeg
	oldAnalyze := analyzeAudioForKind
	oldRender := renderRadioTrack
	oldObserve := observePlaylistAsset
	t.Cleanup(func() {
		ensureFFmpeg = oldEnsure
		analyzeAudioForKind = oldAnalyze
		renderRadioTrack = oldRender
		observePlaylistAsset = oldObserve
	})
	ensureFFmpeg = func() (string, error) { return "ffmpeg", nil }
	analyzeAudioForKind = func(context.Context, string, string, video.SourceKind) (video.AudioAnalysis, error) {
		return video.AudioAnalysis{Duration: 200}, nil
	}
	renderRadioTrack = func(_ context.Context, outDir, _ string, _ video.AudioRenderInput, trackID string, _ video.QualityPreset, progress func(float64)) (string, error) {
		if progress != nil {
			progress(0.5)
		}
		path := filepath.Join(outDir, video.RadioTrackFileName(trackID))
		if err := os.WriteFile(path, []byte("ts"), 0600); err != nil {
			return "", err
		}
		return path, nil
	}
	observePlaylistAsset = func(context.Context, string) (video.RadioAssetSpec, error) {
		contract := video.MusicRadioRenderRecipe(video.QualityPreset{Height: 720, VideoBitrate: "2400k", MaxRate: "2800k", BufferSize: "5600k", AudioBitrate: "160k", RadioLatency: "rtsp-ultra"}, 180).NormalizedEncodingContract()
		return video.RadioAssetSpec{
			Video: video.RadioObservedVideo{Codec: contract.Video.Codec, Width: contract.Video.Width, Height: contract.Video.Height, FrameRate: contract.Video.FrameRate, PixelFormat: contract.Video.PixelFormat, BFrames: contract.Video.BFrames, GOPFrames: contract.Video.GOPFrames, Bitrate: contract.Video.RateControl.Bitrate},
			Audio: video.RadioObservedAudio{Codec: contract.Audio.Codec, Bitrate: contract.Audio.Bitrate, SampleRate: contract.Audio.SampleRate, Channels: contract.Audio.Channels},
		}, nil
	}
}

type barrierMusicRadio struct {
	mu         sync.Mutex
	generation obsrtmp.TrackGeneration
	status     obsrtmp.RadioStatus
	entered    chan struct{}
	release    chan struct{}
	once       sync.Once
	canceled   []uint64
}

func (r *barrierMusicRadio) Start() error                                               { return nil }
func (r *barrierMusicRadio) SetFallbackPreset(func() video.QualityPreset)               {}
func (r *barrierMusicRadio) SetLatencyProfile(func() obsrtmp.LatencyProfile)            {}
func (r *barrierMusicRadio) SetOutputMode(func() obsrtmp.RadioOutputMode)               {}
func (r *barrierMusicRadio) SetRTSPURL(string, string, string) bool                     { return false }
func (r *barrierMusicRadio) Status() obsrtmp.RadioStatus                                { return r.status }
func (r *barrierMusicRadio) Wake()                                                      {}
func (r *barrierMusicRadio) SkipCurrent()                                               {}
func (r *barrierMusicRadio) Stop(time.Duration)                                         {}
func (r *barrierMusicRadio) ProxyLLHLS(http.ResponseWriter, *http.Request, string) bool { return false }

func (r *barrierMusicRadio) Running() bool { return false }

type runningMusicRadio struct {
	barrierMusicRadio
	status obsrtmp.RadioStatus
	skips  int
}

func (r *runningMusicRadio) Status() obsrtmp.RadioStatus { return r.status }
func (r *runningMusicRadio) Running() bool               { return true }
func (r *runningMusicRadio) SkipCurrent()                { r.skips++ }

func (r *barrierMusicRadio) CurrentTrackGeneration() obsrtmp.TrackGeneration {
	if r.entered != nil {
		r.once.Do(func() { close(r.entered) })
		<-r.release
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

func (r *barrierMusicRadio) CancelGeneration(generation uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if generation == 0 || generation != r.generation.Generation {
		return false
	}
	r.canceled = append(r.canceled, generation)
	return true
}

func (r *barrierMusicRadio) canceledGeneration() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint64(nil), r.canceled...)
}

func playlistState(t *testing.T, mux *http.ServeMux) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/music/playlist", nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET playlist = %d: %s", rec.Code, rec.Body.String())
	}
	var st map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func stateTracks(st map[string]interface{}) []map[string]interface{} {
	raw, _ := st["tracks"].([]interface{})
	out := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}

func waitTrackStatus(t *testing.T, mux *http.ServeMux, index int, want string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tracks := stateTracks(playlistState(t, mux))
		if len(tracks) > index {
			if status, _ := tracks[index]["status"].(string); status == want {
				return tracks[index]
			}
			if status, _ := tracks[index]["status"].(string); status == "failed" && want != "failed" {
				t.Fatalf("track failed while waiting for %q: %v", want, tracks[index]["error"])
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for track[%d] status %q; state=%v", index, want, playlistState(t, mux))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMusicPlaylistStateEmpty(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	st := playlistState(t, mux)
	if len(stateTracks(st)) != 0 {
		t.Fatalf("expected empty playlist, got %v", st["tracks"])
	}
	if st["running"] != false || st["playing"] != false {
		t.Fatalf("radio must be stopped initially: %v", st)
	}
	if st["hlsURL"] != "" {
		t.Fatalf("hlsURL must be empty while stopped, got %v", st["hlsURL"])
	}
}

func TestMusicPlaylistAddRequiresMusicMode(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/add", strings.NewReader(`{"input":"https://example.com/song"}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestMusicPlaylistAddURLPreparesTrack(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	fakeRadioPipeline(t)

	oldAcquire := musicURLAcquirer
	defer func() { musicURLAcquirer = oldAcquire }()
	musicURLAcquirer = func(_ context.Context, s *Server, _ string) (video.AcquiredAudio, error) {
		src := filepath.Join(s.store.Dir(), "dl-song.m4a")
		if err := os.WriteFile(src, []byte("audio"), 0600); err != nil {
			return video.AcquiredAudio{}, err
		}
		return video.AcquiredAudio{
			SourcePath:       src,
			SourceName:       "dl-song.m4a",
			Kind:             video.SourceMusic,
			EmbeddedMetadata: video.AudioMetadata{Title: "Fake Song", Artist: "Fake Artist"},
		}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/add", strings.NewReader(`{"input":"https://www.youtube.com/watch?v=abc"}`))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body.String())
	}
	tracks := stateTracks(playlistState(t, mux))
	if len(tracks) != 1 {
		t.Fatalf("track count = %d", len(tracks))
	}

	ready := waitTrackStatus(t, mux, 0, "ready")
	if ready["title"] != "Fake Song" || ready["artist"] != "Fake Artist" {
		t.Fatalf("metadata not applied: %v", ready)
	}
	if ready["durationSeconds"] != float64(200) {
		t.Fatalf("duration = %v, want 200", ready["durationSeconds"])
	}
	// Source audio must be cleaned up after render; TS must exist.
	if _, err := os.Stat(filepath.Join(srv.store.Dir(), "dl-song.m4a")); !os.IsNotExist(err) {
		t.Fatal("source audio must be deleted after render")
	}
	id, _ := ready["id"].(string)
	if _, err := os.Stat(filepath.Join(srv.store.Dir(), video.RadioTrackFileName(id))); err != nil {
		t.Fatalf("rendered TS missing: %v", err)
	}
}

func TestMusicPlaylistAddFailedDownloadMarksTrack(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)

	oldAcquire := musicURLAcquirer
	defer func() { musicURLAcquirer = oldAcquire }()
	musicURLAcquirer = func(context.Context, *Server, string) (video.AcquiredAudio, error) {
		return video.AcquiredAudio{}, context.DeadlineExceeded
	}
	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/add", strings.NewReader(`{"input":"https://www.youtube.com/watch?v=bad"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body.String())
	}
	failed := waitTrackStatus(t, mux, 0, "failed")
	if msg, _ := failed["error"].(string); msg == "" {
		t.Fatal("failed track must carry an error message")
	}
}

func TestMusicPlaylistReorderAndRemove(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	a := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady})
	b := srv.musicQueue.Add(playlist.Track{Title: "B", Status: playlist.TrackReady})

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/reorder", strings.NewReader(`{"ids":["`+b.ID+`","`+a.ID+`"]}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("reorder = %d: %s", rec.Code, rec.Body.String())
	}
	tracks := stateTracks(playlistState(t, mux))
	if tracks[0]["title"] != "B" || tracks[1]["title"] != "A" {
		t.Fatalf("order not applied: %v", tracks)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/reorder", strings.NewReader(`{"ids":["`+b.ID+`"]}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusConflict {
		t.Fatalf("bad reorder = %d, want 409", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/remove", strings.NewReader(`{"id":"`+a.ID+`"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body.String())
	}
	tracks = stateTracks(playlistState(t, mux))
	if len(tracks) != 1 || tracks[0]["title"] != "B" {
		t.Fatalf("remove not applied: %v", tracks)
	}
}

func TestMusicPlaylistRemoveDeletesUnreferencedMedia(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	media := filepath.Join(srv.store.Dir(), "radio-track-zzz.ts")
	if err := os.WriteFile(media, []byte("ts"), 0600); err != nil {
		t.Fatal(err)
	}
	tr := srv.musicQueue.Add(playlist.Track{Title: "Z", Status: playlist.TrackReady, MediaPath: media})

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/remove", strings.NewReader(`{"id":"`+tr.ID+`"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(media); !os.IsNotExist(err) {
		t.Fatal("unreferenced media must be deleted on remove")
	}
}

func TestMusicPlaylistSavedCopySurvivesQueueRemove(t *testing.T) {
	fakeRadioPipeline(t)
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	media := filepath.Join(srv.store.Dir(), "radio-track-keep.mp4")
	if err := os.WriteFile(media, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	tr := srv.musicQueue.Add(playlist.Track{Title: "K", Status: playlist.TrackReady, MediaPath: media})
	if err := srv.playlistStore.Save("saved", srv.musicQueue.Snapshot()); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/remove", strings.NewReader(`{"id":"`+tr.ID+`"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body.String())
	}
	// キュー側のファイルは消えるが、保存済みプレイリストは自前コピーで生き残る。
	if _, err := os.Stat(media); !os.IsNotExist(err) {
		t.Fatal("queue media must be deleted on remove")
	}
	loaded, err := srv.playlistStore.Load("saved")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Status != playlist.TrackReady {
		t.Fatalf("saved playlist must stay playable via its copy: %+v", loaded)
	}
	if _, err := os.Stat(loaded[0].MediaPath); err != nil {
		t.Fatalf("saved copy missing: %v", err)
	}
}

func TestMusicPlaylistMediaOwnershipRemoveDoesNotDeleteSavedMedia(t *testing.T) {
	fakeRadioPipeline(t)
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	media := filepath.Join(srv.store.Dir(), "radio-track-saved.mp4")
	if err := os.WriteFile(media, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := srv.playlistStore.Save("saved", []playlist.Track{{ID: "saved", Status: playlist.TrackReady, MediaPath: media}}); err != nil {
		t.Fatal(err)
	}
	saved, err := srv.playlistStore.Load("saved")
	if err != nil {
		t.Fatal(err)
	}
	queued := srv.musicQueue.Add(saved[0])
	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/remove", strings.NewReader(`{"id":"`+queued.ID+`"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(saved[0].MediaPath); err != nil {
		t.Fatalf("queue remove must not delete saved playlist media: %v", err)
	}
}

func TestMusicPlaylistMediaOwnershipLoadMaterializesAndReplacesRuntimeOnly(t *testing.T) {
	fakeRadioPipeline(t)
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	for _, name := range []string{"first", "second"} {
		media := filepath.Join(srv.store.Dir(), name+".mp4")
		if err := os.WriteFile(media, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		if err := srv.playlistStore.Save(name, []playlist.Track{{ID: name + "-saved", Title: name, Status: playlist.TrackReady, MediaPath: media}}); err != nil {
			t.Fatal(err)
		}
	}
	firstSaved, err := srv.playlistStore.Load("first")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/music/playlists/load", strings.NewReader(`{"name":"first"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("load first = %d: %s", rec.Code, rec.Body.String())
	}
	firstRuntime := srv.musicQueue.Snapshot()[0]
	if firstRuntime.ID == firstSaved[0].ID || firstRuntime.MediaPath == firstSaved[0].MediaPath {
		t.Fatalf("load must materialize a fresh runtime track: saved=%+v runtime=%+v", firstSaved[0], firstRuntime)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlists/load", strings.NewReader(`{"name":"second"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("load second = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(firstRuntime.MediaPath); !os.IsNotExist(err) {
		t.Fatalf("replacing queue must remove displaced runtime media, stat err=%v", err)
	}
	if _, err := os.Stat(firstSaved[0].MediaPath); err != nil {
		t.Fatalf("replacing queue must not delete saved playlist media: %v", err)
	}
}

func TestMusicPlaylistMediaOwnershipPrepareKeepsQueueSource(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	fakeRadioPipeline(t)

	source := filepath.Join(srv.store.Dir(), "input.m4a")
	if err := os.WriteFile(source, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	track := srv.musicQueue.Add(playlist.Track{Title: "Source"})
	srv.prepareRadioTrack(track.ID, video.AcquiredAudio{SourcePath: source, SourceName: "input.m4a", Kind: video.SourceMusic})
	prepared, ok := srv.musicQueue.Get(track.ID)
	if !ok || prepared.SourcePath == "" || prepared.SourcePath == source {
		t.Fatalf("prepared track must retain a queue-owned source copy: %+v", prepared)
	}
	if _, err := os.Stat(prepared.SourcePath); err != nil {
		t.Fatalf("queue-owned source missing: %v", err)
	}
	if data, err := os.ReadFile(prepared.SourcePath); err != nil || string(data) != "audio" {
		t.Fatalf("queue-owned source must retain exact pre-render input: %q, err=%v", data, err)
	}
}

func TestCopyQueueSourceIsAtomicOnFailure(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "source.m4a")
	if err := os.WriteFile(destination, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(dir, "source-dir")
	if err := os.Mkdir(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyQueueSource(destination, sourceDir); err == nil {
		t.Fatal("copyQueueSource must fail when the source cannot be copied")
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "old" {
		t.Fatalf("destination was partially replaced: %q, err=%v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != "source-dir" || entries[1].Name() != "source.m4a" {
		t.Fatalf("temporary copy artifacts remain: %v", entries)
	}
}

func TestMusicPlaylistArtworkServesMaterializedArtworkAndRejectsEscapes(t *testing.T) {
	fakeRadioPipeline(t)
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	artwork := filepath.Join(srv.store.Dir(), "cover.webp")
	media := filepath.Join(srv.store.Dir(), "track.mp4")
	for _, path := range []string{artwork, media} {
		if err := os.WriteFile(path, []byte("asset"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.playlistStore.Save("art", []playlist.Track{{ID: "saved", Status: playlist.TrackReady, MediaPath: media, ThumbnailPath: artwork}}); err != nil {
		t.Fatal(err)
	}
	if rec := adminJSON(t, mux, httptest.NewRequest(http.MethodPost, "/api/music/playlists/load", strings.NewReader(`{"name":"art"}`))); rec.Code != http.StatusOK {
		t.Fatalf("load = %d: %s", rec.Code, rec.Body.String())
	}
	materialized := srv.musicQueue.Snapshot()[0]
	req := httptest.NewRequest(http.MethodGet, "/api/music/playlist/artwork?id="+materialized.ID, nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("materialized artwork = %d", rec.Code)
	}

	escaped := srv.musicQueue.Add(playlist.Track{Title: "escape", ThumbnailPath: filepath.Join(srv.store.Dir(), "..", "outside.webp")})
	req = httptest.NewRequest(http.MethodGet, "/api/music/playlist/artwork?id="+escaped.ID, nil)
	rec = adminJSON(t, mux, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("traversal artwork = %d, want 404", rec.Code)
	}

	directory := srv.musicQueue.Add(playlist.Track{Title: "directory", ThumbnailPath: srv.store.Dir()})
	req = httptest.NewRequest(http.MethodGet, "/api/music/playlist/artwork?id="+directory.ID, nil)
	if rec = adminJSON(t, mux, req); rec.Code != http.StatusNotFound {
		t.Fatalf("directory artwork = %d, want 404", rec.Code)
	}
	missing := srv.musicQueue.Add(playlist.Track{Title: "missing", ThumbnailPath: filepath.Join(srv.store.Dir(), "missing.webp")})
	req = httptest.NewRequest(http.MethodGet, "/api/music/playlist/artwork?id="+missing.ID, nil)
	if rec = adminJSON(t, mux, req); rec.Code != http.StatusNotFound {
		t.Fatalf("missing artwork = %d, want 404", rec.Code)
	}
}

func TestMusicPlaylistArtworkRejectsSymlinkEscape(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	outside := filepath.Join(t.TempDir(), "outside.webp")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(srv.store.Dir(), "escape.webp")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	track := srv.musicQueue.Add(playlist.Track{Title: "link", ThumbnailPath: link})
	req := httptest.NewRequest(http.MethodGet, "/api/music/playlist/artwork?id="+track.ID, nil)
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("symlink escape artwork = %d, want 404", rec.Code)
	}
}

func TestCleanupAbandonedPlaylistRuntimeDirsRemovesOnlyOldDirectChildren(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), "playlist-runtime")
	oldDir := filepath.Join(runtimeDir, "old")
	youngDir := filepath.Join(runtimeDir, "young")
	outsideDir := filepath.Join(t.TempDir(), "outside")
	for _, dir := range []string{oldDir, youngDir, outsideDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	oldTime := now.Add(-25 * time.Hour)
	if err := os.Chtimes(oldDir, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(runtimeDir, "escape")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	cleanupAbandonedPlaylistRuntimeDirs(runtimeDir, now)
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old runtime directory still exists: %v", err)
	}
	if _, err := os.Stat(youngDir); err != nil {
		t.Fatalf("young runtime directory removed: %v", err)
	}
	if _, err := os.Stat(outsideDir); err != nil {
		t.Fatalf("symlink escape target removed: %v", err)
	}
}

func TestMusicPlaylistRuntimeCleanupWaitsForDisplacedTrack(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	runtimeDir := filepath.Join(srv.playlistRuntimeDir(), "load")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(runtimeDir, "track.mp4")
	if err := os.WriteFile(media, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	completed := make(chan struct{})
	sessionDone := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		srv.removeRuntimeTracksAfterGeneration([]playlist.Track{{MediaPath: media}}, obsrtmp.TrackGeneration{Generation: 7, Completed: completed, SessionDone: sessionDone})
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("runtime cleanup ran before the exact generation completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(completed)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("runtime cleanup did not terminate after generation completion")
	}
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Fatalf("runtime directory still exists after completion: %v", err)
	}
}

func TestMusicPlaylistRuntimeCleanupDoesNotDeleteOnCanceledBlockedFeeder(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	runtimeDir := filepath.Join(srv.playlistRuntimeDir(), "blocked")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(runtimeDir, "track.mp4")
	if err := os.WriteFile(media, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	canceled := make(chan struct{})
	close(canceled)
	finished := make(chan struct{})
	go func() {
		srv.removeRuntimeTracksAfterGeneration([]playlist.Track{{MediaPath: media}}, obsrtmp.TrackGeneration{Generation: 9, Completed: make(chan struct{}), SessionCanceled: canceled, SessionDone: make(chan struct{})})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not terminate")
	}
	if _, err := os.Stat(runtimeDir); err != nil {
		t.Fatalf("runtime directory deleted before the blocked feeder completed: %v", err)
	}
}

func TestMusicPlaylistRemoveMutatesQueueBeforeCapturingDisplacedGeneration(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	runtimeDir := filepath.Join(srv.playlistRuntimeDir(), "remove")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(runtimeDir, "track.mp4")
	if err := os.WriteFile(media, []byte("media"), 0600); err != nil {
		t.Fatal(err)
	}
	track := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady, MediaPath: media})
	completed := make(chan struct{})
	radio := &barrierMusicRadio{
		generation: obsrtmp.TrackGeneration{TrackID: track.ID, Generation: 11, Completed: completed, SessionDone: make(chan struct{})},
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	srv.radio = radio
	done := make(chan struct{})
	go func() {
		srv.handleMusicPlaylistRemove(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/music/playlist/remove", strings.NewReader(`{"id":"`+track.ID+`"}`)))
		close(done)
	}()
	<-radio.entered
	if _, ok := srv.musicQueue.Get(track.ID); ok {
		t.Fatal("remove handler captured generation before removing the queue track")
	}
	close(radio.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("remove handler did not return")
	}
	if got := radio.canceledGeneration(); len(got) != 1 || got[0] != 11 {
		t.Fatalf("canceled generations = %v", got)
	}
	if _, err := os.Stat(media); err != nil {
		t.Fatalf("active displaced media was deleted before exact completion: %v", err)
	}
	close(completed)
	waitForPathRemoval(t, runtimeDir)
}

func TestMusicPlaylistsLoadReplacesQueueBeforeCapturingDisplacedGeneration(t *testing.T) {
	fakeRadioPipeline(t)
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	runtimeDir := filepath.Join(srv.playlistRuntimeDir(), "old-load")
	if err := os.MkdirAll(runtimeDir, 0700); err != nil {
		t.Fatal(err)
	}
	mediaA := filepath.Join(runtimeDir, "a.mp4")
	mediaB := filepath.Join(runtimeDir, "b.mp4")
	for _, path := range []string{mediaA, mediaB} {
		if err := os.WriteFile(path, []byte("media"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady, MediaPath: mediaA})
	b := srv.musicQueue.Add(playlist.Track{Title: "B", Status: playlist.TrackReady, MediaPath: mediaB})
	newMedia := filepath.Join(srv.store.Dir(), "new.mp4")
	if err := os.WriteFile(newMedia, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := srv.playlistStore.Save("new", []playlist.Track{{ID: "new", Status: playlist.TrackReady, MediaPath: newMedia}}); err != nil {
		t.Fatal(err)
	}
	bCompleted := make(chan struct{})
	radio := &barrierMusicRadio{
		generation: obsrtmp.TrackGeneration{TrackID: b.ID, Generation: 22, Completed: bCompleted, SessionDone: make(chan struct{})},
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	srv.radio = radio
	done := make(chan struct{})
	go func() {
		srv.handleMusicPlaylistsLoad(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/music/playlists/load", strings.NewReader(`{"name":"new"}`)))
		close(done)
	}()
	<-radio.entered
	if old := srv.musicQueue.Snapshot(); len(old) != 1 || old[0].ID == a.ID || old[0].ID == b.ID {
		t.Fatalf("load handler captured generation before replacing queue: %+v", old)
	}
	close(radio.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("load handler did not return")
	}
	if got := radio.canceledGeneration(); len(got) != 1 || got[0] != 22 {
		t.Fatalf("canceled generations = %v", got)
	}
	if _, err := os.Stat(runtimeDir); err != nil {
		t.Fatalf("old queue directory deleted before active displaced B completed: %v", err)
	}
	close(bCompleted)
	waitForPathRemoval(t, runtimeDir)
}

func waitForPathRemoval(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("path was not removed: %s", path)
}

func TestMusicPlaylistStatePropagatesRadioFailureStatus(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	stoppedAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	srv.radio = &barrierMusicRadio{status: obsrtmp.RadioStatus{
		Phase:      obsrtmp.RadioPhaseFailed,
		LastError:  "publisher exited",
		RetryCount: 4,
		StoppedAt:  stoppedAt,
	}}
	state := srv.musicPlaylistState()
	if state["phase"] != obsrtmp.RadioPhaseFailed || state["lastError"] != "publisher exited" || state["retryCount"] != 4 {
		t.Fatalf("radio failure state = %#v", state)
	}
	if state["stoppedAt"] != stoppedAt.Format(time.RFC3339Nano) {
		t.Fatalf("stoppedAt = %#v", state["stoppedAt"])
	}
}

func TestMusicPlaylistInitialStateReportsStoppedRadio(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	state := srv.musicPlaylistState()
	if state["phase"] != obsrtmp.RadioPhaseStopped || state["running"] != false || state["lastError"] != "" || state["retryCount"] != 0 || state["stoppedAt"] != nil {
		t.Fatalf("initial radio state = %#v", state)
	}
}

func TestRadioTrackFailureStateIsSanitized(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	track := srv.musicQueue.Add(playlist.Track{Title: "Sanitize", Status: playlist.TrackReady})
	srv.onRadioTrackEnd(track.ID, errors.New("Authorization: Bearer bearer-secret password: colon-secret token=equals-secret rtmp://alice:url-secret@127.0.0.1/radio?key=query-secret"))
	got, ok := srv.musicQueue.Get(track.ID)
	if !ok {
		t.Fatal("track disappeared")
	}
	for _, secret := range []string{"bearer-secret", "colon-secret", "equals-secret", "alice", "url-secret", "127.0.0.1", "query-secret"} {
		if strings.Contains(got.Error, secret) {
			t.Fatalf("playlist error leaked %q: %s", secret, got.Error)
		}
	}
}

func TestMusicPlaylistRemoveClearsPausedTrackState(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	track := srv.musicQueue.Add(playlist.Track{Title: "Paused", Status: playlist.TrackReady})
	srv.musicPendingMu.Lock()
	srv.musicPaused = true
	srv.musicPausedTrack = track.ID
	srv.musicPausedOffset = 33
	srv.musicPendingTrack = track.ID
	srv.musicPendingOffset = 33
	srv.musicPendingMu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/remove", strings.NewReader(`{"id":"`+track.ID+`"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body.String())
	}
	st := playlistState(t, mux)
	if st["paused"] != false {
		t.Fatalf("remove must clear paused state for removed track: %v", st)
	}
	if st["currentTrackId"] != "" {
		t.Fatalf("remove must not expose removed paused track as current: %v", st)
	}
}

func TestMusicPlaylistPrepareDeletesRenderedMediaWhenTrackWasRemoved(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	fakeRadioPipeline(t)

	source := filepath.Join(srv.store.Dir(), "removed-source.m4a")
	if err := os.WriteFile(source, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	track := srv.musicQueue.Add(playlist.Track{Title: "Removed"})
	if !srv.musicQueue.Remove(track.ID) {
		t.Fatal("track must be removable before prepare completes")
	}

	srv.prepareRadioTrack(track.ID, video.AcquiredAudio{
		SourcePath: source,
		SourceName: "removed-source.m4a",
		Kind:       video.SourceMusic,
	})

	media := filepath.Join(srv.store.Dir(), video.RadioTrackFileName(track.ID))
	if _, err := os.Stat(media); !os.IsNotExist(err) {
		t.Fatalf("rendered media for removed track must be deleted, stat err=%v", err)
	}
}

func TestMusicPlaylistOptionsToggle(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/options", strings.NewReader(`{"shuffle":true,"loop":true}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("options = %d: %s", rec.Code, rec.Body.String())
	}
	st := playlistState(t, mux)
	if st["shuffle"] != true || st["loop"] != true {
		t.Fatalf("options not applied: %v", st)
	}
}

func TestMusicPlaylistPlayValidation(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/play", strings.NewReader(`{"id":"missing"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusNotFound {
		t.Fatalf("play missing = %d, want 404", rec.Code)
	}
	tr := srv.musicQueue.Add(playlist.Track{Title: "pending"})
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlist/play", strings.NewReader(`{"id":"`+tr.ID+`"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusConflict {
		t.Fatalf("play not-ready = %d, want 409", rec.Code)
	}
}

func TestMusicPlaylistStartCanBeginStreamWithoutTracks(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)

	started := false
	srv.startMusicRadio = func() error {
		started = true
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlist/start", strings.NewReader(`{}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("start empty playlist stream = %d: %s", rec.Code, rec.Body.String())
	}
	if !started {
		t.Fatal("start endpoint must start the radio stream even when no track is selected")
	}
}

func TestMusicPlaylistCopyIgnoresUnpublishedLocalRTSP(t *testing.T) {
	state := map[string]interface{}{
		"hlsURL":     "",
		"rtspUrl":    "rtsp://192.168.0.10:8554/radio",
		"rtspPublic": false,
	}
	if got := urlForCopyTarget(state, "plShareUrl"); got != "" {
		t.Fatalf("playlist copy URL = %q, want empty when only local unpublished RTSP exists", got)
	}
	state["rtspPublic"] = true
	if got, want := urlForCopyTarget(state, "plShareUrl"), "rtsp://192.168.0.10:8554/radio"; got != want {
		t.Fatalf("playlist copy URL = %q, want %q for explicitly public RTSP", got, want)
	}
}

func TestMusicPlaylistURLReadinessRequiresCachedStatus(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)

	radio := &barrierMusicRadio{status: obsrtmp.RadioStatus{
		Running:    true,
		RTSPURL:    "rtsp://198.51.100.20:8554/radio",
		RTSPPublic: true,
	}}
	srv.radio = radio
	state := srv.musicPlaylistState()
	if state["rtspUrl"] != "" || state["hlsURL"] != "" || state["publicHLSURL"] != "" {
		t.Fatalf("unready URLs must be hidden: %v", state)
	}

	radio.status.RTSPReady = true
	radio.status.HLSReady = true
	state = srv.musicPlaylistState()
	if state["rtspUrl"] == "" || state["hlsURL"] == "" {
		t.Fatalf("ready URLs must be published: %v", state)
	}

	radio.status.HLSReady = false
	state = srv.musicPlaylistState()
	if state["hlsURL"] != "" || state["publicHLSURL"] != "" {
		t.Fatalf("lost HLS readiness must withdraw URLs: %v", state)
	}
}

func TestMusicPlaylistsSaveLoadDelete(t *testing.T) {
	fakeRadioPipeline(t)
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	media := filepath.Join(srv.store.Dir(), "radio-track-s1.ts")
	if err := os.WriteFile(media, []byte("ts"), 0600); err != nil {
		t.Fatal(err)
	}
	srv.musicQueue.Add(playlist.Track{Title: "Saved Song", Status: playlist.TrackReady, MediaPath: media})

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlists", strings.NewReader(`{"name":"mylist"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/music/playlists", nil)
	rec := adminJSON(t, mux, req)
	var names map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &names); err != nil {
		t.Fatal(err)
	}
	if list, _ := names["names"].([]interface{}); len(list) != 1 || list[0] != "mylist" {
		t.Fatalf("names = %v", names)
	}

	// Clear the queue, then load the saved playlist back.
	srv.musicQueue.ReplaceAll(nil)
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlists/load", strings.NewReader(`{"name":"mylist"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("load = %d: %s", rec.Code, rec.Body.String())
	}
	tracks := stateTracks(playlistState(t, mux))
	if len(tracks) != 1 || tracks[0]["title"] != "Saved Song" || tracks[0]["status"] != "ready" {
		t.Fatalf("loaded tracks = %v", tracks)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/music/playlists/delete", strings.NewReader(`{"name":"mylist"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/music/playlists/load", strings.NewReader(`{"name":"mylist"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusNotFound {
		t.Fatalf("load deleted = %d, want 404", rec.Code)
	}
}

func TestMusicPlaylistsLoadClearsPausedTrackState(t *testing.T) {
	fakeRadioPipeline(t)
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	old := srv.musicQueue.Add(playlist.Track{Title: "Old", Status: playlist.TrackReady})
	srv.musicPendingMu.Lock()
	srv.musicPaused = true
	srv.musicPausedTrack = old.ID
	srv.musicPausedOffset = 42
	srv.musicPendingMu.Unlock()

	media := filepath.Join(srv.store.Dir(), "radio-track-loaded.ts")
	if err := os.WriteFile(media, []byte("ts"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := srv.playlistStore.Save("loaded", []playlist.Track{
		{ID: "loaded-track", Title: "Loaded", Status: playlist.TrackReady, MediaPath: media},
	}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/music/playlists/load", strings.NewReader(`{"name":"loaded"}`))
	if rec := adminJSON(t, mux, req); rec.Code != http.StatusOK {
		t.Fatalf("load = %d: %s", rec.Code, rec.Body.String())
	}
	st := playlistState(t, mux)
	if st["paused"] != false {
		t.Fatalf("load must clear paused state: %v", st)
	}
	if st["currentTrackId"] != "" {
		t.Fatalf("load must not expose old paused track as current: %v", st)
	}
}

func TestMusicPlaylistAutomaticWakeContinuesSequentialCursor(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	a := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady, MediaPath: "a.ts"})
	b := srv.musicQueue.Add(playlist.Track{Title: "B", Status: playlist.TrackReady, MediaPath: "b.ts"})
	if _, id, _, ok := srv.nextRadioTrack(); !ok || id != a.ID {
		t.Fatalf("first automatic track = %q ok=%v, want A", id, ok)
	}
	if _, id, _, ok := srv.nextRadioTrack(); !ok || id != b.ID {
		t.Fatalf("second automatic track = %q ok=%v, want B", id, ok)
	}
	if _, _, _, ok := srv.nextRadioTrack(); ok {
		t.Fatal("queue must be exhausted before preparation wake")
	}
	c := srv.musicQueue.Add(playlist.Track{Title: "C", Status: playlist.TrackReady, MediaPath: "c.ts"})
	if _, id, _, ok := srv.nextRadioTrack(); !ok || id != c.ID {
		t.Fatalf("automatic wake after adding C = %q ok=%v, want C", id, ok)
	}
}

func TestMusicPlaylistReplayResetsExhaustedSequentialCursor(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	srv.startMusicRadio = func() error { return nil }
	a := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady, MediaPath: "a.ts"})
	srv.musicQueue.Add(playlist.Track{Title: "B", Status: playlist.TrackReady, MediaPath: "b.ts"})
	srv.nextRadioTrack()
	srv.nextRadioTrack()
	srv.nextRadioTrack()
	srv.musicQueue.Add(playlist.Track{Title: "C", Status: playlist.TrackReady, MediaPath: "c.ts"})

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistPlay(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/play", strings.NewReader(`{"id":""}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit replay = %d: %s", rec.Code, rec.Body.String())
	}
	if _, id, _, ok := srv.nextRadioTrack(); !ok || id != a.ID {
		t.Fatalf("explicit replay track = %q ok=%v, want A", id, ok)
	}
}

func TestMusicPlaylistPlaySpecificTrackPreservesSequentialHistory(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	srv.startMusicRadio = func() error { return nil }
	a := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady, MediaPath: "a.ts"})
	b := srv.musicQueue.Add(playlist.Track{Title: "B", Status: playlist.TrackReady, MediaPath: "b.ts"})
	c := srv.musicQueue.Add(playlist.Track{Title: "C", Status: playlist.TrackReady, MediaPath: "c.ts"})
	if !srv.musicQueue.SetCurrent(a.ID) {
		t.Fatal("SetCurrent(A) failed")
	}

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistPlay(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/play", strings.NewReader(`{"id":"`+c.ID+`"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("play C = %d: %s", rec.Code, rec.Body.String())
	}
	if _, id, _, ok := srv.nextRadioTrack(); !ok || id != c.ID {
		t.Fatalf("specific playback = %q ok=%v, want C", id, ok)
	}
	if _, id, _, ok := srv.nextRadioTrack(); !ok || id != b.ID {
		t.Fatalf("Next after C = %q ok=%v, want B without replaying A", id, ok)
	}
}

func TestMusicPlaylistExplicitGPUPlayFailsClosedBeforePendingMutation(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "1")

	current := srv.musicQueue.Add(playlist.Track{Title: "Current", Status: playlist.TrackReady, MediaPath: "current.ts"})
	target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady, MediaPath: "target.ts"})
	if !srv.musicQueue.SetCurrent(current.ID) {
		t.Fatal("SetCurrent(Current) failed")
	}
	startedAt := time.Now()
	srv.radio = &runningMusicRadio{status: obsrtmp.RadioStatus{
		Running:        true,
		CurrentTrackID: current.ID,
		TrackStartedAt: startedAt,
		ActiveSession: &obsrtmp.RadioActiveSessionContract{
			PublisherProfile: obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation,
		},
	}}
	srv.setPlaylistGPUEvaluationArmed(true)
	srv.musicTimelineMu.Lock()
	srv.musicTimelineEpoch = 1
	srv.musicActiveTrackID = current.ID
	srv.musicActiveBaseSeq = 0
	srv.musicActiveTailSeq = 0
	srv.musicActiveStartedAt = startedAt
	srv.musicTimelineMu.Unlock()

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistPlay(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/play", strings.NewReader(`{"id":"`+target.ID+`"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("explicit GPU play without executor = %d: %s", rec.Code, rec.Body.String())
	}
	srv.musicPendingMu.Lock()
	pendingTrack := srv.musicPendingTrack
	srv.musicPendingMu.Unlock()
	if pendingTrack != "" {
		t.Fatalf("failed explicit GPU play must not leave pending track %q", pendingTrack)
	}
	if radio, ok := srv.radio.(*runningMusicRadio); !ok || radio.skips != 0 {
		t.Fatalf("failed explicit GPU play must not skip current track: %#v", srv.radio)
	}
}

func setupExplicitGPUTransitionFailureTest(t *testing.T) (*Server, *runningMusicRadio, playlist.Track) {
	t.Helper()
	srv, _ := testServer(t, true)
	t.Cleanup(func() { cleanupTestServer(srv) })
	enableMusicMode(t)
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "1")
	current := srv.musicQueue.Add(playlist.Track{Title: "Current", Status: playlist.TrackReady, MediaPath: "current.ts"})
	if !srv.musicQueue.SetCurrent(current.ID) {
		t.Fatal("SetCurrent(Current) failed")
	}
	startedAt := time.Now()
	radio := &runningMusicRadio{status: obsrtmp.RadioStatus{
		Running:        true,
		CurrentTrackID: current.ID,
		TrackStartedAt: startedAt,
		ActiveSession: &obsrtmp.RadioActiveSessionContract{
			PublisherProfile: obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation,
		},
	}}
	srv.radio = radio
	srv.setPlaylistGPUEvaluationArmed(true)
	srv.musicTimelineMu.Lock()
	srv.musicTimelineEpoch = 1
	srv.musicActiveTrackID = current.ID
	srv.musicActiveBaseSeq = 0
	srv.musicActiveTailSeq = 0
	srv.musicActiveStartedAt = startedAt
	srv.musicTimelineMu.Unlock()
	return srv, radio, current
}

func TestMusicPlaylistExplicitGPUTransitionPlanUsesAuthoritativeHeadAndTail(t *testing.T) {
	srv, _, current := setupExplicitGPUTransitionFailureTest(t)
	target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady, MediaPath: "target.ts"})
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUAssets = map[string]video.TrackAssets{
		target.ID: {TrackID: target.ID},
	}
	srv.musicPlaylistGPUTimelines = map[string]video.TrackTimeline{
		target.ID: {TrackID: target.ID},
	}
	srv.musicTimelineMu.Unlock()
	var got video.TransitionPlan
	srv.SetPlaylistGPUContinuousTrackSink(func(_ video.TrackAssets, _ video.TrackTimeline, plan video.TransitionPlan) error {
		got = plan
		return nil
	})
	if err := srv.prepareExplicitPlaylistGPUTransitionTo(video.PlaylistTransitionTrackChange, target.ID); err != nil {
		t.Fatalf("prepare explicit GPU transition = %v", err)
	}
	if got.Epoch != 1 || got.SourceTrackID != current.ID || got.TargetTrackID != target.ID {
		t.Fatalf("transition identity = %#v", got)
	}
	if got.PlaybackHeadSequence != 0 || got.QueuedTailSequence != 0 || got.FadeStartSequence <= got.QueuedTailSequence || got.FadeEndSequence < got.FadeStartSequence {
		t.Fatalf("transition sequence range = %#v", got)
	}
	if got.PlaybackHeadPTSNs != 0 || got.QueuedTailPTSNs != 0 || got.AudioFadePlan.StartPTSNs != got.FadeStartPTSNs || got.AudioFadePlan.DurationNS <= 0 {
		t.Fatalf("transition PTS/audio plan = %#v", got)
	}
	srv.musicTimelineMu.Lock()
	reserved := srv.musicTransitionReservedNextSeq
	srv.musicTimelineMu.Unlock()
	if reserved != got.FadeEndSequence+1 {
		t.Fatalf("reserved next sequence = %d, want %d", reserved, got.FadeEndSequence+1)
	}
}

func TestMusicPlaylistExplicitTargetTrackChangeUsesCompleteContinuousExecutor(t *testing.T) {
	srv, _, current := setupExplicitGPUTransitionFailureTest(t)
	target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady})
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUAssets = map[string]video.TrackAssets{
		target.ID: {TrackID: target.ID},
	}
	srv.musicPlaylistGPUTimelines = map[string]video.TrackTimeline{
		target.ID: {TrackID: target.ID},
	}
	srv.musicTimelineMu.Unlock()

	transitionCalls := 0
	continuousCalls := 0
	srv.SetPlaylistGPUTransitionSink(func(video.TransitionPlan) error {
		transitionCalls++
		return nil
	})
	srv.SetPlaylistGPUContinuousTrackSink(func(assets video.TrackAssets, timeline video.TrackTimeline, plan video.TransitionPlan) error {
		continuousCalls++
		if assets.TrackID != target.ID || timeline.TrackID != target.ID || plan.TargetTrackID != target.ID {
			t.Fatalf("continuous executor received target assets/timeline/plan mismatch: %q %q %q", assets.TrackID, timeline.TrackID, plan.TargetTrackID)
		}
		return nil
	})

	if err := srv.prepareExplicitPlaylistGPUTransitionTo(video.PlaylistTransitionTrackChange, target.ID); err != nil {
		t.Fatalf("explicit target GPU transition = %v", err)
	}
	if continuousCalls != 1 || transitionCalls != 0 {
		t.Fatalf("explicit target executor calls: continuous=%d transition=%d, want 1/0 (current=%s)", continuousCalls, transitionCalls, current.ID)
	}
}

func TestMusicPlaylistContinuousSinkReceivesLocalTargetTimelineForWorkerRebase(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady, MediaPath: "target.ts"})
	localTimeline := video.TrackTimeline{
		TrackID:    target.ID,
		FrameCount: 1,
		Frames:     []video.FrameDirective{{Sequence: 0, FrameIndex: 0, PTSNs: 0}},
	}
	localAssets := video.TrackAssets{TrackID: target.ID}
	srv.musicTimelineMu.Lock()
	if srv.musicPlaylistGPUAssets == nil {
		srv.musicPlaylistGPUAssets = make(map[string]video.TrackAssets)
	}
	if srv.musicPlaylistGPUTimelines == nil {
		srv.musicPlaylistGPUTimelines = make(map[string]video.TrackTimeline)
	}
	srv.musicPlaylistGPUAssets[target.ID] = localAssets
	srv.musicPlaylistGPUTimelines[target.ID] = localTimeline
	srv.musicTimelineMu.Unlock()
	var got video.TrackTimeline
	srv.SetPlaylistGPUContinuousTrackSink(func(_ video.TrackAssets, timeline video.TrackTimeline, _ video.TransitionPlan) error {
		got = timeline
		return nil
	})
	if err := srv.prepareExplicitPlaylistGPUTransition(video.PlaylistTransitionTrackChange); err != nil {
		t.Fatalf("prepare automatic GPU transition = %v", err)
	}
	if got.TrackID != target.ID || len(got.Frames) != 1 || got.Frames[0].Sequence != 0 || got.Frames[0].PTSNs != 0 {
		t.Fatalf("continuous sink received pre-rebased target timeline: %#v", got)
	}
}

func TestMusicPlaylistExplicitGPUNextFailsClosedWithoutAdvancingQueue(t *testing.T) {
	srv, _, current := setupExplicitGPUTransitionFailureTest(t)
	target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady, MediaPath: "target.ts"})
	if _, id, _, ok := srv.nextRadioTrack(); ok || id != "" {
		t.Fatalf("explicit GPU next without target timeline = id %q ok=%v, want fail-closed", id, ok)
	}
	if got := srv.musicQueue.CurrentID(); got != current.ID {
		t.Fatalf("failed explicit GPU next advanced queue current to %q, want %q", got, current.ID)
	}
	if preview, ok := srv.musicQueue.PreviewNext(); !ok || preview.ID != target.ID {
		t.Fatalf("failed explicit GPU next consumed queue reservation: %#v ok=%v", preview, ok)
	}
}

func TestMusicPlaylistExplicitGPUHandledClaimDoesNotRequireCPUMediaPath(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady})
	if err := srv.markPlaylistGPUHandledTrack(target.ID, 1); err != nil {
		t.Fatalf("mark handled GPU track = %v", err)
	}

	mode, err := srv.resolvePlaylistGPUTrackClaim("", target.ID, 0)
	if err != nil {
		t.Fatalf("resolve handled GPU track without CPU media path = %v", err)
	}
	if mode != obsrtmp.RadioTrackClaimHandled {
		t.Fatalf("handled GPU claim mode = %q, want %q", mode, obsrtmp.RadioTrackClaimHandled)
	}
}

func TestMusicPlaylistExplicitGPUNextWithContinuousReadyStillRequiresTargetTimeline(t *testing.T) {
	srv, _, current := setupExplicitGPUTransitionFailureTest(t)
	target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady, MediaPath: "target.ts"})
	srv.SetPlaylistGPUContinuousPlaybackReady(true)
	if _, id, _, ok := srv.nextRadioTrack(); ok || id != "" {
		t.Fatalf("explicit GPU next without target timeline = id %q ok=%v, want fail-closed", id, ok)
	}
	if got := srv.musicQueue.CurrentID(); got != current.ID {
		t.Fatalf("failed explicit GPU next advanced queue current to %q, want %q", got, current.ID)
	}
	if preview, ok := srv.musicQueue.PreviewNext(); !ok || preview.ID != target.ID {
		t.Fatalf("failed explicit GPU next consumed queue reservation: %#v ok=%v", preview, ok)
	}
}

func TestMusicPlaylistExplicitGPUContinuousRouteRequiresRuntimeOwnership(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	srv.SetPlaylistGPUContinuousPlaybackReady(true)
	if srv.playlistGPUContinuousRouteAvailable() {
		t.Fatal("continuous GPU route became available without worker/controller/output ownership")
	}
	worker, err := video.NewPlaylistGPUTransitionController(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUController = worker
	srv.musicTimelineMu.Unlock()
	if srv.playlistGPUContinuousRouteAvailable() {
		t.Fatal("continuous GPU route became available without worker/output ownership")
	}
}

func TestMusicPlaylistAutomaticGPUTransitionRequiresCompleteTrackSink(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	srv.SetPlaylistGPUContinuousPlaybackReady(true)
	srv.SetPlaylistGPUTransitionSink(func(video.TransitionPlan) error { return nil })
	if srv.playlistGPUContinuousRouteAvailable() {
		t.Fatal("fade-tail transition sink must not arm complete automatic route")
	}
	srv.SetPlaylistGPUContinuousTrackSink(func(video.TrackAssets, video.TrackTimeline, video.TransitionPlan) error { return nil })
	if srv.playlistGPUContinuousRouteAvailable() {
		t.Fatal("complete automatic track sink must not arm route without runtime output ownership")
	}
	controller, err := video.NewPlaylistGPUTransitionController(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetPlaylistGPUEncodedFrameSink(func(video.EncodedH264Frame) error { return nil })
	srv.SetPlaylistGPUContinuousRuntime(&video.PlaylistGPUWorker{}, controller)
	if !srv.playlistGPUContinuousRouteAvailable() {
		t.Fatal("complete automatic track sink plus runtime ownership did not arm explicit route")
	}
}

func TestMusicPlaylistExplicitGPUPauseFailsClosedBeforePausedMutation(t *testing.T) {
	srv, radio, _ := setupExplicitGPUTransitionFailureTest(t)
	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistPause(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/pause", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("explicit GPU pause without executor = %d: %s", rec.Code, rec.Body.String())
	}
	srv.musicPendingMu.Lock()
	paused := srv.musicPaused
	srv.musicPendingMu.Unlock()
	if paused {
		t.Fatal("failed explicit GPU pause must not set paused state")
	}
	if radio.skips != 0 {
		t.Fatalf("failed explicit GPU pause must not skip current track: %d", radio.skips)
	}
}

func TestMusicPlaylistExplicitGPUSeekFailsClosedBeforePendingMutation(t *testing.T) {
	srv, radio, current := setupExplicitGPUTransitionFailureTest(t)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"seconds":12}`)
	srv.handleMusicPlaylistSeek(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/seek", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("explicit GPU seek without executor = %d: %s", rec.Code, rec.Body.String())
	}
	srv.musicPendingMu.Lock()
	pendingTrack := srv.musicPendingTrack
	pendingOffset := srv.musicPendingOffset
	srv.musicPendingMu.Unlock()
	if pendingTrack != "" || pendingOffset != 0 {
		t.Fatalf("failed explicit GPU seek must not leave pending request: track=%q offset=%d", pendingTrack, pendingOffset)
	}
	if radio.skips != 0 {
		t.Fatalf("failed explicit GPU seek must not skip current track %q: %d", current.ID, radio.skips)
	}
}

func TestMusicPlaylistPlaySpecificTrackPreservesShuffleHistory(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	srv.startMusicRadio = func() error { return nil }
	a := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady, MediaPath: "a.ts"})
	c := srv.musicQueue.Add(playlist.Track{Title: "C", Status: playlist.TrackReady, MediaPath: "c.ts"})
	srv.musicQueue.SetShuffle(true)
	if !srv.musicQueue.SetCurrent(a.ID) {
		t.Fatal("SetCurrent(A) failed")
	}

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistPlay(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/play", strings.NewReader(`{"id":"`+c.ID+`"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("play C = %d: %s", rec.Code, rec.Body.String())
	}
	if _, id, _, ok := srv.nextRadioTrack(); !ok || id != c.ID {
		t.Fatalf("specific playback = %q ok=%v, want C", id, ok)
	}
	if _, id, _, ok := srv.nextRadioTrack(); ok || id != "" {
		t.Fatalf("shuffle Next after C = %q ok=%v, want exhausted without replaying A", id, ok)
	}
}

func TestMusicPlaylistRemovePausedPendingCursorTrack(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	srv.radio = &barrierMusicRadio{}
	a := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady, MediaPath: "a.ts"})
	b := srv.musicQueue.Add(playlist.Track{Title: "B", Status: playlist.TrackReady, MediaPath: "b.ts"})
	c := srv.musicQueue.Add(playlist.Track{Title: "C", Status: playlist.TrackReady, MediaPath: "c.ts"})
	srv.nextRadioTrack()
	srv.nextRadioTrack()
	srv.musicPendingMu.Lock()
	srv.musicPaused = true
	srv.musicPausedTrack = b.ID
	srv.musicPausedOffset = 9
	srv.musicPendingTrack = b.ID
	srv.musicPendingOffset = 9
	srv.musicPendingMu.Unlock()

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistRemove(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/remove", strings.NewReader(`{"id":"`+b.ID+`"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove B = %d: %s", rec.Code, rec.Body.String())
	}
	if _, id, _, ok := srv.nextRadioTrack(); !ok || id != c.ID {
		t.Fatalf("Next after removing paused pending B = %q ok=%v, want C", id, ok)
	}
	if srv.musicQueue.CurrentID() == a.ID {
		t.Fatal("removing B must not restart the cursor at A")
	}
}

func TestMusicPlaylistStopClearsPendingRequest(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	srv.radio = &barrierMusicRadio{}
	track := srv.musicQueue.Add(playlist.Track{Title: "A", Status: playlist.TrackReady, MediaPath: "a.ts"})
	srv.setPendingRadioTrack(track.ID, 17)

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistStop(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/stop", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stop = %d: %s", rec.Code, rec.Body.String())
	}
	srv.musicPendingMu.Lock()
	pending := srv.musicPendingTrack
	offset := srv.musicPendingOffset
	srv.musicPendingMu.Unlock()
	if pending != "" || offset != 0 {
		t.Fatalf("Stop left pending request %q at %d", pending, offset)
	}
}

type sessionContextMusicRadio struct {
	barrierMusicRadio
	running bool
	starts  int
}

func (r *sessionContextMusicRadio) Start() error {
	r.running = true
	r.status.Running = true
	r.starts++
	return nil
}

func (r *sessionContextMusicRadio) Running() bool { return r.running }

func (r *sessionContextMusicRadio) Stop(time.Duration) {
	r.running = false
	r.status.Running = false
}

func (r *sessionContextMusicRadio) Status() obsrtmp.RadioStatus { return r.status }

func TestMusicPlaylistGPUSessionContextIsScopedAndCancelledOnCleanup(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "1")

	radio := &sessionContextMusicRadio{}
	srv.radio = radio
	srv.startMusicRadio = radio.Start
	if err := srv.startMusicRadioSession(obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation); err != nil {
		t.Fatal(err)
	}
	srv.musicTimelineMu.Lock()
	gpuCtx := srv.musicPlaylistGPUSessionCtx
	gpuCancel := srv.musicPlaylistGPUSessionCancel
	srv.musicTimelineMu.Unlock()
	if gpuCtx == nil || gpuCancel == nil {
		t.Fatal("explicit GPU session did not create a cancellable worker context")
	}
	select {
	case <-gpuCtx.Done():
		t.Fatal("GPU worker context was cancelled before session cleanup")
	default:
	}

	srv.closePlaylistGPUEvaluationSession()
	select {
	case <-gpuCtx.Done():
	default:
		t.Fatal("GPU worker context was not cancelled during session cleanup")
	}
	srv.musicTimelineMu.Lock()
	if srv.musicPlaylistGPUSessionCtx != nil || srv.musicPlaylistGPUSessionCancel != nil {
		srv.musicTimelineMu.Unlock()
		t.Fatal("GPU session context was not cleared after cleanup")
	}
	srv.musicTimelineMu.Unlock()

	radio.running = false
	srv.setPlaylistGPUEvaluationArmed(false)
	if err := srv.startMusicRadioSession(obsrtmp.RadioPublisherProfileCPUDefault); err != nil {
		t.Fatal(err)
	}
	srv.musicTimelineMu.Lock()
	cpuCtx := srv.musicPlaylistGPUSessionCtx
	srv.musicTimelineMu.Unlock()
	if cpuCtx != nil {
		t.Fatal("normal CPU session created a GPU worker context")
	}
	if radio.starts != 2 {
		t.Fatalf("radio starts = %d, want 2", radio.starts)
	}
}

func TestMusicPlaylistContinuousRuntimeStartsWithExplicitSessionContext(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	t.Setenv("IMAGEPAD_PLAYLIST_COMPOSITORD", "playlist-compositord-test")
	srv.SetPlaylistGPUEncodedFrameSink(func(video.EncodedH264Frame) error { return nil })

	sessionCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUSessionCtx = sessionCtx
	srv.musicTimelineEpoch = 1
	srv.musicActiveBaseSeq = 0
	srv.musicTimelineMu.Unlock()

	var gotCtx context.Context
	oldStart := startPlaylistGPUWorker
	t.Cleanup(func() { startPlaylistGPUWorker = oldStart })
	startPlaylistGPUWorker = func(ctx context.Context, _, _ string) (*video.PlaylistGPUWorker, error) {
		gotCtx = ctx
		return nil, errors.New("test worker start stop")
	}

	if err := srv.ensurePlaylistGPUContinuousRuntime(); err == nil {
		t.Fatal("worker start failure must remain fail-closed")
	}
	if gotCtx == nil {
		t.Fatal("continuous runtime did not invoke the worker starter")
	}
	if gotCtx != sessionCtx {
		t.Fatal("continuous runtime did not pass the explicit GPU session context to the worker")
	}
}

func TestMusicPlaylistContinuousRuntimeDoesNotReplaceNonRunningSessionWorker(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	t.Setenv("IMAGEPAD_PLAYLIST_COMPOSITORD", "playlist-compositord-test")
	srv.SetPlaylistGPUEncodedFrameSink(func(video.EncodedH264Frame) error { return nil })

	sessionCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	staleWorker := &video.PlaylistGPUWorker{}
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUSessionCtx = sessionCtx
	srv.musicPlaylistGPUWorker = staleWorker
	srv.musicTimelineEpoch = 1
	srv.musicActiveBaseSeq = 0
	srv.musicTimelineMu.Unlock()

	starts := 0
	oldStart := startPlaylistGPUWorker
	t.Cleanup(func() { startPlaylistGPUWorker = oldStart })
	startPlaylistGPUWorker = func(_ context.Context, _, _ string) (*video.PlaylistGPUWorker, error) {
		starts++
		return nil, errors.New("replacement worker must not be started")
	}

	if err := srv.ensurePlaylistGPUContinuousRuntime(); err == nil {
		t.Fatal("non-running session worker must fail closed")
	}
	if starts != 0 {
		t.Fatalf("non-running session worker was replaced by %d new worker starts", starts)
	}
}

func TestMusicPlaylistContinuousSuccessRetainsSessionWorker(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	worker := &video.PlaylistGPUWorker{}
	controller, err := video.NewPlaylistGPUTransitionController(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	telemetry := video.PlaylistGPUOutputTelemetry{ExpectedFrames: 1, AcceptedFrames: 1}
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUSessionCtx = ctx
	srv.musicTimelineMu.Unlock()

	if err := srv.commitPlaylistGPUContinuousSuccess(worker, controller, telemetry); err != nil {
		t.Fatal(err)
	}
	srv.musicTimelineMu.Lock()
	gotWorker := srv.musicPlaylistGPUWorker
	gotController := srv.musicPlaylistGPUController
	srv.musicTimelineMu.Unlock()
	if gotWorker != worker || gotController != controller {
		t.Fatalf("continuous success replaced session ownership: worker=%p/%p controller=%p/%p", gotWorker, worker, gotController, controller)
	}
}

func TestMusicPlaylistTransitionSuccessRetainsSessionWorker(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	worker := &video.PlaylistGPUWorker{}
	controller, err := video.NewPlaylistGPUTransitionController(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := video.CompilePlaylistTransition(video.TransitionRequest{
		Schema:               video.GPUPlaylistTimelineSchema,
		Epoch:                1,
		Reason:               video.PlaylistTransitionStop,
		SourceTrackID:        "current",
		PlaybackHeadSequence: 0,
		PlaybackHeadPTSNs:    0,
	}, "", 0, 0, 2, 2, 33_333_333, video.AudioFadePlan{Curve: "linear", DurationNS: 66_666_666})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return controller.Request(plan) },
		controller.StopNormalSubmit,
		controller.BeginDrainInFlight,
		controller.BeginFadeTailRendering,
		controller.BeginFlushOutput,
		func() error { return controller.CompleteTransition(plan.FadeEndSequence) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if controller.State() != video.PlaylistGPUStateTransitionComplete {
		t.Fatalf("stop transition state = %q", controller.State())
	}
	telemetry := video.PlaylistGPUOutputTelemetry{ExpectedFrames: 1, AcceptedFrames: 1}
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUSessionCtx = ctx
	srv.musicTimelineMu.Unlock()

	if err := srv.commitPlaylistGPUTransitionSuccess(worker, controller, telemetry); err != nil {
		t.Fatal(err)
	}
	srv.musicTimelineMu.Lock()
	gotWorker := srv.musicPlaylistGPUWorker
	gotController := srv.musicPlaylistGPUController
	srv.musicTimelineMu.Unlock()
	if gotWorker != worker || gotController != controller {
		t.Fatalf("stop transition success released session ownership: worker=%p/%p controller=%p/%p", gotWorker, worker, gotController, controller)
	}
}

func TestMusicPlaylistTransitionSuccessDoesNotResumeCancelledSession(t *testing.T) {
	srv, _, _ := setupExplicitGPUTransitionFailureTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	worker := &video.PlaylistGPUWorker{}
	controller, err := video.NewPlaylistGPUTransitionController(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := video.CompilePlaylistTransition(video.TransitionRequest{
		Schema:               video.GPUPlaylistTimelineSchema,
		Epoch:                1,
		Reason:               video.PlaylistTransitionStop,
		SourceTrackID:        "current",
		PlaybackHeadSequence: 0,
		PlaybackHeadPTSNs:    0,
	}, "", 0, 0, 1, 1, 33_333_333, video.AudioFadePlan{Curve: "linear", DurationNS: 33_333_333})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { return controller.Request(plan) },
		controller.StopNormalSubmit,
		controller.BeginDrainInFlight,
		controller.BeginFadeTailRendering,
		controller.BeginFlushOutput,
		func() error { return controller.CompleteTransition(plan.FadeEndSequence) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	srv.musicTimelineMu.Lock()
	srv.musicPlaylistGPUSessionCtx = ctx
	srv.musicTimelineMu.Unlock()
	oldResume := resumePlaylistGPUWorkerAfterStop
	resumes := 0
	resumePlaylistGPUWorkerAfterStop = func(*video.PlaylistGPUWorker) error {
		resumes++
		return nil
	}
	t.Cleanup(func() { resumePlaylistGPUWorkerAfterStop = oldResume })

	if err := srv.commitPlaylistGPUTransitionSuccess(worker, controller, video.PlaylistGPUOutputTelemetry{ExpectedFrames: 1, AcceptedFrames: 1}); err == nil {
		t.Fatal("cancelled GPU session transition was accepted")
	}
	if resumes != 0 {
		t.Fatalf("cancelled GPU session attempted worker resume %d times", resumes)
	}
	if controller.State() != video.PlaylistGPUStateTransitionComplete {
		t.Fatalf("cancelled session changed controller state to %q", controller.State())
	}
}

func TestMusicPlaylistContinuousExecutionUsesFrozenSessionContext(t *testing.T) {
	for _, wantOperation := range []string{"prepare", "execute"} {
		t.Run(wantOperation, func(t *testing.T) {
			srv, _, current := setupExplicitGPUTransitionFailureTest(t)
			target := srv.musicQueue.Add(playlist.Track{Title: "Target", Status: playlist.TrackReady})
			sessionCtx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			worker := &video.PlaylistGPUWorker{}
			controller, err := video.NewPlaylistGPUTransitionController(1, 0)
			if err != nil {
				t.Fatal(err)
			}
			srv.musicTimelineMu.Lock()
			srv.musicPlaylistGPUSessionCtx = sessionCtx
			srv.musicPlaylistGPUWorker = worker
			srv.musicPlaylistGPUController = controller
			srv.musicPlaylistGPUFrameSink = func(video.EncodedH264Frame) error { return nil }
			srv.musicPlaylistGPUAssets = map[string]video.TrackAssets{
				current.ID: {TrackID: current.ID},
				target.ID:  {TrackID: target.ID},
			}
			srv.musicPlaylistGPUTimelines = map[string]video.TrackTimeline{
				current.ID: {TrackID: current.ID},
				target.ID:  {TrackID: target.ID, FPS: 30, FrameCount: 1},
			}
			srv.musicTimelineMu.Unlock()
			plan, err := video.CompilePlaylistTransition(video.TransitionRequest{
				Schema: video.GPUPlaylistTimelineSchema, Epoch: 1, Reason: video.PlaylistTransitionTrackChange,
				SourceTrackID: current.ID, PlaybackHeadSequence: 0, PlaybackHeadPTSNs: 0,
			}, target.ID, 2, 66_666_666, 1, 1, 33_333_333, video.AudioFadePlan{Curve: "linear", DurationNS: 33_333_333})
			if err != nil {
				t.Fatal(err)
			}

			var gotCtx context.Context
			oldPrepare := preparePlaylistGPUTrack
			oldExecute := executePlaylistGPUContinuousTrack
			t.Cleanup(func() {
				preparePlaylistGPUTrack = oldPrepare
				executePlaylistGPUContinuousTrack = oldExecute
			})
			preparePlaylistGPUTrack = func(ctx context.Context, _ *video.PlaylistGPUWorker, _ uint64, _ video.TrackAssets) error {
				gotCtx = ctx
				if wantOperation == "prepare" {
					return errors.New("stop after prepare context capture")
				}
				return nil
			}
			executePlaylistGPUContinuousTrack = func(ctx context.Context, _ *video.PlaylistGPUWorker, _ video.TrackAssets, _ video.TrackTimeline, _ video.TrackAssets, _ video.TrackTimeline, _ video.TransitionPlan, _ uint64, _ func(video.EncodedH264Frame) error) error {
				gotCtx = ctx
				return errors.New("stop after execute context capture")
			}

			if err := srv.submitPlaylistGPUContinuousTrack(video.TrackAssets{TrackID: current.ID}, video.TrackTimeline{TrackID: current.ID}, plan); err == nil {
				t.Fatal("continuous execution unexpectedly succeeded")
			}
			if gotCtx != sessionCtx {
				t.Fatalf("%s received context %p, want frozen session context %p", wantOperation, gotCtx, sessionCtx)
			}
		})
	}
}

func TestRadioHLSNotFoundWhileStopped(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	req := httptest.NewRequest(http.MethodGet, "/radio/index.m3u8", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("radio HLS while stopped = %d, want 404", rec.Code)
	}
}

func TestMusicTimelineCommitAllocatesAuthoritativeEpochAndSequence(t *testing.T) {
	srv, _ := testServer(t, false)
	defer cleanupTestServer(srv)

	first := playlist.Track{ID: "first", DurationSeconds: 2, Status: playlist.TrackReady}
	if !srv.commitMusicTimelineTrack(first) {
		t.Fatal("first timeline commit failed")
	}
	srv.musicTimelineMu.Lock()
	firstEpoch := srv.musicTimelineEpoch
	firstBase := srv.musicActiveBaseSeq
	firstTail := srv.musicActiveTailSeq
	srv.musicTimelineMu.Unlock()
	if firstEpoch != 1 || firstBase != 0 || firstTail != 59 {
		t.Fatalf("first timeline state = epoch %d base %d tail %d", firstEpoch, firstBase, firstTail)
	}

	second := playlist.Track{ID: "second", DurationSeconds: 1, Status: playlist.TrackReady}
	if !srv.commitMusicTimelineTrack(second) {
		t.Fatal("second timeline commit failed")
	}
	srv.musicTimelineMu.Lock()
	secondEpoch := srv.musicTimelineEpoch
	secondBase := srv.musicActiveBaseSeq
	secondTail := srv.musicActiveTailSeq
	activeID := srv.musicActiveTrackID
	srv.musicTimelineMu.Unlock()
	if secondEpoch != 2 || secondBase != 60 || secondTail != 89 || activeID != second.ID {
		t.Fatalf("second timeline state = epoch %d base %d tail %d active %q", secondEpoch, secondBase, secondTail, activeID)
	}
}
