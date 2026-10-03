package xpostimage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"imagepadserver/internal/xpostmodel"
)

type brokenVideoReader struct{ io.Reader }

func (r brokenVideoReader) Read(p []byte) (int, error) {
	if n, err := r.Reader.Read(p); n > 0 || err == nil {
		return n, err
	}
	return 0, errors.New("interrupted download")
}

func TestVideoStreamBoundsAndIncompleteDownloadsNeverPublish(t *testing.T) {
	data := make([]byte, 128)
	copy(data[4:], "ftypisom")
	for _, tc := range []struct {
		name      string
		reader    io.Reader
		limit     int64
		wantError bool
	}{
		{"valid", bytes.NewReader(data), 128, false},
		{"oversized", bytes.NewReader(data), 127, true},
		{"invalid", bytes.NewReader(make([]byte, 128)), 128, true},
		{"interrupted", brokenVideoReader{bytes.NewReader(data)}, 256, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "media.mp4")
			err := saveVideoStream(tc.reader, path, tc.limit)
			if (err != nil) != tc.wantError {
				t.Fatalf("save: %v", err)
			}
			if tc.wantError {
				files, _ := os.ReadDir(dir)
				if len(files) != 0 {
					t.Fatalf("partial media retained: %v", files)
				}
			} else if actual, err := os.ReadFile(path); err != nil || !bytes.Equal(actual, data) {
				t.Fatalf("saved media differs: %v", err)
			}
		})
	}
}

func TestVideoDownloadUsesStreamingPathWithoutBufferFetcher(t *testing.T) {
	called := false
	media, err := downloadMediaFilesWithVideo(t.Context(), []xpostmodel.Media{{Kind: "video", URL: "https://cdn.test/a.mp4"}}, t.TempDir(),
		func(context.Context, string, int64) ([]byte, string, string, error) {
			t.Fatal("video buffered in memory")
			return nil, "", "", nil
		},
		func(_ context.Context, rawURL, path string, limit int64) error {
			called = true
			if limit != maxVideoBytes {
				t.Fatalf("limit: %d", limit)
			}
			return os.WriteFile(path, []byte("fixture"), 0600)
		})
	if err != nil || !called || len(media) != 1 || media[0].Path == "" {
		t.Fatalf("streamed media: %+v %v", media, err)
	}
}
