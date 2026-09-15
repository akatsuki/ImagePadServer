package airplay

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const uxPlayRTPSourceEnv = "IMAGEPAD_UXPLAY_RTP_DIR"

// These files are the only supplemental GStreamer files accepted for the
// pinned UxPlay 2.0.0.1736 bundle. The shared GStreamer ABI files already
// shipped by UxPlay are intentionally not duplicated.
var uxPlayRTPSupplementFiles = []bundleFile{
	{Path: "lib/gstreamer-1.0/libgstrtp.dll", Size: 763911, SHA256: "480fe2686c6b0530f9bd5b689fbad05d33ed30c499be20b46484026ca9bdb949"},
	{Path: "lib/gstreamer-1.0/libgstudp.dll", Size: 98906, SHA256: "5d255468a5fd1fbd23bd0de8ee8954fef97d0bffb66baf23af1ed59abe78c61e"},
	{Path: "libgstrtp-1.0-0.dll", Size: 180197, SHA256: "93d44323c7732b784acddbbe0ba1aed00dd6cf8491068908dee766924c5dae7c"},
	{Path: "libgstnet-1.0-0.dll", Size: 123382, SHA256: "8a18986d1ee9c2c4b62daba844787c733846960c5214c7066fc6f87f1cfea756"},
}

func uxPlayRTPSupplementSource(root string) string {
	if configured := os.Getenv(uxPlayRTPSourceEnv); configured != "" {
		return configured
	}
	return filepath.Join(filepath.Dir(root), uxPlayVersion+"-rtp")
}

func installUxPlayRTPSupplement(root string, manifest *bundleManifest) error {
	seen := make(map[string]struct{}, len(manifest.Files))
	for _, file := range manifest.Files {
		seen[file.Path] = struct{}{}
	}
	sourceRoot := uxPlayRTPSupplementSource(root)
	for _, expected := range uxPlayRTPSupplementFiles {
		if _, exists := seen[expected.Path]; exists {
			if err := verifyUxPlayRTPFile(filepath.Join(root, filepath.FromSlash(expected.Path)), expected); err != nil {
				return fmt.Errorf("verify existing RTP dependency %q: %w", expected.Path, err)
			}
			continue
		}
		source := filepath.Join(sourceRoot, filepath.FromSlash(expected.Path))
		if _, err := os.Stat(source); err != nil {
			// A flat payload directory is accepted for deployment convenience,
			// but the bytes and destination path remain pinned by the manifest.
			source = filepath.Join(sourceRoot, filepath.Base(expected.Path))
		}
		if err := verifyUxPlayRTPFile(source, expected); err != nil {
			return fmt.Errorf("verify RTP dependency payload %q from %q: %w", expected.Path, sourceRoot, err)
		}
		if err := copyUxPlayRTPFile(source, filepath.Join(root, filepath.FromSlash(expected.Path)), expected.Size); err != nil {
			return fmt.Errorf("install RTP dependency %q: %w", expected.Path, err)
		}
		if err := verifyUxPlayRTPFile(filepath.Join(root, filepath.FromSlash(expected.Path)), expected); err != nil {
			return fmt.Errorf("verify installed RTP dependency %q: %w", expected.Path, err)
		}
		manifest.Files = append(manifest.Files, expected)
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	return nil
}

func verifyUxPlayRTPFile(path string, expected bundleFile) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}
	if info.Size() != expected.Size {
		return fmt.Errorf("size %d does not match pinned size %d", info.Size(), expected.Size)
	}
	_, digest, err := hashFile(path, uint64(expected.Size))
	if err != nil {
		return err
	}
	if digest != expected.SHA256 {
		return fmt.Errorf("SHA-256 mismatch: got %s", digest)
	}
	return nil
}

func copyUxPlayRTPFile(source, destination string, size int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".rtp-dependency-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(input, size+1))
	if err == nil && n != size {
		err = fmt.Errorf("copied size %d does not match expected size %d", n, size)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpPath, destination)
}
