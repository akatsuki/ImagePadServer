package video

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testGPUFrame() GpuFrame {
	return GpuFrame{Schema: 1, Sequence: 1, Width: 2, Height: 2, RowStride: 256, Format: PixelRGBA8, ColorSpace: ColorSRGB, Ownership: "OwnedByTransport", Payload: make([]byte, 512)}
}
func TestGPUFrameTransportBoundedAndOrdered(t *testing.T) {
	tr, _ := NewGPUFrameTransport(1)
	ctx := context.Background()
	if err := tr.Submit(ctx, testGPUFrame()); err != nil {
		t.Fatal(err)
	}
	if tr.Len() != 1 {
		t.Fatal(tr.Len())
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := tr.Submit(c, testGPUFrame()); err == nil {
		t.Fatal("expected backpressure")
	}
	f, err := tr.Receive(ctx)
	if err != nil || f.Sequence != 1 {
		t.Fatalf("%v %#v", err, f)
	}
}
func TestGPUFrameTransportCloseUnblocks(t *testing.T) {
	tr, _ := NewGPUFrameTransport(1)
	done := make(chan error)
	go func() { _, e := tr.Receive(context.Background()); done <- e }()
	tr.Close(errors.New("sidecar died"))
	if e := <-done; e == nil || e.Error() != "sidecar died" {
		t.Fatal(e)
	}
}
func TestRGBA8ToYUV420P(t *testing.T) {
	f := testGPUFrame()
	f.Payload[0], f.Payload[1], f.Payload[2] = 255, 0, 0
	y, err := RGBA8ToYUV420P(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(y) != 6 {
		t.Fatal(len(y))
	}
	if y[0] < 60 {
		t.Fatalf("unexpected luma %d", y[0])
	}
}
