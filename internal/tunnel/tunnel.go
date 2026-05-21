package tunnel

import (
	"bufio"
	"context"
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
}

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
				go func() {
					_ = cmd.Wait()
				}()
				return &Tunnel{cmd: cmd, cancel: cancel}, Status{OK: true, URL: match + "/", Message: "Cloudflare Tunnel connected"}
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
		_ = t.cmd.Wait()
	}
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
	if runtime.GOOS != "windows" {
		return "", errors.New("cloudflared was not found in PATH")
	}

	binDir := filepath.Join(settings.Dir(), "bin")
	exe := filepath.Join(binDir, "cloudflared.exe")
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", err
	}
	if err := downloadCloudflared(exe); err != nil {
		return "", err
	}
	return exe, nil
}

func downloadCloudflared(path string) error {
	const downloadURL = "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-windows-amd64.exe"
	client := http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(downloadURL)
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
	if _, err := io.Copy(file, resp.Body); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func ImageURL(base, path, id string) string {
	base = strings.TrimRight(base, "/") + "/"
	if id == "" {
		return base + path
	}
	return base + path + "?v=" + id
}
