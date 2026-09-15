package airplay

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

// TestAirPlayRelayBridgeEndToEnd drives the production ingest path an iOS
// mirroring session uses, with UxPlay's RTP output replaced by an FFmpeg
// sender. The H.264 and L16 relays re-packetize and re-clock the incoming
// streams; the FFmpeg bridge then decodes the SDP session, re-encodes through
// the CPU encoder, and writes FLV. The portrait content must retain its aspect
// ratio inside the fixed 1920x1080 canvas, with AAC 48 kHz.
func TestAirPlayRelayBridgeEndToEnd(t *testing.T) {
	ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
	ffprobe := strings.TrimSpace(os.Getenv("IMAGEPAD_FFPROBE"))
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("set IMAGEPAD_FFMPEG and IMAGEPAD_FFPROBE to run the AirPlay relay->bridge end-to-end test")
	}
	for _, p := range []string{ffmpeg, ffprobe} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("pinned binary %q not accessible: %v", p, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	videoIn, videoOut, audioIn, audioOut, err := reserveRTPPorts()
	if err != nil {
		t.Fatalf("reserve RTP ports: %v", err)
	}

	audioRelay, err := startL16RTPRelay(ctx, audioIn, audioOut)
	if err != nil {
		t.Fatalf("start L16 relay: %v", err)
	}
	defer audioRelay.Close()

	relay, err := startH264RTPRelay(ctx, videoIn, videoOut, audioRelay.NotifyVideoActivity)
	if err != nil {
		t.Fatalf("start H.264 relay: %v", err)
	}
	defer relay.Close()

	dir := t.TempDir()
	sdpPath := filepath.Join(dir, "session.sdp")
	if err := os.WriteFile(sdpPath, []byte(BuildSessionSDP(videoOut, audioOut)), 0600); err != nil {
		t.Fatal(err)
	}

	encoder := video.CPUVideoEncoder(video.EncoderLowLatency)
	preset := video.ResolveQualityForUpload("1080", 20, 0)
	flvPath := filepath.Join(dir, "out.flv")

	bridgeArgs := BuildBridgeArgs(sdpPath, flvPath, encoder, preset)
	// Cap the bridge's output duration so it exits cleanly once the senders
	// stop. A clean exit releases its UDP sockets deterministically; a forced
	// kill leaves them to async Windows cleanup and causes WSAEADDRINUSE on
	// back-to-back runs.
	bridgeArgs = append(bridgeArgs[:len(bridgeArgs)-1], append([]string{"-t", "6"}, bridgeArgs[len(bridgeArgs)-1])...)

	bridge, bridgeLog, untrack, err := startBridgeProcess(ctx, ffmpeg, bridgeArgs)
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	defer untrack()
	scheduleBridgeDecoderRefresh(ctx, relay)

	// Two independent senders: video (H.264, PT=96) and audio (L16, PT=96).
	// Keeping them in separate FFmpeg processes avoids a dynamic PT collision
	// where the RTP muxer reassigns the audio payload type to 97. The senders
	// outrun the bridge's -t 6 so the bridge always has input up to its cap.
	senders := []*exec.Cmd{
		exec.CommandContext(ctx, ffmpeg,
			"-hide_banner", "-loglevel", "warning",
			"-re", "-f", "lavfi", "-i", "testsrc2=size=720x1280:rate=30",
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
			"-pix_fmt", "yuv420p", "-g", "30",
			"-t", "8", "-f", "rtp", fmt.Sprintf("rtp://127.0.0.1:%d", videoIn)),
		exec.CommandContext(ctx, ffmpeg,
			"-hide_banner", "-loglevel", "warning",
			"-re", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
			"-c:a", "pcm_s16be", "-ar", "44100", "-ac", "2", "-payload_type", "96",
			"-t", "8", "-f", "rtp", fmt.Sprintf("rtp://127.0.0.1:%d", audioIn)),
	}

	var wg sync.WaitGroup
	for _, sender := range senders {
		wg.Add(1)
		go func(cmd *exec.Cmd) {
			defer wg.Done()
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("sender ffmpeg: %v\n%s", err, out)
			}
		}(sender)
	}
	wg.Wait()

	// The bridge self-terminates at -t 6; wait for the clean exit.
	_ = bridge.Wait()

	probe := func(stream, entries string) string {
		cmd := exec.Command(ffprobe, "-v", "error", "-select_streams", stream,
			"-show_entries", entries, "-of", "csv=p=0", flvPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("ffprobe %s: %v\n%s\nbridge log:\n%s", stream, err, out, bridgeLog.String())
		}
		return strings.TrimSpace(string(out))
	}

	videoInfo := probe("v:0", "stream=codec_name,width,height,pix_fmt")
	audioInfo := probe("a:0", "stream=codec_name,sample_rate,channels")
	t.Logf("relay->bridge FLV video=%q audio=%q", videoInfo, audioInfo)

	if videoInfo != "h264,1920,1080,yuv420p" {
		t.Fatalf("video stream = %q, want fixed h264 1920x1080 yuv420p canvas", videoInfo)
	}
	if !strings.Contains(audioInfo, "aac") || !strings.Contains(audioInfo, "48000") {
		t.Fatalf("audio stream = %q, want aac 48000 Hz", audioInfo)
	}
	// Decode actual pixels to verify the portrait was fitted, not stretched.
	frame, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-ss", "2", "-i", flvPath,
		"-frames:v", "1", "-vf", "scale=160:90", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1").Output()
	if err != nil || len(frame) != 160*90*3 {
		t.Fatalf("decode portrait sample: %v, bytes=%d", err, len(frame))
	}
	left, right := 160, -1
	for x := 0; x < 160; x++ {
		sum := 0
		for y := 0; y < 90; y++ {
			for c := 0; c < 3; c++ {
				sum += int(frame[(y*160+x)*3+c])
			}
		}
		if sum/(90*3) > 10 {
			if left == 160 {
				left = x
			}
			right = x
		}
	}
	if left < 52 || left > 57 || right-left+1 < 48 || right-left+1 > 54 {
		t.Fatalf("portrait content bounds %d..%d do not preserve 720:1280 aspect on fixed canvas", left, right)
	}

	// Close the relays so their input sockets are released for the next run.
	audioRelay.Close()
	relay.Close()
	// Give the OS a beat to release the bridge/relay UDP sockets so a
	// back-to-back run (e.g. -count=N) does not hit WSAEADDRINUSE.
	time.Sleep(2 * time.Second)
}
