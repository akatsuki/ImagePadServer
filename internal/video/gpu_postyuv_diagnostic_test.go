package video

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

func TestWriteSpectrumRawFrame(t *testing.T) {
	payload := bytes.Repeat([]byte{7}, 2*2*4)
	var out bytes.Buffer
	if err := writeSpectrumRawFrame(context.Background(), &out, payload, 2, 2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatal("raw payload changed")
	}
}

func TestWriteSpectrumRawFrameRejectsAndCancels(t *testing.T) {
	if err := writeSpectrumRawFrame(context.Background(), ioDiscard{}, []byte{1}, 2, 2); err == nil {
		t.Fatal("expected size error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeSpectrumRawFrame(ctx, ioDiscard{}, make([]byte, 16), 2, 2); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
}

func TestWriteSpectrumRawFramesCapsAndHashes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spectrum.rgba")
	frames := [][]byte{bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 16), bytes.Repeat([]byte{3}, 16)}
	r, err := writeSpectrumRawFrames(context.Background(), path, frames, 2, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Frames != 2 || r.SHA256 == "" {
		t.Fatalf("receipt=%+v", r)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
