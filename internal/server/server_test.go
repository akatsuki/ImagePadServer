package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/upnp"
	"imagepadserver/internal/video"
)

type fakeRTSPMapping struct {
	ip         string
	port       int
	closeCalls atomic.Int32
}

func (f *fakeRTSPMapping) ExternalIP() string { return f.ip }
func (f *fakeRTSPMapping) ExternalPort() int  { return f.port }
func (f *fakeRTSPMapping) Close() error       { f.closeCalls.Add(1); return nil }

type rtspMapCall struct {
	protocol     string
	internalPort int
	externalPort int
	description  string
}

func TestValidatePublicURLRejectsLocalhost(t *testing.T) {
	if _, err := validatePublicURL("http://localhost/image.png"); err == nil {
		t.Fatal("expected localhost URL to be rejected")
	}
	if _, err := validatePublicURL("http://127.0.0.1/image.png"); err == nil {
		t.Fatal("expected loopback URL to be rejected")
	}
}

func TestValidateHTTPURL(t *testing.T) {
	if err := validateHTTPURL("https://example.com/watch?v=1"); err != nil {
		t.Fatal(err)
	}
	if err := validateHTTPURL("file:///tmp/video.mp4"); err == nil {
		t.Fatal("expected non-http URL to be rejected")
	}
	if err := validateHTTPURL("http://127.0.0.1/video"); err == nil {
		t.Fatal("expected loopback URL to be rejected")
	}
	if err := validateHTTPURL("http://192.168.0.1/stream"); err == nil {
		t.Fatal("expected private network URL to be rejected")
	}
	if err := validateHTTPURL("http://100.64.0.1/internal"); err == nil {
		t.Fatal("expected CGNAT URL to be rejected")
	}
}

func TestRemoteContentTypeAllowed(t *testing.T) {
	if !remoteContentTypeAllowed("image/webp") {
		t.Fatal("expected image/webp to be allowed")
	}
	if !remoteContentTypeAllowed("image/svg+xml; charset=utf-8") {
		t.Fatal("expected image/svg+xml to be allowed")
	}
	if !remoteContentTypeAllowed("application/octet-stream") {
		t.Fatal("expected octet-stream to be allowed for RAW image downloads")
	}
	if remoteContentTypeAllowed("text/html") {
		t.Fatal("expected text/html to be rejected")
	}
}

func TestRemoteFileNameInfersRAWExtensions(t *testing.T) {
	u := mustURL("https://example.com/download?id=1&filename=sample.CR3")
	if got := remoteFileName(u, "application/octet-stream"); got != "download.cr3" {
		t.Fatalf("remoteFileName = %q, want download.cr3", got)
	}

	u = mustURL("https://example.com/raw")
	if got := remoteFileName(u, "image/x-nikon-nef"); got != "raw.nef" {
		t.Fatalf("remoteFileName = %q, want raw.nef", got)
	}
}

func TestHandleFFmpegChecksConfiguredBinaryWithoutEnablingVideoMode(t *testing.T) {
	ffmpegPath := filepath.Join(t.TempDir(), "ffmpeg.exe")
	if err := os.WriteFile(ffmpegPath, []byte("fake"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_FFMPEG", ffmpegPath)

	srv, mux := testServer(t, false)
	defer srv.store.Reset()

	req := httptest.NewRequest(http.MethodPost, "/api/ffmpeg", nil)
	rec := adminJSON(t, mux, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	if srv.videoPlayerEnabled() {
		t.Fatal("expected FFmpeg check not to enable video player mode")
	}
}

func TestIsHLSSegmentName(t *testing.T) {
	valid := []string{"current0.ts", "current12.ts", "current1779424624066091600-24.ts", "current-242352fb7167ea14-1779429230673092900-60.ts"}
	for _, name := range valid {
		if !isHLSSegmentName(name) {
			t.Fatalf("expected %s to be accepted", name)
		}
	}

	invalid := []string{"current.ts", "currentx.ts", "../current0.ts", "current0.mp4"}
	for _, name := range invalid {
		if isHLSSegmentName(name) {
			t.Fatalf("expected %s to be rejected", name)
		}
	}
}

func TestOptionsFromValuesDefaultAllows8KUpload(t *testing.T) {
	opts := optionsFromValues(func(string) string { return "" })
	if opts.MaxDimension != 2048 {
		t.Fatalf("MaxDimension = %d, want 2048", opts.MaxDimension)
	}
	if opts.MaxInputBytes != 120<<20 {
		t.Fatalf("MaxInputBytes = %d, want %d", opts.MaxInputBytes, int64(120<<20))
	}
	if opts.MaxBytes != 30<<20 {
		t.Fatalf("MaxBytes = %d, want %d", opts.MaxBytes, int64(30<<20))
	}
}

func TestStreamRequestID(t *testing.T) {
	req := adminRequest("https://example.com/stream/abc123/current-abc123.m3u8", "127.0.0.1:50000")
	if got := streamRequestID(req); got != "abc123" {
		t.Fatalf("streamRequestID = %q, want abc123", got)
	}
	req = adminRequest("https://example.com/stream/current.m3u8?v=legacy", "127.0.0.1:50000")
	if got := streamRequestID(req); got != "legacy" {
		t.Fatalf("streamRequestID = %q, want legacy", got)
	}
}

func TestIsVideoUpload(t *testing.T) {
	if !isVideoUpload("clip.mp4", "") {
		t.Fatal("expected mp4 extension to be treated as video")
	}
	if !isVideoUpload("upload.bin", "video/webm; charset=binary") {
		t.Fatal("expected video content type to be treated as video")
	}
	if isVideoUpload("photo.jpg", "image/jpeg") {
		t.Fatal("expected image upload not to be treated as video")
	}
}

func TestAdminAccessRules(t *testing.T) {
	srv := &Server{adminToken: "secret"}

	if !srv.adminAllowed(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000")) {
		t.Fatal("expected localhost admin access to be allowed")
	}
	if srv.adminAllowed(adminRequest("https://example.trycloudflare.com/?token=secret", "127.0.0.1:50000")) {
		t.Fatal("expected tunnel-host admin access to be rejected")
	}
	if !srv.adminAllowed(adminRequest("http://192.168.1.20:8080/?token=secret", "192.168.1.35:50000")) {
		t.Fatal("expected LAN admin access with token to be allowed")
	}
	if srv.adminAllowed(adminRequest("http://203.0.113.10:8080/?token=secret", "198.51.100.25:50000")) {
		t.Fatal("expected public remote admin access to be rejected")
	}
}

func TestPublicReadRules(t *testing.T) {
	if !publicReadAllowed(adminRequest("http://192.168.1.20:8080/image/current", "192.168.1.35:50000")) {
		t.Fatal("expected LAN media read to be allowed")
	}
	if publicReadAllowed(adminRequest("http://203.0.113.10:8080/image/current", "198.51.100.25:50000")) {
		t.Fatal("expected direct public media read to be rejected")
	}
	if !publicReadAllowed(adminRequest("https://example.trycloudflare.com/image/current", "127.0.0.1:50000")) {
		t.Fatal("expected tunnel media read via local origin to be allowed")
	}
}

func TestPrimaryShareURL(t *testing.T) {
	url, label := primaryShareURL(map[string]interface{}{
		"imageURL": "https://example.com/image/current.png",
		"videoURL": "https://example.com/video/current.mp4",
		"hlsURL":   "https://example.com/stream/abc123/current-abc123.m3u8",
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "https://example.com/stream/abc123/current-abc123.m3u8" || label != "HLS URL" {
		t.Fatalf("share URL = %q (%s), want HLS", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"imageURL": "https://example.com/image/current.png",
		"videoPlayer": map[string]interface{}{
			"enabled": false,
		},
	})
	if url != "https://example.com/image/current.png" || label != "ImagePad URL" {
		t.Fatalf("share URL = %q (%s), want image", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"hlsURL": "https://example.com/stream/current.m3u8",
		"obs": obsrtmp.Status{
			Latency:  obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPT),
			RTSPTURL: "rtsp://8.8.8.8:52000/obs_session",
		},
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "rtsp://8.8.8.8:52000/obs_session" || label != "RTSP TCP URL" {
		t.Fatalf("share URL = %q (%s), want RTSP TCP", url, label)
	}

	url, label = primaryShareURL(map[string]interface{}{
		"hlsURL":     "https://example.com/stream/current.m3u8",
		"obsLatency": obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeHLS),
		"obs": obsrtmp.Status{
			Latency:  obsrtmp.NormalizeLatencyProfile(obsrtmp.LatencyModeRTSPRealtime),
			RTSPTURL: "rtsp://8.8.8.8:52000/obs_session",
		},
		"videoPlayer": map[string]interface{}{
			"enabled": true,
		},
	})
	if url != "https://example.com/stream/current.m3u8" || label != "HLS URL" {
		t.Fatalf("share URL = %q (%s), want HLS when selected OBS latency is HLS", url, label)
	}
}

func TestOBSRelayConfigStartsReceiverWithoutPublishing(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = false
		s.OBSLatencyMode = obsrtmp.LatencyModeRTSPT
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	t.Cleanup(func() {
		srv.StopOBSReceiver()
		store.Reset()
	})

	body, err := srv.obsRelayConfig(true)
	if err != nil {
		t.Fatal(err)
	}
	if body["publishing"] == true {
		t.Fatalf("publishing = %v, want false before explicit start", body["publishing"])
	}
	status := srv.obs.Status()
	if !status.Listening {
		t.Fatal("OBS receiver should listen after relay config is requested")
	}
	if status.Publishing {
		t.Fatal("OBS publishing must wait for /api/obs/start")
	}
	if !srv.videoPlayerEnabled() {
		t.Fatal("relay config should still enable video player support")
	}
}

func TestStateExposesHLSURLOnlyAfterFirstSegment(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(imagePath, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(imagePath, library.CurrentImage{
		PublicName:  "current.png",
		ContentType: "image/png",
	}); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")

	playlist := filepath.Join(store.Dir(), video.PlaylistName(current.ID))
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := srv.state(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"))
	if got, _ := state["hlsURL"].(string); got != "" {
		t.Fatalf("hlsURL = %q, want empty before first segment", got)
	}

	if err := os.WriteFile(filepath.Join(store.Dir(), "current-"+current.ID+"-0.ts"), []byte("segment"), 0600); err != nil {
		t.Fatal(err)
	}
	state = srv.state(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"))
	if got, _ := state["hlsURL"].(string); !strings.Contains(got, "/stream/"+current.ID+"/") {
		t.Fatalf("hlsURL = %q, want id-scoped HLS URL after first segment", got)
	}
}

func TestStateExposesHLSURLForPendingStillConversion(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(imagePath, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(imagePath, library.CurrentImage{
		PublicName:  "current.png",
		ContentType: "image/png",
	}); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	video.EnqueueStillImageForID(imagePath, store.Dir(), current.ID, "input.png", video.ResolveQuality("720", 0))
	defer video.CancelQueue(store.Dir())

	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")
	state := srv.state(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"))

	if got, _ := state["shareURL"].(string); !strings.Contains(got, "/stream/"+current.ID+"/") {
		t.Fatalf("shareURL = %q, want pending still conversion HLS URL", got)
	}
	if got, _ := state["shareURLLabel"].(string); got != "HLS URL" {
		t.Fatalf("shareURLLabel = %q, want HLS URL", got)
	}
}

func TestHistorySelectReturnsClipboardURL(t *testing.T) {
	srv, mux := testServer(t, false)
	source := filepath.Join(t.TempDir(), "input.png")
	if err := os.WriteFile(source, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := srv.store.AddHistory(source, library.CurrentImage{
		PublicName:  "current.png",
		ContentType: "image/png",
		Width:       640,
		Height:      480,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/history/select", strings.NewReader(fmt.Sprintf(`{"id":%q}`, item.ID)))
	rec := adminJSON(t, mux, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
	var state map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	copiedURL, _ := state["copiedURL"].(string)
	if !strings.Contains(copiedURL, "/image/current") || !strings.Contains(copiedURL, item.ID) {
		t.Fatalf("copiedURL = %q, want restored current image URL for history item", copiedURL)
	}
	if _, ok := state["clipboardCopied"].(bool); !ok {
		t.Fatalf("clipboardCopied missing or wrong type: %#v", state["clipboardCopied"])
	}
}

func TestStateIgnoresHLSConversionForDifferentCurrentMedia(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	if err := settings.Update(func(s *settings.Settings) error {
		s.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	otherPath := filepath.Join(t.TempDir(), "other.png")
	if err := os.WriteFile(otherPath, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	video.EnqueueStillImageForID(otherPath, store.Dir(), "other-media", "other.png", video.ResolveQuality("720", 0))
	defer video.CancelQueue(store.Dir())

	imagePath := filepath.Join(t.TempDir(), "current.png")
	if err := os.WriteFile(imagePath, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(imagePath, library.CurrentImage{
		PublicName:  "current.png",
		ContentType: "image/png",
	}); err != nil {
		t.Fatal(err)
	}
	current := store.Current()
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.SetTunnelStatus(true, "https://example.trycloudflare.com", "connected")
	state := srv.state(adminRequest("http://127.0.0.1:8080/", "127.0.0.1:50000"))

	if got, _ := state["hlsURL"].(string); got != "" {
		t.Fatalf("hlsURL = %q, want empty for different active media", got)
	}
	if got, _ := state["shareURL"].(string); !strings.Contains(got, "/image/current") || !strings.Contains(got, current.ID) {
		t.Fatalf("shareURL = %q, want current image URL", got)
	}
}

func TestNormalizeQualityMode(t *testing.T) {
	if normalizeQualityMode("1080") != "1080" {
		t.Fatal("expected 1080 to be accepted")
	}
	if normalizeQualityMode("bad") != "auto" {
		t.Fatal("expected invalid mode to fall back to auto")
	}
}

func TestBitrateOnlyPresetKeepsActiveResolution(t *testing.T) {
	active := video.ResolveQuality("1080", 0)
	requested := video.ResolveQuality("360", 0)
	result := video.BitrateOnlyPreset(requested, active)
	if result.Height != active.Height {
		t.Fatalf("height = %d, want active height %d", result.Height, active.Height)
	}
	if result.VideoBitrate != requested.VideoBitrate {
		t.Fatalf("video bitrate = %s, want requested %s", result.VideoBitrate, requested.VideoBitrate)
	}
	if !result.BitrateOnly {
		t.Fatal("expected bitrate-only flag")
	}
}

func TestOBSRelayConfigEnablesReceiverAndReturnsConnectionInfo(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	body, err := srv.obsRelayConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if body["serverAddress"] == "" || body["streamKey"] == "" || body["rtmpURL"] == "" {
		t.Fatalf("missing OBS relay connection info: %#v", body)
	}
	if !strings.HasPrefix(body["rtmpURL"].(string), body["serverAddress"].(string)+"/") {
		t.Fatalf("rtmpURL = %q, serverAddress = %q", body["rtmpURL"], body["serverAddress"])
	}
	if enabled, _ := body["videoPlayerEnabled"].(bool); !enabled {
		t.Fatalf("videoPlayerEnabled = %#v, want true", body["videoPlayerEnabled"])
	}
	appSettings, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !appSettings.VideoPlayerEnabled {
		t.Fatal("expected relay config request to enable video player support")
	}
}

func TestHandleOBSLatencyNormalizesStorage(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.obs = nil

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/obs/latency", strings.NewReader(`{"mode":"  low  ","dvr":true}`))
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	srv.admin(srv.handleOBSLatency)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	appSettings, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if appSettings.OBSLatencyMode != obsrtmp.LatencyModeRTSPLow {
		t.Fatalf("OBSLatencyMode = %q, want %q", appSettings.OBSLatencyMode, obsrtmp.LatencyModeRTSPLow)
	}
	if !appSettings.OBSDVREnabled {
		t.Fatal("expected DVR flag to be stored")
	}
}

func TestOBSStateIncludesLatencyCapabilities(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	status := srv.obsState()
	if len(status.Capabilities) != 5 {
		t.Fatalf("capabilities len = %d, want 5", len(status.Capabilities))
	}
	got := map[string]obsrtmp.LatencyCapability{}
	for _, capability := range status.Capabilities {
		got[capability.Mode] = capability
	}

	for _, mode := range []string{obsrtmp.LatencyModeHLSHigh, obsrtmp.LatencyModeHLS, obsrtmp.LatencyModeRTSPLow, obsrtmp.LatencyModeRTSPUltra, obsrtmp.LatencyModeRTSPRealtime} {
		if _, ok := got[mode]; !ok {
			t.Fatalf("missing capability for mode %q", mode)
		}
	}
	if got[obsrtmp.LatencyModeHLSHigh].Label != "最高画質HLS（遅延増）" || got[obsrtmp.LatencyModeHLSHigh].Experimental {
		t.Fatalf("highest HLS capability = %#v, want non-experimental highest HLS label", got[obsrtmp.LatencyModeHLSHigh])
	}
	if got[obsrtmp.LatencyModeRTSPRealtime].Transport != obsrtmp.LatencyModeRTSPT {
		t.Fatalf("realtime RTSP capability = %#v, want RTSPT transport", got[obsrtmp.LatencyModeRTSPRealtime])
	}
}

func TestRTSPReadyPublishesUPnPURL(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mappings := []*fakeRTSPMapping{
		{ip: "8.8.8.8", port: 52000},
		{ip: "8.8.8.8", port: 52001},
		{ip: "8.8.8.8", port: 52002},
	}
	var calls []rtspMapCall
	srv.mapRTSPPort = func(protocol string, internalPort, externalPort int, description string) (rtspMappingHandle, upnp.Result) {
		calls = append(calls, rtspMapCall{protocol: protocol, internalPort: internalPort, externalPort: externalPort, description: description})
		mapping := mappings[len(calls)-1]
		return mapping, upnp.Result{OK: true, ExternalIP: mapping.ip}
	}
	var updatedSession, updatedURL, updatedMessage string
	srv.setRTSPURL = func(sessionID, publicURL, message string) bool {
		updatedSession = sessionID
		updatedURL = publicURL
		updatedMessage = message
		return true
	}

	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
		SessionID: "new-session",
		Port:      49152,
		RTPPort:   49153,
		RTCPPort:  49154,
		Path:      "obs_new-session",
		LocalURL:  "rtsp://192.168.1.10:49152/obs_new-session",
	})

	wantCalls := []rtspMapCall{
		{protocol: "TCP", internalPort: 49152, externalPort: 49152, description: "ImagePadServer RTSP TCP"},
		{protocol: "UDP", internalPort: 49153, externalPort: 49153, description: "ImagePadServer RTSP RTP"},
		{protocol: "UDP", internalPort: 49154, externalPort: 49154, description: "ImagePadServer RTSP RTCP"},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("mapped calls = %#v, want %#v", calls, wantCalls)
	}
	for i, want := range wantCalls {
		if calls[i] != want {
			t.Fatalf("mapped call %d = %#v, want %#v", i, calls[i], want)
		}
	}
	if got, want := updatedSession, "new-session"; got != want {
		t.Fatalf("updated session = %q, want %q", got, want)
	}
	if got, want := updatedURL, "rtsp://8.8.8.8:52000/obs_new-session"; got != want {
		t.Fatalf("updated URL = %q, want %q", got, want)
	}
	if !strings.Contains(updatedMessage, "UPnP") {
		t.Fatalf("updated message = %q, want UPnP status", updatedMessage)
	}
	if srv.rtspMap == nil || srv.rtspSessionID != "new-session" {
		t.Fatalf("stored mapping/session = %#v/%q", srv.rtspMap, srv.rtspSessionID)
	}
}

func TestRTSPReadyMappingFailureKeepsLANURL(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	srv.mapRTSPPort = func(string, int, int, string) (rtspMappingHandle, upnp.Result) {
		return nil, upnp.Result{Message: "no UPnP gateway found"}
	}
	var updatedURL, updatedMessage string
	srv.setRTSPURL = func(_ string, publicURL, message string) bool {
		updatedURL = publicURL
		updatedMessage = message
		return true
	}

	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
		SessionID: "session",
		Port:      49152,
		Path:      "obs_session",
		LocalURL:  "rtsp://192.168.1.10:49152/obs_session",
	})

	if got, want := updatedURL, "rtsp://192.168.1.10:49152/obs_session"; got != want {
		t.Fatalf("updated URL = %q, want %q", got, want)
	}
	if !strings.Contains(updatedMessage, "no UPnP gateway found") {
		t.Fatalf("updated message = %q", updatedMessage)
	}
	if srv.rtspMap != nil {
		t.Fatalf("failed mapping was stored: %#v", srv.rtspMap)
	}
}

func TestRTSPReadyRejectsCarrierNATAddress(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mapping := &fakeRTSPMapping{ip: "100.64.1.2", port: 49152}
	srv.mapRTSPPort = func(string, int, int, string) (rtspMappingHandle, upnp.Result) {
		return mapping, upnp.Result{OK: true, ExternalIP: mapping.ip}
	}
	var updatedURL, updatedMessage string
	srv.setRTSPURL = func(_ string, publicURL, message string) bool {
		updatedURL = publicURL
		updatedMessage = message
		return true
	}

	srv.handleRTSPReady(obsrtmp.RTSPEndpoint{
		SessionID: "session",
		Port:      49152,
		Path:      "obs_session",
		LocalURL:  "rtsp://192.168.1.10:49152/obs_session",
	})

	if got, want := updatedURL, "rtsp://192.168.1.10:49152/obs_session"; got != want {
		t.Fatalf("updated URL = %q, want %q", got, want)
	}
	if !strings.Contains(updatedMessage, "CGNAT") {
		t.Fatalf("updated message = %q, want CGNAT explanation", updatedMessage)
	}
	if got := mapping.closeCalls.Load(); got != 1 {
		t.Fatalf("mapping close calls = %d, want 1", got)
	}
}

func TestRTSPDoneDoesNotCloseNewerMapping(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 49152}
	srv.rtspMap = mapping
	srv.rtspSessionID = "new-session"

	srv.handleRTSPDone("old-session")
	if got := mapping.closeCalls.Load(); got != 0 {
		t.Fatalf("stale done closed mapping %d times", got)
	}
	srv.handleRTSPDone("new-session")
	if got := mapping.closeCalls.Load(); got != 1 {
		t.Fatalf("matching done closed mapping %d times, want 1", got)
	}
	if srv.rtspMap != nil || srv.rtspSessionID != "" {
		t.Fatalf("mapping ownership not cleared: %#v/%q", srv.rtspMap, srv.rtspSessionID)
	}
}

func TestStopOBSReceiverClosesRTSPMapping(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 49152}
	srv.rtspMap = mapping
	srv.rtspSessionID = "session"

	srv.StopOBSReceiver()

	if got := mapping.closeCalls.Load(); got != 1 {
		t.Fatalf("mapping close calls = %d, want 1", got)
	}
}

func TestChangingAwayFromRTSPClosesMapping(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")
	mapping := &fakeRTSPMapping{ip: "8.8.8.8", port: 49152}
	srv.rtspMap = mapping
	srv.rtspSessionID = "session"
	srv.obs = nil

	req := httptest.NewRequest(http.MethodPost, "/api/obs/latency",
		strings.NewReader(`{"mode":"hls","dvr":false}`))
	rec := httptest.NewRecorder()
	srv.handleOBSLatency(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if got := mapping.closeCalls.Load(); got != 1 {
		t.Fatalf("mapping close calls = %d, want 1", got)
	}
}

func TestRTSPUIUsesSharedURLAndRiskDialog(t *testing.T) {
	mustContain := []string{
		`<option value="hls-high">最高画質HLS（遅延増）</option>`,
		`<option value="hls">高画質HLS（通常遅延）</option>`,
		`<option value="rtsp-low">低遅延RTSP</option>`,
		`<option value="rtsp-ultra">超低遅延RTSP</option>`,
		`<option value="rtsp-realtime">リアルタイムRTSP</option>`,
		`id="shareURL"`,
		`data-copy="shareURL"`,
		`id="rtspRiskDialog"`,
		`role="alertdialog"`,
		`リスクを理解して有効化`,
	}
	for _, want := range mustContain {
		if !strings.Contains(indexHTML, want) {
			t.Fatalf("indexHTML missing %q", want)
		}
	}
	mustNotContain := []string{
		`id="obsRtspt"`,
		`id="obsRtsptCopy"`,
		`obsRtsptURL`,
		`rtspRiskAccepted`,
		`rtspRiskAcknowledged`,
	}
	for _, forbidden := range mustNotContain {
		if strings.Contains(indexHTML, forbidden) {
			t.Fatalf("indexHTML contains forbidden RTSP UI fragment %q", forbidden)
		}
	}
}

func TestOBSEntryPlaylistAliasDoesNotRewriteChildPlaylists(t *testing.T) {
	id := "abc123"
	for _, name := range []string{"current.m3u8", video.PlaylistName(id), ".", "/"} {
		if !isOBSEntryPlaylistAlias(id, name) {
			t.Errorf("entry alias %q was not recognized", name)
		}
	}
	for _, name := range []string{"media_0.m3u8", "stream.m3u8", "index.m3u8"} {
		if isOBSEntryPlaylistAlias(id, name) {
			t.Errorf("child playlist %q was incorrectly treated as an entry alias", name)
		}
	}
}

func TestOBSLatencyAliasesAndCapabilitySurface(t *testing.T) {
	// Legacy aliases (and whitespace/case) normalize onto the canonical
	// transports without ever inventing a new one.
	aliases := map[string]string{
		"auto":   obsrtmp.LatencyModeHLS,
		"normal": obsrtmp.LatencyModeHLS,
		"low":    obsrtmp.LatencyModeRTSPLow,
		"ultra":  obsrtmp.LatencyModeRTSPUltra,
		"lhls":   obsrtmp.LatencyModeRTSPLow,
		"llhls":  obsrtmp.LatencyModeRTSPUltra,
		" HLS ":  obsrtmp.LatencyModeHLS,
		"RTSPT":  obsrtmp.LatencyModeRTSPRealtime,
		"bogus":  obsrtmp.LatencyModeHLS,
	}
	for in, want := range aliases {
		if got := obsrtmp.NormalizeLatencyMode(in); got != want {
			t.Fatalf("NormalizeLatencyMode(%q) = %q, want %q", in, got, want)
		}
	}

	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	caps := map[string]obsrtmp.LatencyCapability{}
	for _, c := range srv.obsState().Capabilities {
		caps[c.Mode] = c
	}
	transports := map[string]string{
		obsrtmp.LatencyModeHLSHigh:      obsrtmp.LatencyModeHLS,
		obsrtmp.LatencyModeHLS:          obsrtmp.LatencyModeHLS,
		obsrtmp.LatencyModeRTSPLow:      obsrtmp.LatencyModeRTSPT,
		obsrtmp.LatencyModeRTSPUltra:    obsrtmp.LatencyModeRTSPT,
		obsrtmp.LatencyModeRTSPRealtime: obsrtmp.LatencyModeRTSPT,
	}
	for mode, transport := range transports {
		c, ok := caps[mode]
		if !ok {
			t.Fatalf("missing capability for mode %q", mode)
		}
		if !c.Available || !c.Selectable {
			t.Fatalf("%s capability must be available and selectable: %#v", mode, c)
		}
		if c.Experimental {
			t.Fatalf("%s experimental = true, want false", mode)
		}
		if c.Transport != transport {
			t.Fatalf("%s transport = %q, want %q", mode, c.Transport, transport)
		}
	}

	// With no active session, no transport leaks a preview URL.
	if url := srv.obsState().PreviewURL; url != "" {
		t.Fatalf("idle state should expose no preview URL, got %q", url)
	}
}

func TestPairingIssuesRelayDeviceAndSignedRelayAuthWorks(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	requestBody := `{"clientName":"BrowserRelayStreamer","deviceName":"Relay PC"}`
	req := httptest.NewRequest(http.MethodPost, "http://192.168.1.20:8080/api/pairing/request", strings.NewReader(requestBody))
	req.RemoteAddr = "192.168.1.50:50000"
	rr := httptest.NewRecorder()
	srv.handlePairingRequest(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("request status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var pairingResp struct {
		PairingID string `json:"pairingId"`
		Nonce     string `json:"nonce"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &pairingResp); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	pairing := srv.pairings[pairingResp.PairingID]
	srv.mu.Unlock()
	if pairing.PIN == "" {
		t.Fatal("expected pairing PIN to be stored for UI display")
	}
	proof := hmacSHA256Hex(pairing.PIN, strings.Join([]string{pairing.ID, pairing.Nonce, "BrowserRelayStreamer", "Relay PC"}, "\n"))
	confirmBody := fmt.Sprintf(`{"pairingId":%q,"clientName":"BrowserRelayStreamer","deviceName":"Relay PC","proof":%q}`, pairingResp.PairingID, proof)
	req = httptest.NewRequest(http.MethodPost, "http://192.168.1.20:8080/api/pairing/confirm", strings.NewReader(confirmBody))
	req.RemoteAddr = "192.168.1.50:50000"
	rr = httptest.NewRecorder()
	srv.handlePairingConfirm(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("confirm status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var device struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &device); err != nil {
		t.Fatal(err)
	}
	if device.ClientID == "" || device.ClientSecret == "" || device.Scope != relayScope {
		t.Fatalf("bad device response: %#v", device)
	}

	authReq := signedRelayRequest(t, device.ClientID, device.ClientSecret, "nonce-1")
	if !srv.relayDeviceAllowed(authReq) {
		t.Fatal("expected signed relay request to authenticate")
	}
	replayed := signedRelayRequest(t, device.ClientID, device.ClientSecret, "nonce-1")
	if srv.relayDeviceAllowed(replayed) {
		t.Fatal("expected nonce replay to be rejected")
	}
}

func TestVideoURLDownloadError(t *testing.T) {
	msg := videoURLDownloadError(fmt.Errorf("not found"))
	if !strings.Contains(msg, "yt-dlp") {
		t.Fatalf("message = %q, want yt-dlp guidance", msg)
	}
}

func TestSoundCloudCurrentInfoUsesVideoPresentationAndSoundCloudSource(t *testing.T) {
	media := video.DownloadedMedia{
		SourcePath: "track.m4a",
		Name:       "track.m4a",
		Kind:       "soundcloud",
	}
	info := soundCloudCurrentInfo(media, "current-video.m4a", "thumb.jpg")
	if info.Kind != "video" {
		t.Fatalf("Kind = %q, want video so existing preview/history paths treat it as media", info.Kind)
	}
	if info.SourceKind != "soundcloud" {
		t.Fatalf("SourceKind = %q, want soundcloud", info.SourceKind)
	}
	if info.Thumbnail != "thumb.jpg" {
		t.Fatalf("Thumbnail = %q, want thumb.jpg", info.Thumbnail)
	}
}

func TestOBSStreamDoneQueuesHLSForRecordedRTSPVideo(t *testing.T) {
	t.Setenv("IMAGEPAD_FFMPEG", slowFFmpegPath(t))
	srv, _ := testServer(t, true)
	defer video.CancelQueue(srv.store.Dir())

	recording := filepath.Join(srv.store.Dir(), "obs-recording-rtsp1.mp4")
	if err := os.WriteFile(recording, []byte("mp4"), 0600); err != nil {
		t.Fatal(err)
	}

	srv.handleOBSStreamDone(obsrtmp.Session{
		ID:        "rtsp1",
		Title:     "RTSP recording",
		Recording: recording,
		Published: true,
	})

	queue := video.QueueStatus(srv.store.Dir())
	for _, item := range queue {
		if item.MediaID == "rtsp1" && item.Kind == "video" {
			return
		}
	}
	t.Fatalf("expected HLS conversion job for RTSP recording, queue = %#v", queue)
}

func signedRelayRequest(t *testing.T, clientID, clientSecret, nonce string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://192.168.1.20:8080/api/obs/relay-config", nil)
	req.RemoteAddr = "192.168.1.50:50000"
	timestamp := time.Now().UTC().Format(time.RFC3339)
	bodyHash := sha256.Sum256(nil)
	message := strings.Join([]string{
		req.Method,
		req.URL.RequestURI(),
		timestamp,
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	req.Header.Set("X-ImagePad-Client-Id", clientID)
	req.Header.Set("X-ImagePad-Timestamp", timestamp)
	req.Header.Set("X-ImagePad-Nonce", nonce)
	req.Header.Set("X-ImagePad-Signature", hmacSHA256Base64URL(clientSecret, message))
	return req
}

func TestAutoQualityPrefersUploadBandwidth(t *testing.T) {
	preset := video.ResolveQualityForUpload("auto", 100, 3)
	if preset.Effective != "360" {
		t.Fatalf("effective = %s, want 360 from upload bandwidth", preset.Effective)
	}
	preset = video.ResolveQualityForUpload("auto", 20, 0)
	if preset.Effective != "1080" {
		t.Fatalf("effective = %s, want download fallback", preset.Effective)
	}
}

func adminRequest(rawURL, remoteAddr string) *http.Request {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		panic(err)
	}
	req.RemoteAddr = remoteAddr
	return req
}

func mustURL(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	return u
}

func slowFFmpegPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if filepath.Separator == '\\' {
		path := filepath.Join(dir, "ffmpeg.cmd")
		if err := os.WriteFile(path, []byte("@echo off\r\nping -n 6 127.0.0.1 > nul\r\nexit /b 1\r\n"), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	path := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 5\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
