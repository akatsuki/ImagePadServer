package video

import (
	"context"
	"fmt"
	"io"
	"os"
)

// gpuPostYUVArtifacts owns all temporary files in the strict post-YUV
// spectrum experiment. Cleanup is idempotent and safe on partial passes.
type gpuPostYUVArtifacts struct {
	videoTS     string
	overlayTS   string
	spectrumRaw string
}

func (a gpuPostYUVArtifacts) cleanup() {
	for _, path := range []string{a.videoTS, a.overlayTS, a.spectrumRaw} {
		if path != "" {
			_ = os.Remove(path)
		}
	}
}

// writeSpectrumRawFrame appends one tightly packed RGBA spectrum frame. It
// enforces the fixed frame size so a partial write cannot desynchronise the
// later FFmpeg rawvideo reader.
func writeSpectrumRawFrame(ctx context.Context, dst io.Writer, payload []byte, width, height int) error {
	want := width * height * 4
	if width <= 0 || height <= 0 || len(payload) != want {
		return fmt.Errorf("invalid spectrum frame: got %d want %d", len(payload), want)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	n, err := dst.Write(payload)
	if err == nil && n != len(payload) {
		err = io.ErrShortWrite
	}
	return err
}
