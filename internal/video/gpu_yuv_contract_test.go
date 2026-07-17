package video

import (
	"errors"
	"testing"
)

func validYUV420PFrame() YUV420PFrame {
	return YUV420PFrame{Schema: GPUContractVersion, Width: 4, Height: 2, YStride: 4, UStride: 2, VStride: 2,
		ColorSpace: ColorSRGB, Ownership: "OwnedByTransport", Y: make([]byte, 8), U: make([]byte, 2), V: make([]byte, 2)}
}

func TestYUV420PFrameValidate(t *testing.T) {
	if err := validYUV420PFrame().Validate(); err != nil {
		t.Fatal(err)
	}
	odd := validYUV420PFrame()
	odd.Width, odd.Height = 3, 3
	odd.YStride, odd.UStride, odd.VStride = 3, 2, 2
	odd.Y, odd.U, odd.V = make([]byte, 9), make([]byte, 4), make([]byte, 4)
	if err := odd.Validate(); err != nil {
		t.Fatalf("odd edge-replicated dimensions rejected: %v", err)
	}
	bad := validYUV420PFrame()
	bad = validYUV420PFrame()
	bad.U = bad.U[:1]
	if err := bad.Validate(); err == nil {
		t.Fatal("short chroma plane accepted")
	}
	bad = validYUV420PFrame()
	bad.Ownership = "OwnedByCaller"
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown ownership accepted")
	}
}

func TestGPUOutputCapabilitiesFailClosed(t *testing.T) {
	if err := RequireGPUYUV420Output(nil); !errors.Is(err, ErrGPUYUVUnsupported) {
		t.Fatalf("nil capabilities: %v", err)
	}
	c := GPUOutputCapabilities{Schema: GPUContractVersion, Formats: []GPUOutputFormat{GPUOutputRGBA8}}
	if err := RequireGPUYUV420Output(&c); !errors.Is(err, ErrGPUYUVUnsupported) {
		t.Fatalf("rgba-only capabilities: %v", err)
	}
	c.Formats = []GPUOutputFormat{GPUOutputYUV420P}
	if err := RequireGPUYUV420Output(&c); err != nil {
		t.Fatalf("yuv capability rejected: %v", err)
	}
}
