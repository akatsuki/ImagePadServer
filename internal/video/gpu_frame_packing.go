package video

import "fmt"

// GPUFrameToPackedRGBA removes GPU row padding for ProgramCompositor's packed
// RGBA contract. Color order conversion is deliberately rejected here; the
// sidecar's initial program contract is RGBA8/sRGB only.
func GPUFrameToPackedRGBA(frame GpuFrame) ([]byte, error) {
	if err := frame.Validate(); err != nil { return nil, err }
	if frame.Format != PixelRGBA8 || frame.ColorSpace != ColorSRGB { return nil, fmt.Errorf("unsupported program GPU frame format") }
	packed := make([]byte, int(frame.Width)*int(frame.Height)*4)
	row := int(frame.Width) * 4
	for y := 0; y < int(frame.Height); y++ { copy(packed[y*row:(y+1)*row], frame.Payload[y*int(frame.RowStride):y*int(frame.RowStride)+row]) }
	return packed, nil
}
