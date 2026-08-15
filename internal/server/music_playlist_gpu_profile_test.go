package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/playlist"
)

func TestMusicPlaylistGPUEnvironmentDoesNotArmNormalStart(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "1")

	started := false
	srv.startMusicRadio = func() error {
		started = true
		if srv.playlistGPUEvaluationArmedState() {
			t.Fatal("normal playlist start armed GPU evaluation from environment")
		}
		return nil
	}

	rec := adminJSON(t, mux, httptest.NewRequest(http.MethodPost, "/api/music/playlist/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("normal start = %d: %s", rec.Code, rec.Body.String())
	}
	if !started {
		t.Fatal("normal playlist start did not call the radio starter")
	}
	if srv.playlistGPUEvaluationArmedState() {
		t.Fatal("normal playlist start left GPU evaluation armed")
	}
	if got := srv.nextRadioPublisherProfile(); got != obsrtmp.RadioPublisherProfileCPUDefault {
		t.Fatalf("normal publisher profile = %q, want CPU default", got)
	}
}

func TestMusicPlaylistGPUEvaluationRequiresDedicatedEntry(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "1")

	started := false
	srv.startMusicRadio = func() error {
		started = true
		return nil
	}

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistGPUEvaluationStart(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/gpu-evaluation/start", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dedicated GPU evaluation start = %d: %s", rec.Code, rec.Body.String())
	}
	if !started {
		t.Fatal("dedicated GPU evaluation entry did not start the radio")
	}
	if !srv.playlistGPUEvaluationArmedState() {
		t.Fatal("dedicated GPU evaluation entry did not arm the session")
	}
	if got := srv.nextRadioPublisherProfile(); got != obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation {
		t.Fatalf("dedicated publisher profile = %q, want GPU evaluation", got)
	}
}

func TestMusicPlaylistGPUEvaluationEntryFailsClosedWithoutCapability(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "")

	started := false
	srv.startMusicRadio = func() error {
		started = true
		return nil
	}

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistGPUEvaluationStart(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/gpu-evaluation/start", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("GPU evaluation without capability = %d: %s", rec.Code, rec.Body.String())
	}
	if started {
		t.Fatal("GPU evaluation without capability started the radio")
	}
	if srv.playlistGPUEvaluationArmedState() {
		t.Fatal("failed GPU evaluation entry left the session armed")
	}
}

func TestMusicPlaylistGPUEvaluationEntryRejectsRunningCPUSession(t *testing.T) {
	srv, _ := testServer(t, true)
	defer cleanupTestServer(srv)
	enableMusicMode(t)
	t.Setenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE", "1")
	srv.radio = &runningMusicRadio{status: obsrtmp.RadioStatus{
		Running: true,
		ActiveSession: &obsrtmp.RadioActiveSessionContract{
			PublisherProfile: obsrtmp.RadioPublisherProfileCPUDefault,
		},
	}}

	started := false
	srv.startMusicRadio = func() error {
		started = true
		return nil
	}

	rec := httptest.NewRecorder()
	srv.handleMusicPlaylistGPUEvaluationStart(rec, httptest.NewRequest(http.MethodPost, "/api/music/playlist/gpu-evaluation/start", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("GPU evaluation while CPU session runs = %d: %s", rec.Code, rec.Body.String())
	}
	if started || srv.playlistGPUEvaluationArmedState() {
		t.Fatal("running CPU session was replaced or GPU evaluation was armed")
	}
}

func TestMusicPlaylistRuntimeRouteUsesFrozenSessionContract(t *testing.T) {
	t.Run("GPU active session remains GPU after arm is cleared", func(t *testing.T) {
		srv, _ := testServer(t, true)
		defer cleanupTestServer(srv)
		srv.radio = &runningMusicRadio{status: obsrtmp.RadioStatus{
			Running: true,
			ActiveSession: &obsrtmp.RadioActiveSessionContract{
				PublisherProfile: obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation,
			},
		}}
		srv.setPlaylistGPUEvaluationArmed(false)

		if !srv.playlistGPUEvaluationRouteActive() {
			t.Fatal("GPU active session was not recognized after arm was cleared")
		}
	})

	t.Run("CPU active session ignores a stale GPU arm", func(t *testing.T) {
		srv, _ := testServer(t, true)
		defer cleanupTestServer(srv)
		srv.radio = &runningMusicRadio{status: obsrtmp.RadioStatus{
			Running: true,
			ActiveSession: &obsrtmp.RadioActiveSessionContract{
				PublisherProfile: obsrtmp.RadioPublisherProfileCPUDefault,
			},
		}}
		srv.setPlaylistGPUEvaluationArmed(true)

		if srv.playlistGPUEvaluationRouteActive() {
			t.Fatal("stale GPU arm changed the active CPU session route")
		}

		track := srv.musicQueue.Add(playlist.Track{Title: "CPU", Status: playlist.TrackReady, MediaPath: "cpu.ts"})
		if _, id, _, ok := srv.nextRadioTrack(); !ok || id != track.ID {
			t.Fatalf("CPU active session route = id:%q ok:%v, want %q", id, ok, track.ID)
		}
	})
}
