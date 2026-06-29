package obsrtmp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMediaMTXResolutionOrder(t *testing.T) {
	root := t.TempDir()
	envPath := writeMediaMTXExecutable(t, filepath.Join(root, "env", "mediamtx.exe"))
	bundledPath := writeMediaMTXExecutable(t, filepath.Join(root, "bundle", mediaMTXVersion, "mediamtx.exe"))
	managedPath := writeMediaMTXExecutable(t, filepath.Join(root, "managed", mediaMTXVersion, "mediamtx.exe"))
	writeMediaMTXMarker(t, filepath.Dir(managedPath), mediaMTXArchiveSHA256)

	m := testMediaMTXManager(root)
	m.bundledRoot = filepath.Join(root, "bundle")
	m.managedRoot = filepath.Join(root, "managed")
	m.getenv = func(name string) string {
		if name == "IMAGEPAD_MEDIAMTX" {
			return envPath
		}
		return ""
	}

	got, err := m.resolve()
	if err != nil || got != envPath {
		t.Fatalf("environment resolution = %q, %v; want %q", got, err, envPath)
	}

	m.getenv = func(string) string { return "" }
	got, err = m.resolve()
	if err != nil || got != bundledPath {
		t.Fatalf("bundled resolution = %q, %v; want %q", got, err, bundledPath)
	}

	if err := os.RemoveAll(filepath.Dir(bundledPath)); err != nil {
		t.Fatal(err)
	}
	got, err = m.resolve()
	if err != nil || got != managedPath {
		t.Fatalf("managed resolution = %q, %v; want %q", got, err, managedPath)
	}
}

func TestMediaMTXRejectsMissingOverrideAndManagedChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	m := testMediaMTXManager(root)
	m.getenv = func(string) string { return filepath.Join(root, "missing.exe") }
	if _, err := m.resolve(); err == nil {
		t.Fatal("missing IMAGEPAD_MEDIAMTX override was accepted")
	}

	m.getenv = func(string) string { return "" }
	managedPath := writeMediaMTXExecutable(t, filepath.Join(m.managedRoot, mediaMTXVersion, "mediamtx.exe"))
	writeMediaMTXMarker(t, filepath.Dir(managedPath), "wrong-checksum")
	if _, err := m.resolve(); err == nil {
		t.Fatal("managed install with mismatched checksum marker was accepted")
	}
}

func TestMediaMTXVersionOutputRequiresExactPinnedVersion(t *testing.T) {
	for _, output := range []string{"v1.19.2", "MediaMTX v1.19.2\r\n"} {
		if !isPinnedMediaMTXVersion(output) {
			t.Errorf("rejected pinned version output %q", output)
		}
	}
	for _, output := range []string{"v1.19.20", "v1.19.2-rc1", "MediaMTX v1.19.1"} {
		if isPinnedMediaMTXVersion(output) {
			t.Errorf("accepted non-pinned version output %q", output)
		}
	}
}

func TestMediaMTXInstallExtractsAllowlistAndReusesValidInstall(t *testing.T) {
	archive := mediaMTXArchive(t, map[string][]byte{
		"mediamtx.exe":     []byte("exe"),
		"mediamtx.yml":     []byte("config"),
		"LICENSE":          []byte("license"),
		"README.md":        []byte("must not extract"),
		"nested/extra.txt": []byte("must not extract"),
	})
	sum := sha256.Sum256(archive)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	root := t.TempDir()
	m := testMediaMTXManager(root)
	m.downloadURL = server.URL
	m.archiveSHA256 = hex.EncodeToString(sum[:])

	path, err := m.ensure(context.Background())
	if err != nil {
		t.Fatalf("ensure MediaMTX: %v", err)
	}
	for _, name := range []string{"mediamtx.exe", "mediamtx.yml", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), name)); err != nil {
			t.Fatalf("required file %s missing: %v", name, err)
		}
	}
	for _, name := range []string{"README.md", filepath.Join("nested", "extra.txt")} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unexpected extracted file %s: %v", name, err)
		}
	}

	again, err := m.ensure(context.Background())
	if err != nil || again != path {
		t.Fatalf("reuse existing install = %q, %v; want %q", again, err, path)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("download requests = %d, want 1", got)
	}
}

func TestMediaMTXInstallSerializesConcurrentCallers(t *testing.T) {
	archive := mediaMTXArchive(t, map[string][]byte{
		"mediamtx.exe": []byte("exe"),
		"mediamtx.yml": []byte("config"),
		"LICENSE":      []byte("license"),
	})
	sum := sha256.Sum256(archive)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	m := testMediaMTXManager(t.TempDir())
	m.downloadURL = server.URL
	m.archiveSHA256 = hex.EncodeToString(sum[:])

	const callers = 12
	paths := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := m.ensure(context.Background())
			paths <- path
			errs <- err
		}()
	}
	wg.Wait()
	close(paths)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ensure: %v", err)
		}
	}
	var first string
	for path := range paths {
		if first == "" {
			first = path
		}
		if path != first {
			t.Fatalf("concurrent paths differ: %q and %q", first, path)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("download requests = %d, want 1", got)
	}
}

func TestMediaMTXInstallCleansUpFailures(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		sha256  string
		prepare func(*mediaMTXToolManager)
	}{
		{name: "interrupted download", body: []byte("partial"), sha256: mediaMTXArchiveSHA256, prepare: func(m *mediaMTXToolManager) {
			m.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(io.MultiReader(bytes.NewReader([]byte("partial")), errorReader{})), Header: make(http.Header)}, nil
			})}
		}},
		{name: "invalid zip", body: []byte("not-a-zip")},
		{name: "checksum mismatch", body: mediaMTXArchive(t, map[string][]byte{"mediamtx.exe": []byte("exe")}), sha256: mediaMTXArchiveSHA256},
		{name: "read-only destination", body: mediaMTXArchive(t, map[string][]byte{"mediamtx.exe": []byte("exe"), "mediamtx.yml": []byte("config"), "LICENSE": []byte("license")}), prepare: func(m *mediaMTXToolManager) {
			m.mkdirAll = func(string, os.FileMode) error { return os.ErrPermission }
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			m := testMediaMTXManager(root)
			if tt.prepare != nil {
				tt.prepare(m)
			}
			if m.client == http.DefaultClient {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(tt.body) }))
				defer server.Close()
				m.client = server.Client()
				m.downloadURL = server.URL
			}
			if tt.sha256 != "" {
				m.archiveSHA256 = tt.sha256
			} else {
				sum := sha256.Sum256(tt.body)
				m.archiveSHA256 = hex.EncodeToString(sum[:])
			}

			if _, err := m.ensure(context.Background()); err == nil {
				t.Fatal("ensure unexpectedly succeeded")
			}
			if _, err := os.Stat(filepath.Join(m.managedRoot, mediaMTXVersion)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed install left final directory: %v", err)
			}
			entries, err := os.ReadDir(m.managedRoot)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) == ".zip" {
					t.Fatalf("failed install left temporary artifact %q", entry.Name())
				}
			}
		})
	}
}

func testMediaMTXManager(root string) *mediaMTXToolManager {
	return &mediaMTXToolManager{
		version:       mediaMTXVersion,
		downloadURL:   "https://example.invalid/mediamtx.zip",
		archiveSHA256: mediaMTXArchiveSHA256,
		maxBytes:      mediaMTXArchiveMaxBytes,
		bundledRoot:   filepath.Join(root, "bundle"),
		managedRoot:   filepath.Join(root, "managed"),
		getenv:        func(string) string { return "" },
		validate: func(path string) error {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return errors.New("not a regular file")
			}
			return nil
		},
		client:    http.DefaultClient,
		mkdirAll:  os.MkdirAll,
		rename:    os.Rename,
		removeAll: os.RemoveAll,
	}
}

func writeMediaMTXExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMediaMTXMarker(t *testing.T, dir, checksum string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, mediaMTXChecksumMarker), []byte(checksum+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mediaMTXArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
