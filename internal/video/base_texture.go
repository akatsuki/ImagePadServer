package video

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
)

// NewBaseTextureMetadata converts a canonical RGBA image into the bounded
// scene payload used by the GPU base-texture path. The source image is copied
// row-by-row so its native stride or backing storage cannot leak into the
// transport contract.
func NewBaseTextureMetadata(id string, src *image.RGBA, colorSpace ColorSpace) (BaseTextureMetadata, error) {
	if src == nil || src.Bounds().Dx() <= 0 || src.Bounds().Dy() <= 0 {
		return BaseTextureMetadata{}, fmt.Errorf("invalid base image")
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	stride := int(((uint32(w*4) + GPURowAlignment - 1) / GPURowAlignment) * GPURowAlignment)
	payload := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		copy(payload[y*stride:y*stride+w*4], src.Pix[y*src.Stride:y*src.Stride+w*4])
	}
	sum := sha256.Sum256(payload)
	b := BaseTextureMetadata{TextureID: id, Width: uint32(w), Height: uint32(h), RowStride: uint32(stride), Format: PixelRGBA8, ColorSpace: colorSpace, Payload: payload, AssetHash: hex.EncodeToString(sum[:])}
	if err := b.Validate(); err != nil {
		return BaseTextureMetadata{}, err
	}
	return b, nil
}
