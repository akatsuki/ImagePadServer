package airplay

import (
	"archive/zip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
)

// RuntimeLicenseAsset describes a redistributable notice or source file in the EXE.
type RuntimeLicenseAsset struct {
	Name string
	Size int64
}

// RuntimeSourceDistribution identifies the complete sources on the same release.
type RuntimeSourceDistribution struct {
	Schema        int    `json:"schema"`
	RuntimeSetID  string `json:"runtimeSetID"`
	Repository    string `json:"repository"`
	ReleaseTag    string `json:"releaseTag"`
	SourceArchive struct {
		AssetName string `json:"assetName"`
		URL       string `json:"url"`
		SHA256    string `json:"sha256"`
		Size      int64  `json:"size"`
	} `json:"sourceArchive"`
}

func parseRuntimeSourceDistribution(data []byte) (*RuntimeSourceDistribution, error) {
	var d RuntimeSourceDistribution
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	digest, err := hex.DecodeString(d.SourceArchive.SHA256)
	if err != nil || len(digest) != 32 || d.Schema != 1 || d.RuntimeSetID == "" || d.SourceArchive.Size <= 0 ||
		!regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(d.Repository) ||
		!regexp.MustCompile(`^v[A-Za-z0-9_.-]+$`).MatchString(d.ReleaseTag) ||
		!regexp.MustCompile(`^[A-Za-z0-9_.-]+\.zip$`).MatchString(d.SourceArchive.AssetName) ||
		d.SourceArchive.URL != "https://github.com/"+d.Repository+"/releases/download/"+d.ReleaseTag+"/"+d.SourceArchive.AssetName {
		return nil, errors.New("invalid corresponding source distribution")
	}
	return &d, nil
}

// ReadRuntimeSourceDistribution reads only embedded metadata; it never downloads sources.
func ReadRuntimeSourceDistribution() (*RuntimeSourceDistribution, error) {
	r, size, err := OpenRuntimeLicenseAsset("source-distribution.json")
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if size > 16384 {
		return nil, errors.New("oversized source distribution metadata")
	}
	data, err := io.ReadAll(io.LimitReader(r, 16385))
	if err != nil {
		return nil, err
	}
	return parseRuntimeSourceDistribution(data)
}

func isRuntimeLicenseAsset(name string) bool {
	if name == "" || path.Clean(name) != name || strings.ContainsAny(name, "\\:") || strings.HasPrefix(name, "/") {
		return false
	}
	if name == "SOURCE-OFFER.md" || name == "license-manifest.json" || name == "source-manifest.json" || name == "source-distribution.json" || name == "gstreamer/share/versions.txt" {
		return true
	}
	if !strings.HasPrefix(name, "sources/") && !strings.HasPrefix(name, "licenses/") && !strings.HasPrefix(name, "gstreamer/share/licenses/") {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".exe", ".dll", ".com", ".html", ".js", ".svg":
		return false
	}
	return true
}

func runtimeLicenseArchive() (*zip.Reader, io.Closer, error) {
	r, descriptor, err := openEmbeddedRuntimeArchive()
	if err != nil {
		return nil, nil, err
	}
	reader, ok := r.(io.ReaderAt)
	if !ok {
		r.Close()
		return nil, nil, errors.New("embedded archive cannot be read at offsets")
	}
	z, err := zip.NewReader(reader, descriptor.ArchiveSize)
	if err != nil {
		r.Close()
		return nil, nil, err
	}
	return z, r, nil
}

// RuntimeLicenseAssets lists the embedded files without installing the runtime or accessing the network.
func RuntimeLicenseAssets() ([]RuntimeLicenseAsset, error) {
	z, closer, err := runtimeLicenseArchive()
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	assets := make([]RuntimeLicenseAsset, 0)
	seen := make(map[string]bool)
	for _, f := range z.File {
		if f.FileInfo().IsDir() || !isRuntimeLicenseAsset(f.Name) {
			continue
		}
		if seen[f.Name] {
			return nil, errors.New("duplicate license asset")
		}
		seen[f.Name] = true
		assets = append(assets, RuntimeLicenseAsset{Name: f.Name, Size: int64(f.UncompressedSize64)})
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	return assets, nil
}

type runtimeLicenseStream struct {
	io.ReadCloser
	archive io.Closer
}

func (r *runtimeLicenseStream) Close() error {
	err := r.ReadCloser.Close()
	r.archive.Close()
	return err
}

func openRuntimeLicenseAsset(z *zip.Reader, name string) (io.ReadCloser, int64, error) {
	if !isRuntimeLicenseAsset(name) {
		return nil, 0, fs.ErrNotExist
	}
	var found *zip.File
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		if found != nil || f.FileInfo().IsDir() {
			return nil, 0, errors.New("invalid license asset")
		}
		found = f
	}
	if found == nil {
		return nil, 0, fs.ErrNotExist
	}
	r, err := found.Open()
	return r, int64(found.UncompressedSize64), err
}

// OpenRuntimeLicenseAsset streams one allowlisted file, keeping large source archives out of the heap.
func OpenRuntimeLicenseAsset(name string) (io.ReadCloser, int64, error) {
	if !isRuntimeLicenseAsset(name) {
		return nil, 0, fs.ErrNotExist
	}
	z, closer, err := runtimeLicenseArchive()
	if err != nil {
		return nil, 0, err
	}
	r, size, err := openRuntimeLicenseAsset(z, name)
	if err != nil {
		closer.Close()
		return nil, 0, err
	}
	return &runtimeLicenseStream{ReadCloser: r, archive: closer}, size, nil
}
