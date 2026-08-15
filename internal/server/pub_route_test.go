package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/config"
	"imagepadserver/internal/library"
)

// TestHandlePubItemServesPublishedOrInactive は /pub/{id} の契約を検証する。
// 公開済み項目は実コンテンツ、非公開項目は 404 ではなく ERROR INACTIVE
// ADDRESS プレースホルダ（HTTP 200）を返す。
func TestHandlePubItemServesPublishedOrInactive(t *testing.T) {
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	src := filepath.Join(t.TempDir(), "pub.jpg")
	if err := os.WriteFile(src, []byte("fake-image-bytes"), 0600); err != nil {
		t.Fatal(err)
	}

	// 公開済み項目（SetCurrent → Published=true）
	if err := store.SetCurrent(src, library.CurrentImage{Kind: "image", PublicName: "pub.jpg", ContentType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	cur := store.Current()

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/pub/"+cur.ID, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	srv.handlePubItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("published status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("published content-type = %q, want image/jpeg", ct)
	}

	// 非公開項目（AddHistory → Published=false）→ プレースホルダ（200、404にしない）
	unpub, err := store.AddHistory(src, library.CurrentImage{Kind: "image", PublicName: "unpub.jpg", ContentType: "image/jpeg"})
	if err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/pub/"+unpub.ID, nil)
	req2.RemoteAddr = "127.0.0.1:50000"
	rec2 := httptest.NewRecorder()
	srv.handlePubItem(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("unpublished status = %d, want 200 (placeholder, not 404); body=%q", rec2.Code, rec2.Body.String())
	}
	if rec2.Body.Len() == 0 {
		t.Fatal("unpublished placeholder body is empty")
	}
}
