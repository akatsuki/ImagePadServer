package video

import (
	"fmt"
	"math"
	"strconv"
)

// Shared by browser and native paths. Keep quality, timing and slice policy identical.
func nicoEncodeArgs(sourcePath, outputPath string, options NicoEncodeOptions) []string {
	preset := options.Preset
	if preset == "" {
		preset = "veryfast"
	}
	audioBitrate := options.AudioBitrate
	if audioBitrate == "" {
		audioBitrate = "128k"
	}
	fps := strconv.FormatInt(options.FPSNum, 10) + "/" + strconv.FormatInt(options.FPSDen, 10)
	geometry := strconv.Itoa(options.Width) + "x" + strconv.Itoa(options.Height)
	// -shortest only compares mapped output streams; overlay repeats its last
	// frame by default. Bound both video and audio to the requested timeline.
	duration := fmt.Sprintf("%d.%03d", options.DurationMs/1000, options.DurationMs%1000)
	gop := int(math.Ceil(4 * float64(options.FPSNum) / float64(options.FPSDen)))
	if gop < 1 {
		gop = 1
	}
	// Normalize the composed stream to the same CFR used by the renderer. This
	// keeps comment motion tied to elapsed video time even when the source is
	// 24/30/60fps or VFR; the source frame rate must not become the comment clock.
	filter := fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black[base];[1:v]format=rgba[overlay];[base][overlay]overlay=0:0:format=auto,fps=%s,format=yuv420p[v]", options.Width, options.Height, options.Width, options.Height, fps)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", sourcePath,
		"-f", "rawvideo", "-pix_fmt", "rgba", "-video_size", geometry, "-framerate", fps, "-i", "pipe:0",
		"-filter_complex", filter,
		"-map", "[v]", "-map", "0:a?",
		"-c:v", "libx264", "-preset", preset, "-crf", strconv.Itoa(options.CRF),
		"-x264-params", "sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0",
		"-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0", "-force_key_frames", "expr:gte(t,n_forced*4)",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", audioBitrate,
		"-t", duration, "-movflags", "+faststart", "-shortest", "-f", "mp4", outputPath,
	}
	return args
}
