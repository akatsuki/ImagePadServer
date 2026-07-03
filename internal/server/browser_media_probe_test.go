package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserMediaCandidateRankingKeepsPlayableManifestsAndVideos(t *testing.T) {
	raw := []browserMediaRequest{
		{URL: "https://example.com/video/seg-1.ts", ContentType: "video/mp2t"},
		{URL: "https://example.com/video/master.m3u8?token=1", ContentType: "application/vnd.apple.mpegurl"},
		{URL: "https://example.com/video/master.m3u8?token=1", ContentType: "application/vnd.apple.mpegurl"},
		{URL: "https://example.com/style.css", ContentType: "text/css"},
		{URL: "https://example.com/movie.mp4", ContentType: "video/mp4"},
		{URL: "https://example.com/manifest.mpd", ContentType: "application/dash+xml"},
	}

	candidates := browserMediaCandidatesFromRequests(raw)

	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3: %#v", len(candidates), candidates)
	}
	if candidates[0].Kind != "hls" || candidates[0].URL != "https://example.com/video/master.m3u8?token=1" {
		t.Fatalf("first candidate should be HLS manifest, got %#v", candidates[0])
	}
	if candidates[1].Kind != "dash" {
		t.Fatalf("second candidate should be DASH manifest, got %#v", candidates[1])
	}
	if candidates[2].Kind != "video" {
		t.Fatalf("third candidate should be direct video, got %#v", candidates[2])
	}
}

func TestBrowserMediaCandidatesAPIUsesProbeAndReturnsCandidates(t *testing.T) {
	s := &Server{}
	original := browserMediaProbe
	t.Cleanup(func() { browserMediaProbe = original })
	browserMediaProbe = func(ctx context.Context, pageURL string) ([]browserMediaCandidate, error) {
		if pageURL != "https://example.com/watch" {
			t.Fatalf("probe URL = %q", pageURL)
		}
		return []browserMediaCandidate{{
			ID:          "c1",
			URL:         "https://example.com/master.m3u8",
			Kind:        "hls",
			Label:       "HLS",
			ContentType: "application/vnd.apple.mpegurl",
			Source:      "network",
		}}, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/browser-media-candidates", strings.NewReader(`{"url":"https://example.com/watch"}`))
	rec := httptest.NewRecorder()
	s.handleBrowserMediaCandidates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Candidates []browserMediaCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Candidates) != 1 || body.Candidates[0].URL != "https://example.com/master.m3u8" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestBrowserMediaCandidatesAPISkipsKnownYTDLPSites(t *testing.T) {
	s := &Server{}
	original := browserMediaProbe
	t.Cleanup(func() { browserMediaProbe = original })
	browserMediaProbe = func(ctx context.Context, pageURL string) ([]browserMediaCandidate, error) {
		t.Fatalf("browser media probe must not run for known yt-dlp page URL %q", pageURL)
		return nil, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/api/browser-media-candidates", strings.NewReader(`{"url":"https://www.youtube.com/watch?v=test"}`))
	rec := httptest.NewRecorder()
	s.handleBrowserMediaCandidates(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Candidates  []browserMediaCandidate `json:"candidates"`
		Unavailable bool                    `json:"unavailable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Unavailable {
		t.Fatalf("expected unavailable response for known yt-dlp page URL: %#v", body)
	}
	if len(body.Candidates) != 0 {
		t.Fatalf("expected no candidates, got %#v", body.Candidates)
	}
}

func TestBrowserMediaProbeBackendIsCDP(t *testing.T) {
	if got := browserMediaProbeBackend(); got != "cdp" {
		t.Fatalf("browser media probe backend = %q, want cdp", got)
	}
}

func TestBrowserMediaRequestsFromCDPEvents(t *testing.T) {
	events := []cdpMessage{
		{
			Method: "Network.requestWillBeSent",
			Params: json.RawMessage(`{"request":{"url":"https://example.com/watch","method":"GET"}}`),
		},
		{
			Method: "Network.responseReceived",
			Params: json.RawMessage(`{"response":{"url":"https://example.com/live/master.m3u8","mimeType":"application/vnd.apple.mpegurl","headers":{"content-type":"application/vnd.apple.mpegurl"}}}`),
		},
		{
			Method: "Network.responseReceived",
			Params: json.RawMessage(`{"response":{"url":"https://example.com/movie.mp4","mimeType":"video/mp4","headers":{"Content-Type":"video/mp4"}}}`),
		},
	}

	requests := browserMediaRequestsFromCDPEvents(events)
	candidates := browserMediaCandidatesFromRequests(requests)

	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2: %#v", len(candidates), candidates)
	}
	if candidates[0].Kind != "hls" || candidates[0].ContentType != "application/vnd.apple.mpegurl" {
		t.Fatalf("first candidate should be HLS from response headers, got %#v", candidates[0])
	}
	if candidates[1].Kind != "video" || candidates[1].ContentType != "video/mp4" {
		t.Fatalf("second candidate should be video from response headers, got %#v", candidates[1])
	}
}
