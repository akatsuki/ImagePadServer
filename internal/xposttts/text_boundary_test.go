package xposttts

import (
	"imagepadserver/internal/xpostmodel"
	"testing"
	"unicode/utf8"
)

func TestURLImmediatelyAfterEmojiUsesExactUTF16Boundary(t *testing.T) {
	post := xpostmodel.Post{RawText: "😀https://t.co/x本文", Entities: []xpostmodel.Entity{{Start: 2, End: 16, Kind: "url"}}}
	got, err := BuildSpeechText(post)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) || got != "😀 本文" {
		t.Fatalf("URL removal corrupted body: %q", got)
	}
}

func TestFallbackURLAndMentionNextToJapanese(t *testing.T) {
	for _, text := range []string{"本文https://example.com 20", "本文@alice 20"} {
		got, err := BuildSpeechText(xpostmodel.Post{RawText: text})
		if err != nil {
			t.Fatal(err)
		}
		if got != "本文 20" {
			t.Fatalf("URL/ID narrated: %q from %q", got, text)
		}
	}
}
