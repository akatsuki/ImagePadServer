//go:build nico_sprite_spike

package nicorender

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const spikeMaxTextureBytes = 128 << 20

// decodeSpikeTexture expands a probe texture into the RGBA bytes expected by
// the native compositor. All packed forms are deliberately lossless.
func decodeSpikeTexture(tex spikeTexture) ([]byte, error) {
	if tex.Width == 0 || tex.Height == 0 || tex.Width > 16384 || tex.Height > 16384 {
		return nil, fmt.Errorf("invalid sprite texture dimensions %dx%d", tex.Width, tex.Height)
	}
	pixels := uint64(tex.Width) * uint64(tex.Height)
	rawLen := pixels * 4
	if rawLen > spikeMaxTextureBytes {
		return nil, fmt.Errorf("sprite texture exceeds %d bytes", spikeMaxTextureBytes)
	}
	decodeExact := func(value string, want uint64, name string) ([]byte, error) {
		if strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("invalid %s base64: newline", name)
		}
		if uint64(len(value)) != ((want+2)/3)*4 {
			return nil, fmt.Errorf("invalid %s base64 length %d", name, len(value))
		}
		data, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("invalid %s base64: %w", name, err)
		}
		if uint64(len(data)) != want {
			return nil, fmt.Errorf("invalid %s length %d, want %d", name, len(data), want)
		}
		return data, nil
	}
	if tex.Encoding == "" || tex.Encoding == "rgba" {
		if tex.Palette != "" {
			return nil, fmt.Errorf("palette supplied for %s texture", tex.Encoding)
		}
		return decodeExact(tex.Data, rawLen, "rgba data")
	}
	if tex.Encoding == "grayalpha8" {
		if tex.Palette != "" {
			return nil, fmt.Errorf("palette supplied for grayalpha8 texture")
		}
		data, err := decodeExact(tex.Data, pixels*2, "grayalpha8 data")
		if err != nil {
			return nil, err
		}
		out := make([]byte, rawLen)
		for i, j := uint64(0), uint64(0); i < pixels; i, j = i+1, j+2 {
			gray, alpha := data[j], data[j+1]
			o := i * 4
			out[o], out[o+1], out[o+2], out[o+3] = gray, gray, gray, alpha
		}
		return out, nil
	}
	if tex.Encoding != "palette8" {
		return nil, fmt.Errorf("unknown sprite texture encoding %q", tex.Encoding)
	}
	if strings.ContainsAny(tex.Palette, "\r\n") || len(tex.Palette) > 1368 {
		return nil, fmt.Errorf("invalid palette base64 length")
	}
	palette, err := base64.StdEncoding.DecodeString(tex.Palette)
	if err != nil {
		return nil, fmt.Errorf("invalid palette base64: %w", err)
	}
	if len(palette) == 0 || len(palette)%4 != 0 || len(palette) > 256*4 {
		return nil, fmt.Errorf("invalid palette length %d", len(palette))
	}
	indexes, err := decodeExact(tex.Data, pixels, "palette8 indexes")
	if err != nil {
		return nil, err
	}
	out := make([]byte, rawLen)
	for i, index := range indexes {
		if int(index)*4 >= len(palette) {
			return nil, fmt.Errorf("palette index %d out of range", index)
		}
		copy(out[i*4:], palette[int(index)*4:int(index)*4+4])
	}
	return out, nil
}
