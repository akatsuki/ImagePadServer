package settings

import (
	"imagepadserver/internal/xpostmodel"
	"testing"
)

func TestLastXPostVoicePersistsOneSelection(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if err := Save(Settings{VideoPlayerEnabled: true, VideoQualityMode: "720"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{0, 12} {
		voice := xpostmodel.Voice{EngineURL: "http://127.0.0.1:50021", SpeakerUUID: "a", StyleID: id, Speed: 1.2}
		if err := Update(func(s *Settings) error { s.LastXPostVoice = &voice; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.LastXPostVoice == nil || s.LastXPostVoice.StyleID != 12 || !s.VideoPlayerEnabled || s.VideoQualityMode != "720" {
		t.Fatalf("settings lost: %+v", s)
	}
}
