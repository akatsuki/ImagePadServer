//go:build nico_sprite_spike

package video

import (
	"context"
	"encoding/json"
	"fmt"
	"imagepadserver/internal/niconico"
	"imagepadserver/internal/nicorender"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in comparison. Uses the production encoder unchanged, with either the
// production binary renderer or the throwaway native sprite renderer.
func TestNicoSpritePipelineSpike(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_SPRITE_EXE") == "" {
		t.Skip("opt-in sprite probe")
	}
	t.Setenv("IMAGEPAD_DATA_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	data, err := os.ReadFile(os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot niconico.Snapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	duration := int64(6000)
	if v := os.Getenv("IMAGEPAD_NICO_PERF_DURATION_MS"); v != "" {
		duration, err = strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	options := nicorender.RenderOptions{Width: 1920, Height: 1080, DurationMs: duration, FPSNum: 30, FPSDen: 1, Transport: "binary", BatchFrames: 30, SparseFrames: true, ReuseUnchanged: true}
	enc := NicoEncodeOptions{Width: options.Width, Height: options.Height, DurationMs: duration, FPSNum: 30, FPSDen: 1, CRF: 26, AudioBitrate: "160k"}
	ffmpeg := os.Getenv("IMAGEPAD_NICO_PERF_FFMPEG")
	source := os.Getenv("IMAGEPAD_NICO_PERF_SOURCE")
	mode := os.Getenv("IMAGEPAD_NICO_SPRITE_MODE")
	if mode != "sprite" && mode != "binary" && mode != "direct" && mode != "stream" {
		t.Fatal("set IMAGEPAD_NICO_SPRITE_MODE=sprite, binary, direct or stream")
	}
	dir := t.TempDir()
	output := filepath.Join(dir, "out.mp4")
	var stats nicorender.SpriteSpikeStats
	start := time.Now()
	if mode == "binary" {
		options.Backend = "browser"
		_, _, err = EncodeNicoCommentedWithRenderer(ctx, ffmpeg, source, output, snapshot, options, enc)
	} else if mode == "direct" || mode == "stream" {
		stats, err = encodeSpriteDirect(ctx, ffmpeg, source, output, snapshot, options, enc, mode == "stream")
	} else {
		pipe := nicorender.NewFramePipe(2)
		done := make(chan error, 1)
		go func() {
			var e error
			stats, e = nicorender.RenderSpriteSpike(ctx, snapshot, options, pipe)
			pipe.Close(e)
			done <- e
		}()
		_, err = EncodeNicoCommented(ctx, ffmpeg, source, output, enc, pipe)
		if err != nil {
			cancel()
		}
		renderErr := <-done
		if err == nil {
			err = renderErr
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	encodeTime := time.Since(start)
	hlsStart := time.Now()
	if _, err = CreateNicoHLS(ctx, ffmpeg, output, filepath.Join(dir, "hls")); err != nil {
		t.Fatal(err)
	}
	hlsTime := time.Since(hlsStart)
	if data, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", filepath.Join(dir, "hls", "playlist.m3u8"), "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("HLS full decode: %v %s", err, data)
	}
	// Validation is outside the timing: full decode and the actual H.264 AU contract.
	md5, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", output, "-map", "0:v:0", "-f", "framemd5", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	decoded := 0
	for _, line := range strings.Split(string(md5), "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "#") {
			decoded++
		}
	}
	if decoded == 0 {
		t.Fatal("no decoded frames")
	}
	if dest := os.Getenv("IMAGEPAD_NICO_SPRITE_MD5"); dest != "" {
		if err = os.WriteFile(dest, md5, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if duration == 6000 && decoded != 180 {
		t.Fatalf("decoded %d, want 180", decoded)
	}
	stream, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", output, "-map", "0:v:0", "-c:v", "copy", "-bsf:v", "h264_metadata=aud=insert", "-f", "h264", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	au, slices := 0, 0
	for i := 0; i+4 < len(stream); i++ {
		start := 0
		if stream[i] == 0 && stream[i+1] == 0 && stream[i+2] == 1 {
			start = i + 3
		} else if stream[i] == 0 && stream[i+1] == 0 && stream[i+2] == 0 && stream[i+3] == 1 {
			start = i + 4
		}
		if start == 0 {
			continue
		}
		kind := stream[start] & 31
		if kind == 9 {
			if au > 0 && slices != 1 {
				t.Fatalf("AU %d slices=%d", au, slices)
			}
			au++
			slices = 0
		} else if kind == 1 || kind == 5 {
			slices++
		}
		i = start
	}
	if au != decoded || slices != 1 {
		t.Fatalf("AU count %d decoded %d final slices %d", au, decoded, slices)
	}
	t.Logf("mode=%s duration_ms=%d decoded=%d single_slice_AUs=%d encode_ms=%.1f hls_ms=%.1f total_ms=%.1f stats=%+v", mode, duration, decoded, au, float64(encodeTime.Microseconds())/1000, float64(hlsTime.Microseconds())/1000, float64((encodeTime+hlsTime).Microseconds())/1000, stats)
	if dest := os.Getenv("IMAGEPAD_NICO_SPRITE_OUTPUT"); dest != "" {
		input, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(input)
		e := f.Close()
		if err != nil || e != nil {
			t.Fatal(fmt.Sprint(err, e))
		}
	}
}
