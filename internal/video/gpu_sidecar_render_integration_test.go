package video

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSidecarGPUFrameRenderIntegration(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")
	if exe == "" {
		t.Skip("set IMAGEPAD_PLAYLIST_COMPOSITORD to run hardware integration")
	}
	p, err := StartSidecar(context.Background(), filepath.Clean(exe), "go-render-test")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Hello(context.Background(), "go-render-test"); err != nil {
		t.Fatal(err)
	}
	for _, size := range []uint32{360, 720} {
		f, err := p.Render(context.Background(), size, size, uint64(size), int64(size)*1000000)
		if err != nil {
			t.Fatal(err)
		}
		if f.Sequence != uint64(size) || f.Width != size || f.PTSNs != int64(size)*1000000 || len(f.Payload) != int(f.RowStride*f.Height) {
			t.Fatalf("unexpected %dx%d frame: %#v", size, size, f)
		}
		if f.RowStride < f.Width*4 || f.RowStride%256 != 0 {
			t.Fatalf("GPU row alignment contract violated: %d", f.RowStride)
		}
		repeat, err := p.Render(context.Background(), size, size, uint64(size), int64(size)*1000000)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(f.Payload, repeat.Payload) {
			t.Fatalf("same %dx%d scene/frame request produced different GPU pixels", size, size)
		}
	}
	transport, err := NewGPUFrameTransport(2)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewGPUSidecarBridge(transport, p)
	if err != nil {
		t.Fatal(err)
	}
	yuv, err := bridge.RenderYUV(context.Background(), 64, 64, 10, 456)
	if err != nil {
		t.Fatal(err)
	}
	if len(yuv) != 64*64+2*32*32 {
		t.Fatalf("unexpected yuv size %d", len(yuv))
	}
	_ = bridge.Close(nil)
}
