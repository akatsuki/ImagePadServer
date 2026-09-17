package niconico

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientFetchesNvCommentWithRequiredRequestShape(t *testing.T) {
	var watchSeen, threadsSeen bool
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/watch/v3_guest/") {
			watchSeen = true
			if r.URL.Query().Get("actionTrackId") == "" || r.Header.Get("X-Frontend-Id") != "6" || r.Header.Get("X-Frontend-Version") != "0" {
				t.Errorf("watch request missing required shape: %s headers=%v", r.URL, r.Header)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"status": 200},
				"data": map[string]any{"response": map[string]any{"comment": map[string]any{"nvComment": map[string]any{
					"server": serverURLForPath(server, ""), "params": "p", "threadKey": "k",
				}}}},
			})
			return
		}
		if r.URL.Path == "/v1/threads" {
			threadsSeen = true
			if r.Header.Get("Content-Type") != "text/plain;charset=UTF-8" || r.Header.Get("Origin") != "https://www.nicovideo.jp" {
				t.Errorf("threads request missing headers: %v", r.Header)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["threadKey"] != "k" {
				t.Errorf("threads request body = %#v, err=%v", body, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{"status": 200},
				"data": map[string]any{"threads": []any{
					map[string]any{"id": "main-thread", "fork": "main", "comments": []any{
						map[string]any{"id": "c1", "no": 1, "vposMs": 1234, "body": "  hello\nworld  ", "commands": []string{"red"}},
					}},
					map[string]any{"id": "easy-thread", "fork": "easy", "comments": []any{
						map[string]any{"id": "c2", "no": 2, "vposMs": 2000, "body": "easy"},
					}},
				}},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.Client(), []string{parsed.Hostname()})
	client.WatchBaseURL = server.URL
	got, err := client.Fetch(t.Context(), "sm9")
	if err != nil {
		t.Fatal(err)
	}
	if !watchSeen || !threadsSeen {
		t.Fatalf("watchSeen=%v threadsSeen=%v", watchSeen, threadsSeen)
	}
	if got.CommentCount != 2 || len(got.Threads) != 2 || got.Threads[0].Comments[0].Body != "  hello\nworld  " {
		t.Fatalf("snapshot = %#v", got)
	}
	if len(got.SelectedForks) != 1 || got.SelectedForks[0] != "main" {
		t.Fatalf("selected forks = %#v, want only main/owner display forks", got.SelectedForks)
	}
}

func TestClientRejectsUnapprovedCommentHost(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"status": 200}, "data": map[string]any{"response": map[string]any{"comment": map[string]any{"nvComment": map[string]any{
			"server": "https://evil.example", "params": "p", "threadKey": "k",
		}}}}})
	}))
	defer server.Close()
	client := NewClient(server.Client(), []string{"127.0.0.1"})
	client.WatchBaseURL = server.URL
	if _, err := client.Fetch(t.Context(), "sm9"); err == nil || !strings.Contains(err.Error(), "comment host") {
		t.Fatalf("Fetch() err=%v, want comment host error", err)
	}
}

func TestClientRetriesTransientWatchFailure(t *testing.T) {
	var watchCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/watch/v3_guest/") {
			if watchCalls.Add(1) == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"status": 200}, "data": map[string]any{"response": map[string]any{"comment": map[string]any{"nvComment": map[string]any{
				"server": server.URL, "params": "p", "threadKey": "k",
			}}}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"status": 200}, "data": map[string]any{"threads": []any{}}})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.Client(), []string{parsed.Hostname()})
	client.WatchBaseURL = server.URL
	got, err := client.Fetch(t.Context(), "sm9")
	if err != nil {
		t.Fatal(err)
	}
	if got.CommentStatus != CommentStatusEmpty || watchCalls.Load() != 2 {
		t.Fatalf("status=%q watchCalls=%d", got.CommentStatus, watchCalls.Load())
	}
}

func serverURLForPath(server *httptest.Server, path string) string {
	return strings.TrimRight(server.URL, "/") + path
}
