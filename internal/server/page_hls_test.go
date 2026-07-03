package server

import "testing"

func TestExtractPageHLSURLFromVideoSource(t *testing.T) {
	html := `<html><body><video controls><source src="/media/master.m3u8" type="application/vnd.apple.mpegurl"></video></body></html>`
	got, ok := extractPageHLSURL("https://example.com/watch/123", html)
	if !ok {
		t.Fatal("expected HLS URL")
	}
	if got != "https://example.com/media/master.m3u8" {
		t.Fatalf("HLS URL = %q, want resolved absolute URL", got)
	}
}

func TestExtractPageHLSURLFromVideoSrc(t *testing.T) {
	html := `<video src="https://cdn.example.test/live/playlist.m3u8?token=abc"></video>`
	got, ok := extractPageHLSURL("https://example.com/watch", html)
	if !ok {
		t.Fatal("expected HLS URL")
	}
	if got != "https://cdn.example.test/live/playlist.m3u8?token=abc" {
		t.Fatalf("HLS URL = %q", got)
	}
}

func TestExtractPageHLSURLIgnoresNonHLSVideo(t *testing.T) {
	html := `<video src="https://cdn.example.test/video.mp4"></video>`
	if got, ok := extractPageHLSURL("https://example.com/watch", html); ok {
		t.Fatalf("unexpected HLS URL %q", got)
	}
}
