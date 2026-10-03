package xpostexport

import (
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xposttts"
	"strings"
	"testing"
)

func TestRequestProtocolRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, data := range []string{`{"unknown":1}`, `{} {}`, strings.Repeat("x", 128*1024+1)} {
		if _, err := ReadRequest(strings.NewReader(data)); err == nil {
			t.Fatal("accepted invalid protocol")
		}
	}
}

func TestVoiceSelectionMatchesIdentityAndStyle(t *testing.T) {
	speakers := []xposttts.Speaker{{UUID: "a", Name: "声", Styles: []xposttts.Style{{ID: 0, Name: "普通"}}}}
	voice := xpostmodel.Voice{EngineURL: "http://127.0.0.1:50021", SpeakerUUID: "a", StyleID: 0, Speed: 1}
	got, err := ValidateVoice(voice, speakers)
	if err != nil || got.SpeakerName != "声" || got.StyleName != "普通" {
		t.Fatalf("canonical voice: %+v %v", got, err)
	}
	voice.SpeakerUUID = "different"
	if _, err := ValidateVoice(voice, speakers); err == nil {
		t.Fatal("accepted changed speaker for reused style ID")
	}
	voice.SpeakerUUID = "a"
	voice.Speed = 3
	if _, err := ValidateVoice(voice, speakers); err == nil {
		t.Fatal("accepted invalid speed")
	}
}
