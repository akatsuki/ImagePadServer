package xpostmodel

import (
	"encoding/json"
	"testing"
)

func TestVoiceAndPostRoundTrip(t *testing.T) {
	v := Voice{EngineURL: "http://127.0.0.1:50021", StyleID: 3, Speed: 1.2, SpeakerUUID: "voice", SpeakerName: "話者", StyleName: "ノーマル"}
	p := Post{ID: "123", Text: "本文", RawText: "本文 https://t.co/test", Entities: []Entity{{Start: 3, End: 20, Kind: "url"}}, Media: []Media{{Kind: "video", URL: "https://video.twimg.com/test.mp4", Width: 720, Height: 1280}}}
	raw, err := json.Marshal(struct {
		Voice Voice
		Post  Post
	}{v, p})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Voice Voice
		Post  Post
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Voice != v || decoded.Post.Media[0].URL != p.Media[0].URL || decoded.Post.RawText != p.RawText {
		t.Fatal("job snapshot lost fields")
	}
}
