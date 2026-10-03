package voicevoxruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureArchive(t *testing.T, extra string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	files := map[string]string{"run.exe": "fixture", "run": "fixture", "engine_manifest.json": `{"version":"0.25.2","command":"run"}`, "resources/engine_manifest_assets/terms_of_service.md": "terms", "resources/engine_manifest_assets/dependency_licenses.json": "[]", "_internal/voicevox_core.dll": "core", "_internal/voicevox_onnxruntime.dll": "onnx", "_internal/models/0.vvm": "model", "_internal/dictionary/sys.dic": "dictionary"}
	if extra != "" {
		files[extra] = "bad"
	}
	for name, data := range files {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(data))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInstallVerifiedRuntimeReusesFilesAndRejectsUnsafeArchive(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		badHash     bool
	}{{"good", "", false}, {"checksum", "", true}, {"traversal", "../escape", false}, {"absolute", "C:/escape", false}} {
		t.Run(tc.name, func(t *testing.T) {
			body := fixtureArchive(t, tc.extra)
			sum := sha256.Sum256(body)
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write(body) }))
			defer s.Close()
			asset := Asset{Name: "fixture", URL: s.URL, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
			if tc.badHash {
				asset.SHA256 = hex.EncodeToString(make([]byte, 32))
			}
			root := t.TempDir()
			_, err := install(context.Background(), root, asset, nil)
			if tc.badHash || tc.extra != "" {
				if err == nil {
					t.Fatal("unsafe runtime accepted")
				}
				dirs, _ := os.ReadDir(root)
				if len(dirs) != 1 || dirs[0].Name() != ".install.lock" {
					t.Fatalf("partial files: %v", dirs)
				}
				return
			}
			path, err := install(context.Background(), root, asset, nil)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("redownloaded installed runtime: %d", calls)
			}
			if _, err := os.Stat(filepath.Join(path, "resources/engine_manifest_assets/terms_of_service.md")); err != nil {
				t.Fatal("terms lost")
			}
		})
	}
}

func TestCancelledInstallKeepsNoPartialRuntime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	_, err := install(ctx, root, Asset{Name: "fixture", URL: "http://127.0.0.1:1", SHA256: "abc", Size: 20}, nil)
	if err == nil {
		t.Fatal("cancelled install succeeded")
	}
	dirs, _ := os.ReadDir(root)
	if len(dirs) != 0 {
		t.Fatalf("partial dirs: %v", dirs)
	}
}

func TestArchiveRejectsCollisionSymlinkAndExpansionBomb(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []zip.FileHeader
	}{
		{"case collision", []zip.FileHeader{{Name: "file"}, {Name: "FILE"}}},
		{"symlink", []zip.FileHeader{{Name: "link", ExternalAttrs: (uint32(0120777) << 16), CreatorVersion: 3 << 8}}},
		{"expansion", []zip.FileHeader{{Name: "bomb", UncompressedSize64: uint64(maxExpandedBytes) + 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			z := zip.NewWriter(&b)
			for _, header := range tc.headers {
				if _, err := z.CreateRaw(&header); err != nil {
					t.Fatal(err)
				}
			}
			if err := z.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "bad.zip")
			if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			dst := t.TempDir()
			if err := extract(context.Background(), path, dst, nil); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			files, _ := os.ReadDir(dst)
			if len(files) != 0 {
				t.Fatal("preflight wrote partial files")
			}
		})
	}
}

func TestDamagedRuntimeIsPreservedAndReinstalled(t *testing.T) {
	body := fixtureArchive(t, "")
	sum := sha256.Sum256(body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	asset := Asset{Name: "fixture", URL: server.URL, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	root := t.TempDir()
	dir, err := install(context.Background(), root, asset, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "_internal/voicevox_onnxruntime.dll")); err != nil {
		t.Fatal(err)
	}
	if _, err := install(context.Background(), root, asset, nil); err != nil {
		t.Fatal(err)
	}
	if !installed(dir, asset) {
		t.Fatal("repair did not produce complete runtime")
	}
	files, _ := os.ReadDir(root)
	found := false
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "fixture.incomplete-") {
			found = true
		}
	}
	if !found {
		t.Fatal("previous installation was not preserved")
	}
	if err := checkOwnedPath(root, root); err == nil {
		t.Fatal("root cleanup accepted")
	}
	if err := checkOwnedPath(root, t.TempDir()); err == nil {
		t.Fatal("outside cleanup accepted")
	}
}

func TestCrashStagingRecoveryRespectsActiveInstallerLock(t *testing.T) {
	body := fixtureArchive(t, "")
	sum := sha256.Sum256(body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	asset := Asset{Name: "fixture", URL: server.URL, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
	root := t.TempDir()
	stage := filepath.Join(root, ".install-crash")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stage, "partial.vvpp")
	if err := os.WriteFile(path, []byte("crash leftovers"), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireInstallLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := install(context.Background(), root, asset, nil); err == nil {
		t.Fatal("concurrent install accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("active staging was deleted")
	}
	lock.Close() // Simulate OS-released ownership after the earlier process died.
	if _, err := install(context.Background(), root, asset, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("orphan staging was not reclaimed")
	}
}
