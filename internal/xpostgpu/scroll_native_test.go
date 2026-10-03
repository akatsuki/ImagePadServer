package xpostgpu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSidecarUVScrollSamplesOnlySelectedBand(t *testing.T) {
	exe := os.Getenv("XPOST_COMPOSITORD_BIN")
	if exe == "" {
		t.Skip("set XPOST_COMPOSITORD_BIN for native UV pixel verification")
	}
	pixels := make([]byte, 8*32*4)
	for y := 0; y < 32; y++ {
		for x := 0; x < 8; x++ {
			i := (y*8 + x) * 4
			pixels[i+3] = 255
			pixels[i+y/8] = 255
		}
	}
	path := filepath.Join(t.TempDir(), "bands.rgba")
	if err := os.WriteFile(path, pixels, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Start(t.Context(), exe, 8, 8, []Asset{{ID: "bands", Path: path, Width: 8, Height: 32}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Stay inside the blue band so linear filtering cannot blend its neighbours.
	frame, err := c.Render(Frame{Layers: []Layer{{AssetID: "bands", Width: 8, Height: 8, Opacity: 1, UV: &[4]float32{0, 17.0 / 32, 1, 23.0 / 32}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(frame); i += 4 {
		if frame[i] != 0 || frame[i+1] != 0 || frame[i+2] != 255 || frame[i+3] != 255 {
			t.Fatalf("UV crop sampled outside blue band: pixel %d %v", i/4, frame[i:i+4])
		}
	}
}
