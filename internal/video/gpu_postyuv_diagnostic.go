package video

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

type gpuPostYUVRawReceipt struct {
	Frames int
	SHA256 string
}

func writeSpectrumRawFrames(ctx context.Context, path string, frames [][]byte, width, height, maxFrames int) (gpuPostYUVRawReceipt, error) {
	if maxFrames <= 0 || maxFrames > len(frames) {
		maxFrames = len(frames)
	}
	f, err := os.Create(path)
	if err != nil {
		return gpuPostYUVRawReceipt{}, err
	}
	defer f.Close()
	h := sha256.New()
	for i := 0; i < maxFrames; i++ {
		if err := writeSpectrumRawFrame(ctx, io.MultiWriter(f, h), frames[i], width, height); err != nil {
			return gpuPostYUVRawReceipt{}, err
		}
	}
	return gpuPostYUVRawReceipt{Frames: maxFrames, SHA256: fmt.Sprintf("%x", h.Sum(nil))}, nil
}

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
