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

func TestIsHLSSegmentName(t *testing.T) {
	valid := []string{"current0.ts", "current12.ts"}
	for _, name := range valid {
		if !isHLSSegmentName(name) {
			t.Fatalf("expected %s to be accepted", name)
		}
	}

	invalid := []string{"current.ts", "currentx.ts", "../current0.ts", "current0.mp4"}
	for _, name := range invalid {
		if isHLSSegmentName(name) {
			t.Fatalf("expected %s to be rejected", name)
		}
	}
}

func TestIsVideoUpload(t *testing.T) {
	if !isVideoUpload("clip.mp4", "") {
		t.Fatal("expected mp4 extension to be treated as video")
	}
	if !isVideoUpload("upload.bin", "video/webm; charset=binary") {
		t.Fatal("expected video content type to be treated as video")
	}
	if isVideoUpload("photo.jpg", "image/jpeg") {
		t.Fatal("expected image upload not to be treated as video")
	}
}
