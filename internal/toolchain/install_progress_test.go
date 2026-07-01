package toolchain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestInstallTrackerLifecycle(t *testing.T) {
	resetInstallProgress()
	if s := ToolInstallStatus(); s.Active {
		t.Fatalf("fresh tracker Active=true")
	}
	installBegin("ffmpeg")
	installPhase("download")
	installPercent(42)
	s := ToolInstallStatus()
	if !s.Active || s.Tool != "ffmpeg" || s.Phase != "download" || s.Percent != 42 {
		t.Fatalf("unexpected status: %+v", s)
	}
	installDone()
	if s := ToolInstallStatus(); s.Active || s.Failed || s.Percent != 100 {
		t.Fatalf("after done: %+v", s)
	}
	installBegin("yt-dlp")
	installFail("boom")
	if s := ToolInstallStatus(); s.Active || !s.Failed || s.Message != "boom" {
		t.Fatalf("after fail: %+v", s)
	}
}

func TestProgressWriterReportsMonotonicPercent(t *testing.T) {
	resetInstallProgress()
	var seen []int
	pw := &progressWriter{total: 100, onProgress: func(p int) { seen = append(seen, p) }}
	src := bytes.NewReader(make([]byte, 100))
	var dst bytes.Buffer
	if _, err := io.Copy(&dst, io.TeeReader(src, pw)); err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 || seen[len(seen)-1] != 100 {
		t.Fatalf("expected final 100, got %v", seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("non-monotonic percent: %v", seen)
		}
	}
}

func TestInstallTrackerConcurrentSnapshot(t *testing.T) {
	resetInstallProgress()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			installBegin("ffmpeg")
			installPercent(10)
			_ = ToolInstallStatus()
		}()
	}
	wg.Wait()
}

func TestDownloadFileReportsProgress(t *testing.T) {
	resetInstallProgress()
	installBegin("yt-dlp")
	payload := make([]byte, 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	sum := sha256.Sum256(payload)
	dst := filepath.Join(t.TempDir(), "out.bin")
	if err := downloadFile(dst, srv.URL, 1<<20, hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	if s := ToolInstallStatus(); s.Phase != "download" || s.Percent != 100 {
		t.Fatalf("expected download phase at 100%%, got %+v", s)
	}
}
