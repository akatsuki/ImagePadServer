package airplay

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func runtimeArtifactBytes(t *testing.T, runtimeSetID string) ([]byte, RuntimeArtifactDescriptor) {
	t.Helper()
	license := []byte(`{"schema":1,"runtimeSetID":"` + runtimeSetID + `"}`)
	offer := []byte("SOURCE-OFFER for " + runtimeSetID + "\n")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, data := range map[string][]byte{
		"license-manifest.json": license,
		"SOURCE-OFFER.md":       offer,
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archiveData := archive.Bytes()
	hash := sha256.Sum256(archiveData)
	return archiveData, RuntimeArtifactDescriptor{
		RuntimeSetID: runtimeSetID, ArchiveURL: "https://example.test/runtime.zip",
		ArchiveSHA256: hex.EncodeToString(hash[:]), ArchiveSize: int64(len(archiveData)),
		LicenseManifestSHA256: hashBytes(license), SourceOfferSHA256: hashBytes(offer),
	}
}

func TestRuntimeDownloadIntegrity(t *testing.T) {
	archiveData, descriptor := runtimeArtifactBytes(t, "download-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archiveData)
	}))
	defer server.Close()
	descriptor.ArchiveURL = server.URL + "/runtime.zip"
	destination := filepath.Join(t.TempDir(), "cache", "runtime.zip")
	if err := DownloadAirPlayRuntimeArchive(context.Background(), server.Client(), descriptor, destination, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, archiveData) {
		t.Fatal("downloaded runtime archive differs from server content")
	}
}

func TestEmbeddedRuntimeBootstrapDescriptor(t *testing.T) {
	_, descriptor := runtimeArtifactBytes(t, "embedded-bootstrap-test")
	data, err := json.Marshal(runtimeBootstrapDescriptor{
		Schema:                    1,
		RuntimeArtifactDescriptor: descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := embeddedRuntimeBootstrapBase64
	defer func() { embeddedRuntimeBootstrapBase64 = previous }()
	embeddedRuntimeBootstrapBase64 = base64.StdEncoding.EncodeToString(data)
	t.Setenv(envAirPlayRuntimeBootstrap, "")
	got, err := loadRuntimeBootstrapDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	if got.RuntimeSetID != descriptor.RuntimeSetID || got.ArchiveSHA256 != descriptor.ArchiveSHA256 {
		t.Fatalf("embedded descriptor = %+v, want %+v", got, descriptor)
	}
}

func TestRuntimeDownloadRejectsHashAndLeavesNoPartial(t *testing.T) {
	archiveData, descriptor := runtimeArtifactBytes(t, "hash-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(w, bytes.NewReader(archiveData))
	}))
	defer server.Close()
	descriptor.ArchiveURL = server.URL + "/runtime.zip"
	descriptor.ArchiveSHA256 = hex.EncodeToString(make([]byte, sha256.Size))
	destination := filepath.Join(t.TempDir(), "cache", "runtime.zip")
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		t.Fatal(err)
	}
	if err := DownloadAirPlayRuntimeArchive(context.Background(), server.Client(), descriptor, destination, true); err == nil {
		t.Fatal("hash mismatch unexpectedly succeeded")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("invalid download left destination: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid download left partial files: %d", len(entries))
	}
}

func TestRuntimeDownloadCancellation(t *testing.T) {
	_, descriptor := runtimeArtifactBytes(t, "cancel-test")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	descriptor.ArchiveURL = server.URL + "/runtime.zip"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(t.TempDir(), "runtime.zip")
	if err := DownloadAirPlayRuntimeArchive(ctx, server.Client(), descriptor, destination, true); err == nil {
		t.Fatal("cancelled runtime download unexpectedly succeeded")
	}
}

func TestRuntimeDownloadRejectsHTTPSDowngrade(t *testing.T) {
	_, descriptor := runtimeArtifactBytes(t, "redirect-test")
	descriptor.ArchiveURL = "http://example.test/runtime.zip"
	if err := DownloadAirPlayRuntimeArchive(context.Background(), http.DefaultClient, descriptor, filepath.Join(t.TempDir(), "runtime.zip"), false); err == nil {
		t.Fatal("non-HTTPS runtime URL unexpectedly accepted")
	}
}
