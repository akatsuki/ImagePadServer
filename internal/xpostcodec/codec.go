// Package xpostcodec prepares local media and encodes an xpostvideo plan into
// one H.264/AAC MP4 plus an optional stream-copy HLS rendition.
package xpostcodec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"imagepadserver/internal/video"
	"imagepadserver/internal/xpostmodel"
	"imagepadserver/internal/xpostvideo"
)

const (
	maxDiagnostics = 32 << 10
	maxAssetBytes  = 64 << 20
	pcmRate        = 48000
	pcmChannels    = 2
)

type Options struct {
	FFmpeg, FFprobe, Compositor, OutDir, OutputPath, Encoder, AudioBitrate string
	CRF                                                                    int
	Progress                                                               func(done, total int)
}

type Result struct {
	OutputPath, HLSDir, Adapter, Backend string
	Frames, RendererCalls                int
	Duration                             float64
}

type streamInfo struct {
	CodecName    string            `json:"codec_name"`
	CodecType    string            `json:"codec_type"`
	AvgFrameRate string            `json:"avg_frame_rate"`
	Width        int               `json:"width"`
	Height       int               `json:"height"`
	SampleRate   string            `json:"sample_rate"`
	Channels     int               `json:"channels"`
	StartTime    string            `json:"start_time"`
	SAR          string            `json:"sample_aspect_ratio"`
	Duration     string            `json:"duration"`
	DurationTS   int64             `json:"duration_ts"`
	TimeBase     string            `json:"time_base"`
	PixFmt       string            `json:"pix_fmt"`
	Primaries    string            `json:"color_primaries"`
	Transfer     string            `json:"color_transfer"`
	Matrix       string            `json:"color_space"`
	Tags         map[string]string `json:"tags"`
	SideData     []struct {
		Rotation float64 `json:"rotation"`
		Type     string  `json:"side_data_type"`
	} `json:"side_data_list"`
}

type probeResult struct {
	Streams []streamInfo `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func PrepareMedia(ctx context.Context, ffmpeg, ffprobe string, media []xpostmodel.Media, outDir string, width, height, fps int) ([]xpostmodel.Media, error) {
	return prepareMedia(ctx, ffmpeg, ffprobe, media, outDir, width, height, fps, "black")
}

// PrepareMediaWithTheme also matches any subpixel-rounding padding in the
// first/last posters to the slideshow background.
func PrepareMediaWithTheme(ctx context.Context, ffmpeg, ffprobe string, media []xpostmodel.Media, outDir string, width, height, fps int, theme string) ([]xpostmodel.Media, error) {
	if theme != "" && theme != "light" && theme != "dark" {
		return nil, errors.New("xpost codec: invalid media background theme")
	}
	return prepareMedia(ctx, ffmpeg, ffprobe, media, outDir, width, height, fps, mediaBackgroundColor(theme))
}

func mediaBackgroundColor(theme string) string {
	if theme == "light" {
		return "white"
	}
	return "black"
}

func prepareMedia(ctx context.Context, ffmpeg, ffprobe string, media []xpostmodel.Media, outDir string, width, height, fps int, background string) ([]xpostmodel.Media, error) {
	if ffmpeg == "" || ffprobe == "" || outDir == "" || width < 2 || height < 2 || fps < 1 || fps > 60 {
		return nil, errors.New("xpost codec: invalid media preparation options")
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return nil, fmt.Errorf("create media preparation directory: %w", err)
	}
	prepDir, err := os.MkdirTemp(outDir, ".xpost-media-")
	if err != nil {
		return nil, fmt.Errorf("create private poster directory: %w", err)
	}
	keepPosters := false
	defer func() {
		if !keepPosters {
			_ = os.RemoveAll(prepDir)
		}
	}()
	prepared := append([]xpostmodel.Media(nil), media...)
	for i := range prepared {
		m := &prepared[i]
		if strings.TrimSpace(m.Path) == "" {
			return nil, fmt.Errorf("media %d has no local path", i)
		}
		info, err := probe(ctx, ffprobe, m.Path)
		if err != nil {
			return nil, fmt.Errorf("probe media %d: %w", i, err)
		}
		var videoStream, audioStream *streamInfo
		for j := range info.Streams {
			s := &info.Streams[j]
			if s.CodecType == "video" && videoStream == nil {
				videoStream = s
			}
			if s.CodecType == "audio" && audioStream == nil {
				audioStream = s
			}
		}
		if videoStream == nil {
			if m.Kind == "video" {
				return nil, fmt.Errorf("media %d has no video stream", i)
			}
			m.HasAudio = audioStream != nil
			continue
		}
		if isHDR(*videoStream) {
			return nil, fmt.Errorf("media %d uses HDR video; explicit HDR conversion is required", i)
		}
		displayW, displayH, err := displayDimensions(*videoStream)
		if err != nil {
			return nil, fmt.Errorf("media %d dimensions: %w", i, err)
		}
		m.Width, m.Height = displayW, displayH
		m.HasAudio = audioStream != nil
		probedDuration := durationSeconds(info, *videoStream)
		if m.Kind == "video" && probedDuration <= 0 {
			return nil, fmt.Errorf("media %d has no valid source duration", i)
		}
		if m.Kind == "video" {
			m.Duration = probedDuration
		} else if m.Duration <= 0 {
			m.Duration = probedDuration
		}
		if m.Kind != "video" {
			continue
		}
		base := filepath.Join(prepDir, fmt.Sprintf("xpost-media-%d", i))
		m.FirstPath = base + "-first.png"
		m.LastPath = base + "-last.png"
		if err := extractPosterWithBackground(ctx, ffmpeg, m.Path, m.FirstPath, displayW, displayH, width, height, fps, false, background); err != nil {
			return nil, fmt.Errorf("extract media %d first frame: %w", i, err)
		}
		if err := extractPosterWithBackground(ctx, ffmpeg, m.Path, m.LastPath, displayW, displayH, width, height, fps, true, background); err != nil {
			os.Remove(m.FirstPath)
			return nil, fmt.Errorf("extract media %d last frame: %w", i, err)
		}
	}
	keepPosters = true
	return prepared, nil
}

func Encode(ctx context.Context, opts Options, plan xpostvideo.Plan) (Result, error) {
	var result Result
	if err := validateOptions(opts, plan); err != nil {
		return result, err
	}
	if err := os.MkdirAll(opts.OutDir, 0700); err != nil {
		return result, fmt.Errorf("create X video output directory: %w", err)
	}
	outputPath := opts.OutputPath
	if outputPath == "" {
		outputPath = filepath.Join(opts.OutDir, "xpost.mp4")
	}
	outputPath, err := pathInside(opts.OutDir, outputPath)
	if err != nil {
		return result, fmt.Errorf("output path: %w", err)
	}
	hlsDir := outputPath + ".hls"
	if _, err := os.Lstat(outputPath); err == nil {
		return result, errors.New("X video output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if _, err := os.Lstat(hlsDir); err == nil {
		return result, errors.New("X video HLS output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	jobDir, err := os.MkdirTemp(opts.OutDir, ".xpostcodec-")
	if err != nil {
		return result, fmt.Errorf("create private X video workspace: %w", err)
	}
	defer os.RemoveAll(jobDir)
	videoPath := filepath.Join(jobDir, "video-silent.mp4")
	pcmPath := filepath.Join(jobDir, "audio.f32le")
	stageOutput := filepath.Join(jobDir, "final.mp4")
	stageHLS := filepath.Join(jobDir, "hls")
	if err := renderFrames(ctx, opts, plan, jobDir, videoPath, &result); err != nil {
		return Result{}, err
	}
	if err := mixAudio(ctx, opts.FFmpeg, opts.FFprobe, pcmPath, plan); err != nil {
		return Result{}, err
	}
	if err := runFFmpeg(ctx, opts.FFmpeg, append([]string{"-hide_banner", "-loglevel", "error", "-y"}, muxArgsWithBitrate(videoPath, pcmPath, stageOutput, opts.AudioBitrate)...), nil); err != nil {
		return Result{}, fmt.Errorf("mux X video audio: %w", err)
	}
	if err := validateEncoded(ctx, opts.FFmpeg, opts.FFprobe, stageOutput, plan); err != nil {
		return Result{}, err
	}
	if err := os.Mkdir(stageHLS, 0700); err != nil {
		return Result{}, fmt.Errorf("create HLS workspace: %w", err)
	}
	if err := writeHLS(ctx, opts.FFmpeg, stageOutput, stageHLS, plan.Options.FPS); err != nil {
		return Result{}, err
	}
	if err := os.Rename(stageOutput, outputPath); err != nil {
		return Result{}, fmt.Errorf("publish final X video: %w", err)
	}
	if err := os.Rename(stageHLS, hlsDir); err != nil {
		os.Remove(outputPath)
		return Result{}, fmt.Errorf("publish X video HLS: %w", err)
	}
	result.OutputPath, result.HLSDir = outputPath, hlsDir
	result.Frames = plan.TotalFrames
	result.Duration = float64(plan.TotalFrames) / float64(plan.Options.FPS)
	return result, nil
}

func validateOptions(opts Options, plan xpostvideo.Plan) error {
	if opts.FFmpeg == "" || opts.FFprobe == "" || opts.Compositor == "" || opts.OutDir == "" || opts.Encoder == "" {
		return errors.New("xpost codec: FFmpeg, FFprobe, compositor, output directory, and encoder are required")
	}
	if plan.TotalFrames <= 0 || plan.Options.Width < 2 || plan.Options.Height < 2 || plan.Options.FPS < 1 || plan.Options.FPS > 60 {
		return errors.New("xpost codec: invalid video plan")
	}
	if opts.Encoder != "libx264" && opts.Encoder != "h264_nvenc" && opts.Encoder != "h264_qsv" && opts.Encoder != "h264_amf" && opts.Encoder != "h264_videotoolbox" {
		return fmt.Errorf("xpost codec: unsupported H.264 encoder %q", opts.Encoder)
	}
	if opts.CRF < 0 || opts.CRF > 51 {
		return errors.New("xpost codec: CRF must be between 0 and 51")
	}
	if !validAudioBitrate(opts.AudioBitrate) {
		return errors.New("xpost codec: invalid AAC bitrate")
	}
	return nil
}

func validAudioBitrate(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return true
	}
	multiplier := 1.0
	if strings.HasSuffix(value, "k") {
		multiplier, value = 1000, strings.TrimSuffix(value, "k")
	} else if strings.HasSuffix(value, "m") {
		multiplier, value = 1000000, strings.TrimSuffix(value, "m")
	}
	n, err := strconv.ParseFloat(value, 64)
	return err == nil && n > 0 && !math.IsNaN(n) && !math.IsInf(n, 0) && n*multiplier <= 1000000
}

func probe(ctx context.Context, ffprobe, path string) (probeResult, error) {
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", path)
	hideWindow(cmd)
	stdout, stderr, err := video.SeparateOutputTrackedFFmpeg(cmd)
	if err != nil {
		return probeResult{}, fmt.Errorf("ffprobe: %w: %s", err, trimDiagnostics(string(stderr)))
	}
	var result probeResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return result, fmt.Errorf("decode ffprobe response: %w", err)
	}
	return result, nil
}

func isHDR(s streamInfo) bool {
	transfer := strings.ToLower(strings.TrimSpace(s.Transfer))
	switch transfer {
	case "smpte2084", "arib-std-b67", "smpte428", "bt2020-10", "bt2020-12":
		return true
	}
	for _, data := range s.SideData {
		if strings.Contains(strings.ToLower(data.Type), "mastering display") || strings.Contains(strings.ToLower(data.Type), "content light") {
			return true
		}
	}
	return false
}

func displayDimensions(s streamInfo) (int, int, error) {
	if s.Width <= 0 || s.Height <= 0 {
		return 0, 0, errors.New("invalid dimensions")
	}
	w, h := float64(s.Width), float64(s.Height)
	if s.SAR != "" && s.SAR != "N/A" && s.SAR != "0:1" {
		parts := strings.SplitN(s.SAR, ":", 2)
		if len(parts) == 2 {
			n, e1 := strconv.ParseFloat(parts[0], 64)
			d, e2 := strconv.ParseFloat(parts[1], 64)
			if e1 == nil && e2 == nil && n > 0 && d > 0 {
				w *= n / d
			}
		}
	}
	rotation := 0.0
	if s.Tags != nil {
		rotation, _ = strconv.ParseFloat(s.Tags["rotate"], 64)
	}
	if len(s.SideData) > 0 && s.SideData[0].Rotation != 0 {
		rotation = s.SideData[0].Rotation
	}
	if int(math.Round(math.Mod(math.Abs(rotation), 180))) == 90 {
		w, h = h, w
	}
	width, height := int(math.Round(w)), int(math.Round(h))
	if width < 2 || height < 2 || width > 32768 || height > 32768 {
		return 0, 0, errors.New("display dimensions are outside supported bounds")
	}
	return width, height, nil
}

func durationSeconds(p probeResult, s streamInfo) float64 {
	// ffprobe's decimal duration is rounded to microseconds. For example,
	// 68/30 seconds becomes 2.266667 and adds a frame when rounded up at 30 fps.
	// Use exact stream ticks when available.
	if parts := strings.Split(s.TimeBase, "/"); s.DurationTS > 0 && len(parts) == 2 {
		numerator, nErr := strconv.ParseInt(parts[0], 10, 64)
		denominator, dErr := strconv.ParseInt(parts[1], 10, 64)
		if nErr == nil && dErr == nil && numerator > 0 && denominator > 0 {
			d := float64(s.DurationTS) * float64(numerator) / float64(denominator)
			if d > 0 && d < 24*60*60 {
				return d
			}
		}
	}
	if d, err := strconv.ParseFloat(s.Duration, 64); err == nil && d > 0 && d < 24*60*60 {
		return d
	}
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil && d > 0 && d < 24*60*60 {
		return d
	}
	return 0
}

func extractPoster(ctx context.Context, ffmpeg, source, output string, sourceW, sourceH, maxW, maxH, fps int, last bool) error {
	return extractPosterWithBackground(ctx, ffmpeg, source, output, sourceW, sourceH, maxW, maxH, fps, last, "black")
}

func extractPosterWithBackground(ctx context.Context, ffmpeg, source, output string, sourceW, sourceH, maxW, maxH, fps int, last bool, background string) error {
	w, h := fitSize(sourceW, sourceH, maxW, maxH)
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if last {
		args = append(args, "-sseof", "-1")
	}
	args = append(args, "-i", source, "-an", "-sn", "-dn", "-vf", fmt.Sprintf("setpts=PTS-STARTPTS,fps=%d:eof_action=pass,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s,setsar=1", fps, w, h, w, h, background))
	if last {
		args = append(args, "-vf", fmt.Sprintf("setpts=PTS-STARTPTS,fps=%d:eof_action=pass,reverse,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=%s,setsar=1", fps, w, h, w, h, background))
	}
	args = append(args, "-frames:v", "1", "-threads", "1", output)
	if err := runFFmpeg(ctx, ffmpeg, args, nil); err != nil {
		return err
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() == 0 {
		return errors.New("poster extraction produced no file")
	}
	return nil
}

func fitSize(w, h, maxW, maxH int) (int, int) {
	if w < 1 || h < 1 || maxW < 2 || maxH < 2 {
		return 2, 2
	}
	scale := math.Min(1, math.Min(float64(maxW)/float64(w), float64(maxH)/float64(h)))
	outW := int(math.Round(float64(w) * scale))
	outH := int(math.Round(float64(h) * scale))
	outW &^= 1
	outH &^= 1
	if outW < 2 {
		outW = 2
	}
	if outH < 2 {
		outH = 2
	}
	return outW, outH
}

func pathInside(root, candidate string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	pathAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path must stay inside the job output directory")
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	parentReal, err := filepath.EvalSymlinks(filepath.Dir(pathAbs))
	if err != nil {
		return "", errors.New("output parent must be an existing directory inside the job output directory")
	}
	realRel, err := filepath.Rel(rootReal, parentReal)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
		return "", errors.New("output path resolves outside the job output directory")
	}
	return pathAbs, nil
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.limit = maxDiagnostics
	}
	if b.buf.Len() < b.limit {
		n := b.limit - b.buf.Len()
		if n > len(p) {
			n = len(p)
		}
		_, _ = b.buf.Write(p[:n])
	}
	return len(p), nil
}
func (b *limitedBuffer) String() string { return b.buf.String() }

func runFFmpeg(ctx context.Context, ffmpeg string, args []string, stdin io.Reader) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	hideWindow(cmd)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("FFmpeg: %w: %s", err, trimDiagnostics(stderr.String()))
	}
	return nil
}

func trimDiagnostics(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 4096 {
		s = s[len(s)-4096:]
	}
	return s
}

func encoderArgs(encoder video.VideoEncoderProfile, fps int) []string {
	return encoderArgsCRF(encoder, fps, 20)
}

func encoderArgsCRF(encoder video.VideoEncoderProfile, fps, crf int) []string {
	if encoder.Name == "" {
		encoder = video.CPUVideoEncoder(video.EncoderStandard)
	}
	args := encoder.FFmpegArgs(video.QualityPreset{CRF: crf}, "medium")
	gop := strconv.Itoa(fps * 4)
	args = append(args, "-bf", "0", "-g", gop, "-keyint_min", gop, "-aud", "1")
	if encoder.Name == "libx264" {
		args = append(args, "-x264-params", "aud=1:repeat-headers=1:sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0")
	} else {
		args = append(args, "-slices", "1")
	}
	return args
}

func muxArgs(videoPath, pcmPath, outputPath string) []string {
	return []string{"-i", videoPath, "-f", "f32le", "-ar", "48000", "-ac", "2", "-i", pcmPath, "-map", "0:v:0", "-map", "1:a:0", "-c:v", "copy", "-c:a", "aac", "-ar", "48000", "-ac", "2", "-movflags", "+faststart", outputPath}
}

func mixPCMAt(destination, source []float32, start int) {
	if start < 0 {
		source = source[-start:]
		start = 0
	}
	if start >= len(destination) {
		return
	}
	n := len(source)
	if n > len(destination)-start {
		n = len(destination) - start
	}
	for i := 0; i < n; i++ {
		destination[start+i] += source[i]
	}
}

func writeHLS(ctx context.Context, ffmpeg, input, dir string, fps int) error {
	segment := filepath.Join(dir, "segment-%05d.ts")
	playlist := filepath.Join(dir, "playlist.m3u8")
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", input, "-map", "0:v:0", "-map", "0:a:0", "-c", "copy", "-f", "hls", "-hls_time", "4", "-hls_playlist_type", "vod", "-hls_segment_filename", segment, playlist}
	if err := runFFmpeg(ctx, ffmpeg, args, nil); err != nil {
		return fmt.Errorf("create stream-copy HLS: %w", err)
	}
	if fps < 1 {
		return errors.New("invalid HLS frame rate")
	}
	playlistInfo, err := os.Stat(playlist)
	if err != nil || playlistInfo.Size() == 0 {
		return errors.New("HLS generation produced no playlist")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	segments := 0
	for _, entry := range entries {
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".ts") && !entry.IsDir() {
			if info, e := entry.Info(); e == nil && info.Size() > 0 {
				segments++
			}
		}
	}
	if segments == 0 {
		return errors.New("HLS generation produced no media segments")
	}
	return nil
}

func validateEncoded(ctx context.Context, ffmpeg, ffprobe, path string, plan xpostvideo.Plan) error {
	info, err := probe(ctx, ffprobe, path)
	if err != nil {
		return fmt.Errorf("probe encoded X video: %w", err)
	}
	var v, a *streamInfo
	for i := range info.Streams {
		if info.Streams[i].CodecType == "video" && v == nil {
			v = &info.Streams[i]
		}
		if info.Streams[i].CodecType == "audio" && a == nil {
			a = &info.Streams[i]
		}
	}
	if v == nil || v.CodecName != "h264" || v.Width != plan.Options.Width || v.Height != plan.Options.Height {
		return errors.New("encoded X video has an invalid H.264 stream or dimensions")
	}
	if a == nil || a.CodecName != "aac" || a.SampleRate != "48000" || a.Channels != 2 {
		return errors.New("encoded X video is missing stereo 48 kHz AAC")
	}
	actualFPS, err := parseRate(v.AvgFrameRate)
	if err != nil || math.Abs(actualFPS-float64(plan.Options.FPS)) > .001 {
		return fmt.Errorf("encoded frame rate %q does not match plan %d fps", v.AvgFrameRate, plan.Options.FPS)
	}
	duration := durationSeconds(info, *v)
	expectedDuration := float64(plan.TotalFrames) / float64(plan.Options.FPS)
	if audioDuration := durationSeconds(info, *a); audioDuration <= 0 || math.Abs(audioDuration-expectedDuration) > math.Max(.1, 1/float64(plan.Options.FPS)) {
		return fmt.Errorf("encoded AAC duration %.3f does not match video duration %.3f", audioDuration, expectedDuration)
	}
	if duration <= 0 || math.Abs(duration-expectedDuration) > .5/float64(plan.Options.FPS) {
		return fmt.Errorf("encoded duration %.3f does not match plan duration %.3f", duration, expectedDuration)
	}
	frames, err := countDecodedFrames(ctx, ffprobe, ffmpeg, path)
	if err != nil {
		return fmt.Errorf("decode final X video: %w", err)
	}
	if frames != plan.TotalFrames {
		return fmt.Errorf("encoded frame count %d does not match plan %d", frames, plan.TotalFrames)
	}
	accessUnits, err := validateSingleSlice(ctx, ffmpeg, path)
	if err != nil {
		return err
	}
	if accessUnits != plan.TotalFrames {
		return fmt.Errorf("encoded access-unit count %d does not match plan %d", accessUnits, plan.TotalFrames)
	}
	return nil
}

func parseRate(s string) (float64, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, errors.New("invalid rational frame rate")
	}
	n, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}
	d, err := strconv.ParseFloat(parts[1], 64)
	if err != nil || d == 0 {
		return 0, errors.New("invalid rational frame-rate denominator")
	}
	return n / d, nil
}

func countDecodedFrames(ctx context.Context, ffprobe, ffmpeg, path string) (int, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-xerror", "-err_detect", "explode", "-i", path, "-map", "0", "-f", "null", "-")
	hideWindow(cmd)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("%w: %s", err, trimDiagnostics(stderr.String()))
	}
	probeCmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "v:0", "-count_frames", "-show_entries", "stream=nb_read_frames", "-of", "default=nokey=1:noprint_wrappers=1", path)
	hideWindow(probeCmd)
	data, probeErrOutput, err := video.SeparateOutputTrackedFFmpeg(probeCmd)
	if err != nil {
		return 0, fmt.Errorf("count decoded frames: %w: %s", err, trimDiagnostics(string(probeErrOutput)))
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse decoded frame count: %w", err)
	}
	return n, nil
}

func validateSingleSlice(ctx context.Context, ffmpeg, path string) (int, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-map", "0:v:0", "-c:v", "copy", "-bsf:v", "h264_mp4toannexb", "-f", "h264", "pipe:1")
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	count, parseErr := countVCLByAUD(stdout)
	if parseErr != nil {
		_ = cmd.Process.Kill()
		_ = stdout.Close()
	}
	waitErr := cmd.Wait()
	if parseErr != nil {
		return 0, parseErr
	}
	if waitErr != nil {
		return 0, fmt.Errorf("read encoded H.264 access units: %w: %s", waitErr, trimDiagnostics(stderr.String()))
	}
	if count == 0 {
		return 0, errors.New("encoded H.264 contains no access units")
	}
	return count, nil
}

func countVCLByAUD(r io.Reader) (int, error) {
	reader := bufio.NewReaderSize(r, 1<<20)
	var zeros int
	var currentType byte
	var haveNAL bool
	vcl, total := 0, 0
	finish := func() error {
		if !haveNAL {
			return nil
		}
		if currentType == 9 {
			if total > 0 && vcl != 1 {
				return fmt.Errorf("H.264 access unit has %d VCL NAL units; expected one", vcl)
			}
			total++
			vcl = 0
		} else if currentType == 1 || currentType == 5 {
			if total == 0 {
				return errors.New("H.264 VCL NAL appeared before AUD")
			}
			vcl++
		}
		return nil
	}
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, err
		}
		if b == 0 {
			zeros++
			continue
		}
		if b == 1 && zeros >= 2 {
			if err := finish(); err != nil {
				return 0, err
			}
			haveNAL = false
			zeros = 0
			continue
		}
		if !haveNAL {
			currentType = b & 0x1f
			haveNAL = true
		}
		zeros = 0
	}
	if err := finish(); err != nil {
		return 0, err
	}
	if total > 0 && vcl != 1 {
		return 0, fmt.Errorf("final H.264 access unit has %d VCL NAL units; expected one", vcl)
	}
	return total, nil
}
