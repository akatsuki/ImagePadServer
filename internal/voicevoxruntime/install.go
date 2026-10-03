package voicevoxruntime

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxExpandedBytes int64 = 8 << 30
const maxArchiveEntries = 50000

type receipt struct {
	Version, SHA256 string
	Required        []string
}
type Progress func(phase string, percent int)

func install(ctx context.Context, root string, asset Asset, progress Progress) (string, error) {
	if filepath.Base(asset.Name) != asset.Name || asset.Name == "" || asset.Size < 1 || asset.Size > 3<<30 {
		return "", errors.New("invalid VOICEVOX asset")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	lock, err := acquireInstallLock(root)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := cleanupStaging(root); err != nil {
		return "", err
	}
	target := filepath.Join(root, strings.TrimSuffix(asset.Name, ".vvpp"))
	if installed(target, asset) {
		return target, nil
	}
	// Preserve a damaged version before replacing it; never delete a previous installation.
	if _, err := os.Stat(target); err == nil {
		if err := checkOwnedPath(root, target); err != nil {
			return "", err
		}
		backup := target + ".incomplete-" + time.Now().UTC().Format("20060102T150405.000000000")
		if err := os.Rename(target, backup); err != nil {
			return "", fmt.Errorf("VOICEVOX runtimeを退避できません: %w", err)
		}
	}
	if err := checkDiskSpace(root, uint64(asset.Size+maxExpandedBytes)); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".install-")
	if err != nil {
		return "", err
	}
	defer func() {
		if checkOwnedPath(root, stage) == nil {
			_ = os.RemoveAll(stage)
		}
	}()
	archive := filepath.Join(stage, "engine.vvpp")
	if err := download(ctx, archive, asset, progress); err != nil {
		return "", err
	}
	dir := filepath.Join(stage, "runtime")
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	if err := extract(ctx, archive, dir, progress); err != nil {
		return "", err
	}
	required, err := validateRuntime(dir)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(receipt{Version: Version, SHA256: asset.SHA256, Required: required})
	if err := os.WriteFile(filepath.Join(dir, "imagepad-runtime.json"), data, 0600); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(dir, target); err != nil {
		return "", err
	}
	return target, nil
}

func installed(dir string, asset Asset) bool {
	data, err := os.ReadFile(filepath.Join(dir, "imagepad-runtime.json"))
	if err != nil {
		return false
	}
	var r receipt
	if json.Unmarshal(data, &r) != nil || r.Version != Version || r.SHA256 != asset.SHA256 || len(r.Required) < 7 {
		return false
	}
	for _, name := range r.Required {
		if !safeArchiveName(name) {
			return false
		}
		s, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil || !s.Mode().IsRegular() || s.Size() == 0 {
			return false
		}
	}
	_, err = validateRuntime(dir)
	return err == nil
}

func cleanupStaging(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".install-") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if err := checkOwnedPath(root, path); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

// Resolve both paths before any recursive cleanup or directory move.
func checkOwnedPath(root, path string) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("VOICEVOX cleanup path is outside runtime root")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func download(ctx context.Context, path string, a Asset, progress Progress) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ImagePadServer/VOICEVOX-runtime")
	client := &http.Client{Timeout: 30 * time.Minute}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("VOICEVOX download: %s", resp.Status)
	}
	if resp.ContentLength > a.Size {
		return errors.New("VOICEVOX archive exceeds expected size")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	var total int64
	lastPercent := -1
	buf := make([]byte, 256<<10)
	reader := io.LimitReader(resp.Body, a.Size+1)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return err
			}
			_, _ = hash.Write(buf[:n])
			total += int64(n)
			p := int(total * 100 / a.Size)
			if p != lastPercent && progress != nil {
				progress("downloading", p)
				lastPercent = p
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				f.Close()
				return readErr
			}
			break
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if progress != nil {
		progress("verifying", 100)
	}
	if total != a.Size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), a.SHA256) {
		return errors.New("VOICEVOX archive SHA256/size mismatch")
	}
	return ctx.Err()
}

func safeArchiveName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func extract(ctx context.Context, path, dir string, progress Progress) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer z.Close()
	if len(z.File) > maxArchiveEntries {
		return errors.New("VOICEVOX archive contains too many files")
	}
	seen := map[string]bool{}
	var expanded uint64
	for _, f := range z.File {
		name := strings.TrimSuffix(f.Name, "/")
		key := strings.ToLower(name)
		if !safeArchiveName(f.Name) || seen[key] || f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return fmt.Errorf("unsafe VOICEVOX archive entry: %s", f.Name)
		}
		seen[key] = true
		if f.UncompressedSize64 > uint64(maxExpandedBytes)-expanded {
			return errors.New("VOICEVOX expanded archive exceeds limit")
		}
		expanded += f.UncompressedSize64
	}
	var written uint64
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, f.Mode().Perm()|0600)
		if err != nil {
			src.Close()
			return err
		}
		n, copyErr := io.Copy(dst, contextReader{ctx, io.LimitReader(src, int64(f.UncompressedSize64)+1)})
		closeErr := dst.Close()
		src.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if uint64(n) != f.UncompressedSize64 {
			return errors.New("VOICEVOX ZIP entry size mismatch")
		}
		written += uint64(n)
		if progress != nil && expanded > 0 {
			progress("extracting", int(written*100/expanded))
		}
	}
	return nil
}

func engineBinary() string {
	if runtime.GOOS == "windows" {
		return "run.exe"
	}
	return "run"
}
func validateRuntime(dir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "engine_manifest.json"))
	if err != nil {
		return nil, err
	}
	var m struct {
		Version  string `json:"version"`
		Terms    string `json:"terms_of_service"`
		Licenses string `json:"dependency_licenses"`
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Version != Version {
		return nil, errors.New("VOICEVOX engine version mismatch")
	}
	if m.Terms == "" {
		m.Terms = "resources/engine_manifest_assets/terms_of_service.md"
	}
	if m.Licenses == "" {
		m.Licenses = "resources/engine_manifest_assets/dependency_licenses.json"
	}
	required := []string{engineBinary(), "engine_manifest.json", m.Terms, m.Licenses}
	var core, onnx, model, dict string
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		name := strings.ToLower(d.Name())
		if strings.Contains(name, "voicevox_core") && (strings.HasSuffix(name, ".dll") || strings.HasSuffix(name, ".so") || strings.HasSuffix(name, ".dylib")) {
			core = filepath.ToSlash(rel)
		}
		if strings.Contains(name, "onnxruntime") && (strings.HasSuffix(name, ".dll") || strings.Contains(name, ".so") || strings.HasSuffix(name, ".dylib")) {
			onnx = filepath.ToSlash(rel)
		}
		if strings.HasSuffix(name, ".vvm") {
			model = filepath.ToSlash(rel)
		}
		if name == "sys.dic" {
			dict = filepath.ToSlash(rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if core == "" || onnx == "" || model == "" || dict == "" {
		return nil, errors.New("VOICEVOX core/ONNX/model/dictionary is missing")
	}
	required = append(required, core, onnx, model, dict)
	for _, name := range required {
		if !safeArchiveName(name) {
			return nil, errors.New("VOICEVOX manifest path is unsafe")
		}
		s, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil || !s.Mode().IsRegular() || s.Size() == 0 {
			return nil, fmt.Errorf("VOICEVOX required file is missing: %s", name)
		}
	}
	if err := os.Chmod(filepath.Join(dir, engineBinary()), 0700); err != nil {
		return nil, err
	}
	return required, nil
}
