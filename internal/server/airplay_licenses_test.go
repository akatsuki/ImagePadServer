package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAirPlayLicenseHandlerRejectsPrivateFilesAndWrites(t *testing.T) {
	s := &Server{}
	for _, test := range []struct {
		method, url string
		status      int
	}{
		{http.MethodGet, "/licenses/airplay?file=gstreamer/bin/libx264-164.dll", http.StatusNotFound},
		{http.MethodGet, "/licenses/airplay?file=sources%2f..%2fsecret", http.StatusNotFound},
		{http.MethodGet, "/licenses/airplay?file=C%3a%2fsecret", http.StatusNotFound},
		{http.MethodPost, "/licenses/airplay", http.StatusMethodNotAllowed},
	} {
		r := httptest.NewRequest(test.method, test.url, nil)
		w := httptest.NewRecorder()
		s.handleAirPlayLicenses(w, r)
		if w.Code != test.status {
			t.Errorf("%s %s: %d", test.method, test.url, w.Code)
		}
	}
}
