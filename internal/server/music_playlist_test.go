package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	t.Cleanup(func() {
		ensureFFmpeg = oldEnsure
		analyzeAudioForKind = oldAnalyze
		renderRadioTrack = oldRender
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
	if st["hlsUrl"] != "" {
		t.Fatalf("hlsUrl must be empty while stopped, got %v", st["hlsUrl"])
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

func TestMusicPlaylistsSaveLoadDelete(t *testing.T) {
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
