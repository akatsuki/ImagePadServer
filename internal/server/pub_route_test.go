package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// TestHandleHistoryPublishDetectsStaleRevision はマルチクライアント同時上書き
// 検知を検証する。古い revision での公開変更は 409 を返し、最新 revision は
// 200 を返す（last-writer-wins は仕様）。
func TestHandleHistoryPublishDetectsStaleRevision(t *testing.T) {
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	src := filepath.Join(t.TempDir(), "pub.jpg")
	if err := os.WriteFile(src, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(src, library.CurrentImage{Kind: "image", PublicName: "pub.jpg", ContentType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	id := store.Current().ID
	baseRev := store.PublishedRevision()

	// 別端末が公開状態を変更（revision を進める）
	if err := store.SetPublished(id, false); err != nil {
		t.Fatal(err)
	}
	if got := store.PublishedRevision(); got <= baseRev {
		t.Fatalf("revision did not advance: base=%d got=%d", baseRev, got)
	}

	// 古い revision で上書き → 409
	body := `{"id":"` + id + `","published":true,"revision":` + strconv.FormatInt(baseRev, 10) + `}`
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/history/publish", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleHistoryPublish(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale revision status = %d, want 409; body=%q", rec.Code, rec.Body.String())
	}

	// 最新 revision で上書き → 200
	latest := store.PublishedRevision()
	body2 := `{"id":"` + id + `","published":true,"revision":` + strconv.FormatInt(latest, 10) + `}`
	req2 := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/history/publish", strings.NewReader(body2))
	rec2 := httptest.NewRecorder()
	srv.handleHistoryPublish(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("fresh revision status = %d, want 200; body=%q", rec2.Code, rec2.Body.String())
	}
}

// TestSetPublishedSyncsCurrentFlag は SetPublished が history と current の
// Published フラグを同期させることを検証する（単一真実源）。
func TestSetPublishedSyncsCurrentFlag(t *testing.T) {
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "pub.jpg")
	if err := os.WriteFile(src, []byte("fake"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(src, library.CurrentImage{Kind: "image", PublicName: "pub.jpg", ContentType: "image/jpeg"}); err != nil {
		t.Fatal(err)
	}
	id := store.Current().ID

	if err := store.SetPublished(id, false); err != nil {
		t.Fatal(err)
	}
	if got := store.Current().Published; got {
		t.Fatalf("current.Published = %v after unpublish, want false", got)
	}

	if err := store.SetPublished(id, true); err != nil {
		t.Fatal(err)
	}
	if got := store.Current().Published; !got {
		t.Fatalf("current.Published = %v after republish, want true", got)
	}
}

// TestHandlePubItemVideoServesVideoPlaceholder は非公開の動画項目が /pub/{id}
// で動画プレースホルダ（video/mp4）を返すことを検証する。ffmpeg が無い環境
// では画像プレースホルダへフォールバックする。
func TestHandlePubItemVideoServesVideoPlaceholder(t *testing.T) {
	store, err := library.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(config.Config{Host: "127.0.0.1", Port: 8080}, store, "http://127.0.0.1:8080/")

	src := filepath.Join(t.TempDir(), "pub.mp4")
	if err := os.WriteFile(src, []byte("fake-video-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	item, err := store.AddHistory(src, library.CurrentImage{Kind: "video", PublicName: "pub.mp4", ContentType: "video/mp4"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/pub/"+item.ID, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	srv.handlePubItem(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unpublished video status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	hasFFmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG")) != ""
	if !hasFFmpeg {
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			hasFFmpeg = true
		}
	}
	if hasFFmpeg {
		if ct != "video/mp4" {
			t.Fatalf("unpublished video content-type = %q, want video/mp4", ct)
		}
	} else if ct != "image/jpeg" && ct != "image/png" {
		t.Fatalf("unpublished video fallback content-type = %q, want image", ct)
	}
}
