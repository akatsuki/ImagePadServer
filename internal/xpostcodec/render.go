package xpostcodec

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"imagepadserver/internal/video"
	"imagepadserver/internal/xpostgpu"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xpostvideo"
)

type assetSpec struct {
	id, source string
	w, h       int
}

type assetSet struct {
	assets []xpostgpu.Asset
	dims   map[string]image.Point
	dir    string
}

type rawDecoder struct {
	cmd        *exec.Cmd
	stdout     io.ReadCloser
	stderr     *limitedBuffer
	width      int
	height     int
	frames     int
	readFrames int
}

func renderFrames(ctx context.Context, opts Options, plan xpostvideo.Plan, jobDir, output string, result *Result) error {
	set, err := writeAssets(plan, jobDir)
	if err != nil {
		return err
	}
	client, err := xpostgpu.StartWithTheme(ctx, opts.Compositor, plan.Options.Width, plan.Options.Height, set.assets, plan.Options.Theme)
	if err != nil {
		return fmt.Errorf("start X video compositor: %w", err)
	}
	defer client.Close()
	result.Adapter, result.Backend = client.Adapter, client.Backend
	profile := video.NewVideoEncoderProfile(opts.Encoder, video.EncoderStandard)
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "rawvideo", "-pixel_format", "rgba", "-video_size", fmt.Sprintf("%dx%d", plan.Options.Width, plan.Options.Height), "-framerate", strconv.Itoa(plan.Options.FPS), "-i", "pipe:0", "-map", "0:v:0", "-an", "-frames:v", strconv.Itoa(plan.TotalFrames)}
	args = append(args, encoderArgsCRF(profile, plan.Options.FPS, opts.CRF)...)
	args = append(args, "-movflags", "+faststart", "-f", "mp4", output)
	cmd := exec.CommandContext(ctx, opts.FFmpeg, args...)
	hideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open X video encoder input: %w", err)
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start X video encoder: %w", err)
	}
	encoderDone := false
	finishEncoder := func() error {
		if encoderDone {
			return nil
		}
		encoderDone = true
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("encode X video: %w: %s", err, trimDiagnostics(stderr.String()))
		}
		return nil
	}
	defer func() {
		if !encoderDone {
			_ = stdin.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		}
	}()

	var active *rawDecoder
	defer func() {
		if active != nil {
			_ = active.close()
		}
	}()
	activeMedia := -1
	lastVideoMedia := -1
	var lastVideoPixels []byte
	var lastStaticKey string
	var lastStaticPixels []byte
	writeFrame := func(pixels []byte) error {
		want := plan.Options.Width * plan.Options.Height * 4
		if len(pixels) != want {
			return fmt.Errorf("rendered RGBA frame has %d bytes, expected %d", len(pixels), want)
		}
		if _, err := stdin.Write(pixels); err != nil {
			return fmt.Errorf("write X video encoder frame: %w", err)
		}
		return nil
	}
	for frameNo := 0; frameNo < plan.TotalFrames; frameNo++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame := plan.FrameAt(frameNo)
		var outputFrame []byte
		var update *xpostgpu.Update
		if frame.VideoFrame >= 0 {
			if frame.VideoFrame == 0 {
				if active != nil {
					_ = active.close()
					active = nil
				}
				active, err = startMediaDecoder(ctx, opts.FFmpeg, plan, set, frame.MediaIndex, frame.Direct, segmentFrameCount(plan, frameNo))
				if err != nil {
					return err
				}
				activeMedia = frame.MediaIndex
			} else if active == nil || activeMedia != frame.MediaIndex {
				return errors.New("video timeline entered a clip without a decoder")
			}
			pixels, readErr := active.readFrame()
			if readErr != nil {
				return fmt.Errorf("decode media %d frame %d: %w", frame.MediaIndex, frame.VideoFrame, readErr)
			}
			lastVideoMedia, lastVideoPixels = frame.MediaIndex, append(lastVideoPixels[:0], pixels...)
			if frame.Direct && active.width == plan.Options.Width && active.height == plan.Options.Height {
				outputFrame = pixels
				lastStaticKey = ""
			} else {
				update = &xpostgpu.Update{AssetID: fmt.Sprintf("media-%d", frame.MediaIndex), Pixels: pixels}
				outputFrame, err = renderGPU(client, frame, update)
				if err != nil {
					return err
				}
				result.RendererCalls++
				lastStaticKey = ""
			}
			if frameNo+1 == segmentStart(plan, frameNo)+segmentFrameCount(plan, frameNo) {
				if err := active.close(); err != nil {
					return fmt.Errorf("finish media %d decoder: %w", activeMedia, err)
				}
				active, activeMedia = nil, -1
			}
		} else {
			if active != nil {
				_ = active.close()
				active, activeMedia = nil, -1
			}
			if exitIndex, ok := exitLayerIndex(frame.Layers); ok && lastVideoMedia == exitIndex && len(lastVideoPixels) > 0 {
				update = &xpostgpu.Update{AssetID: fmt.Sprintf("exit-%d", exitIndex), Pixels: lastVideoPixels}
			}
			if frame.StaticKey != "" && frame.StaticKey == lastStaticKey && update == nil {
				outputFrame = lastStaticPixels
			} else {
				outputFrame, err = renderGPU(client, frame, update)
				if err != nil {
					return err
				}
				result.RendererCalls++
				if frame.StaticKey != "" && update == nil {
					lastStaticKey, lastStaticPixels = frame.StaticKey, append(lastStaticPixels[:0], outputFrame...)
				} else {
					lastStaticKey = ""
				}
			}
		}
		if err := writeFrame(outputFrame); err != nil {
			return err
		}
		if opts.Progress != nil {
			opts.Progress(frameNo+1, plan.TotalFrames)
		}
	}
	if active != nil {
		if err := active.close(); err != nil {
			return err
		}
	}
	if err := client.Close(); err != nil {
		return fmt.Errorf("stop X video compositor: %w", err)
	}
	if err := finishEncoder(); err != nil {
		return err
	}
	return nil
}

func renderGPU(client *xpostgpu.Client, frame xpostvideo.Frame, update *xpostgpu.Update) ([]byte, error) {
	layers := make([]xpostgpu.Layer, len(frame.Layers))
	for i, l := range frame.Layers {
		layers[i] = xpostgpu.Layer{AssetID: l.AssetID, X: l.X, Y: l.Y, Width: l.Width, Height: l.Height, Tilt: l.Tilt, Opacity: l.Opacity, UV: l.UV}
	}
	pixels, err := client.Render(xpostgpu.Frame{Layers: layers}, update)
	if err != nil {
		return nil, fmt.Errorf("render X video frame: %w", err)
	}
	return pixels, nil
}

func writeAssets(plan xpostvideo.Plan, jobDir string) (assetSet, error) {
	set := assetSet{dims: map[string]image.Point{}, dir: jobDir}
	add := func(id, source string, maxW, maxH int) error {
		if id == "" || source == "" {
			return fmt.Errorf("missing asset path for %s", id)
		}
		pixels, w, h, err := decodeRGBA(source, maxW, maxH)
		if err != nil {
			return fmt.Errorf("load compositor asset %s: %w", id, err)
		}
		if len(pixels) > maxAssetBytes {
			return fmt.Errorf("compositor asset %s exceeds the memory limit", id)
		}
		rawPath := filepath.Join(jobDir, safeAssetName(id)+".rgba")
		if err := os.WriteFile(rawPath, pixels, 0600); err != nil {
			return err
		}
		set.assets = append(set.assets, xpostgpu.Asset{ID: id, Path: rawPath, Width: w, Height: h})
		set.dims[id] = image.Pt(w, h)
		return nil
	}
	for i, p := range plan.Pages {
		if err := add(fmt.Sprintf("card-%d", i), p.CardPath, plan.Options.Width, plan.Options.Height); err != nil {
			return set, err
		}
		if err := add(fmt.Sprintf("panel-%d", i), p.PanelPath, plan.Options.Width/2, plan.Options.Height); err != nil {
			return set, err
		}
		if p.CardEndPath != "" {
			if err := add(fmt.Sprintf("card-end-%d", i), p.CardEndPath, plan.Options.Width, plan.Options.Height); err != nil {
				return set, err
			}
		}
		if p.PanelEndPath != "" {
			if err := add(fmt.Sprintf("panel-end-%d", i), p.PanelEndPath, plan.Options.Width/2, plan.Options.Height); err != nil {
				return set, err
			}
		}
		for _, body := range []struct {
			kind string
			view *xpostmodel.ScrollView
		}{{"card", p.CardScroll}, {"panel", p.PanelScroll}} {
			if body.view == nil {
				continue
			}
			for n, tile := range body.view.Tiles {
				if err := add(fmt.Sprintf("%s-body-%d-%d", body.kind, i, n), tile.Path, body.view.Width, tile.Height); err != nil {
					return set, err
				}
			}
		}
	}
	for i, m := range plan.Media {
		source := m.Path
		if m.Kind == "video" {
			source = m.FirstPath
		}
		w, h := fitSize(m.Width, m.Height, plan.Options.Width, plan.Options.Height)
		direct := m.Kind == "video" && videoHasDirectFrames(plan, i) && sameAspect(m.Width, m.Height, plan.Options.Width, plan.Options.Height)
		if direct {
			w, h = plan.Options.Width, plan.Options.Height
		}
		if m.Kind == "video" {
			if err := set.addExact(idFor("media", i), source, w, h); err != nil {
				return set, err
			}
		} else if err := add(idFor("media", i), source, w, h); err != nil {
			return set, err
		}
		exitSource := m.Path
		if m.Kind == "video" {
			exitSource = m.LastPath
		}
		if m.Kind == "video" {
			if err := set.addExact(idFor("exit", i), exitSource, w, h); err != nil {
				return set, err
			}
		} else if err := add(idFor("exit", i), exitSource, w, h); err != nil {
			return set, err
		}
	}
	return set, nil
}

func (s *assetSet) addExact(id, source string, w, h int) error {
	if id == "" || source == "" || w < 1 || h < 1 {
		return fmt.Errorf("missing image path or dimensions for %s", id)
	}
	pixels, actualW, actualH, err := decodeRGBA(source, w, h)
	if err != nil {
		return fmt.Errorf("load compositor asset %s: %w", id, err)
	}
	if actualW != w || actualH != h {
		pixels = resizeRGBA(pixels, actualW, actualH, w, h)
	}
	if len(pixels) > maxAssetBytes {
		return fmt.Errorf("compositor asset %s exceeds the memory limit", id)
	}
	rawPath := filepath.Join(s.dir, safeAssetName(id)+".rgba")
	if err := os.WriteFile(rawPath, pixels, 0600); err != nil {
		return err
	}
	s.assets = append(s.assets, xpostgpu.Asset{ID: id, Path: rawPath, Width: w, Height: h})
	s.dims[id] = image.Pt(w, h)
	return nil
}

func decodeRGBA(path string, maxW, maxH int) ([]byte, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	config, _, err := image.DecodeConfig(f)
	if err != nil {
		return nil, 0, 0, err
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height)*4 > maxAssetBytes {
		return nil, 0, 0, errors.New("image dimensions exceed the asset limit")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, 0, err
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, 0, 0, err
	}
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW < 1 || srcH < 1 {
		return nil, 0, 0, errors.New("empty image")
	}
	w, h := fitSize(srcW, srcH, maxW, maxH)
	bytesN := int64(w) * int64(h) * 4
	if bytesN <= 0 || bytesN > maxAssetBytes {
		return nil, 0, 0, errors.New("image dimensions exceed the asset limit")
	}
	pixels := make([]byte, int(bytesN))
	for y := 0; y < h; y++ {
		sy := bounds.Min.Y + y*srcH/h
		for x := 0; x < w; x++ {
			sx := bounds.Min.X + x*srcW/w
			c := color.NRGBAModel.Convert(img.At(sx, sy)).(color.NRGBA)
			o := (y*w + x) * 4
			pixels[o] = c.R
			pixels[o+1] = c.G
			pixels[o+2] = c.B
			pixels[o+3] = c.A
		}
	}
	return pixels, w, h, nil
}

func resizeRGBA(src []byte, srcW, srcH, dstW, dstH int) []byte {
	if srcW == dstW && srcH == dstH {
		return src
	}
	dst := make([]byte, dstW*dstH*4)
	for y := 0; y < dstH; y++ {
		sy := y * srcH / dstH
		for x := 0; x < dstW; x++ {
			sx := x * srcW / dstW
			so := (sy*srcW + sx) * 4
			to := (y*dstW + x) * 4
			copy(dst[to:to+4], src[so:so+4])
		}
	}
	return dst
}

func startMediaDecoder(ctx context.Context, ffmpeg string, plan xpostvideo.Plan, set assetSet, index int, direct bool, frames int) (*rawDecoder, error) {
	if index < 0 || index >= len(plan.Media) {
		return nil, errors.New("video timeline has an invalid media index")
	}
	m := plan.Media[index]
	dims := set.dims[fmt.Sprintf("media-%d", index)]
	w, h := dims.X, dims.Y
	if direct && sameAspect(m.Width, m.Height, plan.Options.Width, plan.Options.Height) {
		w, h = plan.Options.Width, plan.Options.Height
	}
	background := mediaBackgroundColor(plan.Options.Theme)
	args := []string{"-hide_banner", "-loglevel", "error", "-i", m.Path, "-map", "0:v:0", "-an", "-sn", "-dn", "-vf", fmt.Sprintf("setpts=PTS-STARTPTS,fps=%d:eof_action=pass,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s,setsar=1,format=rgba", plan.Options.FPS, w, h, w, h, background), "-frames:v", strconv.Itoa(frames), "-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1"}
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start media %d decoder: %w", index, err)
	}
	return &rawDecoder{cmd: cmd, stdout: stdout, stderr: &stderr, width: w, height: h, frames: frames}, nil
}

func (d *rawDecoder) readFrame() ([]byte, error) {
	n := d.width * d.height * 4
	if n < 4 || n > maxAssetBytes {
		return nil, errors.New("decoded frame dimensions exceed the buffer limit")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(d.stdout, buf); err != nil {
		return nil, err
	}
	d.readFrames++
	return buf, nil
}

func (d *rawDecoder) close() error {
	if d == nil {
		return nil
	}
	_ = d.stdout.Close()
	if err := d.cmd.Wait(); err != nil {
		return fmt.Errorf("%w: %s", err, trimDiagnostics(d.stderr.String()))
	}
	if d.readFrames != d.frames {
		return fmt.Errorf("decoder produced %d frames, expected %d", d.readFrames, d.frames)
	}
	return nil
}

func segmentFrameCount(plan xpostvideo.Plan, frame int) int {
	for _, s := range plan.Segments {
		if frame >= s.Start && frame < s.Start+s.Frames && s.Kind == "video" {
			return s.Frames
		}
	}
	return 0
}
func segmentStart(plan xpostvideo.Plan, frame int) int {
	for _, s := range plan.Segments {
		if frame >= s.Start && frame < s.Start+s.Frames && s.Kind == "video" {
			return s.Start
		}
	}
	return -1
}
func exitLayerIndex(layers []xpostvideo.Draw) (int, bool) {
	for _, l := range layers {
		if strings.HasPrefix(l.AssetID, "exit-") {
			i, e := strconv.Atoi(strings.TrimPrefix(l.AssetID, "exit-"))
			if e == nil {
				return i, true
			}
		}
	}
	return -1, false
}
func safeAssetName(s string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(s)
}
func idFor(prefix string, index int) string { return fmt.Sprintf("%s-%d", prefix, index) }
func videoHasDirectFrames(plan xpostvideo.Plan, index int) bool {
	for _, s := range plan.Segments {
		if s.Kind == "video" && s.MediaIndex == index {
			return plan.FrameAt(s.Start).Direct
		}
	}
	return false
}
func sameAspect(w, h, tw, th int) bool {
	if w < 1 || h < 1 || tw < 1 || th < 1 {
		return false
	}
	return absInt(w*th-h*tw) <= maxInt(tw, th)
}
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
