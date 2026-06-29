package obsrtmp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

// fakeProcess is a controllable managedProcess for lifecycle tests. It never
// touches a real OS process, so a test can drive stop/kill/exit ordering
// deterministically.
type fakeProcess struct {
	processID  int
	exit       chan error
	stopCalls  atomic.Int32
	killCalls  atomic.Int32
	exitOnStop bool
	mu         sync.Mutex
	closed     bool
}

func newFakeProcess() *fakeProcess { return &fakeProcess{exit: make(chan error, 1)} }

func (f *fakeProcess) pid() int { return f.processID }

func (f *fakeProcess) stop() error {
	f.stopCalls.Add(1)
	if f.exitOnStop {
		f.finish(nil)
	}
	return nil
}

func (f *fakeProcess) kill() error {
	f.killCalls.Add(1)
	f.finish(nil)
	return nil
}

func (f *fakeProcess) done() <-chan error { return f.exit }

func (f *fakeProcess) finish(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.closed = true
	f.exit <- err
	close(f.exit)
}

func testRuntime(cfg mediaMTXSessionConfig) *mediaMTXRuntime {
	rt := newMediaMTXRuntime("mediamtx", cfg)
	rt.stopGrace = 50 * time.Millisecond
	return rt
}

func defaultTestConfig() mediaMTXSessionConfig {
	return mediaMTXSessionConfig{
		Path:          "obs_session",
		PublishUser:   "pub",
		PublishPass:   "secret",
		Ports:         mediaMTXPorts{API: 9997, HLS: 8888, RTSP: 8554},
		AdvertiseHost: "192.168.1.50",
	}
}

func TestRenderMediaMTXConfigDisablesAndRestricts(t *testing.T) {
	out := renderMediaMTXConfig(defaultTestConfig())
	mustContain := []string{
		"rtmp: no",
		"webrtc: no",
		"srt: no",
		"moq: no",
		"rtsp: yes",
		"rtspTransports: [tcp]",
		"apiAddress: 127.0.0.1:9997",
		"hlsAddress: 127.0.0.1:8888",
		"hlsVariant: lowLatency",
		"rtspAddress: :8554",
		"user: pub",
		"pass: secret",
		"path: obs_session",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("config missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "source: publisher") != 1 {
		t.Fatalf("expected exactly one publisher path:\n%s", out)
	}
}

func TestRenderMediaMTXConfigAllowsExternalReadersOnRandomPath(t *testing.T) {
	out := renderMediaMTXConfig(defaultTestConfig())
	readUser := "  - user: any\n    permissions:\n      - action: read\n        path: obs_session\n      - action: playback\n        path: obs_session\n"
	if !strings.Contains(out, readUser) {
		t.Fatalf("external path-scoped read permission missing:\n%s", out)
	}
	if strings.Contains(out, "ips: ['127.0.0.1/32', '10.0.0.0/8'") {
		t.Fatalf("read permission remains private-network-only:\n%s", out)
	}
	apiUser := "  - user: any\n    ips: ['127.0.0.1/32']\n    permissions:\n      - action: api\n"
	if !strings.Contains(out, apiUser) {
		t.Fatalf("loopback API permission missing:\n%s", out)
	}
}

func TestMediaMTXRuntimeURLs(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	if got, want := rt.publishURL(), "rtsp://pub:secret@127.0.0.1:8554/obs_session"; got != want {
		t.Fatalf("publishURL = %q, want %q", got, want)
	}
	if got, want := rt.hlsBaseURL(), "http://127.0.0.1:8888/obs_session"; got != want {
		t.Fatalf("hlsBaseURL = %q, want %q", got, want)
	}
	if got, want := rt.rtspURL(), "rtsp://192.168.1.50:8554/obs_session"; got != want {
		t.Fatalf("rtspURL = %q, want %q", got, want)
	}
}

func TestMediaMTXStartWaitsForHealth(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	var calls atomic.Int32
	rt.checkHealth = func(context.Context, string) error {
		if calls.Add(1) < 3 {
			return context.DeadlineExceeded
		}
		return nil
	}
	if err := rt.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = rt.stop(0) })
	if calls.Load() < 3 {
		t.Fatalf("health polled %d times, want >= 3", calls.Load())
	}
}

func TestMediaMTXStartFailsOnEarlyExit(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	proc.finish(errOf("port in use"))
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	rt.checkHealth = func(context.Context, string) error { return context.DeadlineExceeded }
	err := rt.start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exited before becoming healthy") {
		t.Fatalf("expected early-exit error, got %v", err)
	}
}

func TestMediaMTXStartTimesOutAndTearsDown(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	rt.checkHealth = func(context.Context, string) error { return context.DeadlineExceeded }
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := rt.start(ctx); err == nil {
		t.Fatal("expected start to fail when health never succeeds")
	}
	// Teardown must have signalled the owned process.
	if proc.stopCalls.Load() == 0 && proc.killCalls.Load() == 0 {
		t.Fatal("timed-out start did not stop the process")
	}
}

func TestMediaMTXGracefulStopAvoidsKill(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	proc.exitOnStop = true
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	rt.checkHealth = func(context.Context, string) error { return nil }
	if err := rt.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := rt.stop(time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if proc.stopCalls.Load() != 1 {
		t.Fatalf("stop called %d times, want 1", proc.stopCalls.Load())
	}
	if proc.killCalls.Load() != 0 {
		t.Fatalf("graceful stop should not kill, kill called %d times", proc.killCalls.Load())
	}
}

func TestMediaMTXForcedStopEscalatesToKill(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess() // never exits on stop
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	rt.checkHealth = func(context.Context, string) error { return nil }
	if err := rt.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := rt.stop(50 * time.Millisecond); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if proc.killCalls.Load() == 0 {
		t.Fatal("forced stop should escalate to kill")
	}
}

func TestMediaMTXStopOnlyTouchesOwnedProcess(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	owned := newFakeProcess()
	owned.exitOnStop = true
	unrelated := newFakeProcess()
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return owned, nil }
	rt.checkHealth = func(context.Context, string) error { return nil }
	if err := rt.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := rt.stop(time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if unrelated.stopCalls.Load() != 0 || unrelated.killCalls.Load() != 0 {
		t.Fatal("stop must never touch an unrelated process")
	}
}

func TestMediaMTXStopIsIdempotent(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	proc.exitOnStop = true
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	rt.checkHealth = func(context.Context, string) error { return nil }
	if err := rt.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = rt.stop(time.Second)
	_ = rt.stop(time.Second)
	if proc.stopCalls.Load() != 1 {
		t.Fatalf("second stop re-signalled the process: %d calls", proc.stopCalls.Load())
	}
}

func TestMediaMTXWaitObservesCrash(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	proc := newFakeProcess()
	rt.startProcess = func(context.Context, string, string) (managedProcess, error) { return proc, nil }
	rt.checkHealth = func(context.Context, string) error { return nil }
	if err := rt.start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = rt.stop(0) })
	go proc.finish(errOf("sidecar crashed"))
	select {
	case err := <-rt.wait():
		if err == nil || !strings.Contains(err.Error(), "crashed") {
			t.Fatalf("wait error = %v, want crash", err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not observe the crash")
	}
}

func TestMediaMTXProxyPreservesSemantics(t *testing.T) {
	var gotMethod, gotPath, gotQuery, gotRange string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery, gotRange = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Range")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Content-Range", "bytes 0-9/100")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer upstream.Close()

	rt := testRuntime(defaultTestConfig())
	rt.httpClient = upstream.Client()
	base := strings.TrimPrefix(upstream.URL, "http://")
	host, port := splitHostPortForTest(t, base)
	rt.cfg.Ports.HLS = port
	rt.cfg.Path = "live"
	_ = host

	req := httptest.NewRequest(http.MethodGet, "/stream/abc/index.m3u8?_HLS_msn=3&_HLS_part=2", nil)
	req.Header.Set("Range", "bytes=0-9")
	rec := httptest.NewRecorder()
	rt.proxyHLS(rec, req, "index.m3u8")

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Fatalf("content-type not preserved: %q", ct)
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 0-9/100" {
		t.Fatalf("content-range not preserved: %q", cr)
	}
	if gotMethod != http.MethodGet || gotPath != "/live/index.m3u8" {
		t.Fatalf("upstream method/path = %s %s", gotMethod, gotPath)
	}
	if gotQuery != "_HLS_msn=3&_HLS_part=2" {
		t.Fatalf("blocking-reload query not preserved: %q", gotQuery)
	}
	if gotRange != "bytes=0-9" {
		t.Fatalf("range not preserved: %q", gotRange)
	}
}

func TestMediaMTXProxyCancellationReturnsBadGateway(t *testing.T) {
	rt := testRuntime(defaultTestConfig())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/stream/abc/index.m3u8", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	rt.proxyHLS(rec, req, "index.m3u8")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("cancelled proxy status = %d, want 502", rec.Code)
	}
}

func TestLLHLSMediaReadyRequiresAllTags(t *testing.T) {
	full := "#EXTM3U\n#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES\n#EXT-X-PART-INF:PART-TARGET=0.2\n" +
		"#EXT-X-MAP:URI=\"init.mp4\"\n#EXT-X-PART:DURATION=0.2,URI=\"p.mp4\"\n#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"p2.mp4\"\n"
	if !llhlsMediaReady([]byte(full)) {
		t.Fatal("full LL-HLS playlist should be ready")
	}
	for _, tag := range llhlsRequiredTags {
		partial := strings.ReplaceAll(full, tag, "#EXT-X-OTHER")
		if llhlsMediaReady([]byte(partial)) {
			t.Fatalf("playlist missing %s must not be ready", tag)
		}
	}
}

func TestFFmpegRTSPArgsPublishOverTCP(t *testing.T) {
	manager := newTestManager(t, "rtspt")
	url := "rtsp://pub:secret@127.0.0.1:8554/obs_session"
	args := manager.ffmpegRTSPArgs("media123", "recording.mp4", url, video.ResolveQuality("720", 0), video.CPUVideoEncoder(video.EncoderLowLatency))

	if got := valueAfter(args, "-f"); got != "rtsp" {
		t.Fatalf("-f = %q, want rtsp\nargs: %s", got, strings.Join(args, " "))
	}
	if got := valueAfter(args, "-rtsp_transport"); got != "tcp" {
		t.Fatalf("-rtsp_transport = %q, want tcp", got)
	}
	if !containsSubsequence(args, []string{"-c:v", "libx264"}) {
		t.Fatalf("expected re-encode to H.264: %s", strings.Join(args, " "))
	}
	if !containsSubsequence(args, []string{url, "-map", "0:v:0", "-map", "0:a:0?", "-c", "copy", "-movflags", "+faststart", "recording.mp4"}) {
		t.Fatalf("expected separate stream-copy MP4 recording after the RTSP output: %s", strings.Join(args, " "))
	}
}

func TestMediaMTXPathNameSanitizes(t *testing.T) {
	if got := mediaMTXPathName("ab12CD"); got != "obs_ab12CD" {
		t.Fatalf("path = %q, want obs_ab12CD", got)
	}
	if got := mediaMTXPathName("../../etc/passwd"); strings.ContainsAny(got, "./\\") {
		t.Fatalf("path %q must not contain traversal characters", got)
	}
}

func TestProxyLLHLSGating(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/stream/x/index.m3u8", nil)
	rec := httptest.NewRecorder()

	// Wrong transport: never proxied.
	if newTestManager(t, "hls").ProxyLLHLS(rec, req, "x", "index.m3u8") {
		t.Fatal("non-LLHLS mode must not proxy")
	}
	// LL-HLS mode but no active session/runtime: not proxied.
	if newTestManager(t, "llhls").ProxyLLHLS(rec, req, "x", "index.m3u8") {
		t.Fatal("LLHLS with no active runtime must not proxy")
	}
}

// TestMediaMTXRuntimeBootsRealBinary starts the real, pinned MediaMTX with the
// rendered session config and confirms it reaches a healthy API. This validates
// the generated YAML schema against the actual binary. It is opt-in (it may
// download the pinned release) so the default suite never executes MediaMTX.
func TestMediaMTXRuntimeBootsRealBinary(t *testing.T) {
	if os.Getenv("IMAGEPAD_MEDIAMTX_TEST") == "" {
		t.Skip("set IMAGEPAD_MEDIAMTX_TEST=1 to boot the real MediaMTX binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	exe, err := EnsureMediaMTX(ctx)
	if err != nil {
		t.Fatalf("ensure MediaMTX: %v", err)
	}
	ports, err := allocMediaMTXPorts()
	if err != nil {
		t.Fatalf("alloc ports: %v", err)
	}
	user, pass, err := mediaMTXCredential()
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	rt := newMediaMTXRuntime(exe, mediaMTXSessionConfig{
		Path:          mediaMTXPathName("boottest"),
		PublishUser:   user,
		PublishPass:   pass,
		Ports:         ports,
		AdvertiseHost: "127.0.0.1",
	})
	startCtx, startCancel := context.WithTimeout(ctx, 20*time.Second)
	defer startCancel()
	if err := rt.start(startCtx); err != nil {
		t.Fatalf("MediaMTX did not boot healthy with the rendered config: %v", err)
	}
	if err := rt.stop(5 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// TestMediaMTXPublishAndLLHLSReady publishes a synthetic H.264/AAC stream to the
// real MediaMTX over RTSP/TCP using the per-session credential, then verifies the
// LL-HLS readiness gate (required low-latency tags) and the RTSP path-readiness
// gate. Opt-in; needs the pinned MediaMTX and a pinned FFmpeg.
func TestMediaMTXPublishAndLLHLSReady(t *testing.T) {
	if os.Getenv("IMAGEPAD_MEDIAMTX_TEST") == "" {
		t.Skip("set IMAGEPAD_MEDIAMTX_TEST=1 to run the MediaMTX publish test")
	}
	ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
	if ffmpeg == "" {
		t.Skip("set IMAGEPAD_FFMPEG to a pinned FFmpeg to run the MediaMTX publish test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	exe, err := EnsureMediaMTX(ctx)
	if err != nil {
		t.Fatalf("ensure MediaMTX: %v", err)
	}
	ports, err := allocMediaMTXPorts()
	if err != nil {
		t.Fatalf("alloc ports: %v", err)
	}
	user, pass, err := mediaMTXCredential()
	if err != nil {
		t.Fatalf("credential: %v", err)
	}
	rt := newMediaMTXRuntime(exe, mediaMTXSessionConfig{
		Path:          mediaMTXPathName("publish"),
		PublishUser:   user,
		PublishPass:   pass,
		Ports:         ports,
		AdvertiseHost: "127.0.0.1",
	})
	startCtx, startCancel := context.WithTimeout(ctx, 20*time.Second)
	defer startCancel()
	if err := rt.start(startCtx); err != nil {
		t.Fatalf("start MediaMTX: %v", err)
	}
	t.Cleanup(func() { _ = rt.stop(5 * time.Second) })

	pubArgs := []string{
		"-hide_banner", "-loglevel", "error",
		"-re",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=15",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000",
		"-t", "15",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-g", "15", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "96k", "-ar", "48000", "-ac", "2",
		"-f", "rtsp", "-rtsp_transport", "tcp",
		rt.publishURL(),
	}
	var stderr strings.Builder
	pub := exec.CommandContext(ctx, ffmpeg, pubArgs...)
	pub.Stderr = &stderr
	if err := pub.Start(); err != nil {
		t.Fatalf("start publisher: %v", err)
	}
	t.Cleanup(func() {
		if pub.Process != nil {
			_ = pub.Process.Kill()
		}
		_ = pub.Wait()
	})

	deadline := time.Now().Add(15 * time.Second)
	var llReady, pathReady bool
	for time.Now().Before(deadline) {
		rc, rcancel := context.WithTimeout(ctx, 2*time.Second)
		llReady = rt.llhlsReady(rc)
		pathReady = rt.pathReady(rc)
		rcancel()
		if llReady && pathReady {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !pathReady {
		t.Fatalf("MediaMTX path never became ready\nffmpeg stderr:\n%s", stderr.String())
	}
	if !llReady {
		t.Fatalf("LL-HLS never advertised the required low-latency tags\nffmpeg stderr:\n%s", stderr.String())
	}
}

func errOf(msg string) error { return &simpleError{msg} }

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

func splitHostPortForTest(t *testing.T, hostport string) (string, int) {
	t.Helper()
	host, portStr, ok := strings.Cut(hostport, ":")
	if !ok {
		t.Fatalf("bad host:port %q", hostport)
	}
	port := 0
	for _, r := range portStr {
		port = port*10 + int(r-'0')
	}
	return host, port
}
