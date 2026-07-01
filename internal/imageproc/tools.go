package imageproc

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"imagepadserver/internal/settings"
)

const (
	pngquantVersion = "3.0.3"
	oxipngVersion   = "9.1.3"
)

func EnsurePngquant() (string, error) {
	return ensureImageTool(imageToolSpec{
		name:        "pngquant",
		envPath:     "IMAGEPAD_PNGQUANT",
		envChecksum: "IMAGEPAD_PNGQUANT_SHA256",
		version:     pngquantVersion,
		versionArg:  "--version",
		url: func() string {
			if runtime.GOOS == "windows" {
				return "https://pngquant.org/pngquant-windows.zip"
			}
			return ""
		},
	})
}

func EnsureOxipng() (string, error) {
	return ensureImageTool(imageToolSpec{
		name:        "oxipng",
		envPath:     "IMAGEPAD_OXIPNG",
		envChecksum: "IMAGEPAD_OXIPNG_SHA256",
		version:     oxipngVersion,
		versionArg:  "--version",
		url: func() string {
			switch runtime.GOOS {
			case "windows":
				return "https://github.com/shssoichiro/oxipng/releases/download/v9.1.3/oxipng-9.1.3-x86_64-pc-windows-msvc.zip"
			case "darwin":
				if runtime.GOARCH == "arm64" {
					return "https://github.com/shssoichiro/oxipng/releases/download/v9.1.3/oxipng-9.1.3-aarch64-apple-darwin.tar.gz"
				}
				return "https://github.com/shssoichiro/oxipng/releases/download/v9.1.3/oxipng-9.1.3-x86_64-apple-darwin.tar.gz"
			default:
				return ""
			}
		},
	})
}

func ValidateImageTools() {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return
	}
	_, _ = EnsurePngquant()
	_, _ = EnsureOxipng()
}

type imageToolSpec struct {
	name        string
	envPath     string
	envChecksum string
	version     string
	versionArg  string
	url         func() string
}

func ensureImageTool(spec imageToolSpec) (string, error) {
	if configured := strings.TrimSpace(os.Getenv(spec.envPath)); configured != "" {
		if err := validateImageTool(configured, spec.versionArg); err != nil {
			return "", err
		}
		return configured, nil
	}
	local := localImageToolPath(spec)
	if validateImageTool(local, spec.versionArg) == nil {
		return local, nil
	}
	if path, err := exec.LookPath(executableName(spec.name)); err == nil && validateImageTool(path, spec.versionArg) == nil {
		return path, nil
	}
	downloadURL := spec.url()
	if downloadURL == "" || os.Getenv("IMAGEPAD_SKIP_IMAGE_TOOL_DOWNLOAD") == "1" {
		return "", nil
	}
	if err := downloadImageTool(spec, downloadURL, local); err != nil {
		return "", err
	}
	if validateImageTool(local, spec.versionArg) != nil {
		return "", nil
	}
	return local, nil
}

func executableName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

func localImageToolPath(spec imageToolSpec) string {
	return filepath.Join(settings.Dir(), "bin", "image-tools", spec.name+"-"+spec.version, executableName(spec.name))
}

func validateImageTool(path, versionArg string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	cmd := exec.Command(path, versionArg)
	hideWindow(cmd)
	return cmd.Run()
}

func downloadImageTool(spec imageToolSpec, rawURL, target string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download %s failed: %s", spec.name, resp.Status)
	}
	tmp, err := os.CreateTemp("", spec.name+"-*"+archiveExt(rawURL))
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if want := strings.TrimSpace(os.Getenv(spec.envChecksum)); want != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("%s checksum mismatch: got %s", spec.name, got)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	if strings.HasSuffix(rawURL, ".zip") {
		return extractZipExecutable(tmpPath, target, executableName(spec.name))
	}
	return extractTarGzExecutable(tmpPath, target, executableName(spec.name))
}

func archiveExt(rawURL string) string {
	if strings.HasSuffix(rawURL, ".tar.gz") {
		return ".tar.gz"
	}
	return filepath.Ext(rawURL)
}

func extractZipExecutable(archivePath, target, exeName string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if filepath.Base(f.Name) != exeName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeExecutable(target, rc)
		_ = rc.Close()
		return err
	}
	return fmt.Errorf("%s not found in archive", exeName)
}

func extractTarGzExecutable(archivePath, target, exeName string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != exeName {
			continue
		}
		return writeExecutable(target, tr)
	}
	return fmt.Errorf("%s not found in archive", exeName)
}

func writeExecutable(target string, src io.Reader) error {
	tmp := target + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, target)
}
