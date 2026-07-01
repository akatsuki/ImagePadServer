package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/video"
)

// TestOBSPublicStreamHTTPAcceptance drives the real /stream handlers with a live
// RTMP publish (FFmpeg standing in for OBS) and records the public HTTP surface
// for the HLS family: MIME type, Cache-Control, and an FFprobe-readable first
// segment. Opt-in (needs a pinned FFmpeg) so the default suite never runs it.
//
// This is the automatable part of plan Task 7 Step 1; VRChat playback and
// in-world latency remain operator tasks (see the acceptance doc).
func TestOBSPublicStreamHTTPAcceptance(t *testing.T) {
	if os.Getenv("IMAGEPAD_OBS_HTTP_TEST") == "" {
		t.Skip("set IMAGEPAD_OBS_HTTP_TEST=1 (and IMAGEPAD_FFMPEG) to run the OBS HTTP acceptance test")
	}

	for _, mode := range []string{obsrtmp.LatencyModeHLSHigh, obsrtmp.LatencyModeHLS} {
		t.Run(mode, func(t *testing.T) {
			srv, mux := testServer(t, true)
			ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
			if ffmpeg == "" {
				t.Skip("IMAGEPAD_FFMPEG not set")
			}

			port := freeTCPPort(t)
			mgr := obsrtmp.New(srv.store.Dir(), "127.0.0.1", port, "imagepad",
				func() video.QualityPreset { return video.ResolveQuality("720", 0) },
				func() obsrtmp.LatencyProfile { return obsrtmp.NormalizeLatencyProfile(mode) },
				obsrtmp.Callbacks{})
			srv.obs = mgr
			mgr.Start()
			t.Cleanup(func() { mgr.StopAndWait(5 * time.Second) })

			// Give the manager's RTMP receiver a moment to bind before publishing.
			time.Sleep(2 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			t.Cleanup(cancel)
			pub := exec.CommandContext(ctx, ffmpeg,
				"-hide_banner", "-loglevel", "error",
				"-re",
				"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15",
				"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
				"-t", "25",
				"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
				"-g", "15", "-pix_fmt", "yuv420p",
				"-c:a", "aac", "-b:a", "96k", "-ar", "48000", "-ac", "2",
				"-f", "flv", "rtmp://127.0.0.1:"+strconv.Itoa(port)+"/live/imagepad",
			)
			if err := pub.Start(); err != nil {
				t.Fatalf("start publisher: %v", err)
			}
			t.Cleanup(func() {
				if pub.Process != nil {
					_ = pub.Process.Kill()
				}
				_ = pub.Wait()
			})

			// Wait until the manager reports a connected, ready session.
			var id string
			deadline := time.Now().Add(35 * time.Second)
			for time.Now().Before(deadline) {
				st := mgr.Status()
				if st.Connected && st.MediaID != "" {
					id = st.MediaID
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if id == "" {
				t.Fatal("OBS session never became connected/ready")
			}

			// Master/entry playlist through the real handler chain.
			master := getStream(t, mux, "/stream/"+id+"/current.m3u8")
			if master.code != http.StatusOK {
				t.Fatalf("playlist status = %d", master.code)
			}
			if ct := master.header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
				t.Fatalf("playlist Content-Type = %q", ct)
			}
			if cc := master.header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
				t.Fatalf("playlist Cache-Control = %q, want no-store", cc)
			}
			t.Logf("[%s] playlist: %d, Content-Type=%q, Cache-Control=%q, %d bytes",
				mode, master.code, master.header.Get("Content-Type"), master.header.Get("Cache-Control"), len(master.body))

			// Resolve a playable media playlist + a segment reference.
			segName := resolveSegment(t, mux, id, mode, string(master.body))
			seg := getStream(t, mux, "/stream/"+id+"/"+segName)
			if seg.code != http.StatusOK {
				t.Fatalf("segment %q status = %d", segName, seg.code)
			}
			t.Logf("[%s] segment %s: %d, Content-Type=%q, %d bytes",
				mode, segName, seg.code, seg.header.Get("Content-Type"), len(seg.body))

			// FFprobe the first segment (best effort, logged).
			if probe := ffprobeBytes(t, seg.body, segName); probe != "" {
				t.Logf("[%s] ffprobe first segment: %s", mode, probe)
			} else {
				t.Logf("[%s] ffprobe unavailable or unreadable (non-fatal)", mode)
			}
		})
	}
}

type streamResp struct {
	code   int
	header http.Header
	body   []byte
}

func getStream(t *testing.T, mux *http.ServeMux, path string) streamResp {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return streamResp{code: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

// resolveSegment returns a segment filename to fetch from an HLS playlist.
func resolveSegment(t *testing.T, mux *http.ServeMux, id, mode, master string) string {
	t.Helper()
	if seg := firstURI(master, ".ts"); seg != "" {
		return seg
	}
	t.Fatalf("HLS playlist has no .ts segment:\n%s", master)
	return ""
}

func firstURI(playlist, suffix string) string {
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-PREFETCH:") {
			cand := strings.TrimPrefix(line, "#EXT-X-PREFETCH:")
			if strings.HasSuffix(cand, suffix) {
				return filepath.Base(cand)
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, suffix) {
			return filepath.Base(line)
		}
	}
	return ""
}

func ffprobeBytes(t *testing.T, data []byte, name string) string {
	t.Helper()
	probe := resolveFFprobe()
	if probe == "" {
		return ""
	}
	tmp := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return ""
	}
	out, err := exec.Command(probe, "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "csv=p=0", tmp).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(string(out), "\n", " "))
}

func resolveFFprobe() string {
	if p := strings.TrimSpace(os.Getenv("IMAGEPAD_FFPROBE")); p != "" {
		return p
	}
	if ff := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG")); ff != "" {
		cand := filepath.Join(filepath.Dir(ff), "ffprobe.exe")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	if p, err := exec.LookPath("ffprobe"); err == nil {
		return p
	}
	return ""
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
