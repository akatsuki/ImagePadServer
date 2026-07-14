package obsrtmp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGPUFallbackFramesUseSidecar(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if exe == "" {
		t.Skip("set IMAGEPAD_PLAYLIST_COMPOSITORD for hardware integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	frames, err := writeGPURadioFallbackFrames(ctx, io.Discard, filepath.Clean(exe), 64, 64)
	if err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	if frames == 0 {
		t.Fatal("GPU fallback produced no frames")
	}
}
