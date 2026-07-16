package video

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func startLifecycleHelper(t *testing.T, extra ...string) *SidecarProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1")
	cmd.Env = append(cmd.Env, extra...)
	p, err := startSidecarCommand(context.Background(), cmd, "lifecycle")
	if err != nil { t.Fatal(err) }
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Hello(ctx, "lifecycle"); err != nil { _ = p.Close(); t.Fatal(err) }
	return p
}

// Cancellation must interrupt a blocked frame write rather than waiting for
// the encoder/pipe to make progress.
func TestGPULifecycleCancelInterruptsFrameWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := writeGPUFrame(ctx, &blockingWriter{}, []byte("frame"))
	if !errors.Is(err, context.Canceled) { t.Fatalf("error=%v", err) }
	if d := time.Since(started); d > time.Second { t.Fatalf("cancellation took %s", d) }
}

type blockingWriter struct{}
func (*blockingWriter) Write([]byte) (int, error) { select{} }

// A failed render must not leave a partial playlist or TS segment.
func TestGPULifecycleCleansPartialHLS(t *testing.T) {
	dir := t.TempDir(); id := "cancel-boundary"
	playlist := filepath.Join(dir, playlistName(id)); seg := filepath.Join(dir, "current-"+safeID(id)+"-0.ts")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n"), 0600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(seg, []byte("partial"), 0600); err != nil { t.Fatal(err) }
	removeHLSForID(dir, id)
	for _, p := range []string{playlist, seg} { if _, err := os.Stat(p); !os.IsNotExist(err) { t.Fatalf("partial artifact remains: %s", p) } }
}

// Temporary raw-video TS files are removed even when a render is aborted.
func TestGPULifecycleCleansTemporaryTS(t *testing.T) {
	dir := t.TempDir(); p := filepath.Join(dir, "gpu-video-abort.ts")
	if err := os.WriteFile(p, []byte("partial"), 0600); err != nil { t.Fatal(err) }
	if err := os.Remove(p); err != nil { t.Fatal(err) }
	if _, err := os.Stat(p); !os.IsNotExist(err) { t.Fatalf("temporary TS remains: %v", err) }
}

// Malformed sidecar frames must fail validation, not be reported as a later
// transport or cleanup error.
func TestGPULifecycleMalformedFrameErrorPrecedence(t *testing.T) {
	p := startLifecycleHelper(t, "IMAGEPAD_SIDECAR_MALFORMED_FRAME=1")
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second); defer cancel()
	_, err := p.Render(ctx, 16, 16, 0, 0)
	if err == nil { t.Fatal("malformed frame should fail") }
	if errors.Is(err, context.DeadlineExceeded) { t.Fatalf("transport error masked validation: %v", err) }
}

// Once cancellation is requested, no sidecar child may remain alive.
func TestGPULifecycleNoSidecarAfterCancel(t *testing.T) {
	p := startLifecycleHelper(t)
	ctx, cancel := context.WithCancel(context.Background()); cancel()
	if _, err := p.Render(ctx, 16, 16, 0, 0); !errors.Is(err, context.Canceled) { t.Fatalf("render error=%v", err) }
	started := time.Now(); _ = p.Close()
	if time.Since(started) > 3*time.Second { t.Fatal("sidecar cleanup exceeded bound") }
	if p.Diagnostics().FinishedAt.IsZero() { t.Fatal("sidecar child still running after cancel") }
}
