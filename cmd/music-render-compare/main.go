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
	Duration float64 `json:"duration"`
	Frames   int64   `json:"frames"`
}
type renderResult struct {
	Output            string                  `json:"output,omitempty"`
	Error             string                  `json:"error,omitempty"`
	WallSeconds       float64                 `json:"wallSeconds"`
	Probe             probeResult             `json:"probe"`
	Screenshots       map[string]string       `json:"screenshots,omitempty"`
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
type report struct {
	Input                 string                     `json:"input"`
	InputSHA256           string                     `json:"inputSha256,omitempty"`
	GeneratedAt           time.Time                  `json:"generatedAt"`
	FFmpeg                string                     `json:"ffmpeg,omitempty"`
	GPUAdapter            string                     `json:"gpuAdapter,omitempty"`
	CPU                   renderResult               `json:"cpu"`
	GPU                   renderResult               `json:"gpu"`
	ScreenshotComparisons map[string]imageComparison `json:"screenshotComparisons,omitempty"`
	ComparisonGate        comparisonGate             `json:"comparisonGate"`
}

type comparisonGate struct {
	DurationDeltaSeconds float64 `json:"durationDeltaSeconds"`
	FrameDelta           int64   `json:"frameDelta"`
	DurationMatch        bool    `json:"durationMatch"`
	FrameCountMatch      bool    `json:"frameCountMatch"`
	Pass                 bool    `json:"pass"`
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
	out, err := exec.CommandContext(ctx, ff, "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries", "stream=duration,nb_read_frames:format=duration", "-of", "json", path).Output()
	if err != nil {
		return probeResult{}, err
	}
	var raw struct {
		Streams []struct {
			Duration string `json:"duration"`
			Frames   string `json:"nb_read_frames"`
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
		if r.Duration == 0 {
			r.Duration, _ = strconv.ParseFloat(raw.Streams[0].Duration, 64)
		}
		r.Frames, _ = strconv.ParseInt(raw.Streams[0].Frames, 10, 64)
	}
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
	r.ScreenshotMetrics = map[string]imageMetrics{}
	for name, path := range r.Screenshots {
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
	for name, at := range map[string]float64{"start": 0, "mid": duration / 2, "end": duration - 0.05} {
		if at < 0 {
			at = 0
		}
		path := filepath.Join(out, name+".png")
		// Seek after opening the HLS input so the end frame is decoded accurately;
		// fast pre-input seeking can land on an earlier TS keyframe or return a
		// black frame when the audio tail extends beyond the last video keyframe.
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
	inputSpec := video.AudioRenderInput{SourcePath: *input, Kind: video.SourceMusic, Analysis: analysis}
	id := "compare"
	ctx := context.Background()
	inputHash, _ := sha256File(*input)
	adapter := os.Getenv("IMAGEPAD_GPU_ADAPTER")
	if adapter == "" {
		adapter = os.Getenv("WGPU_ADAPTER_NAME")
	}
	if adapter == "" {
		adapter = "unknown (set IMAGEPAD_GPU_ADAPTER to record explicit adapter)"
	}
	rep := report{Input: *input, InputSHA256: inputHash, GeneratedAt: time.Now(), FFmpeg: ff, GPUAdapter: adapter}
	rep.CPU = render(ctx, false, filepath.Join(*output, "cpu"), ff, inputSpec, id, p)
	rep.GPU = render(ctx, true, filepath.Join(*output, "gpu"), ff, inputSpec, id, p)
	rep.ScreenshotComparisons = map[string]imageComparison{}
	for _, name := range []string{"start", "mid", "end"} {
		if a, ok := rep.CPU.Screenshots[name]; ok {
			if b, ok := rep.GPU.Screenshots[name]; ok {
				if c, e := compareImages(a, b); e == nil {
					rep.ScreenshotComparisons[name] = c
				}
			}
		}
	}
	rep.ComparisonGate = comparisonGate{
		DurationDeltaSeconds: math.Abs(rep.CPU.Probe.Duration - rep.GPU.Probe.Duration),
		FrameDelta:           rep.CPU.Probe.Frames - rep.GPU.Probe.Frames,
	}
	rep.ComparisonGate.DurationMatch = rep.ComparisonGate.DurationDeltaSeconds <= 0.05
	rep.ComparisonGate.FrameCountMatch = rep.ComparisonGate.FrameDelta == 0
	rep.ComparisonGate.Pass = rep.ComparisonGate.DurationMatch && rep.ComparisonGate.FrameCountMatch
	b, _ := json.MarshalIndent(rep, "", "  ")
	_ = os.WriteFile(filepath.Join(*output, "report.json"), append(b, '\n'), 0644)
	writeMarkdown(*output, rep)
	b2, _ := json.Marshal(rep)
	fmt.Println(string(b2))
}
func writeMarkdown(dir string, r report) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Music render comparison\n\nInput: `%s`\n\n| Mode | Wall time (s) | Duration (s) | Frames | Error |\n|---|---:|---:|---:|---|\n", r.Input)
	fmt.Fprintf(&b, "| CPU | %.3f | %.3f | %d | %s |\n", r.CPU.WallSeconds, r.CPU.Probe.Duration, r.CPU.Probe.Frames, r.CPU.Error)
	fmt.Fprintf(&b, "| GPU | %.3f | %.3f | %d | %s |\n", r.GPU.WallSeconds, r.GPU.Probe.Duration, r.GPU.Probe.Frames, r.GPU.Error)
	b.WriteString("\n## Screenshot comparisons\n\n| Point | Size match | Mean absolute RGBA | RMSE | Mismatch ratio |\n|---|---|---:|---:|---:|\n")
	for _, name := range []string{"start", "mid", "end"} {
		if c, ok := r.ScreenshotComparisons[name]; ok {
			fmt.Fprintf(&b, "| %s | %t | %.3f | %.3f | %.3f |\n", name, c.SizeMatch, c.MeanAbsoluteRGBA, c.RMSE, c.MismatchRatio)
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
