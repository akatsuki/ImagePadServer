package video

import (
	"testing"
)

// --- NormalizeEmbeddedTag tests ---

func TestNormalizeEmbeddedTagAscii(t *testing.T) {
	got := NormalizeEmbeddedTag("hello world")
	if got != "hello world" {
		t.Fatalf("expected %q, got %q", "hello world", got)
	}
}

func TestNormalizeEmbeddedTagJapanese(t *testing.T) {
	input := "日本語"
	got := NormalizeEmbeddedTag(input)
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestNormalizeEmbeddedTagSimplifiedChinese(t *testing.T) {
	input := "简体中文"
	got := NormalizeEmbeddedTag(input)
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestNormalizeEmbeddedTagKorean(t *testing.T) {
	input := "한국어"
	got := NormalizeEmbeddedTag(input)
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestNormalizeEmbeddedTagCafe(t *testing.T) {
	input := "café"
	got := NormalizeEmbeddedTag(input)
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestNormalizeEmbeddedTagRepairsGunpeiCP932(t *testing.T) {
	artist := string([]rune{0x93, 0xA1, 0x8E, 0x71, 0x96, 0xBC, 0x90, 0x6C})
	album := string([]rune{0x94, 0x5A, 0x93, 0x78})
	if got := NormalizeEmbeddedTag(artist); got != "藤子名人" {
		t.Fatalf("artist=%q", got)
	}
	if got := NormalizeEmbeddedTag(album); got != "濃度" {
		t.Fatalf("album=%q", got)
	}
}

// --- ResolveAudioMetadata tests ---

func TestResolveAudioMetadataSoundCloudPrefersEmbedded(t *testing.T) {
	embedded := AudioMetadata{Title: "Embedded Title", Artist: "Embedded Artist", Album: "Embedded Album"}
	sc := AudioMetadata{Title: "SC Title", Artist: "SC Artist", Album: "SC Album"}
	result := ResolveAudioMetadata(SourceSoundCloud, "track.mp3", embedded, sc)
	if result.Title != "Embedded Title" || result.Artist != "Embedded Artist" || result.Album != "Embedded Album" {
		t.Fatalf("got %+v", result)
	}
}

func TestResolveAudioMetadataSoundCloudFallsBackToSC(t *testing.T) {
	embedded := AudioMetadata{Title: "", Artist: "", Album: ""}
	sc := AudioMetadata{Title: "SC Title", Artist: "SC Artist", Album: "SC Album"}
	result := ResolveAudioMetadata(SourceSoundCloud, "track.mp3", embedded, sc)
	if result.Title != "SC Title" || result.Artist != "SC Artist" || result.Album != "SC Album" {
		t.Fatalf("got %+v", result)
	}
}

func TestResolveAudioMetadataSoundCloudFallsBackToSourceName(t *testing.T) {
	embedded := AudioMetadata{Title: "", Artist: "", Album: ""}
	sc := AudioMetadata{Title: "", Artist: "", Album: ""}
	result := ResolveAudioMetadata(SourceSoundCloud, "my_track.mp3", embedded, sc)
	if result.Title != "my_track" {
		t.Fatalf("title=%q", result.Title)
	}
	if result.Artist != "" || result.Album != "" {
		t.Fatalf("got %+v", result)
	}
}

func TestResolveAudioMetadataLocalOnlyEmbedded(t *testing.T) {
	embedded := AudioMetadata{Title: "Embedded Title", Artist: "Embedded Artist", Album: "Embedded Album"}
	sc := AudioMetadata{Title: "SC Title", Artist: "SC Artist", Album: "SC Album"}
	result := ResolveAudioMetadata(SourceLocalAudio, "local.mp3", embedded, sc)
	if result.Title != "Embedded Title" || result.Artist != "Embedded Artist" || result.Album != "Embedded Album" {
		t.Fatalf("got %+v", result)
	}
}

func TestResolveAudioMetadataLocalIgnoresSoundCloud(t *testing.T) {
	embedded := AudioMetadata{Title: "", Artist: "", Album: ""}
	sc := AudioMetadata{Title: "SC Title", Artist: "SC Artist", Album: "SC Album"}
	result := ResolveAudioMetadata(SourceLocalAudio, "local.mp3", embedded, sc)
	// Must NOT use SoundCloud metadata for local; title falls back to source name
	if result.Title != "local" || result.Artist != "" || result.Album != "" {
		t.Fatalf("local should not use SC metadata, got %+v", result)
	}
}

func TestResolveAudioMetadataRemoteOnlyEmbedded(t *testing.T) {
	embedded := AudioMetadata{Title: "Remote Title", Artist: "Remote Artist", Album: ""}
	sc := AudioMetadata{Title: "SC Title", Artist: "SC Artist", Album: "SC Album"}
	result := ResolveAudioMetadata(SourceRemoteAudio, "remote.mp3", embedded, sc)
	if result.Title != "Remote Title" || result.Artist != "Remote Artist" || result.Album != "" {
		t.Fatalf("got %+v", result)
	}
}

func TestResolveAudioMetadataRemoteIgnoresSoundCloud(t *testing.T) {
	embedded := AudioMetadata{Title: "", Artist: "", Album: ""}
	sc := AudioMetadata{Title: "SC Title", Artist: "SC Artist", Album: "SC Album"}
	result := ResolveAudioMetadata(SourceRemoteAudio, "remote.mp3", embedded, sc)
	// Must NOT use SoundCloud metadata for remote; title falls back to source name
	if result.Title != "remote" || result.Artist != "" || result.Album != "" {
		t.Fatalf("remote should not use SC metadata, got %+v", result)
	}
}

func TestResolveAudioMetadataLocalUsesSourceNameFallback(t *testing.T) {
	embedded := AudioMetadata{Title: "", Artist: "", Album: ""}
	result := ResolveAudioMetadata(SourceLocalAudio, "song.mp3", embedded, AudioMetadata{})
	if result.Title != "song" {
		t.Fatalf("title=%q", result.Title)
	}
}

func TestResolveAudioMetadataRemoteUsesSourceNameFallback(t *testing.T) {
	embedded := AudioMetadata{Title: "", Artist: "", Album: ""}
	result := ResolveAudioMetadata(SourceRemoteAudio, "podcast.mp3", embedded, AudioMetadata{})
	if result.Title != "podcast" {
		t.Fatalf("title=%q", result.Title)
	}
}

func TestResolveAudioMetadataSoundCloudPartialFallback(t *testing.T) {
	embedded := AudioMetadata{Title: "Embedded Title", Artist: "", Album: ""}
	sc := AudioMetadata{Title: "", Artist: "SC Artist", Album: "SC Album"}
	result := ResolveAudioMetadata(SourceSoundCloud, "track.mp3", embedded, sc)
	if result.Title != "Embedded Title" || result.Artist != "SC Artist" || result.Album != "SC Album" {
		t.Fatalf("got %+v", result)
	}
}
