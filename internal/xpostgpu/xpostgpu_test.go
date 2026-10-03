package xpostgpu

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayerUVRoundTripsAndRejectsInvalidRanges(t *testing.T) {
	var frame Frame
	if err := json.Unmarshal([]byte(`{"layers":[{"assetId":"tile","x":1,"y":2,"width":30,"height":40,"tilt":0,"opacity":1,"uv":[0.2,0.1,0.8,0.9]}]}`), &frame); err != nil {
		t.Fatal(err)
	}
	if err := validateFrame(frame, map[string]int{"tile": 4}); err != nil {
		t.Fatalf("valid crop rejected: %v", err)
	}
	encoded, err := encodeRequest(frame, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"uv":[0.2,0.1,0.8,0.9]`)) {
		t.Fatalf("encoded request lost the UV crop: %s", encoded)
	}
}

func TestValidateFrameRejectsInvalidUVRanges(t *testing.T) {
	for name, uv := range map[string]string{
		"below zero":   `[-0.1,0,1,1]`,
		"above one":    `[0,0,1.1,1]`,
		"empty width":  `[0.5,0,0.5,1]`,
		"empty height": `[0,0.5,1,0.5]`,
	} {
		t.Run(name, func(t *testing.T) {
			var frame Frame
			payload := []byte(`{"layers":[{"assetId":"tile","x":1,"y":2,"width":30,"height":40,"tilt":0,"opacity":1,"uv":` + uv + `}]}`)
			if err := json.Unmarshal(payload, &frame); err != nil {
				t.Fatalf("fixture should decode to exercise frame validation: %v", err)
			}
			if err := validateFrame(frame, map[string]int{"tile": 4}); err == nil {
				t.Fatalf("validateFrame accepted invalid UV range %s", uv)
			}
		})
	}
}

func TestValidateFrameRejectsNonFiniteUVRanges(t *testing.T) {
	for name, invalid := range map[string]float32{"NaN": float32(math.NaN()), "positive infinity": float32(math.Inf(1)), "negative infinity": float32(math.Inf(-1))} {
		t.Run(name, func(t *testing.T) {
			uv := [4]float32{0, 0, invalid, 1}
			frame := Frame{Layers: []Layer{{AssetID: "tile", X: 1, Y: 2, Width: 30, Height: 40, Opacity: 1, UV: &uv}}}
			if err := validateFrame(frame, map[string]int{"tile": 4}); err == nil {
				t.Fatalf("validateFrame accepted non-finite UV range %v", uv)
			}
		})
	}
}

func TestEncodeRequestUsesSidecarUpdateIDWireField(t *testing.T) {
	encoded, err := encodeRequest(Frame{Layers: []Layer{{AssetID: "cover", X: 1, Y: 2, Width: 3, Height: 4, Opacity: 1}}}, &Update{AssetID: "cover", Pixels: []byte{1, 2, 3, 4}})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	update, ok := wire["update"].(map[string]any)
	if !ok || update["updateID"] != "cover" || update["byteLen"] != float64(4) {
		t.Fatalf("unexpected update wire record: %#v", wire["update"])
	}
}

func TestValidateAssetsRejectsOutputBufferAboveProtocolLimit(t *testing.T) {
	if err := validateAssets(16_384, 16_384, nil); err == nil {
		t.Fatal("expected canvas byte limit error")
	}
}

func TestLockedBufferKeepsOnlyTheMostRecent64KiB(t *testing.T) {
	buffer := &lockedBuffer{}
	if _, err := buffer.Write(bytes.Repeat([]byte("a"), 40<<10)); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write(bytes.Repeat([]byte("b"), 24<<10)); err != nil {
		t.Fatal(err)
	}
	if _, err := buffer.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	got := buffer.String()
	if len(got) != 64<<10 || cap(buffer.data) > 64<<10 || !strings.HasSuffix(got, "tail") || got[0] != 'a' {
		t.Fatalf("unexpected retained stderr: length=%d suffix=%q", len(got), got[max(0, len(got)-4):])
	}
}

func TestSidecarGPUFourByFourRGBAFrame(t *testing.T) {
	exe := os.Getenv("XPOST_COMPOSITORD_BIN")
	if exe == "" {
		t.Skip("set XPOST_COMPOSITORD_BIN to run the actual WGPU smoke")
	}
	pixels := make([]byte, 4*4*4)
	for i := 0; i < len(pixels); i += 4 {
		pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 255, 0, 255, 255
	}
	assetPath := filepath.Join(t.TempDir(), "magenta.rgba")
	if err := os.WriteFile(assetPath, pixels, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := Start(context.Background(), exe, 4, 4, []Asset{{ID: "magenta", Path: assetPath, Width: 4, Height: 4}})
	if err != nil {
		message := err.Error()
		if strings.Contains(message, "no WGPU adapter") || strings.Contains(message, "requesting WGPU device") {
			t.Skipf("unsupported GPU/runtime environment: %v", err)
		}
		t.Fatal(err)
	}
	defer client.Close()
	frame, err := client.Render(Frame{Layers: []Layer{{AssetID: "magenta", Width: 4, Height: 4, Opacity: 1}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	center := (2*4 + 2) * 4
	if got := frame[center : center+4]; got[0] != 255 || got[1] != 0 || got[2] != 255 || got[3] != 255 {
		t.Fatalf("unexpected rendered center pixel: %v", got)
	}
	updatedPixels := make([]byte, 4*4*4)
	for i := 0; i < len(updatedPixels); i += 4 {
		updatedPixels[i+1], updatedPixels[i+3] = 255, 255
	}
	updated, err := client.Render(Frame{Layers: []Layer{{AssetID: "magenta", Width: 4, Height: 4, Opacity: 1}}}, &Update{AssetID: "magenta", Pixels: updatedPixels})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated[center : center+4]; got[0] != 0 || got[1] != 255 || got[2] != 0 || got[3] != 255 {
		t.Fatalf("asset update was not visible in rendered frame: %v", got)
	}
	black, err := client.Render(Frame{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := black[0:4]; got[0] != 0 || got[1] != 0 || got[2] != 0 || got[3] != 255 {
		t.Fatalf("empty frame is not opaque black: %v", got)
	}
	emptyClient, err := Start(context.Background(), exe, 4, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer emptyClient.Close()
	empty, err := emptyClient.Render(Frame{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := empty[0:4]; got[0] != 0 || got[1] != 0 || got[2] != 0 || got[3] != 255 {
		t.Fatalf("assetless compositor frame is not opaque black: %v", got)
	}
}

func TestSidecarThemedBackground(t *testing.T) {
	exe := os.Getenv("XPOST_COMPOSITORD_BIN")
	if exe == "" {
		t.Skip("set XPOST_COMPOSITORD_BIN to check the actual theme clear color")
	}
	for _, theme := range []string{"light", "dark"} {
		t.Run(theme, func(t *testing.T) {
			client, err := StartWithTheme(t.Context(), exe, 4, 4, nil, theme)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			pixels, err := client.Render(Frame{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := byte(0)
			if theme == "light" {
				want = 255
			}
			for i := 0; i < len(pixels); i += 4 {
				if pixels[i] != want || pixels[i+1] != want || pixels[i+2] != want || pixels[i+3] != 255 {
					t.Fatalf("%s frame pixel %d: %v", theme, i/4, pixels[i:i+4])
				}
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
