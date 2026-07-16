package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

type probeResult struct {
	Duration           float64 `json:"duration"`
	VideoDuration      float64 `json:"videoDuration"`
	Frames             int64   `json:"frames"`
	PixFmt             string  `json:"pixFmt,omitempty"`
	ColorSpace         string  `json:"colorSpace,omitempty"`
	ColorRange         string  `json:"colorRange,omitempty"`
	FirstPTS           float64 `json:"firstPts"`
	LastPTS            float64 `json:"lastPts"`
	NormalizedFirstPTS float64 `json:"normalizedFirstPts"`
	NormalizedLastPTS  float64 `json:"normalizedLastPts"`
	AudioExcluded      bool    `json:"audioExcluded"`
}
type renderResult struct {
	Output            string                  `json:"output,omitempty"`
	Error             string                  `json:"error,omitempty"`
	WallSeconds       float64                 `json:"wallSeconds"`
	Probe             probeResult             `json:"probe"`
	Screenshots       map[string]string       `json:"screenshots,omitempty"`
	ScreenshotSHA256  map[string]string       `json:"screenshotSha256,omitempty"`
	ScreenshotMetrics map[string]imageMetrics `json:"screenshotMetrics,omitempty"`
}
type imageBounds struct {
	MinX int `json:"minX"`
	MinY int `json:"minY"`
	MaxX int `json:"maxX"`
	MaxY int `json:"maxY"`
}
type imageMetrics struct {
	Width               int          `json:"width"`
	Height              int          `json:"height"`
	AverageRGBA         [4]float64   `json:"averageRgba"`
	NonBackgroundPixels int          `json:"nonBackgroundPixels"`
	NonBackgroundBounds *imageBounds `json:"nonBackgroundBounds,omitempty"`
}
type imageComparison struct {
	SizeMatch        bool    `json:"sizeMatch"`
	MeanAbsoluteRGBA float64 `json:"meanAbsoluteRgba"`
	RMSE             float64 `json:"rmse"`
	MismatchedPixels int     `json:"mismatchedPixels"`
	MismatchRatio    float64 `json:"mismatchRatio"`
}

// glyphMaskComparison compares a luminance-derived foreground mask in a text
// region. It is diagnostic-only: screenshots are opaque after encoding, so
// this intentionally measures the rendered glyph coverage rather than atlas
// transport. The threshold is recorded to keep reports reproducible.
type glyphMaskComparison struct {
	Threshold       uint8   `json:"threshold"`
	CPUVisible      int     `json:"cpuVisible"`
	GPUVisible      int     `json:"gpuVisible"`
	Intersection    int     `json:"intersection"`
	Union           int     `json:"union"`
	IoU             float64 `json:"iou"`
	MeanAbsoluteErr float64 `json:"meanAbsoluteErr"`
	RMSE            float64 `json:"rmse"`
}

// regionComparison is intentionally diagnostic-only. It lets the parity gate
// identify which static layer diverges without changing the aggregate gate.
type regionComparison struct {
	Region  imageBounds                `json:"region"`
	Samples map[string]imageComparison `json:"samples,omitempty"`
}

// sidecarProvenance identifies the exact binary used by a GPU comparison.
type sidecarProvenance struct {
	Path        string             `json:"path,omitempty"`
	SHA256      string             `json:"sha256,omitempty"`
	SizeBytes   int64              `json:"sizeBytes,omitempty"`
	ModTime     string             `json:"modTime,omitempty"`
	Fingerprint runtimeFingerprint `json:"fingerprint"`
	Error       string             `json:"error,omitempty"`
}
type report struct {
	Input                   string                                    `json:"input"`
	InputSHA256             string                                    `json:"inputSha256,omitempty"`
	GeneratedAt             time.Time                                 `json:"generatedAt"`
	FFmpeg                  string                                    `json:"ffmpeg,omitempty"`
	GPUAdapter              string                                    `json:"gpuAdapter,omitempty"`
	GPUFingerprint          runtimeFingerprint                        `json:"gpuFingerprint"`
	GPUSidecar              sidecarProvenance                         `json:"gpuSidecar"`
	CPUOnly                 bool                                      `json:"cpuOnly,omitempty"`
	CPU                     renderResult                              `json:"cpu"`
	GPU                     renderResult                              `json:"gpu"`
	FrameContract           frameContract                             `json:"frameContract"`
	ScreenshotComparisons   map[string]imageComparison                `json:"screenshotComparisons,omitempty"`
	StaticRegionComparisons map[string]regionComparison               `json:"staticRegionComparisons,omitempty"`
	GlyphMaskComparisons    map[string]map[string]glyphMaskComparison `json:"glyphMaskComparisons,omitempty"`
	ComparisonGate          comparisonGate                            `json:"comparisonGate"`
	SceneEvidence           sceneEvidence                             `json:"sceneEvidence,omitempty"`
}

// sceneEvidence records the exact static payload supplied to both renderers.
// It makes an empty compare fixture distinguishable from a real metadata/
// artwork parity run without embedding the potentially large raster twice.
type sceneEvidence struct {
	Fingerprint                string                        `json:"fingerprint,omitempty"`
	ArtworkHash                string                        `json:"artworkHash,omitempty"`
	GlyphHash                  string                        `json:"glyphHash,omitempty"`
	GlyphPayloadHash           string                        `json:"glyphPayloadHash,omitempty"`
	GlyphWidth                 uint32                        `json:"glyphWidth,omitempty"`
	GlyphHeight                uint32                        `json:"glyphHeight,omitempty"`
	GlyphRowStride             uint32                        `json:"glyphRowStride,omitempty"`
	GlyphCount                 int                           `json:"glyphCount,omitempty"`
	TextRunCount               int                           `json:"textRunCount,omitempty"`
	GlyphCoveragePixels        int                           `json:"glyphCoveragePixels,omitempty"`
	GPUInstanceCount           int                           `json:"gpuInstanceCount,omitempty"`
	GPUInstanceSample          any                           `json:"gpuInstanceSample,omitempty"`
	GPUInstanceDiagnosticError string                        `json:"gpuInstanceDiagnosticError,omitempty"`
	SyntheticGlyph             *video.GlyphSyntheticEvidence `json:"syntheticGlyph,omitempty"`
	TextOverlayCPUHash         string                        `json:"textOverlayCpuHash,omitempty"`
	TextOverlayGPUReceipt      *video.TextOverlayReceipt     `json:"textOverlayGpuReceipt,omitempty"`
	TextOverlaySHAEqual        bool                          `json:"textOverlayShaEqual,omitempty"`
	TextOverlayAlphaCoverage   int                           `json:"textOverlayAlphaCoverage,omitempty"`
	TextOverlayRegionCrop      imageBounds                   `json:"textOverlayRegionCrop,omitempty"`
	TextOverlayRenderer        string                        `json:"textOverlayRenderer,omitempty"`
	TextOverlayCompositeSHA    string                        `json:"textOverlayCompositeSha,omitempty"`
	CPUInstanceManifest        video.GlyphInstanceManifest   `json:"cpuInstanceManifest,omitempty"`
	GPUInstanceParity          video.GlyphInstanceParity     `json:"gpuInstanceParity,omitempty"`
	Title                      string                        `json:"title,omitempty"`
	Artist                     string                        `json:"artist,omitempty"`
	Album                      string                        `json:"album,omitempty"`
}

// glyphAtlasEvidence is deliberately derived from the exact bytes sent to
// the sidecar.  It catches stride/format/payload drift without making the
// comparison tool depend on a renderer implementation.
func glyphAtlasEvidence(atlas *video.GlyphAtlasMetadata) (sceneEvidence, bool) {
	if atlas == nil {
		return sceneEvidence{}, false
	}
	sum := sha256.Sum256(atlas.Payload)
	coverage := 0
	stride := int(atlas.RowStride)
	if stride > 0 && int(atlas.Height) > 0 && len(atlas.Payload) >= stride*int(atlas.Height) {
		for y := 0; y < int(atlas.Height); y++ {
			row := atlas.Payload[y*stride : (y+1)*stride]
			for x := 3; x < len(row); x += 4 {
				if row[x] != 0 {
					coverage++
				}
			}
		}
	}
	return sceneEvidence{
		GlyphHash: atlas.AssetHash, GlyphPayloadHash: fmt.Sprintf("%x", sum[:]),
		GlyphWidth: atlas.Width, GlyphHeight: atlas.Height, GlyphRowStride: atlas.RowStride,
		GlyphCount: len(atlas.Glyphs), TextRunCount: len(atlas.TextRuns), GlyphCoveragePixels: coverage,
	}, true
}

func compareImageRegion(aPath, bPath string, region imageBounds) (imageComparison, error) {
	af, err := os.Open(aPath)
	if err != nil {
		return imageComparison{}, err
	}
	defer af.Close()
	bf, err := os.Open(bPath)
	if err != nil {
		return imageComparison{}, err
	}
	defer bf.Close()
	a, _, err := image.Decode(af)
	if err != nil {
		return imageComparison{}, err
	}
	b, _, err := image.Decode(bf)
	if err != nil {
		return imageComparison{}, err
	}
	ab, bb := a.Bounds(), b.Bounds()
	x0, y0 := maxInt(region.MinX, 0), maxInt(region.MinY, 0)
	x1, y1 := minInt(region.MaxX, ab.Dx()), minInt(region.MaxY, ab.Dy())
	if x1 <= x0 || y1 <= y0 || x1 > bb.Dx() || y1 > bb.Dy() {
		return imageComparison{}, fmt.Errorf("invalid comparison region %+v", region)
	}
	c := imageComparison{SizeMatch: true}
	total := (x1 - x0) * (y1 - y0) * 4
	var sum, sq float64
	mism := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			ar, ag, abv, aa := a.At(x+ab.Min.X, y+ab.Min.Y).RGBA()
			br, bg, bbv, ba := b.At(x+bb.Min.X, y+bb.Min.Y).RGBA()
			vals := [...]uint32{ar, ag, abv, aa}
			vals2 := [...]uint32{br, bg, bbv, ba}
			pixel := false
			for i := 0; i < 4; i++ {
				d := float64(absU32(vals[i], vals2[i])) / 257
				sum += d
				sq += d * d
				if d > 8 {
					pixel = true
				}
			}
			if pixel {
				mism++
			}
		}
	}
	c.MeanAbsoluteRGBA = sum / float64(total)
	c.RMSE = math.Sqrt(sq / float64(total))
	c.MismatchedPixels = mism
	c.MismatchRatio = float64(mism) / float64((x1-x0)*(y1-y0))
	return c, nil
}

func compareGlyphMaskRegion(aPath, bPath string, region imageBounds) (glyphMaskComparison, error) {
	af, err := os.Open(aPath)
	if err != nil {
		return glyphMaskComparison{}, err
	}
	defer af.Close()
	bf, err := os.Open(bPath)
	if err != nil {
		return glyphMaskComparison{}, err
	}
	defer bf.Close()
	a, _, err := image.Decode(af)
	if err != nil {
		return glyphMaskComparison{}, err
	}
	b, _, err := image.Decode(bf)
	if err != nil {
		return glyphMaskComparison{}, err
	}
	ab, bb := a.Bounds(), b.Bounds()
	x0, y0 := maxInt(region.MinX, 0), maxInt(region.MinY, 0)
	x1, y1 := minInt(region.MaxX, ab.Dx()), minInt(region.MaxY, ab.Dy())
	if x1 <= x0 || y1 <= y0 || x1 > bb.Dx() || y1 > bb.Dy() {
		return glyphMaskComparison{}, fmt.Errorf("invalid glyph region %+v", region)
	}
	const threshold uint8 = 180
	r := glyphMaskComparison{Threshold: threshold}
	var sum, sq float64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			ar, ag, abv, _ := a.At(x+ab.Min.X, y+ab.Min.Y).RGBA()
			br, bg, bbv, _ := b.At(x+bb.Min.X, y+bb.Min.Y).RGBA()
			// sRGB luminance, normalized to 8-bit. Text foreground is bright in
			// the canonical scene; using luminance avoids RGB-channel artifacts.
			al := uint8((299*(ar/257) + 587*(ag/257) + 114*(abv/257)) / 1000)
			bl := uint8((299*(br/257) + 587*(bg/257) + 114*(bbv/257)) / 1000)
			am, bm := al >= threshold, bl >= threshold
			if am {
				r.CPUVisible++
			}
			if bm {
				r.GPUVisible++
			}
			if am && bm {
				r.Intersection++
			}
			if am || bm {
				r.Union++
			}
			d := float64(al) - float64(bl)
			if d < 0 {
				d = -d
			}
			sum += d
			sq += d * d
		}
	}
	n := float64((x1 - x0) * (y1 - y0))
	r.MeanAbsoluteErr = sum / n
	r.RMSE = math.Sqrt(sq / n)
	if r.Union > 0 {
		r.IoU = float64(r.Intersection) / float64(r.Union)
	}
	return r, nil
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// frameContract separates the renderer's canonical raw-frame clock from the
// number ffprobe observes after MPEG-TS/HLS muxing.  A muxer may expose a
// short encoder-drain tail; that is evidence about packaging, not permission
// to alter the GPU scene frame count.
type frameContract struct {
	ExpectedFrames    int64 `json:"expectedFrames"`
	CPUObservedFrames int64 `json:"cpuObservedFrames"`
	GPUObservedFrames int64 `json:"gpuObservedFrames"`
	CPUMuxDelta       int64 `json:"cpuMuxDelta"`
	GPUMuxDelta       int64 `json:"gpuMuxDelta"`
}

type comparisonGate struct {
	DurationDeltaSeconds float64 `json:"durationDeltaSeconds"`
	FrameDelta           int64   `json:"frameDelta"`
	DurationMatch        bool    `json:"durationMatch"`
	FrameCountMatch      bool    `json:"frameCountMatch"`
	Pass                 bool    `json:"pass"`
	FingerprintRecorded  bool    `json:"fingerprintRecorded"`
}

type runtimeFingerprint struct {
	Adapter   string `json:"adapter"`
	Backend   string `json:"backend"`
	Toolchain string `json:"toolchain"`
}

func runtimeFingerprintFromEnv() runtimeFingerprint {
	return runtimeFingerprint{
		Adapter:   strings.TrimSpace(firstNonEmpty(os.Getenv("IMAGEPAD_GPU_ADAPTER"), os.Getenv("WGPU_ADAPTER_NAME"))),
		Backend:   strings.TrimSpace(firstNonEmpty(os.Getenv("IMAGEPAD_GPU_BACKEND"), os.Getenv("WGPU_BACKEND"))),
		Toolchain: strings.TrimSpace(os.Getenv("IMAGEPAD_GPU_TOOLCHAIN")),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (f runtimeFingerprint) Complete() bool {
	return f.Adapter != "" && f.Backend != "" && f.Toolchain != ""
}

func loadImageMetrics(path string) (imageMetrics, error) {
	f, err := os.Open(path)
	if err != nil {
		return imageMetrics{}, err
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	if err != nil {
		return imageMetrics{}, err
	}
	b := im.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return imageMetrics{}, fmt.Errorf("empty image %s", path)
	}
	var sums [4]uint64
	// Use the top-left pixel as a stable background reference. A 12/255
	// channel distance excludes the uniform canvas while retaining artwork,
	// text, spectrum and progress elements.
	bg := im.At(b.Min.X, b.Min.Y)
	br, bgG, bb, ba := bg.RGBA()
	const threshold uint32 = 12 * 257
	m := imageMetrics{Width: w, Height: h, NonBackgroundBounds: nil}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := im.At(x, y).RGBA()
			sums[0] += uint64(r)
			sums[1] += uint64(g)
			sums[2] += uint64(bl)
			sums[3] += uint64(a)
			d := absU32(r, br) + absU32(g, bgG) + absU32(bl, bb) + absU32(a, ba)
			if d > threshold {
				m.NonBackgroundPixels++
				if m.NonBackgroundBounds == nil {
					m.NonBackgroundBounds = &imageBounds{MinX: x, MinY: y, MaxX: x, MaxY: y}
				} else {
					q := m.NonBackgroundBounds
					if x < q.MinX {
						q.MinX = x
					}
					if y < q.MinY {
						q.MinY = y
					}
					if x > q.MaxX {
						q.MaxX = x
					}
					if y > q.MaxY {
						q.MaxY = y
					}
				}
			}
		}
	}
	for i := range sums {
		m.AverageRGBA[i] = float64(sums[i]) / float64(w*h*257)
	}
	return m, nil
}
func absU32(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
func compareImages(aPath, bPath string) (imageComparison, error) {
	af, err := os.Open(aPath)
	if err != nil {
		return imageComparison{}, err
	}
	defer af.Close()
	bf, err := os.Open(bPath)
	if err != nil {
		return imageComparison{}, err
	}
	defer bf.Close()
	a, _, err := image.Decode(af)
	if err != nil {
		return imageComparison{}, err
	}
	b, _, err := image.Decode(bf)
	if err != nil {
		return imageComparison{}, err
	}
	ab, bb := a.Bounds(), b.Bounds()
	c := imageComparison{SizeMatch: ab.Dx() == bb.Dx() && ab.Dy() == bb.Dy()}
	if !c.SizeMatch {
		return c, nil
	}
	var sum, sq float64
	total := ab.Dx() * ab.Dy() * 4
	mism := 0
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			ar, ag, abv, aa := a.At(x+ab.Min.X, y+ab.Min.Y).RGBA()
			br, bg, bbv, ba := b.At(x+bb.Min.X, y+bb.Min.Y).RGBA()
			vals := [...]uint32{ar, ag, abv, aa}
			vals2 := [...]uint32{br, bg, bbv, ba}
			pixelDiff := false
			for i := 0; i < 4; i++ {
				d := float64(absU32(vals[i], vals2[i])) / 257
				sum += d
				sq += d * d
				if d > 8 {
					pixelDiff = true
				}
			}
			if pixelDiff {
				mism++
			}
		}
	}
	c.MeanAbsoluteRGBA = sum / float64(total)
	c.RMSE = math.Sqrt(sq / float64(total))
	c.MismatchedPixels = mism
	c.MismatchRatio = float64(mism) / float64(ab.Dx()*ab.Dy())
	return c, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func inspectSidecar(path string, fingerprint runtimeFingerprint) sidecarProvenance {
	p := sidecarProvenance{Path: path, Fingerprint: fingerprint}
	if strings.TrimSpace(path) == "" {
		p.Error = "sidecar path not configured"
		return p
	}
	info, err := os.Stat(path)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.SizeBytes = info.Size()
	p.ModTime = info.ModTime().UTC().Format(time.RFC3339Nano)
	p.SHA256, err = sha256File(path)
	if err != nil {
		p.Error = err.Error()
	}
	return p
}

func ffmpegVersion(ctx context.Context, ff string) string {
	out, err := exec.CommandContext(ctx, ff, "-version").Output()
	if err != nil {
		return ff
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if line == "" {
		return ff
	}
	return line
}

func latestAudio(dir string) (string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var paths []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".opus" || ext == ".ogg" || ext == ".mp3" || ext == ".m4a" || ext == ".wav" || ext == ".flac" {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("no audio cache files in %s", dir)
	}
	sort.Slice(paths, func(i, j int) bool {
		a, _ := os.Stat(paths[i])
		b, _ := os.Stat(paths[j])
		return a.ModTime().After(b.ModTime())
	})
	return paths[0], nil
}

func probe(ctx context.Context, path string) (probeResult, error) {
	ff, err := video.EnsureFFprobe()
	if err != nil {
		return probeResult{}, err
	}
	// Probe format duration separately because HLS stream duration is often
	// omitted even when the format duration is available.
	out, err := exec.CommandContext(ctx, ff, "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=duration,nb_read_frames,pix_fmt,color_space,color_range:format=duration", "-of", "json", path).Output()
	if err != nil {
		return probeResult{}, err
	}
	var raw struct {
		Streams []struct {
			Duration   string `json:"duration"`
			Frames     string `json:"nb_read_frames"`
			PixFmt     string `json:"pix_fmt"`
			ColorSpace string `json:"color_space"`
			ColorRange string `json:"color_range"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return probeResult{}, err
	}
	r := probeResult{}
	if raw.Format.Duration != "" {
		r.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	}
	if len(raw.Streams) > 0 {
		r.VideoDuration, _ = strconv.ParseFloat(raw.Streams[0].Duration, 64)
		if r.Duration == 0 {
			r.Duration, _ = strconv.ParseFloat(raw.Streams[0].Duration, 64)
		}
		r.Frames, _ = strconv.ParseInt(raw.Streams[0].Frames, 10, 64)
		r.PixFmt = raw.Streams[0].PixFmt
		r.ColorSpace = raw.Streams[0].ColorSpace
		r.ColorRange = raw.Streams[0].ColorRange
	}
	// PTS summary is intentionally separate so malformed/non-video streams do
	// not make the primary probe fail.
	pts, _ := exec.CommandContext(ctx, ff, "-v", "error", "-select_streams", "v:0", "-show_entries", "frame=pts_time", "-of", "csv=p=0", path).Output()
	for _, line := range strings.Split(strings.TrimSpace(string(pts)), "\n") {
		if line == "" {
			continue
		}
		v, e := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if e != nil {
			continue
		}
		if r.FirstPTS == 0 && r.LastPTS == 0 {
			r.FirstPTS = v
		}
		r.LastPTS = v
	}
	if r.LastPTS != 0 || r.FirstPTS != 0 {
		r.NormalizedFirstPTS = 0
		r.NormalizedLastPTS = r.LastPTS - r.FirstPTS
		if r.VideoDuration == 0 {
			// MPEG-TS/HLS often omits stream duration. Reconstruct the observed
			// video clock from normalized PTS plus one frame period (30 Hz).
			r.VideoDuration = r.NormalizedLastPTS + 1.0/30.0
		}
	}
	// The probe explicitly selects v:0; audio is excluded from these metrics.
	r.AudioExcluded = true
	return r, nil
}

func render(ctx context.Context, gpu bool, out, ffmpeg string, input video.AudioRenderInput, id string, p video.QualityPreset) renderResult {
	if err := os.MkdirAll(out, 0755); err != nil {
		return renderResult{Error: err.Error()}
	}
	started := time.Now()
	var err error
	if gpu {
		err = video.RunAudioVisualizerHLS(ctx, out, ffmpeg, input, id, p)
	} else {
		err = video.RunAudioVisualizerHLSCPUReference(ctx, out, ffmpeg, input, id, p)
	}
	r := renderResult{WallSeconds: time.Since(started).Seconds()}
	if err != nil {
		r.Error = err.Error()
		return r
	}
	// HLS helpers use the canonical current-<id>.m3u8 name; resolve it by
	// globbing so the CLI remains independent of that internal naming detail.
	matches, _ := filepath.Glob(filepath.Join(out, "*.m3u8"))
	if len(matches) == 0 {
		r.Error = "render succeeded but no HLS playlist was produced"
		return r
	}
	r.Output = matches[0]
	r.Probe, _ = probe(ctx, r.Output)
	r.Screenshots = extractScreenshots(ctx, ffmpeg, r.Output, out, r.Probe.Duration)
	r.ScreenshotSHA256 = map[string]string{}
	r.ScreenshotMetrics = map[string]imageMetrics{}
	for name, path := range r.Screenshots {
		if hash, e := sha256File(path); e == nil {
			r.ScreenshotSHA256[name] = hash
		}
		if m, e := loadImageMetrics(path); e == nil {
			r.ScreenshotMetrics[name] = m
		}
	}
	return r
}

func extractScreenshots(ctx context.Context, ffmpeg, playlist, out string, duration float64) map[string]string {
	result := map[string]string{}
	if duration <= 0 {
		duration = 1
	}
	// Avoid exact segment boundaries: some HLS muxers expose no decodable
	// frame at t=0 or during the final encoder drain interval.
	for name, at := range map[string]float64{"start": math.Min(0.1, duration/4), "mid": duration / 2, "end": math.Max(0, duration-0.5)} {
		if at < 0 {
			at = 0
		}
		path := filepath.Join(out, name+".png")
		// Seek after opening the HLS playlist. Input-side seeking can land before
		// the first keyframe and produce a misleading black start/end artifact.
		cmd := exec.CommandContext(ctx, ffmpeg, "-y", "-i", playlist, "-ss", fmt.Sprintf("%.3f", at), "-frames:v", "1", "-vf", "format=rgba", path)
		if err := cmd.Run(); err == nil {
			result[name] = path
		}
	}
	return result
}

func main() {
	input := flag.String("input", "", "audio cache file (default: newest)")
	dataDir := flag.String("data-dir", filepath.Join(settings.Dir(), "media"), "audio cache directory")
	output := flag.String("output-dir", "", "comparison output directory")
	height := flag.Int("height", 360, "render height")
	cpuOnly := flag.Bool("cpu-only", false, "render CPU reference only and skip GPU startup")
	title := flag.String("title", "", "metadata title to include in the canonical scene")
	artist := flag.String("artist", "", "metadata artist to include in the canonical scene")
	album := flag.String("album", "", "metadata album to include in the canonical scene")
	artwork := flag.String("artwork", "", "artwork image path to include in the canonical scene")
	flag.Parse()
	if *output == "" {
		*output = filepath.Join(settings.Dir(), "diagnostics", "music-render-compare", time.Now().Format("20060102-150405"))
	}
	if *input == "" {
		var err error
		*input, err = latestAudio(*dataDir)
		if err != nil {
			fatal(err)
		}
	}
	if _, err := os.Stat(*input); err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(*output, 0755); err != nil {
		fatal(err)
	}
	ff, err := video.EnsureFFmpeg()
	if err != nil {
		fatal(err)
	}
	analysis, err := video.AnalyzeAudioForKind(context.Background(), ff, *input, video.SourceMusic)
	if err != nil {
		fatal(err)
	}
	p := video.ResolveQuality(strconv.Itoa(*height), 100)
	inputSpec := video.AudioRenderInput{SourcePath: *input, Kind: video.SourceMusic, Analysis: analysis,
		Metadata: video.AudioMetadata{Title: *title, Artist: *artist, Album: *album}, ArtworkPath: *artwork}
	id := "compare"
	ctx := context.Background()
	inputHash, _ := sha256File(*input)
	fingerprint := runtimeFingerprintFromEnv()
	if !*cpuOnly {
		if executable := strings.TrimSpace(os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")); executable != "" {
			if actual, probeErr := video.ProbeGPUFingerprint(ctx, executable, "compare-fingerprint"); probeErr == nil {
				fingerprint = runtimeFingerprint{Adapter: actual.Adapter, Backend: actual.Backend, Toolchain: actual.Toolchain}
			}
		}
	}
	adapter := fingerprint.Adapter
	if adapter == "" {
		adapter = "unknown (set GPU fingerprint environment variables to record explicit runtime)"
	}
	rep := report{Input: *input, InputSHA256: inputHash, GeneratedAt: time.Now(), FFmpeg: ffmpegVersion(ctx, ff), GPUAdapter: adapter, GPUFingerprint: fingerprint, CPUOnly: *cpuOnly}
	if !*cpuOnly {
		rep.GPUSidecar = inspectSidecar(strings.TrimSpace(os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")), fingerprint)
	}
	rep.FrameContract.ExpectedFrames = int64(math.Max(1, math.Ceil(analysis.Duration*30)))
	scene := video.CanonicalMusicScene(inputSpec, 0, 0)
	rep.SceneEvidence = sceneEvidence{Fingerprint: scene.Fingerprint, Title: inputSpec.Metadata.Title, Artist: inputSpec.Metadata.Artist, Album: inputSpec.Metadata.Album}
	if scene.Artwork != nil {
		rep.SceneEvidence.ArtworkHash = scene.Artwork.AssetHash
	}
	if scene.TextOverlay != nil {
		rep.SceneEvidence.TextOverlayCPUHash = scene.TextOverlay.AssetHash
		rep.SceneEvidence.TextOverlayRegionCrop = imageBounds{MaxX: int(scene.TextOverlay.Width), MaxY: int(scene.TextOverlay.Height)}
		for i := 3; i < len(scene.TextOverlay.Payload); i += 4 {
			if scene.TextOverlay.Payload[i] > 0 {
				rep.SceneEvidence.TextOverlayAlphaCoverage++
			}
		}
	}
	if glyphEvidence, ok := glyphAtlasEvidence(scene.GlyphAtlas); ok {
		rep.SceneEvidence.GlyphHash = glyphEvidence.GlyphHash
		rep.SceneEvidence.GlyphPayloadHash = glyphEvidence.GlyphPayloadHash
		rep.SceneEvidence.GlyphWidth = glyphEvidence.GlyphWidth
		rep.SceneEvidence.GlyphHeight = glyphEvidence.GlyphHeight
		rep.SceneEvidence.GlyphRowStride = glyphEvidence.GlyphRowStride
		rep.SceneEvidence.GlyphCount = glyphEvidence.GlyphCount
		rep.SceneEvidence.TextRunCount = glyphEvidence.TextRunCount
		rep.SceneEvidence.GlyphCoveragePixels = glyphEvidence.GlyphCoveragePixels
	}
	if !*cpuOnly {
		if executable := strings.TrimSpace(os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD")); executable != "" {
			if scene.TextOverlay != nil {
				octx, ocancel := context.WithTimeout(ctx, 3*time.Second)
				receipt, oe := video.ProbeGPUSceneTextOverlay(octx, executable, "compare-text-overlay", uint32(p.Height*16/9), uint32(p.Height), &scene)
				ocancel()
				if oe == nil {
					rep.SceneEvidence.TextOverlayGPUReceipt = receipt
					if receipt != nil {
						rep.SceneEvidence.TextOverlaySHAEqual = receipt.SHA256 == rep.SceneEvidence.TextOverlayCPUHash
						rep.SceneEvidence.TextOverlayRenderer = receipt.RendererID + "/" + receipt.RendererVersion
					}
				}
				cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
				if cf, ce := video.ProbeGPUSceneTextOverlayComposite(cctx, executable, uint32(math.Round(float64(p.Height)*16.0/9.0)), uint32(p.Height), &scene); ce == nil {
					h := sha256.Sum256(cf.Payload)
					rep.SceneEvidence.TextOverlayCompositeSHA = fmt.Sprintf("%x", h[:])
				}
				ccancel()
			}
			probeWidth := uint32(math.Round(float64(p.Height) * 16.0 / 9.0))
			cpuManifest := video.ExpandMusicGlyphManifest(&scene, probeWidth, uint32(p.Height))
			rep.SceneEvidence.CPUInstanceManifest = cpuManifest
			diagCtx, cancelDiag := context.WithTimeout(ctx, 3*time.Second)
			gd, e := video.ProbeGPUSceneGlyphDiagnostics(diagCtx, executable, "compare-glyph-diagnostics", probeWidth, uint32(p.Height), &scene)
			cancelDiag()
			if e == nil && gd != nil {
				rep.SceneEvidence.GPUInstanceCount = gd.Count
				rep.SceneEvidence.GPUInstanceParity = video.CompareMusicGlyphManifest(cpuManifest, gd)
				if len(gd.Instances) > 0 {
					rep.SceneEvidence.GPUInstanceSample = gd.Instances[0]
				}
			} else if e != nil {
				rep.SceneEvidence.GPUInstanceDiagnosticError = e.Error()
			}
			synthCtx, cancelSynth := context.WithTimeout(ctx, 3*time.Second)
			if sf, e := video.ProbeGPUSceneGlyphOnly(synthCtx, executable, "compare-glyph-only", probeWidth, uint32(p.Height), &scene); e == nil {
				ev := video.CompareSyntheticGlyph(scene.GlyphAtlas, cpuManifest, sf, probeWidth, uint32(p.Height))
				rep.SceneEvidence.SyntheticGlyph = &ev
			}
			cancelSynth()
			synCtx, cancelSyn := context.WithTimeout(ctx, 3*time.Second)
			syn, e := video.ProbeGPUSceneGlyphOnly(synCtx, executable, "compare-glyph-only", probeWidth, uint32(p.Height), &scene)
			cancelSyn()
			if e == nil {
				ev := video.CompareSyntheticGlyph(scene.GlyphAtlas, cpuManifest, syn, probeWidth, uint32(p.Height))
				rep.SceneEvidence.SyntheticGlyph = &ev
			} else if rep.SceneEvidence.GPUInstanceDiagnosticError == "" {
				rep.SceneEvidence.GPUInstanceDiagnosticError = e.Error()
			}
		}
	}
	if *cpuOnly {
		rep.SceneEvidence.CPUInstanceManifest = video.ExpandMusicGlyphManifest(&scene, uint32(math.Round(float64(p.Height)*16.0/9.0)), uint32(p.Height))
	}
	rep.CPU = render(ctx, false, filepath.Join(*output, "cpu"), ff, inputSpec, id, p)
	if !*cpuOnly {
		rep.GPU = render(ctx, true, filepath.Join(*output, "gpu"), ff, inputSpec, id, p)
	}
	rep.ScreenshotComparisons = map[string]imageComparison{}
	rep.StaticRegionComparisons = map[string]regionComparison{}
	rep.GlyphMaskComparisons = map[string]map[string]glyphMaskComparison{}
	for _, name := range []string{"start", "mid", "end"} {
		if a, ok := rep.CPU.Screenshots[name]; ok {
			if b, ok := rep.GPU.Screenshots[name]; ok {
				if c, e := compareImages(a, b); e == nil {
					rep.ScreenshotComparisons[name] = c
				}
			}
		}
	}
	// Compare static compositor regions independently. Coordinates come from
	// the same canonical scene payload sent to the GPU, then scale to the
	// comparison output. This is evidence only; it does not relax Pass.
	if !*cpuOnly {
		scaleRect := func(r video.SceneRect) imageBounds {
			outputWidth := int(math.Round(float64(p.Height) * 16.0 / 9.0))
			sx, sy := float64(outputWidth)/1280.0, float64(p.Height)/720.0
			return imageBounds{MinX: int(float64(r.X) * sx), MinY: int(float64(r.Y) * sy), MaxX: int(float64(r.X+r.W) * sx), MaxY: int(float64(r.Y+r.H) * sy)}
		}
		regions := map[string]video.SceneRect{"artwork": scene.Layout.Artwork, "title": scene.Layout.Title, "artist": scene.Layout.Artist, "album": scene.Layout.Album}
		for name, rect := range regions {
			rc := regionComparison{Region: scaleRect(rect), Samples: map[string]imageComparison{}}
			if name == "title" || name == "artist" || name == "album" {
				rep.GlyphMaskComparisons[name] = map[string]glyphMaskComparison{}
			}
			for _, point := range []string{"start", "mid", "end"} {
				if a, ok := rep.CPU.Screenshots[point]; ok {
					if b, ok := rep.GPU.Screenshots[point]; ok {
						if c, e := compareImageRegion(a, b, rc.Region); e == nil {
							rc.Samples[point] = c
						}
						if _, ok := rep.GlyphMaskComparisons[name]; ok {
							if gm, e := compareGlyphMaskRegion(a, b, rc.Region); e == nil {
								rep.GlyphMaskComparisons[name][point] = gm
							}
						}
					}
				}
			}
			rep.StaticRegionComparisons[name] = rc
		}
	}
	if !*cpuOnly {
		rep.FrameContract.CPUObservedFrames = rep.CPU.Probe.Frames
		rep.FrameContract.GPUObservedFrames = rep.GPU.Probe.Frames
		rep.FrameContract.CPUMuxDelta = rep.CPU.Probe.Frames - rep.FrameContract.ExpectedFrames
		rep.FrameContract.GPUMuxDelta = rep.GPU.Probe.Frames - rep.FrameContract.ExpectedFrames
		cpuVideoDuration := rep.CPU.Probe.VideoDuration
		gpuVideoDuration := rep.GPU.Probe.VideoDuration
		if cpuVideoDuration == 0 {
			cpuVideoDuration = rep.CPU.Probe.Duration
		}
		if gpuVideoDuration == 0 {
			gpuVideoDuration = rep.GPU.Probe.Duration
		}
		rep.ComparisonGate = comparisonGate{
			DurationDeltaSeconds: math.Abs(cpuVideoDuration - gpuVideoDuration),
			FrameDelta:           rep.CPU.Probe.Frames - rep.GPU.Probe.Frames,
		}
	}
	if !*cpuOnly {
		rep.ComparisonGate.DurationMatch = rep.ComparisonGate.DurationDeltaSeconds <= 0.05
		// Strict gate: both observed streams must match the canonical clock.
		// The separate frameContract fields make a mux tail diagnosable instead
		// of hiding it as a CPU-vs-GPU delta.
		rep.ComparisonGate.FrameCountMatch = rep.FrameContract.CPUMuxDelta == 0 && rep.FrameContract.GPUMuxDelta == 0
		rep.ComparisonGate.FingerprintRecorded = rep.GPUFingerprint.Complete()
		rep.ComparisonGate.Pass = rep.ComparisonGate.DurationMatch && rep.ComparisonGate.FrameCountMatch && rep.ComparisonGate.FingerprintRecorded
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.WriteFile(filepath.Join(*output, "report.json"), append(b, '\n'), 0644)
	writeMarkdown(*output, rep)
	b2, _ := json.Marshal(rep)
	fmt.Println(string(b2))
}
func writeMarkdown(dir string, r report) {
	var b strings.Builder
	title := "Music render comparison"
	if r.CPUOnly {
		title = "CPU music render fixture"
	}
	fmt.Fprintf(&b, "# %s\n\nInput: `%s`\n\n| Mode | Wall time (s) | Duration (s) | Frames | Error |\n|---|---:|---:|---:|---|\n", title, r.Input)
	fmt.Fprintf(&b, "| CPU | %.3f | %.3f | %d | %s |\n", r.CPU.WallSeconds, r.CPU.Probe.Duration, r.CPU.Probe.Frames, r.CPU.Error)
	fmt.Fprintf(&b, "| GPU | %.3f | %.3f | %d | %s |\n", r.GPU.WallSeconds, r.GPU.Probe.Duration, r.GPU.Probe.Frames, r.GPU.Error)
	b.WriteString("\n## Screenshot comparisons\n\n| Point | Size match | Mean absolute RGBA | RMSE | Mismatch ratio |\n|---|---|---:|---:|---:|\n")
	for _, name := range []string{"start", "mid", "end"} {
		if c, ok := r.ScreenshotComparisons[name]; ok {
			fmt.Fprintf(&b, "| %s | %t | %.3f | %.3f | %.3f |\n", name, c.SizeMatch, c.MeanAbsoluteRGBA, c.RMSE, c.MismatchRatio)
		}
	}
	b.WriteString("\n## Screenshot SHA-256\n\n| Mode | Point | SHA-256 |\n|---|---|---|\n")
	for _, mode := range []struct {
		name string
		res  renderResult
	}{
		{name: "CPU", res: r.CPU},
		{name: "GPU", res: r.GPU},
	} {
		for _, point := range []string{"start", "mid", "end"} {
			if hash, ok := mode.res.ScreenshotSHA256[point]; ok {
				fmt.Fprintf(&b, "| %s | %s | `%s` |\n", mode.name, point, hash)
			}
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "report.md"), []byte(b.String()), 0644)
}
func fatal(err error) {
	if !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "music-render-compare:", err)
	}
	os.Exit(1)
}
