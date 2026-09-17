package nicorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const nativeCompositorABI = "NICO_COMPOSITOR 1 NPS3 WARP"

// PrepareNativeCompositor returns a job-owned executable and cleanup function.
// Failure before encoding is safe to fall back from; explicit native callers
// must still surface the error. No download or compiler runs on the user's PC.
func PrepareNativeCompositor(ctx context.Context, configured string) (string, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return "", nil, fmt.Errorf("%w: native compositor requires Windows amd64", ErrUnavailable)
	}
	path := strings.TrimSpace(configured)
	cleanup := func() {}
	var err error
	if path == "" {
		payload, meta := nativePayload()
		if len(payload) == 0 {
			return "", nil, fmt.Errorf("%w: native compositor is not embedded", ErrUnavailable)
		}
		path, cleanup, err = materializeNativePayload(payload, meta)
		if err != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	} else {
		path, err = filepath.Abs(path)
		if err != nil {
			return "", nil, err
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, path, "--self-test")
	hideNativeWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	var output nativeProbeBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err = cmd.Run(); err != nil || strings.TrimSpace(output.String()) != nativeCompositorABI {
		cleanup()
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		return "", nil, fmt.Errorf("%w: native self-test: %v (%s)", ErrUnavailable, err, strings.TrimSpace(output.String()))
	}
	return path, cleanup, nil
}

type nativeProbeBuffer struct{ bytes.Buffer }

func (b *nativeProbeBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := 4096 - b.Len()
	if left > 0 {
		_, _ = b.Buffer.Write(p[:min(left, n)])
	}
	return n, nil
}

func materializeNativePayload(payload, metadata []byte) (string, func(), error) {
	var meta struct {
		Schema                         int
		Protocol, Architecture, SHA256 string
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return "", nil, err
	}
	if meta.Schema != 1 || meta.Protocol != "NPS3" || meta.Architecture != "amd64" || !strings.EqualFold(meta.SHA256, fmt.Sprintf("%x", sha256.Sum256(payload))) {
		return "", nil, fmt.Errorf("native payload manifest/hash mismatch")
	}
	executable, err := pe.NewFile(bytes.NewReader(payload))
	if err != nil {
		return "", nil, fmt.Errorf("native payload PE: %w", err)
	}
	machine := executable.Machine
	_ = executable.Close()
	if machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		return "", nil, fmt.Errorf("native payload architecture mismatch")
	}
	dir, err := os.MkdirTemp("", "imagepad-nico-compositor-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("niconico: temporary compositor cleanup: %v", err)
		}
	}
	temp := filepath.Join(dir, "nico-compositor.tmp")
	path := filepath.Join(dir, "nico-compositor.exe")
	if err = os.WriteFile(temp, payload, 0700); err != nil {
		cleanup()
		return "", nil, err
	}
	staged, err := os.ReadFile(temp)
	if err != nil || sha256.Sum256(staged) != sha256.Sum256(payload) {
		cleanup()
		return "", nil, fmt.Errorf("native payload write verification failed: %v", err)
	}
	if err = os.Rename(temp, path); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
