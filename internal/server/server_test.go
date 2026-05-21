package server

import "testing"

func TestValidatePublicURLRejectsLocalhost(t *testing.T) {
	if _, err := validatePublicURL("http://localhost/image.png"); err == nil {
		t.Fatal("expected localhost URL to be rejected")
	}
	if _, err := validatePublicURL("http://127.0.0.1/image.png"); err == nil {
		t.Fatal("expected loopback URL to be rejected")
	}
}

func TestRemoteContentTypeAllowed(t *testing.T) {
	if !remoteContentTypeAllowed("image/webp") {
		t.Fatal("expected image/webp to be allowed")
	}
	if !remoteContentTypeAllowed("image/svg+xml; charset=utf-8") {
		t.Fatal("expected image/svg+xml to be allowed")
	}
	if remoteContentTypeAllowed("text/html") {
		t.Fatal("expected text/html to be rejected")
	}
}
