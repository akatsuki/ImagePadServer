package airplay

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestGStreamerBridgeEndToEnd is an opt-in media-level test for the native
// bridge. It deliberately sends SPS/PPS in-band, as UxPlay's config-interval
// RTP branch does, and verifies that the single Matroska stream contains both
// decoded tracks.
func TestGStreamerBridgeEndToEnd(t *testing.T) {
	bridgePath := strings.TrimSpace(os.Getenv("IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE"))
	ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
	ffprobe := strings.TrimSpace(os.Getenv("IMAGEPAD_FFPROBE"))
	if bridgePath == "" || ffmpeg == "" || ffprobe == "" {
		t.Skip("set IMAGEPAD_AIRPLAY_GSTREAMER_BRIDGE, IMAGEPAD_FFMPEG and IMAGEPAD_FFPROBE to run the native bridge E2E test")
	}
	for _, path := range []string{bridgePath, ffmpeg, ffprobe} {
		if _, err := os.Stat(path); err != nil {
			t.Skipf("test binary %q is not accessible: %v", path, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	videoIn, videoOut, audioIn, audioOut, err := reserveRTPPorts()
	if err != nil {
		t.Fatalf("reserve RTP ports: %v", err)
	}
	audioRelay, err := startL16RTPRelay(ctx, audioIn, audioOut)
	if err != nil {
		t.Fatalf("start audio relay: %v", err)
	}
	defer audioRelay.Close()
	videoRelay, err := startH264RTPRelay(ctx, videoIn, videoOut, audioRelay.NotifyVideoActivity)
	if err != nil {
		t.Fatalf("start video relay: %v", err)
	}
	defer videoRelay.Close()
	scheduleBridgeDecoderRefresh(ctx, videoRelay)

	bridge := exec.CommandContext(ctx, bridgePath,
		"--video-port", strconv.Itoa(videoOut),
		"--audio-port", strconv.Itoa(audioOut),
		"--stdout")
	bridgeLog := &limitedBuffer{max: 8192}
	configureGStreamerBridgeProcess(bridge, bridgeLog, bridgePath)
	bridgeStdout, err := bridge.StdoutPipe()
	if err != nil {
		t.Fatalf("bridge stdout pipe: %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "bridge.mkv")
	consumer := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "matroska", "-i", "pipe:0",
		"-t", "3", "-map", "0:v:0", "-map", "0:a:0",
		"-c", "copy", "-f", "matroska", outputPath)
	consumer.Stdin = bridgeStdout
	consumerLog := &limitedBuffer{max: 8192}
	configureProcess(consumer, consumerLog)
	if err := consumer.Start(); err != nil {
		t.Fatalf("start Matroska consumer: %v", err)
	}
	if err := bridge.Start(); err != nil {
		_ = consumer.Process.Kill()
		_ = consumer.Wait()
		t.Fatalf("start GStreamer bridge: %v", err)
	}

	senders := []*exec.Cmd{
		exec.CommandContext(ctx, ffmpeg,
			"-hide_banner", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", "testsrc2=size=720x1280:rate=30",
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
			"-x264-params", "repeat-headers=1", "-pix_fmt", "yuv420p", "-g", "30",
			"-t", "5", "-f", "rtp", fmt.Sprintf("rtp://127.0.0.1:%d", videoIn)),
		exec.CommandContext(ctx, ffmpeg,
			"-hide_banner", "-loglevel", "error", "-re",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
			"-c:a", "pcm_s16be", "-ar", "44100", "-ac", "2", "-payload_type", "96",
			"-t", "5", "-f", "rtp", fmt.Sprintf("rtp://127.0.0.1:%d", audioIn)),
	}
	var senderWG sync.WaitGroup
	for _, sender := range senders {
		senderWG.Add(1)
		go func(command *exec.Cmd) {
			defer senderWG.Done()
			if output, runErr := command.CombinedOutput(); runErr != nil {
				t.Errorf("RTP sender failed: %v\n%s", runErr, output)
			}
		}(sender)
	}
	senderWG.Wait()
	inputPackets, outputPackets, silencePackets := audioRelay.Stats()
	t.Logf("audio relay stats input=%d output=%d silence=%d", inputPackets, outputPackets, silencePackets)

	// The native bridge intentionally has no EOS while an AirPlay session is
	// alive. Close its stdout after the finite fixture so FFmpeg can finalize
	// the stream and the probe can inspect it.
	_ = bridge.Process.Kill()
	bridgeErr := bridge.Wait()
	consumerErr := consumer.Wait()
	if consumerErr != nil {
		info, _ := os.Stat(outputPath)
		var outputSize int64
		if info != nil {
			outputSize = info.Size()
		}
		t.Fatalf("Matroska consumer failed: %v\noutput=%d bytes\nbridgeErr=%v\nconsumer=%s\nbridge=%s", consumerErr, outputSize, bridgeErr, consumerLog.String(), bridgeLog.String())
	}
	// bridgeErr is expected to be non-nil because the finite fixture closes the
	// live bridge with Kill rather than sending an AirPlay session EOS.
	_ = bridgeErr

	probe := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,width,height,sample_rate,channels", "-of", "csv=p=0", outputPath)
	probeOutput, err := probe.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe failed: %v\n%s\nbridge=%s", err, probeOutput, bridgeLog.String())
	}
	result := string(probeOutput)
	if !strings.Contains(result, "rawvideo,video,1920,1080") {
		t.Fatalf("expected 1920x1080 raw video stream, got %q\nbridge=%s", result, bridgeLog.String())
	}
	if !strings.Contains(result, "pcm_s16le,audio,48000,2") {
		t.Fatalf("expected 48kHz stereo PCM stream, got %q\nbridge=%s", result, bridgeLog.String())
	}
}
