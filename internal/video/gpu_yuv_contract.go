package video

import (
	"errors"
	"fmt"
)

// GPUOutputFormat names a sidecar output format. RGBA remains the only
// production format until the YUV compute path is implemented and negotiated.
type GPUOutputFormat string

const (
	GPUOutputRGBA8   GPUOutputFormat = "rgba8"
	GPUOutputYUV420P GPUOutputFormat = "yuv420p"
)

// GPUOutputCapabilities is an additive handshake description. Unknown or
// absent formats must be treated as unsupported by callers (fail closed).
type GPUOutputCapabilities struct {
	Schema       uint16            `json:"schema"`
	Formats      []GPUOutputFormat `json:"formats"`
	MaxWidth     uint32            `json:"max_width"`
	MaxHeight    uint32            `json:"max_height"`
	RowAlignment uint32            `json:"row_alignment"`
}

var ErrGPUYUVUnsupported = errors.New("gpu yuv420p output unsupported")

// RequireGPUYUV420Output validates an advertised output set before a
// production GPU-only render is selected. Missing capabilities and unknown
// formats fail closed; callers must not substitute RGBA8ToYUV420P.
func RequireGPUYUV420Output(c *GPUOutputCapabilities) error {
	if c == nil {
		return ErrGPUYUVUnsupported
	}
	for _, format := range c.Formats {
		if format == GPUOutputYUV420P {
			return nil
		}
	}
	return ErrGPUYUVUnsupported
}

// YUV420PFrame is the bounded, CPU-readable transport shape for a future
// compute-produced output. Planes are separate to make strides explicit and
// prevent accidental RGBA/YUV mixing.
type YUV420PFrame struct {
	Schema     uint16     `json:"schema"`
	Sequence   uint64     `json:"sequence"`
	PTSNs      int64      `json:"pts_ns"`
	Width      uint32     `json:"width"`
	Height     uint32     `json:"height"`
	YStride    uint32     `json:"y_stride"`
	UStride    uint32     `json:"u_stride"`
	VStride    uint32     `json:"v_stride"`
	ColorSpace ColorSpace `json:"color_space"`
	Ownership  string     `json:"ownership"`
	Y          []byte     `json:"y"`
	U          []byte     `json:"u"`
	V          []byte     `json:"v"`
}

func (f YUV420PFrame) Validate() error {
	if f.Schema != GPUContractVersion || f.Width == 0 || f.Height == 0 || f.Width > GPUMaxDimension || f.Height > GPUMaxDimension {
		return errors.New("invalid yuv420p dimensions")
	}
	// Chroma planes use ceil subsampling; the final luma row/column is edge
	// replicated for odd dimensions.
	cw, ch := (f.Width+1)/2, (f.Height+1)/2
	if f.YStride < f.Width || f.UStride < cw || f.VStride < cw {
		return errors.New("invalid yuv420p stride")
	}
	yBytes := uint64(f.YStride) * uint64(f.Height)
	uBytes := uint64(f.UStride) * uint64(ch)
	vBytes := uint64(f.VStride) * uint64(ch)
	if yBytes > GPUMaxPayload || uBytes > GPUMaxPayload || vBytes > GPUMaxPayload || yBytes+uBytes+vBytes > GPUMaxPayload {
		return errors.New("yuv420p payload too large")
	}
	if uint64(len(f.Y)) != yBytes || uint64(len(f.U)) != uBytes || uint64(len(f.V)) != vBytes {
		return fmt.Errorf("invalid yuv420p plane payload lengths")
	}
	if f.Ownership != "OwnedByTransport" {
		return errors.New("invalid yuv420p ownership")
	}
	return nil
}
