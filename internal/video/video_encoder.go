package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type EncoderPurpose string

const (
	EncoderStandard   EncoderPurpose = "standard"
	EncoderLowLatency EncoderPurpose = "low_latency"
)

type VideoEncoderProfile struct {
	Name     string         `json:"name"`
	Hardware bool           `json:"hardware"`
	Purpose  EncoderPurpose `json:"-"`
}

var (
	videoEncoderCacheMu   sync.Mutex
	videoEncoderCache     = map[string]string{}
	lastVideoEncoder      VideoEncoderProfile
	listAvailableEncoders = defaultListAvailableEncoders
	probeEncoder          = defaultProbeEncoder
)

func EncoderPriority(goos string) []string {
	switch goos {
	case "windows":
		return []string{"h264_nvenc", "h264_qsv", "h264_amf", "libx264"}
	case "darwin":
		return []string{"h264_videotoolbox", "libx264"}
	default:
		return []string{"libx264"}
	}
}

func NewVideoEncoderProfile(name string, purpose EncoderPurpose) VideoEncoderProfile {
	if name == "" {
		name = "libx264"
	}
	return VideoEncoderProfile{Name: name, Hardware: name != "libx264", Purpose: purpose}
}

func CPUVideoEncoder(purpose EncoderPurpose) VideoEncoderProfile {
	return NewVideoEncoderProfile("libx264", purpose)
}

func SelectVideoEncoder(ctx context.Context, ffmpeg string, purpose EncoderPurpose) VideoEncoderProfile {
	return selectVideoEncoderForOS(ctx, ffmpeg, runtime.GOOS, purpose)
}

func selectVideoEncoderForOS(ctx context.Context, ffmpeg, goos string, purpose EncoderPurpose) VideoEncoderProfile {
	priority := EncoderPriority(goos)
	if len(priority) == 1 {
		profile := CPUVideoEncoder(purpose)
		setCurrentVideoEncoder(profile)
		return profile
	}
	key := ffmpeg + "|" + goos
	videoEncoderCacheMu.Lock()
	defer videoEncoderCacheMu.Unlock()
	if name := videoEncoderCache[key]; name != "" {
		profile := NewVideoEncoderProfile(name, purpose)
		lastVideoEncoder = profile
		return profile
	}

	available, err := listAvailableEncoders(ctx, ffmpeg)
	if err == nil {
		for _, name := range priority {
			if name == "libx264" {
				break
			}
			if !available[name] {
				continue
			}
			profile := NewVideoEncoderProfile(name, purpose)
			if err := probeEncoder(ctx, ffmpeg, profile); err == nil {
				videoEncoderCache[key] = name
				lastVideoEncoder = profile
				return profile
			}
		}
	}
	videoEncoderCache[key] = "libx264"
	profile := CPUVideoEncoder(purpose)
	lastVideoEncoder = profile
	return profile
}

func setCurrentVideoEncoder(profile VideoEncoderProfile) {
	videoEncoderCacheMu.Lock()
	lastVideoEncoder = profile
	videoEncoderCacheMu.Unlock()
}

func CurrentVideoEncoder() (VideoEncoderProfile, bool) {
	videoEncoderCacheMu.Lock()
	defer videoEncoderCacheMu.Unlock()
	return lastVideoEncoder, lastVideoEncoder.Name != ""
}

func resetVideoEncoderCacheForTest() {
	videoEncoderCacheMu.Lock()
	videoEncoderCache = map[string]string{}
	lastVideoEncoder = VideoEncoderProfile{}
	videoEncoderCacheMu.Unlock()
}

func parseAdvertisedVideoEncoders(output string) map[string]bool {
	result := make(map[string]bool)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields[0]) == 0 || fields[0][0] != 'V' {
			continue
		}
		result[fields[1]] = true
	}
	return result
}

func defaultListAvailableEncoders(ctx context.Context, ffmpeg string) (map[string]bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, ffmpeg, "-hide_banner", "-encoders")
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list FFmpeg encoders: %w: %s", err, trimOutput(output))
	}
	return parseAdvertisedVideoEncoders(string(output)), nil
}

func defaultProbeEncoder(ctx context.Context, ffmpeg string, profile VideoEncoderProfile) error {
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "imagepad-encoder-probe-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "probe.mp4")
	preset := QualityPreset{VideoBitrate: "500k", MaxRate: "700k", BufferSize: "1000k", CRF: 28}
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=black:s=256x256:r=30", "-frames:v", "2"}
	args = append(args, profile.FFmpegArgs(preset, "veryfast")...)
	args = append(args, "-an", "-f", "mp4", out)
	cmd := exec.CommandContext(probeCtx, ffmpeg, args...)
	hideWindow(cmd)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("probe %s: %w: %s", profile.Name, err, trimOutput(output))
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("probe %s produced no output", profile.Name)
	}
	return nil
}

func runVideoEncodeWithFallback(ctx context.Context, selected VideoEncoderProfile, cleanup func(), attempt func(VideoEncoderProfile) error) error {
	if selected.Name == "" {
		selected = CPUVideoEncoder(selected.Purpose)
	}
	hardwareErr := attempt(selected)
	if hardwareErr == nil || !selected.Hardware {
		return hardwareErr
	}
	if ctx.Err() != nil || errors.Is(hardwareErr, context.Canceled) || errors.Is(hardwareErr, context.DeadlineExceeded) {
		return hardwareErr
	}
	if cleanup != nil {
		cleanup()
	}
	cpu := CPUVideoEncoder(selected.Purpose)
	if cpuErr := attempt(cpu); cpuErr != nil {
		return fmt.Errorf("%s failed: %v; libx264 fallback failed: %w", selected.Name, hardwareErr, cpuErr)
	}
	setCurrentVideoEncoder(cpu)
	return nil
}

func (p VideoEncoderProfile) FFmpegArgs(preset QualityPreset, softwarePreset string) []string {
	if p.Name == "" {
		p = CPUVideoEncoder(p.Purpose)
	}
	args := []string{"-c:v", p.Name}
	if !p.Hardware {
		if p.Purpose == EncoderLowLatency {
			args = append(args, "-preset", "ultrafast", "-tune", "zerolatency")
		} else {
			if softwarePreset == "" {
				softwarePreset = "veryfast"
			}
			args = append(args, "-preset", softwarePreset, "-crf", itoa(preset.CRF))
		}
	} else {
		cq := itoa(preset.CRF)
		lowLatency := p.Purpose == EncoderLowLatency
		switch p.Name {
		case "h264_nvenc":
			if lowLatency {
				args = append(args, "-preset", "p1", "-tune", "ull")
				args = append(args, hardwareTargetBitrate(preset)...)
			} else {
				// Capped constant-quality VBR: -cq governs quality and -b:v 0
				// disables the bitrate target, so highly compressible video
				// (a near-static visualizer) stays small like libx264 -crf,
				// while -maxrate caps spikes. p6 + lookahead + spatial/temporal
				// AQ spend a little more GPU time (we have headroom now that
				// conversion is no longer paced to real time) to hit the same
				// -cq quality at a noticeably smaller size.
				args = append(args, "-preset", "p6", "-rc", "vbr", "-cq", cq, "-b:v", "0",
					"-rc-lookahead", "20", "-spatial_aq", "1", "-temporal_aq", "1", "-bf", "3")
				args = append(args, hardwareBitrateCeiling(preset)...)
			}
		case "h264_qsv":
			if lowLatency {
				args = append(args, "-preset", "veryfast")
				args = append(args, hardwareTargetBitrate(preset)...)
			} else {
				// ICQ constant-quality mode; it ignores a bitrate target.
				args = append(args, "-preset", "medium", "-global_quality", cq)
			}
		case "h264_amf":
			if lowLatency {
				args = append(args, "-quality", "speed", "-usage", "lowlatency")
				args = append(args, hardwareTargetBitrate(preset)...)
			} else {
				// AMD analog of the NVENC path: the "quality" preset plus VBAQ
				// (variance-based adaptive quantization) reaches the same QP
				// quality at a smaller size. Keep constant-QP rate control,
				// which is the most broadly supported AMF mode.
				args = append(args, "-quality", "quality", "-usage", "transcoding",
					"-rc", "cqp", "-qp_i", cq, "-qp_p", cq, "-qp_b", cq, "-vbaq", "1")
			}
		case "h264_videotoolbox":
			// videotoolbox has no reliable constant-quality mode across builds;
			// keep bitrate-based rate control (which still caps file size).
			if lowLatency {
				args = append(args, "-realtime", "1")
			} else {
				args = append(args, "-realtime", "0")
			}
			args = append(args, hardwareTargetBitrate(preset)...)
		}
	}
	return append(args, "-pix_fmt", "yuv420p")
}

// staticContentEncodeOptions returns extra encoder options for largely-static
// video (the audio visualizer and the SoundCloud artwork+waveform render):
// a long GOP aligned to the 4s HLS segment so the big static background is
// re-encoded once per segment, plus — for libx264 only — the animation tune
// and disabled scene-cut detection. The GOP options are generic; the libx264
// private options are not valid for the GPU encoders, so they are software-only.
// Place the result after encoder.FFmpegArgs(...).
func staticContentEncodeOptions(encoder VideoEncoderProfile) []string {
	// Long GOP aligned to the 4s segment is generic and safe everywhere.
	a := []string{"-g", "120", "-keyint_min", "120"}
	if !encoder.Hardware {
		// libx264: flat-content tune + no scene-cut keyframes + strong
		// macroblock-level adaptive quantization. The waveform is a tiny moving
		// region on a large static background; AQ lets the encoder spend bits on
		// the waveform while starving the background.
		return append(a, "-tune", "animation", "-sc_threshold", "0", "-aq-mode", "3")
	}
	// GPU encoders have no content-type tune (NVENC's tune is hq/ll/ull and hq
	// is already the default), but they expose equivalent static-content knobs:
	// look-ahead, scene-cut keyframes disabled, and B-frames.
	switch encoder.Name {
	case "h264_nvenc":
		// -no-scenecut is the NVENC analog of libx264 -sc_threshold 0; it only
		// applies when look-ahead is enabled. Spatial AQ is disabled for
		// largely-static content: NVENC's spatial AQ favors flat regions over
		// high-detail regions, which starves the moving waveform in the music
		// visualizer. The base NVENC args already enable -spatial_aq 1, so we
		// override it here.
		a = append(a, "-rc-lookahead", "20", "-no-scenecut", "1", "-bf", "3", "-spatial_aq", "0")
	case "h264_amf":
		a = append(a, "-bf", "3")
	}
	return a
}

// hardwareTargetBitrate returns target-bitrate rate control, used for
// low-latency streaming and for videotoolbox (which lacks a reliable
// constant-quality mode). Empty preset fields are omitted.
func hardwareTargetBitrate(preset QualityPreset) []string {
	var a []string
	if preset.VideoBitrate != "" {
		a = append(a, "-b:v", preset.VideoBitrate)
	}
	if preset.MaxRate != "" {
		a = append(a, "-maxrate", preset.MaxRate)
	}
	if preset.BufferSize != "" {
		a = append(a, "-bufsize", preset.BufferSize)
	}
	return a
}

// hardwareBitrateCeiling returns only the maxrate/bufsize cap (no target),
// used alongside constant-quality modes to bound peak bitrate.
func hardwareBitrateCeiling(preset QualityPreset) []string {
	var a []string
	if preset.MaxRate != "" {
		a = append(a, "-maxrate", preset.MaxRate)
	}
	if preset.BufferSize != "" {
		a = append(a, "-bufsize", preset.BufferSize)
	}
	return a
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
