package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/settings"
)

func TestUploadURLVideoModeRejectsPrivateHost(t *testing.T) {
	srv, mux := testServer(t, true)
	defer srv.store.Reset()

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"http://192.168.0.1/video.mp4"}`))
	rec := adminJSON(t, mux, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "private network") {
		t.Fatalf("body = %q, want private network rejection", rec.Body.String())
	}
}

func TestUploadURLVideoModeDoesNotFallbackToImageOnYTDLPFailure(t *testing.T) {
	localYTDLP := filepath.Join(settings.Dir(), "bin", "yt-dlp.exe")
	if _, err := os.Stat(localYTDLP); os.IsNotExist(err) {
		if _, err := exec.LookPath("yt-dlp"); err != nil {
			t.Skip("yt-dlp not available for integration check")
		}
	}

	srv, mux := testServer(t, true)
	defer srv.store.Reset()

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"https://example.com/"}`))
	rec := adminJSON(t, mux, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "yt-dlp") {
		t.Fatalf("body = %q, want yt-dlp guidance", body)
	}
	if strings.Contains(body, "remote content is not an image") {
		t.Fatal("fell back to image download path")
	}
}

func TestUploadURLImageModeRejectsPrivateHost(t *testing.T) {
	srv, mux := testServer(t, false)
	defer srv.store.Reset()

	req := httptest.NewRequest(http.MethodPost, "/api/upload-url", strings.NewReader(`{"url":"http://127.0.0.1/image.png"}`))
	rec := adminJSON(t, mux, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for blocked host; body = %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "private network") {
		t.Fatalf("body = %q, want private network rejection", rec.Body.String())
	}
}

func testServer(t *testing.T, videoEnabled bool) (*Server, *http.ServeMux) {
	t.Helper()

	appDir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", appDir)
	pinDevTools(t)

	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = videoEnabled
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(filepath.Join(settings.Dir(), "media"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Host: "127.0.0.1", Port: 8080}
	srv := New(cfg, store, "")
	t.Cleanup(srv.StopOBSReceiver)
	mux := http.NewServeMux()
	srv.Register(mux)
	return srv, mux
}

// pinDevTools points IMAGEPAD_FFMPEG/IMAGEPAD_FFPROBE at the dev machine's
// tools when present on PATH. Production never resolves tools from PATH, so the
// test helper locates them via exec.LookPath and supplies them as explicit env
// overrides. Without this, EnsureFFmpeg would attempt a real network download
// in tests that exercise audio/video paths. If the tools are absent, the env
// vars are left unset and ffmpeg-dependent tests fail as they would in CI.
func pinDevTools(t *testing.T) {
	t.Helper()
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		t.Setenv("IMAGEPAD_FFMPEG", p)
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		t.Setenv("IMAGEPAD_FFPROBE", p)
	}
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		t.Setenv("IMAGEPAD_YTDLP", p)
	}
}

func adminJSON(t *testing.T, mux *http.ServeMux, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:50000"
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
