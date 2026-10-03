package voicevoxruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
)

func TestNativeOfficialInstallAndSpeech(t *testing.T) {
	if os.Getenv("VOICEVOX_NATIVE_INTEGRATION") != "1" {
		t.Skip("set VOICEVOX_NATIVE_INTEGRATION=1 for official large runtime download/native test")
	}
	root := os.Getenv("VOICEVOX_NATIVE_ROOT")
	if root == "" {
		t.Fatal("VOICEVOX_NATIVE_ROOT must identify the app-private runtime directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	var m *Manager
	m = New(root, func() {
		s := m.Status()
		if evidence := os.Getenv("VOICEVOX_NATIVE_STATUS"); evidence != "" {
			data, _ := json.Marshal(s)
			_ = os.WriteFile(evidence, data, 0600)
		}
	})
	defer func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := m.Ensure(ctx, ctx); err != nil {
		t.Fatal(err)
	}
	client, err := xposttts.NewClient(Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	speakers, err := client.Speakers(ctx)
	if err != nil || len(speakers) == 0 {
		t.Fatalf("native speakers: %v", err)
	}
	speaker := speakers[0]
	if len(speaker.Styles) == 0 {
		t.Fatal("speaker has no styles")
	}
	voice := xpostmodel.Voice{EngineURL: Endpoint, SpeakerUUID: speaker.UUID, StyleID: speaker.Styles[0].ID, Speed: 1, SpeakerName: speaker.Name, StyleName: speaker.Styles[0].Name}
	path := os.Getenv("VOICEVOX_NATIVE_PREVIEW")
	if path == "" {
		path = filepath.Join(t.TempDir(), "speech.wav")
	}
	speech, err := client.Synthesize(ctx, "VOICEVOXの準備ができました。この声で投稿内容を読み上げます。", voice, path)
	if err != nil {
		t.Fatal(err)
	}
	if speech.Samples == 0 || speech.Duration <= 0 {
		t.Fatalf("empty native speech: %+v", speech)
	}
	t.Logf("official ENGINE %s, %d speakers, %s/%s, %.3fs PCM, %s", Version, len(speakers), voice.SpeakerName, voice.StyleName, speech.Duration, path)
}
