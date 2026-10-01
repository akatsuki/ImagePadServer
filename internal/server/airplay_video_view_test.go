package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"imagepadserver/internal/settings"
)

func postAirPlayVideoView(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	return adminJSON(t, mux, httptest.NewRequest(http.MethodPost, "/api/airplay/video-view", bytes.NewBufferString(body)))
}

func TestAirPlayVideoViewAPIReadsAndPersistsWhileStopped(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	get := adminJSON(t, mux, httptest.NewRequest(http.MethodGet, "/api/airplay/video-view", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET AirPlay video view status = %d, want %d", get.Code, http.StatusOK)
	}
	var initial struct {
		View map[string]interface{} `json:"airplayVideoView"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.View["configuredMode"] != "contain" {
		t.Fatalf("initial configured mode = %#v, want contain", initial.View["configuredMode"])
	}

	post := postAirPlayVideoView(t, mux, `{"mode":"cover"}`)
	if post.Code != http.StatusOK {
		t.Fatalf("stopped POST status = %d, want %d; body=%s", post.Code, http.StatusOK, post.Body.String())
	}
	var response struct {
		View map[string]interface{} `json:"airplayVideoView"`
	}
	if err := json.Unmarshal(post.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.View["configuredMode"] != "cover" || response.View["appliedMode"] != "cover" {
		t.Fatalf("stopped response = %#v, want cover modes", response.View)
	}
	saved, err := settings.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.AirPlayVideoViewMode != "cover" {
		t.Fatalf("saved video view mode = %q, want cover", saved.AirPlayVideoViewMode)
	}
}

func TestAirPlayVideoViewAPIRejectsInvalidUnknownMultipleAndOversizedBodies(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	bodies := []string{
		`{}`,
		`{"mode":"stretch"}`,
		`{"mode":"cover","extra":true}`,
		`{"mode":"cover"}{"mode":"contain"}`,
		`{"mode":"cover","padding":"` + strings.Repeat("x", 5000) + `"}`,
	}
	for _, body := range bodies {
		rr := postAirPlayVideoView(t, mux, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body length %d status = %d, want %d", len(body), rr.Code, http.StatusBadRequest)
		}
	}
}

func TestAirPlayVideoViewStateIsIncludedInStatePayloadAndUI(t *testing.T) {
	srv, mux := testServer(t, true)
	defer cleanupTestServer(srv)

	stateResponse := adminJSON(t, mux, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if stateResponse.Code != http.StatusOK {
		t.Fatalf("GET state status = %d, want %d", stateResponse.Code, http.StatusOK)
	}
	var state map[string]interface{}
	if err := json.Unmarshal(stateResponse.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if _, ok := state["airplayVideoView"].(map[string]interface{}); !ok {
		t.Fatalf("state airplayVideoView = %#v, want object", state["airplayVideoView"])
	}
	for _, want := range []string{
		`id="airplayVideoViewCover"`,
		`/api/airplay/video-view`,
		`airplayVideoViewPending`,
		`airplayVideoViewCover.disabled = pending`,
		`data.persistenceState === 'failed'`,
		`保存に失敗しました。もう一度切り替えて再試行してください。`,
		`resetOBSPreview`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("dashboard missing AirPlay video view wiring %q", want)
		}
	}
	if strings.Contains(indexHTML, `resetOBSPreview();`) {
		// The existing AirPlay quality flow has a legitimate preview reset. The
		// video-view handler itself must not add one.
		start := strings.Index(indexHTML, `async function saveAirPlayVideoView()`)
		end := strings.Index(indexHTML[start:], `if (airplayVideoViewCover)`) + start
		if start < 0 || end < start || strings.Contains(indexHTML[start:end], `resetOBSPreview`) {
			t.Error("video-view switch must not reset the preview")
		}
	}
}
