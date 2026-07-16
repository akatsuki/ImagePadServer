package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// RenderASSOverlayRGBA rasterizes an already-resolved production ASS script
// through the same libass filter used by the video encoder. The input is a
// transparent canvas, so the returned bytes are suitable for a premultiplied
// screen-space GPU texture once the alpha convention is verified by the
// acceptance probe.
func RenderASSOverlayRGBA(ctx context.Context, ffmpeg, assPath, fontDir string, width, height uint32) ([]byte, uint32, error) {
	if ffmpeg == "" || assPath == "" || width == 0 || height == 0 {
		return nil, 0, fmt.Errorf("invalid ASS overlay render arguments")
	}
	stride := (width*4 + 255) &^ 255
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=black@0.0:s=" + fmt.Sprintf("%dx%d", width, height) + ":r=1", "-vf", "ass=filename='" + escapeFilterPath(assPath) + "':fontsdir='" + escapeFilterPath(fontDir) + "',format=rgba", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1"}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("ffmpeg ASS overlay: %w\n%s", err, stderr.String())
	}
	rowBytes := width * 4
	if uint64(len(out)) != uint64(rowBytes)*uint64(height) {
		return nil, 0, fmt.Errorf("unexpected ASS overlay bytes: got %d want %d", len(out), uint64(rowBytes)*uint64(height))
	}
	// libass emits straight-alpha RGBA. Convert once at the transport boundary
	// so the GPU can use the contract's premultiplied source-over equation.
	for i := 0; i+3 < len(out); i += 4 {
		a := uint32(out[i+3])
		out[i] = byte((uint32(out[i])*a + 127) / 255)
		out[i+1] = byte((uint32(out[i+1])*a + 127) / 255)
		out[i+2] = byte((uint32(out[i+2])*a + 127) / 255)
	}
	// The transport contract requires aligned rows. Expand each row without
	// changing the libass-generated texels.
	if stride != rowBytes {
		aligned := make([]byte, uint64(stride)*uint64(height))
		for y := uint32(0); y < height; y++ {
			copy(aligned[uint64(y)*uint64(stride):], out[uint64(y)*uint64(rowBytes):uint64(y+1)*uint64(rowBytes)])
		}
		out = aligned
	}
	return out, stride, nil
}

// RenderCanonicalASSOverlay resolves the same fonts and libass measurements
// used by the CPU encoder, then returns one canonical screen-space overlay.
// Callers may use it as a texture-only diagnostic before routing it into the
// production GPU scene.
func RenderCanonicalASSOverlay(ctx context.Context, ffmpeg string, metadata AudioMetadata, duration float64, layout VisualizerLayout, mode ForegroundMode, width, height int) (*TextOverlayMetadata, error) {
	fonts, err := VisualizerFonts()
	if err != nil {
		return nil, fmt.Errorf("visualizer fonts: %w", err)
	}
	faces, err := ResolveVisualizerFontFaces(fonts)
	if err != nil {
		return nil, fmt.Errorf("resolve visualizer fonts: %w", err)
	}
	fontDir := filepath.Dir(fonts.Regular400)
	metrics := map[string]TextMetrics{}
	measure := func(text, family string, weight, size int, key string) error {
		if text == "" {
			return nil
		}
		w, e := MeasureASSEncodedWidth(ctx, ffmpeg, family, weight, fontDir, text, size)
		if e != nil {
			return e
		}
		metrics[key] = TextMetrics{Width: w}
		return nil
	}
	if err := measure(metadata.Title, faces.SemiBold600.ASSFamily, 600, scaledFontSize(48, width), "title"); err != nil {
		return nil, fmt.Errorf("measure title: %w", err)
	}
	if err := measure(metadata.Artist, faces.Medium500.ASSFamily, 500, scaledFontSize(28, width), "artist"); err != nil {
		return nil, fmt.Errorf("measure artist: %w", err)
	}
	if err := measure(metadata.Album, faces.Regular400.ASSFamily, 400, scaledFontSize(24, width), "album"); err != nil {
		return nil, fmt.Errorf("measure album: %w", err)
	}
	ass, err := BuildVisualizerASSWithMode(metadata, duration, layout, fonts, metrics, mode, width, height)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "imagepad-ass-overlay-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	assPath := filepath.Join(tmp, "overlay.ass")
	if err := os.WriteFile(assPath, []byte(ass), 0644); err != nil {
		return nil, err
	}
	payload, stride, err := RenderASSOverlayRGBA(ctx, ffmpeg, assPath, fontDir, uint32(width), uint32(height))
	if err != nil {
		return nil, err
	}
	overlay := NewScreenTextOverlayMetadata(uint32(width), uint32(height), stride, payload, "ffmpeg-libass")
	overlay.Title = metadata.Title
	overlay.Artist = metadata.Artist
	overlay.Album = metadata.Album
	return overlay, nil
}

// NewScreenTextOverlayMetadata wraps a libass RGBA raster with the explicit
// screen-space semantics required by the shared contract.
func NewScreenTextOverlayMetadata(width, height, stride uint32, payload []byte, rendererVersion string) *TextOverlayMetadata {
	h := sha256.Sum256(payload)
	return &TextOverlayMetadata{Kind: "screen_rgba", Width: width, Height: height, RowStride: stride, Format: PixelRGBA8, ColorSpace: ColorSRGB, Premultiplied: true, Payload: payload, AssetHash: fmt.Sprintf("%x", h[:]), RendererID: "ffmpeg-libass-overlay", RendererVersion: rendererVersion, ScreenRect: SceneRect{W: int(width), H: int(height)}, AlphaMode: "premultiplied", PixelOrigin: "top_left"}
}
