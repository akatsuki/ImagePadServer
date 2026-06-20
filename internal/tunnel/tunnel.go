package tunnel

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/settings"
)

type Status struct {
	OK      bool   `json:"ok"`
	URL     string `json:"url,omitempty"`
	Message string `json:"message"`
}

type Tunnel struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan error
}

const (
	cloudflaredReleaseVersion           = "2026.5.0"
	cloudflaredWindowsDownloadURL       = "https://github.com/cloudflare/cloudflared/releases/download/2026.5.0/cloudflared-windows-amd64.exe"
	cloudflaredWindowsSHA256            = "f141cded099c239171ad2cea6fb5da0fdaa2bd36104c3074d883f9546519eba7"
	cloudflaredDarwinArm64SHA256        = "116ef11a59fc4f31e7f1bcc4378070cd7ca053fa37b4484b1432bb150b358219"
	cloudflaredDarwinAMD64SHA256        = "7f2c4c8c86e787226804694112682aefacd4cfb98f54508f1a5a841a78bbbef9"
	cloudflaredDownloadMaxBytes   int64 = 100 << 20
)

var tryCloudflareURL = regexp.MustCompile(`https://[-a-zA-Z0-9]+\.trycloudflare\.com`)

func Start(originURL string) (*Tunnel, Status) {
	exe, err := ensureCloudflared()
	if err != nil {
		return nil, Status{Message: err.Error()}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, exe, "tunnel", "--no-autoupdate", "--url", originURL)
	hideWindow(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, Status{Message: err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, Status{Message: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, Status{Message: err.Error()}
	}

	lines := make(chan string, 64)
	var wg sync.WaitGroup
	wg.Add(2)
	go scanPipe(stdout, lines, &wg)
	go scanPipe(stderr, lines, &wg)
	go func() {
		wg.Wait()
		close(lines)
	}()

	deadline := time.After(20 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				cancel()
				_ = cmd.Wait()
				return nil, Status{Message: "cloudflared exited before tunnel URL was issued"}
			}
			if match := tryCloudflareURL.FindString(line); match != "" {
				go drainLines(lines)
				done := make(chan error, 1)
				go func() {
					done <- cmd.Wait()
					close(done)
				}()
				return &Tunnel{cmd: cmd, cancel: cancel, done: done}, Status{OK: true, URL: match + "/", Message: "Cloudflare Tunnel connected"}
			}
		case <-deadline:
			cancel()
			_ = cmd.Wait()
			return nil, Status{Message: "timed out waiting for Cloudflare Tunnel URL"}
		}
	}
}

func (t *Tunnel) Stop() {
	if t == nil {
		return
	}
	t.cancel()
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	if t.done != nil {
		<-t.done
	}
}

func (t *Tunnel) Wait() error {
	if t == nil || t.done == nil {
		return nil
	}
	return <-t.done
}

func (t *Tunnel) Done() <-chan error {
	if t == nil || t.done == nil {
		ch := make(chan error)
		close(ch)
		return ch
	}
	return t.done
}

func (t *Tunnel) IsRunning() bool {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return false
	}
	return t.cmd.ProcessState == nil
}

func scanPipe(r io.Reader, lines chan<- string, wg *sync.WaitGroup) {
	defer wg.Done()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		lines <- scanner.Text()
	}
}

func drainLines(lines <-chan string) {
	for range lines {
	}
}

func ensureCloudflared() (string, error) {
	if exe, err := exec.LookPath("cloudflared"); err == nil {
		return exe, nil
	}
	if local := localCloudflaredPath(); fileExists(local) {
		return local, nil
	}
	if runtime.GOOS != "windows" {
		if runtime.GOOS == "darwin" {
			exe := localCloudflaredPath()
			if err := os.MkdirAll(filepath.Dir(exe), 0755); err != nil {
				return "", err
			}
			if err := downloadCloudflared(exe); err != nil {
				return "", err
			}
			return exe, nil
		}
		return "", fmt.Errorf("cloudflared was not found. %s", installHint("cloudflared"))
	}

	exe := localCloudflaredPath()
	binDir := filepath.Dir(exe)
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", err
	}
	if err := downloadCloudflared(exe); err != nil {
		return "", err
	}
	return exe, nil
}

func localCloudflaredPath() string {
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(settings.Dir(), "bin", name)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func installHint(name string) string {
	switch runtime.GOOS {
	case "darwin":
		return fmt.Sprintf("Install it with Homebrew (`brew install %s`) or add it to PATH.", name)
	case "linux":
		return fmt.Sprintf("Install %s with your package manager or add it to PATH.", name)
	default:
		return fmt.Sprintf("Add %s to PATH.", name)
	}
}

func downloadCloudflared(path string) error {
	if runtime.GOOS == "darwin" {
		return downloadDarwinCloudflared(path)
	}

	checksum := strings.TrimSpace(os.Getenv("IMAGEPAD_CLOUDFLARED_SHA256"))
	if checksum == "" {
		checksum = cloudflaredWindowsSHA256
	}
	client := http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(cloudflaredWindowsDownloadURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudflared download failed: %s", resp.Status)
	}

	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	written, err := io.Copy(file, io.LimitReader(resp.Body, 100<<20))
	if err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if written == 0 {
		_ = file.Close()
		_ = os.Remove(tmp)
		return errors.New("cloudflared download returned empty file")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := verifySHA256(tmp, checksum); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func downloadDarwinCloudflared(path string) error {
	rawURL, checksum, err := darwinCloudflaredDownload()
	if err != nil {
		return err
	}
	if override := strings.TrimSpace(os.Getenv("IMAGEPAD_CLOUDFLARED_SHA256")); override != "" {
		checksum = override
	}
	archivePath := path + ".tgz"
	if err := downloadFile(archivePath, rawURL, cloudflaredDownloadMaxBytes, checksum); err != nil {
		return err
	}
	defer os.Remove(archivePath)
	if err := extractTarGzBinary(archivePath, path, "cloudflared"); err != nil {
		return err
	}
	return validateExecutable(path, "--version")
}

func darwinCloudflaredDownload() (string, string, error) {
	switch runtime.GOARCH {
	case "arm64":
		return fmt.Sprintf("https://github.com/cloudflare/cloudflared/releases/download/%s/cloudflared-darwin-arm64.tgz", cloudflaredReleaseVersion), cloudflaredDarwinArm64SHA256, nil
	case "amd64":
		return fmt.Sprintf("https://github.com/cloudflare/cloudflared/releases/download/%s/cloudflared-darwin-amd64.tgz", cloudflaredReleaseVersion), cloudflaredDarwinAMD64SHA256, nil
	default:
		return "", "", fmt.Errorf("automatic cloudflared install is not available for darwin/%s", runtime.GOARCH)
	}
}

func downloadFile(path, rawURL string, maxBytes int64, expectedSHA256 string) error {
	expectedSHA256 = strings.TrimSpace(expectedSHA256)
	if expectedSHA256 == "" {
		return errors.New("automatic cloudflared download is disabled until a trusted SHA256 checksum is configured")
	}
	client := http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudflared download failed: %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return errors.New("cloudflared download exceeds size limit")
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	written, err := io.Copy(file, io.LimitReader(resp.Body, maxBytes+1))
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if written == 0 || written > maxBytes {
		_ = os.Remove(tmp)
		return errors.New("cloudflared download has invalid size")
	}
	if err := verifySHA256(tmp, expectedSHA256); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

func extractTarGzBinary(archivePath, targetPath, binaryName string) error {
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
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.FileInfo().IsDir() || filepath.Base(header.Name) != binaryName {
			continue
		}
		return writeExecutable(targetPath, reader)
	}
	return fmt.Errorf("%s was not found in archive", binaryName)
}

func writeExecutable(path string, r io.Reader) error {
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, r)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	if err := os.Chmod(tmp, 0755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}

func validateExecutable(path string, args ...string) error {
	cmd := exec.Command(path, args...)
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("installed executable validation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func verifySHA256(path, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return errors.New("expected SHA256 checksum is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(data)
	actual := hex.EncodeToString(hash[:])
	if actual != expected {
		return fmt.Errorf("download checksum mismatch: want %s, got %s", expected, actual)
	}
	return nil
}
