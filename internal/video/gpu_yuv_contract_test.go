package video

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestSidecarYUVFrameResponseDecodesAndValidates(t *testing.T) {
	f := validYUV420PFrame()
	b, err := json.Marshal(struct {
		Type  string       `json:"type"`
		Frame YUV420PFrame `json:"frame"`
	}{"yuv_frame", f})
	if err != nil {
		t.Fatal(err)
	}
	var got sidecarResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.YuvFrame == nil || got.Frame != nil {
		t.Fatalf("unexpected response decode: %+v", got)
	}
	if err := got.YuvFrame.Validate(); err != nil {
		t.Fatalf("decoded frame invalid: %v", err)
	}

	bad := f
	bad.Y = bad.Y[:len(bad.Y)-1]
	b, _ = json.Marshal(struct {
		Type  string       `json:"type"`
		Frame YUV420PFrame `json:"frame"`
	}{"yuv_frame", bad})
	if err := json.Unmarshal(b, &got); err == nil {
		t.Fatal("expected malformed YUV response to fail closed")
	}
}

func TestSidecarLegacyFrameResponseStillDecodes(t *testing.T) {
	f := GpuFrame{Schema: GPUContractVersion, Sequence: 1, PTSNs: 0, Width: 1, Height: 1, RowStride: 256, Format: PixelRGBA8, ColorSpace: ColorSRGB, Alpha: true, Ownership: "OwnedByTransport", Payload: []byte{0, 0, 0, 255}}
	b, _ := json.Marshal(struct {
		Type  string   `json:"type"`
		Frame GpuFrame `json:"frame"`
	}{"frame", f})
	var got sidecarResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Frame == nil || got.YuvFrame != nil {
		t.Fatalf("legacy frame changed: %+v", got)
	}
}

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

func TestYUV420PFramePackedBytesCopiesOnlyPlaneRows(t *testing.T) {
	f := YUV420PFrame{Schema: GPUContractVersion, Width: 3, Height: 3, YStride: 4, UStride: 3, VStride: 3, ColorSpace: ColorSRGB, Ownership: "OwnedByTransport", Y: []byte{1, 2, 3, 99, 4, 5, 6, 99, 7, 8, 9, 99}, U: []byte{10, 11, 88, 12, 13, 88}, V: []byte{20, 21, 77, 22, 23, 77}}
	got, err := f.PackedBytes()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 20, 21, 22, 23}
	if !bytes.Equal(got, want) {
		t.Fatalf("packed=%v want=%v", got, want)
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

func TestGPUH264BitstreamContractFailClosed(t *testing.T) {
	if err := RequireGPUH264Output(nil); !errors.Is(err, ErrGPUH264Unsupported) {
		t.Fatalf("nil capabilities: %v", err)
	}
	c := GPUOutputCapabilities{Schema: GPUContractVersion, Formats: []GPUOutputFormat{GPUOutputYUV420P}}
	if err := RequireGPUH264Output(&c); !errors.Is(err, ErrGPUH264Unsupported) {
		t.Fatalf("non-H264 capabilities: %v", err)
	}
	c.Formats = []GPUOutputFormat{GPUOutputH264Bitstream}
	if err := RequireGPUH264Output(&c); err != nil {
		t.Fatalf("H264 capability rejected: %v", err)
	}

	frame := EncodedH264Frame{
		Schema:             GPUContractVersion,
		Sequence:           1,
		Width:              640,
		Height:             360,
		Codec:              "h264",
		Profile:            "High",
		Backend:            "dx12",
		PixelReadbackBytes: 0,
		AssetCacheReady:    true,
		AssetReceipt:       H264AssetReceipt{ArtworkHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", GlyphHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		Payload:            []byte{0, 0, 0, 1, 9},
	}
	if err := frame.Validate(); err != nil {
		t.Fatalf("valid encoded frame rejected: %v", err)
	}
	bad := frame
	bad.PixelReadbackBytes = 4
	if err := bad.Validate(); err == nil {
		t.Fatal("encoded frame with pixel readback accepted")
	}
	bad = frame
	bad.Backend = "vulkan"
	if err := bad.Validate(); err == nil {
		t.Fatal("non-DX12 encoded frame accepted")
	}
	bad = frame
	bad.Payload = nil
	if err := bad.Validate(); err == nil {
		t.Fatal("empty encoded payload accepted")
	}
	bad = frame
	bad.AssetCacheReady = false
	if err := bad.Validate(); err == nil {
		t.Fatal("encoded frame without asset acknowledgement accepted")
	}
}
