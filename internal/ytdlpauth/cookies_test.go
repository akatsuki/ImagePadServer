package ytdlpauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNetscapeCookies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cookies.txt")
	cookies := []Cookie{
		{
			Name:     "SID",
			Value:    "abc",
			Domain:   ".youtube.com",
			Path:     "/",
			Expires:  1893456000,
			HTTPOnly: true,
			Secure:   true,
		},
	}

	if err := WriteNetscapeCookies(path, cookies); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "# Netscape HTTP Cookie File") {
		t.Fatalf("missing Netscape header: %q", text)
	}
	if !strings.Contains(text, "#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1893456000\tSID\tabc") {
		t.Fatalf("cookie line not in yt-dlp compatible format: %q", text)
	}
}

func TestStatusAndDeleteCookies(t *testing.T) {
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	if Status().Saved {
		t.Fatal("fresh data dir should not report saved cookies")
	}
	if err := os.MkdirAll(filepath.Dir(CookieFilePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CookieFilePath(), []byte("cookie"), 0600); err != nil {
		t.Fatal(err)
	}
	if !Status().Saved {
		t.Fatal("cookie file should report saved cookies")
	}
	if err := DeleteCookies(); err != nil {
		t.Fatal(err)
	}
	if Status().Saved {
		t.Fatal("deleted cookie file should not report saved cookies")
	}
}

func TestHasYouTubeLoginCookieRequiresYouTubeCookie(t *testing.T) {
	googleOnly := []Cookie{
		{Name: "SID", Domain: ".google.com", Path: "/", Value: "google"},
		{Name: "__Secure-1PSID", Domain: ".google.com", Path: "/", Value: "google"},
	}
	if hasYouTubeLoginCookie(googleOnly) {
		t.Fatal("google account cookies alone should not complete YouTube login")
	}
	youtubeLogin := append(googleOnly, Cookie{Name: "LOGIN_INFO", Domain: ".youtube.com", Path: "/", Value: "youtube"})
	if !hasYouTubeLoginCookie(youtubeLogin) {
		t.Fatal("youtube login cookie should complete YouTube login")
	}
}
