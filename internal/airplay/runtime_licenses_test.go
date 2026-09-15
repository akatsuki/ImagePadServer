package airplay

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func TestRuntimeSourceDistributionValidatesReleaseLink(t *testing.T) {
	d := RuntimeSourceDistribution{Schema: 1, RuntimeSetID: "runtime", Repository: "owner/repo", ReleaseTag: "v1.0"}
	d.SourceArchive.AssetName = "sources.zip"
	d.SourceArchive.URL = "https://github.com/owner/repo/releases/download/v1.0/sources.zip"
	d.SourceArchive.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	d.SourceArchive.Size = 123
	data, _ := json.Marshal(d)
	if _, err := parseRuntimeSourceDistribution(data); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"javascript:alert(1)", "https://example.com/sources.zip", "https://github.com/owner/repo/releases/download/v2/sources.zip"} {
		d.SourceArchive.URL = url
		data, _ = json.Marshal(d)
		if _, err := parseRuntimeSourceDistribution(data); err == nil {
			t.Fatalf("accepted %q", url)
		}
	}
}

func TestRuntimeLicenseAssetsExcludeBinariesAndUnsafePaths(t *testing.T) {
	for name, want := range map[string]bool{
		"sources/x264.tar.gz": true, "licenses/uxplay/COPYING": true,
		"gstreamer/share/licenses/x264/COPYING": true, "SOURCE-OFFER.md": true,
		"gstreamer/bin/private.dll": false, "sources/../private.txt": false,
		"sources/test.exe": false, "sources/C:/secret.txt": false,
		"sources\\private.txt": false, "/licenses/COPYING": false,
	} {
		if got := isRuntimeLicenseAsset(name); got != want {
			t.Errorf("%q: got %v want %v", name, got, want)
		}
	}
}

func TestRuntimeLicenseAssetStreamsAndRejectsDuplicate(t *testing.T) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("sources/example.tar.gz")
	f.Write([]byte("source bytes"))
	w.Close()
	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	r, size, err := openRuntimeLicenseAsset(z, "sources/example.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil || string(data) != "source bytes" || size != 12 {
		t.Fatalf("%q %d %v", data, size, err)
	}
	z.File = append(z.File, z.File[0])
	if _, _, err = openRuntimeLicenseAsset(z, "sources/example.tar.gz"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, _, err = openRuntimeLicenseAsset(z, "../secret"); err == nil {
		t.Fatal("unsafe path accepted")
	}
}
