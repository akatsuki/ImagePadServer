package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/voicevoxruntime"
	"imagepadserver/internal/xpostmodel"
)

type fakeVoicevoxRuntime struct {
	status                 voicevoxruntime.Status
	starts, retries, stops int
}

func (f *fakeVoicevoxRuntime) Start(context.Context)          { f.starts++ }
func (f *fakeVoicevoxRuntime) Retry(context.Context)          { f.retries++; f.status.Phase = "starting" }
func (f *fakeVoicevoxRuntime) Close() error                   { f.stops++; f.status.Phase = "stopped"; return nil }
func (f *fakeVoicevoxRuntime) Status() voicevoxruntime.Status { return f.status }

func TestManagedVoicePreparationAndControls(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	runtime := &fakeVoicevoxRuntime{status: voicevoxruntime.Status{Phase: "downloading", Percent: 40, Endpoint: voicevoxruntime.Endpoint, Version: voicevoxruntime.Version}}
	s := &Server{voicevoxRuntime: runtime}
	response := httptest.NewRecorder()
	s.handleXPostVoices(response, httptest.NewRequest("GET", "/api/xpost/voices", nil))
	if response.Code != 503 || runtime.starts != 1 || !strings.Contains(response.Body.String(), `"percent":40`) {
		t.Fatalf("preparation: %d %s %+v", response.Code, response.Body.String(), runtime)
	}
	for _, action := range []string{"retry", "cancel"} {
		response = httptest.NewRecorder()
		s.handleVoicevoxRuntime(response, httptest.NewRequest("POST", "/api/xpost/voicevox-runtime", strings.NewReader(`{"action":"`+action+`"}`)))
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", action, response.Code, response.Body.String())
		}
	}
	if runtime.retries != 1 || runtime.stops != 1 {
		t.Fatalf("controls: %+v", runtime)
	}
	response = httptest.NewRecorder()
	s.handleVoicevoxRuntime(response, httptest.NewRequest("POST", "/api/xpost/voicevox-runtime", strings.NewReader(`{"action":"unknown"}`)))
	if response.Code != 400 {
		t.Fatal("unknown action accepted")
	}
}

func TestSavedExternalVoiceIsPreservedUntilManagedSelected(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	engine := testVoiceEngine(t)
	defer engine.Close()
	cfg, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.LastXPostVoice = &xpostmodel.Voice{EngineURL: engine.URL, SpeakerUUID: "fixture", StyleID: 0, Speed: 1.2}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeVoicevoxRuntime{status: voicevoxruntime.Status{Phase: "failed", Endpoint: voicevoxruntime.Endpoint, Error: "fixture failure"}}
	s := &Server{voicevoxRuntime: runtime}
	response := httptest.NewRecorder()
	s.handleXPostVoices(response, httptest.NewRequest("GET", "/api/xpost/voices", nil))
	if response.Code != 200 || runtime.starts != 0 {
		t.Fatalf("external: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		EngineURL string `json:"engineUrl"`
		Managed   bool   `json:"managed"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.EngineURL != engine.URL || body.Managed {
		t.Fatalf("external redirected: %+v", body)
	}
	response = httptest.NewRecorder()
	s.handleXPostVoices(response, httptest.NewRequest("GET", "/api/xpost/voices?engineUrl=managed", nil))
	if response.Code != 503 || runtime.starts != 1 {
		t.Fatal("explicit managed switch ignored")
	}
	cfg, err = settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LastXPostVoice.EngineURL != engine.URL {
		t.Fatal("selection changed persisted last-used voice")
	}
}

func TestSavedExternalOnManagedPortIsNotAdopted(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:50121")
	if err != nil {
		t.Skip("dedicated test port already occupied")
	}
	external := httptest.NewUnstartedServer(testVoiceEngineHandler())
	external.Listener = listener
	external.Start()
	defer external.Close()
	cfg, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.LastXPostVoice = &xpostmodel.Voice{EngineURL: voicevoxruntime.Endpoint, SpeakerUUID: "fixture", StyleID: 0, Speed: 1}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeVoicevoxRuntime{status: voicevoxruntime.Status{Phase: "failed", Error: "port is busy"}}
	s := &Server{voicevoxRuntime: runtime}
	response := httptest.NewRecorder()
	s.handleXPostVoices(response, httptest.NewRequest("GET", "/api/xpost/voices", nil))
	if response.Code != 200 || runtime.starts != 0 {
		t.Fatalf("external endpoint adopted: %d %s", response.Code, response.Body.String())
	}
	data, _ := json.Marshal(cfg.LastXPostVoice)
	response = httptest.NewRecorder()
	s.handleXPostVoicePreview(response, httptest.NewRequest("POST", "/api/xpost/voice-preview", strings.NewReader(string(data))))
	if response.Code != 200 || runtime.starts != 0 {
		t.Fatalf("external preview adopted: %d %s", response.Code, response.Body.String())
	}
}

func TestNativeManagedVoiceAPIAndPreview(t *testing.T) {
	if os.Getenv("VOICEVOX_SERVER_INTEGRATION") != "1" {
		t.Skip("opt-in official runtime/server integration")
	}
	root := os.Getenv("VOICEVOX_NATIVE_ROOT")
	if root == "" {
		t.Fatal("VOICEVOX_NATIVE_ROOT required")
	}
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := voicevoxruntime.New(root, nil)
	s := &Server{voicevoxRuntime: m}
	s.SetLifecycleContext(ctx)
	s.StartVoicevoxRuntime()
	defer s.StopVoicevoxRuntime()
	if err := m.Ensure(ctx, ctx); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.handleXPostVoices(response, httptest.NewRequest("GET", "/api/xpost/voices", nil))
	if response.Code != 200 {
		t.Fatalf("managed voices: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Managed  bool `json:"managed"`
		Speakers []struct {
			UUID   string `json:"speaker_uuid"`
			Styles []struct {
				ID int `json:"id"`
			} `json:"styles"`
		} `json:"speakers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Managed || len(body.Speakers) == 0 || len(body.Speakers[0].Styles) == 0 {
		t.Fatal("managed speakers missing")
	}
	voice := xpostmodel.Voice{EngineURL: voicevoxruntime.Endpoint, Managed: true, SpeakerUUID: body.Speakers[0].UUID, StyleID: body.Speakers[0].Styles[0].ID, Speed: 1}
	data, _ := json.Marshal(voice)
	response = httptest.NewRecorder()
	s.handleXPostVoicePreview(response, httptest.NewRequest("POST", "/api/xpost/voice-preview", strings.NewReader(string(data))))
	if response.Code != 200 || response.Header().Get("Content-Type") != "audio/wav" || response.Body.Len() < 44 {
		t.Fatalf("managed preview: %d %s", response.Code, response.Body.String())
	}
	cfg, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LastXPostVoice != nil {
		t.Fatal("native preview saved last voice")
	}
	if err := s.StopVoicevoxRuntime(); err != nil {
		t.Fatal(err)
	}
	if m.Status().Phase != "stopped" {
		t.Fatal("engine not stopped")
	}
	t.Logf("official managed API: %d speakers; preview %d bytes; owned shutdown passed", len(body.Speakers), response.Body.Len())
}
