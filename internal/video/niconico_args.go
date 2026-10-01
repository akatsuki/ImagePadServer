package video

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"imagepadserver/internal/niconico"
)

// Shared by browser and native paths. Keep quality, timing and slice policy identical.
func nicoEncodeArgs(sourcePath, outputPath string, options NicoEncodeOptions) []string {
	return nicoEncodeArgsWithOverlayAlpha(sourcePath, outputPath, options, "auto")
}

// The timeline compositor writes premultiplied RGBA. Legacy browser renderers
// keep FFmpeg's auto/straight-alpha behavior through nicoEncodeArgs above.
func nicoTimelineEncodeArgs(sourcePath, outputPath string, options NicoEncodeOptions) []string {
	return nicoTimelineEncodeArgsWithAlphaMode(sourcePath, outputPath, options, false)
}

func nicoTimelineEncodeArgsWithAlphaMode(sourcePath, outputPath string, options NicoEncodeOptions, supportsAlphaModeMetadata bool) []string {
	alphaMode := "unpremultiply"
	if supportsAlphaModeMetadata {
		alphaMode = "premultiplied"
	}
	return nicoEncodeArgsWithOverlayAlpha(sourcePath, outputPath, options, alphaMode)
}

func nicoEncodeArgsWithOverlayAlpha(sourcePath, outputPath string, options NicoEncodeOptions, overlayAlpha string) []string {
	encoder := strings.ToLower(strings.TrimSpace(options.Encoder))
	if encoder == "" {
		encoder = "x264"
	}
	preset := options.Preset
	if encoder == "nvenc" {
		// Keep the existing x264 preset default from leaking into NVENC. p4 is
		// the measured candidate profile and is explicit for reproducibility.
		if preset == "" || preset == "veryfast" {
			preset = "p4"
		}
	} else if preset == "" {
		preset = "veryfast"
	}
	audioBitrate := options.AudioBitrate
	if audioBitrate == "" {
		audioBitrate = "128k"
	}
	fps := strconv.FormatInt(options.FPSNum, 10) + "/" + strconv.FormatInt(options.FPSDen, 10)
	geometry := strconv.Itoa(options.Width) + "x" + strconv.Itoa(options.Height)
	// Bound both streams to the requested timeline, but do not use -shortest:
	// a short audio tail must not truncate the final video frames.
	frameCount := niconico.MustFrameClock(options.FPSNum, options.FPSDen).FrameCountForDurationMs(options.DurationMs)
	// -t must cover the complete final CFR frame. The requested media duration
	// can fall between frame boundaries, while the contract requires
	// N=ceil(D*F) output frames.
	durationNumerator := frameCount * options.FPSDen
	wholeSeconds := durationNumerator / options.FPSNum
	remaining := durationNumerator % options.FPSNum
	nanos := (remaining*1_000_000_000 + options.FPSNum - 1) / options.FPSNum
	if nanos == 1_000_000_000 {
		wholeSeconds++
		nanos = 0
	}
	duration := fmt.Sprintf("%d.%09d", wholeSeconds, nanos)
	gop := int(math.Ceil(4 * float64(options.FPSNum) / float64(options.FPSDen)))
	if gop < 1 {
		gop = 1
	}
	overlayInput := "[1:v]format=rgba"
	alphaOption := ""
	if overlayAlpha == "premultiplied" {
		// Older FFmpeg builds lack alpha_mode metadata support. The caller selects
		// this path only after the setparams filter reports the needed option.
		overlayInput += ",setparams=alpha_mode=premultiplied"
		alphaOption = ":alpha=premultiplied"
	} else if overlayAlpha == "unpremultiply" {
		// Preserve premultiplied input pixels on older FFmpeg builds by converting
		// them to straight alpha before the broadly supported overlay filter. The
		// alpha plane must be supplied as the filter's second input; inplace mode
		// is a no-op for the packed RGBA frame formats used by the timeline pipe.
		overlayInput += ",split[comment_premultiplied][comment_alpha_source];[comment_alpha_source]alphaextract[comment_alpha];[comment_premultiplied][comment_alpha]unpremultiply"
	}
	// Normalize the composed stream to the same CFR used by the renderer. This
	// keeps comment motion tied to elapsed video time even when the source is
	// 24/30/60fps or VFR; the source frame rate must not become the comment clock.
	filter := fmt.Sprintf("[0:v]scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,tpad=stop_mode=clone:stop_duration=1[base];%s[overlay];[base][overlay]overlay=0:0:format=auto%s,fps=%s,format=yuv420p[v]", options.Width, options.Height, options.Width, options.Height, overlayInput, alphaOption, fps)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
	}
	if options.FilterThreads > 0 {
		args = append(args, "-filter_complex_threads", strconv.Itoa(options.FilterThreads))
	}
	if options.DecoderThreads > 0 {
		args = append(args, "-threads", strconv.Itoa(options.DecoderThreads))
	}
	args = append(args,
		"-i", sourcePath,
		"-f", "rawvideo", "-pix_fmt", "rgba", "-video_size", geometry, "-framerate", fps, "-i", "pipe:0",
		"-filter_complex", filter,
		"-map", "[v]", "-map", "0:a?", "-frames:v", strconv.FormatInt(frameCount, 10),
		"-c:v", map[string]string{"x264": "libx264", "nvenc": "h264_nvenc"}[encoder],
	)
	if encoder == "x264" && options.EncoderThreads > 0 {
		args = append(args, "-threads:v", strconv.Itoa(options.EncoderThreads))
	}
	if encoder == "nvenc" {
		args = append(args,
			"-preset", preset, "-tune", "hq", "-rc", "vbr", "-cq", strconv.Itoa(options.CRF), "-b:v", "0",
			"-bf", "3", "-slices", "1", "-forced-idr", "1", "-no-scenecut", "1",
		)
	} else {
		args = append(args,
			"-preset", preset, "-crf", strconv.Itoa(options.CRF),
			"-x264-params", "sliced-threads=0:slices=1:slice-max-size=0:slice-max-mbs=0",
		)
	}
	args = append(args,
		"-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop), "-sc_threshold", "0", "-force_key_frames", "expr:gte(t,n_forced*4)",
		"-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", audioBitrate,
		"-t", duration,
	)
	if options.OutputMode == NicoOutputTee {
		args = append(args,
			"-flags:v", "+global_header", "-f", "tee",
			"[f=mp4:movflags=+faststart:onfail=abort]out.mp4|[f=hls:hls_time=4:hls_playlist_type=vod:hls_flags=independent_segments:start_number=0:hls_segment_filename=segment-%05d.ts:onfail=abort]playlist.m3u8",
		)
	} else {
		args = append(args, "-movflags", "+faststart", "-f", "mp4", outputPath)
	}
	return args
}
