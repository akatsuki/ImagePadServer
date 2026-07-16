package video

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// DiagnosticPostFilterReceipt describes a bounded CPU reference capture.
// Stage is hls-decode because the diagnostic runner intentionally leaves the
// production filter/mux path untouched and decodes its resulting video.
type DiagnosticPostFilterReceipt struct {
	Stage      string `json:"stage"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Frames     int    `json:"frames"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

// DiagnosticRenderPostFilterRGBA captures at most maxFrames of the CPU
// reference output as packed RGBA. It is diagnostic-only; the production HLS
// route and its encoder arguments are unchanged. The callback owns the frame
// until it returns.
func DiagnosticRenderPostFilterRGBA(ctx context.Context, ffmpeg string, input AudioRenderInput, id string, preset QualityPreset, maxFrames int, onFrame func(index int, rgba []byte) error) (DiagnosticPostFilterReceipt, error) {
	if maxFrames <= 0 || maxFrames > 150 || onFrame == nil {
		return DiagnosticPostFilterReceipt{}, fmt.Errorf("invalid capture limit")
	}
	started := time.Now()
	tmp, err := os.MkdirTemp("", "imagepad-postfilter-")
	if err != nil {
		return DiagnosticPostFilterReceipt{}, err
	}
	defer os.RemoveAll(tmp)
	if err := RunAudioVisualizerHLSCPUReference(ctx, tmp, ffmpeg, input, id, preset); err != nil {
		return DiagnosticPostFilterReceipt{}, err
	}
	playlists, _ := filepath.Glob(filepath.Join(tmp, "*.m3u8"))
	if len(playlists) == 0 {
		return DiagnosticPostFilterReceipt{}, fmt.Errorf("diagnostic CPU render produced no playlist")
	}
	w := preset.Height * 16 / 9
	if w%2 != 0 {
		w++
	}
	h := preset.Height
	frames := maxFrames
	if n := canonicalMusicVideoFrameCount(input.Analysis); n < frames {
		frames = n
	}
	if frames <= 0 {
		return DiagnosticPostFilterReceipt{}, fmt.Errorf("diagnostic CPU render has no frames")
	}
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-i", playlists[0], "-frames:v", strconv.Itoa(frames), "-f", "rawvideo", "-pix_fmt", "rgba", "pipe:1")
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return DiagnosticPostFilterReceipt{}, err
	}
	if err := cmd.Start(); err != nil {
		return DiagnosticPostFilterReceipt{}, err
	}
	frameBytes := w * h * 4
	reader := bufio.NewReaderSize(stdout, frameBytes)
	hash := sha256.New()
	var total int64
	count := 0
	for i := 0; i < frames; i++ {
		buf := make([]byte, frameBytes)
		if _, err := io.ReadFull(reader, buf); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return DiagnosticPostFilterReceipt{}, fmt.Errorf("capture frame %d: %w", i, err)
		}
		if err := onFrame(i, buf); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return DiagnosticPostFilterReceipt{}, err
		}
		hash.Write(buf)
		total += int64(len(buf))
		count++
	}
	if err := cmd.Wait(); err != nil {
		return DiagnosticPostFilterReceipt{}, err
	}
	finished := time.Now()
	return DiagnosticPostFilterReceipt{Stage: "hls-decode", Width: w, Height: h, Frames: count, Bytes: total, SHA256: hex.EncodeToString(hash.Sum(nil)), StartedAt: started.UTC().Format(time.RFC3339Nano), FinishedAt: finished.UTC().Format(time.RFC3339Nano)}, nil
}
