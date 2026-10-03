package server

import (
	"encoding/json"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/voicevoxruntime"
	"imagepadserver/internal/xpostexport"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
	"net/http"
	"os"
	"path/filepath"
)

func (s *Server) handleXPostVoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	appSettings, err := settings.Load()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	engine := voicevoxruntime.Endpoint
	managed := true
	if appSettings.LastXPostVoice != nil {
		engine = appSettings.LastXPostVoice.EngineURL
		managed = appSettings.LastXPostVoice.Managed
	}
	if override := r.URL.Query().Get("engineUrl"); override != "" {
		engine = override
		managed = override == "managed"
	}
	if engine == "managed" {
		engine = voicevoxruntime.Endpoint
	}
	if managed && engine != voicevoxruntime.Endpoint {
		http.Error(w, "invalid managed engine", 400)
		return
	}
	if managed && !s.managedVoiceReady(w) {
		return
	}
	client, err := xposttts.NewClient(engine)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	speakers, err := client.Speakers(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"managed": managed, "engineUrl": engine, "error": "VOICEVOXに接続できません。アプリ内VOICEVOXへの切り替え、または声一覧の更新をお試しください: " + err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"speakers": speakers, "lastVoice": appSettings.LastXPostVoice, "engineUrl": engine, "managed": managed})
}

func (s *Server) handleXPostVoicePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var voice xpostmodel.Voice
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	d.DisallowUnknownFields()
	if err := d.Decode(&voice); err != nil {
		http.Error(w, "invalid voice preview request", 400)
		return
	}
	if voice.Managed && voice.EngineURL != voicevoxruntime.Endpoint {
		http.Error(w, "invalid managed engine", 400)
		return
	}
	if voice.Managed && !s.managedVoiceReady(w) {
		return
	}
	client, err := xposttts.NewClient(voice.EngineURL)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	speakers, err := client.Speakers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	voice, err = xpostexport.ValidateVoice(voice, speakers)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	dir, err := os.MkdirTemp("", "imagepad-xpost-preview-")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer os.RemoveAll(dir)
	speech, err := client.Synthesize(r.Context(), "この声で、投稿内容を読み上げます。", voice, filepath.Join(dir, "preview.wav"))
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, speech.Path)
}
