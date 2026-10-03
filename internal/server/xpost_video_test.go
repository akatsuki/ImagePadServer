package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/library"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/xpostexport"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xpostvideo"
)

func testVoiceEngine(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(testVoiceEngineHandler())
}
func testVoiceEngineHandler() http.Handler {
	var wav bytes.Buffer
	const frames = 2400
	wav.WriteString("RIFF")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(36+frames*2))
	wav.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(24000), uint32(48000), uint16(2), uint16(16)} {
		_ = binary.Write(&wav, binary.LittleEndian, v)
	}
	wav.WriteString("data")
	_ = binary.Write(&wav, binary.LittleEndian, uint32(frames*2))
	wav.Write(make([]byte, frames*2))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/speakers":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"speaker_uuid":"fixture","name":"テスト用の声","styles":[{"id":0,"name":"普通"}]}]`)
		case "/audio_query":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"speedScale":1}`)
		case "/synthesis":
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(wav.Bytes())
		default:
			http.NotFound(w, r)
		}
	})
}

func TestXPostVoicePreviewDoesNotSaveLastVoice(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	engine := testVoiceEngine(t)
	defer engine.Close()
	v := xpostmodel.Voice{EngineURL: engine.URL, SpeakerUUID: "fixture", StyleID: 0, Speed: 1}
	data, _ := json.Marshal(v)
	req := httptest.NewRequest("POST", "/api/xpost/voice-preview", bytes.NewReader(data))
	response := httptest.NewRecorder()
	(&Server{}).handleXPostVoicePreview(response, req)
	if response.Code != 200 || response.Header().Get("Content-Type") != "audio/wav" || response.Body.Len() < 44 {
		t.Fatalf("preview: %d %s", response.Code, response.Body.String())
	}
	s, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.LastXPostVoice != nil {
		t.Fatalf("preview saved a voice: %+v", s.LastXPostVoice)
	}
	v.SpeakerUUID = "unavailable"
	data, _ = json.Marshal(v)
	response = httptest.NewRecorder()
	(&Server{}).handleXPostVoicePreview(response, httptest.NewRequest("POST", "/api/xpost/voice-preview", bytes.NewReader(data)))
	if response.Code != 400 {
		t.Fatalf("missing voice silently replaced: %d", response.Code)
	}
}

func TestXPostWorkerEventsRejectWrongIdentityAndPartialOutput(t *testing.T) {
	c := &xpostEventCollector{request: xpostexport.Request{JobID: "job", WorkDir: t.TempDir()}}
	if _, err := c.Write([]byte("{\"jobId\":\"other\",\"type\":\"progress\"}\n")); err == nil {
		t.Fatal("accepted foreign job")
	}
	c = &xpostEventCollector{request: xpostexport.Request{JobID: "job"}}
	_, _ = c.Write([]byte("{\"jobId\":\"job\",\"type\":\"progress\"}"))
	if _, err := c.finish(); err == nil {
		t.Fatal("accepted truncated result")
	}
}

func TestXPostWorkerProgressKeepsFrameCountAndETAInDashboardState(t *testing.T) {
	s := &Server{}
	s.setIngest(ingestProcessing, "Xの投稿動画を生成中")
	message := "映像変換 2345 / 9943 フレーム・映像の残り約1分20秒"
	data, err := json.Marshal(xpostexport.Event{JobID: "job", Type: "progress", Percent: 36, Message: message})
	if err != nil {
		t.Fatal(err)
	}
	c := &xpostEventCollector{request: xpostexport.Request{JobID: "job"}, onProgress: s.setIngestProgress}
	// Native child stdout can split inside a UTF-8 character or JSON line.
	for _, b := range append(data, '\n') {
		if _, err := c.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	state := s.ingestState()
	if state["progressText"] != message || state["progressPercent"] != 36 {
		t.Fatalf("dashboard progress = %+v", state)
	}
}

func TestXPostVideoRejectsInvalidSourceBeforeWorker(t *testing.T) {
	for _, mode := range []string{"video", "unknown"} {
		response := httptest.NewRecorder()
		handled := (&Server{}).handleXPostVideoUpload(response, httptest.NewRequest("POST", "/api/upload-url", nil), "https://example.com/video.mp4", "light", xPostRequest{Mode: mode}, false)
		if !handled || response.Code != 400 {
			t.Fatalf("invalid X input: %v %d", handled, response.Code)
		}
	}
}

// Opt-in because this starts the built application worker and real GPU/FFmpeg.
// It uses a fake local speech engine: actual VOICEVOX voice quality is separate.
func TestXPostNativeWorkerCPU20AndVoiceAcceptance(t *testing.T) {
	if os.Getenv("XPOST_WORKER_INTEGRATION") != "1" {
		t.Skip("set XPOST_WORKER_INTEGRATION=1 with a built worker executable")
	}
	if runtime.GOOS != "windows" {
		t.Skip("Windows Job Object CPU20 verification")
	}
	exe := os.Getenv("IMAGEPAD_XPOST_WORKER_EXE")
	if exe == "" {
		t.Fatal("IMAGEPAD_XPOST_WORKER_EXE required")
	}
	previous := xpostWorkerExecutable
	xpostWorkerExecutable = func() (string, error) { return filepath.Abs(exe) }
	defer func() { xpostWorkerExecutable = previous }()
	dir := t.TempDir()
	t.Setenv("IMAGEPAD_DATA_DIR", filepath.Join(dir, "settings"))
	fetcher := filepath.Join(dir, "fetch.mjs")
	if err := os.WriteFile(fetcher, []byte(`console.log(JSON.stringify({tweetId:process.argv[2],userName:"Test fixture",handle:"not-read",rawText:"動作確認の本文。https://t.co/x @skip",text:"動作確認の本文。https://t.co/x @skip",entities:[],media:[]}));`), 0600); err != nil {
		t.Fatal(err)
	}
	manifestDir := filepath.Join(dir, "node_modules", "react-tweet")
	if err := os.MkdirAll(manifestDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, "package.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGEPAD_XPOST_FETCHER", fetcher)
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	compositor, err := findXPostCompositor()
	if err != nil {
		t.Fatal(err)
	}
	font, err := filepath.Abs(filepath.Join("..", "video", "fonts", "NotoSansJP-Regular.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	engine := testVoiceEngine(t)
	defer engine.Close()
	voice := xpostmodel.Voice{EngineURL: engine.URL, SpeakerUUID: "fixture", StyleID: 0, Speed: 1}
	options := xpostvideo.DefaultOptions()
	options.Width = 640
	options.Height = 360
	request := xpostexport.Request{JobID: "native-fixture", URL: "https://x.com/test/status/123", Theme: "light", WorkDir: filepath.Join(dir, "job"), FontPath: font, FFmpeg: ffmpeg, FFprobe: ffprobe, Compositor: compositor, EncoderMode: "cpu", CRF: 28, AudioBitrate: "128k", Voice: &voice, Options: options}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	start := time.Now()
	result, report, err := runXPostWorker(ctx, request, func(int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Verified || report.JobCPURate != 2000 || result.Frames != 18 || result.RendererCalls != 1 {
		t.Fatalf("native result/report: %+v %+v", result, report)
	}
	s, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.LastXPostVoice == nil || s.LastXPostVoice.StyleID != 0 || s.LastXPostVoice.SpeakerName != "テスト用の声" {
		t.Fatalf("voice not accepted: %+v", s.LastXPostVoice)
	}
	if strings.TrimSpace(result.Adapter) == "" {
		t.Fatal("GPU adapter missing")
	}
	store, err := library.NewStore(filepath.Join(dir, "published"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CommitPreparedVideo(library.PreparedVideo{
		Info:       library.CurrentImage{ID: "xpost-native", Kind: "video", FileName: "native.mp4", PublicName: "current-video.mp4", ContentType: "video/mp4", Width: 640, Height: 360},
		SourcePath: result.OutputPath, ThumbnailPath: result.ThumbnailPath, SnapshotPath: result.SnapshotPath, HLSDir: result.HLSDir,
		RunID: request.JobID, SelectCurrent: true, ExpectedRevision: store.PublishedRevision(), Resolution: "xpost", SnapshotPrefix: "xpost-snapshot",
	})
	if err != nil {
		t.Fatalf("native publication: %v", err)
	}
	t.Logf("wall %.3fs, CPU %.3fs, frames %d, GPU calls %d, adapter %s / %s, processes %v", time.Since(start).Seconds(), report.CPUSeconds, result.Frames, result.RendererCalls, result.Adapter, result.Backend, report.PIDs)
	if evidencePath := os.Getenv("XPOST_WORKER_EVIDENCE"); evidencePath != "" {
		data, err := json.MarshalIndent(map[string]any{"scope": "640x360 text-only fixture with fake local VOICEVOX engine; no VRChat acceptance", "wallSeconds": time.Since(start).Seconds(), "report": report, "result": result}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	engine.Close()
	if err := os.WriteFile(fetcher, []byte(`console.log(JSON.stringify({tweetId:process.argv[2],userName:"Empty fixture",handle:"not-read",rawText:"",text:"",media:[]}));`), 0600); err != nil {
		t.Fatal(err)
	}
	request.JobID = "empty-fixture"
	request.WorkDir = filepath.Join(dir, "empty-job")
	request.Voice = nil
	request.EncoderMode = "gpu"
	empty, gpuReport, err := runXPostWorker(ctx, request, nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Frames != 45 || empty.RendererCalls != 1 || empty.Encoder == "libx264" || !gpuReport.Verified {
		t.Fatalf("empty/GPU export: %+v %+v", empty, gpuReport)
	}
	s, err = settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.LastXPostVoice == nil || s.LastXPostVoice.SpeakerUUID != "fixture" {
		t.Fatal("empty speech changed last voice")
	}
	t.Logf("empty-body GPU export: %s, %d frames; VOICEVOX offline", empty.Encoder, empty.Frames)
}
