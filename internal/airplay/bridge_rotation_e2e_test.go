package airplay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

// TestAirPlayBridgeRotationUsesFixedCanvas reproduces a portrait->landscape
// mirror rotation by feeding the relay->bridge ingest two sequential H.264
// senders at different resolutions (320x568 then 568x320). The bridge stays
// alive across the SPS/PPS change and the AirPlay output filter keeps the FLV
// canvas fixed at 1920x1080. This test drives the real manager.monitor() loop
// end to end and asserts the final FLV contains fixed-canvas frames with
// square pixels (SAR 1:1).
func TestAirPlayBridgeRotationUsesFixedCanvas(t *testing.T) {
	ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
	ffprobe := strings.TrimSpace(os.Getenv("IMAGEPAD_FFPROBE"))
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("set IMAGEPAD_FFMPEG and IMAGEPAD_FFPROBE to run the rotation end-to-end test")
	}
	for _, p := range []string{ffmpeg, ffprobe} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("pinned binary %q not accessible: %v", p, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	// The monitor deletes sdpDir on exit; keep the FLV in a separate directory
	// so it survives teardown for probing.
	sdpDir := t.TempDir()
	sdpPath := filepath.Join(sdpDir, "session.sdp")
	if err := os.WriteFile(sdpPath, []byte(BuildSessionSDP(videoOut, audioOut)), 0600); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	flvPath := filepath.Join(outDir, "rotation.flv")

	encoder := video.CPUVideoEncoder(video.EncoderLowLatency)
	preset := video.ResolveQualityForUpload("1080", 20, 0)
	bridgeArgs := BuildBridgeArgs(sdpPath, flvPath, encoder, preset)
	// -y lets the respawned bridge truncate the FLV its killed predecessor
	// wrote; -flush_packets 1 flushes each tag so a killed bridge still leaves
	// a readable (truncated) file for probing.
	last := bridgeArgs[len(bridgeArgs)-1]
	bridgeArgs = append(bridgeArgs[:len(bridgeArgs)-1], "-y", "-flush_packets", "1", last)

	bridge, bridgeLog, untrack, err := startBridgeProcess(ctx, ffmpeg, bridgeArgs)
	if err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	defer untrack()
	scheduleBridgeDecoderRefresh(ctx, relay)

	// Dummy receiver: the monitor only needs a live process whose Wait() blocks
	// until it is killed. An infinite lavfi source fills that role without a
	// UxPlay binary.
	receiverLog := &limitedBuffer{max: 8192}
	receiver := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=black:s=16x16:r=1",
		"-f", "null", "-")
	receiver.Stdout = receiverLog
	receiver.Stderr = receiverLog
	if err := receiver.Start(); err != nil {
		t.Fatalf("start dummy receiver: %v", err)
	}

	m := New(nil)
	done := make(chan struct{})
	go m.monitor(ctx, cancel, done, relay, audioRelay, bridge, receiver, ffmpeg, bridgeArgs, bridgeLog, receiverLog, untrack, sdpDir)

	send := func(size string, seconds string) {
		cmd := exec.CommandContext(ctx, ffmpeg,
			"-hide_banner", "-loglevel", "warning",
			"-re", "-f", "lavfi", "-i", "testsrc2=size="+size+":rate=30",
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
			"-pix_fmt", "yuv420p", "-g", "30",
			"-t", seconds, "-f", "rtp", fmt.Sprintf("rtp://127.0.0.1:%d", videoIn))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("video sender %s: %v\n%s", size, err, out)
		}
	}

	// The L16 relay emits silence when no audio sender is present, so the
	// bridge still receives an audio stream; audio is orthogonal to the
	// video-rotation assertion and a standalone lavfi audio sender
	// collides with the relay's 0.0.0.0 bind on Windows.
	// Portrait for 2s, then landscape for 3s.
	send("320x568", "2")
	send("568x320", "3")

	// Let the long-lived bridge drain and encode both orientations.
	time.Sleep(3 * time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("manager monitor did not shut down after cancel")
	}

	frames := probeRotationFrames(t, ffprobe, flvPath)
	t.Logf("FLV frame resolutions: %v", frames)
	if frames["1920,1080"] == 0 {
		t.Fatalf("no fixed-canvas (1920x1080) frames after rotation; got %v\nbridge log:\n%s", frames, bridgeLog.String())
	}
	for dimensions := range frames {
		if dimensions != "1920,1080" {
			t.Fatalf("canvas changed after rotation: %v", frames)
		}
	}
	sars := probeRotationSAR(t, ffprobe, flvPath)
	t.Logf("FLV frame SAR values: %v", sars)
	for _, s := range sars {
		if s != "1:1" && s != "1/1" {
			t.Fatalf("output FLV sample aspect ratio = %v; want 1:1 (square pixels)", sars)
		}
	}
}

func probeRotationFrames(t *testing.T, ffprobe, flvPath string) map[string]int {
	t.Helper()
	cmd := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=width,height", "-of", "json", flvPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe frame=width,height: %v\n%s", err, out)
	}
	var result struct {
		Frames []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	frames := map[string]int{}
	for _, frame := range result.Frames {
		frames[fmt.Sprintf("%d,%d", frame.Width, frame.Height)]++
	}
	return frames
}

func probeRotationSAR(t *testing.T, ffprobe, flvPath string) []string {
	t.Helper()
	cmd := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=sample_aspect_ratio", "-of", "json", flvPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe frame=sample_aspect_ratio: %v\n%s", err, out)
	}
	var result struct {
		Frames []struct {
			SAR string `json:"sample_aspect_ratio"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Frames) == 0 {
		t.Fatal("no decoded frames for SAR check")
	}
	sars := make([]string, 0, len(result.Frames))
	for _, frame := range result.Frames {
		sars = append(sars, frame.SAR)
	}
	return sars
}
