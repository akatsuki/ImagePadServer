package obsrtmp

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestPlaylistGPUMuxArgsUseGPUEncodedVideoAndPCMInputs(t *testing.T) {
	args, err := playlistGPUMuxArgs(640, 360, 30, 48_000, 2, 5_500_000_000)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{
		{"-f", "h264", "-i", "pipe:3"},
		{"-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "pipe:4"},
		{"-map", "0:v:0", "-map", "1:a:0"},
		{"-c:v", "copy", "-bsf:v", "h264_metadata=video_full_range_flag=0:colour_primaries=1:transfer_characteristics=1:matrix_coefficients=1", "-c:a", "aac"},
		{"-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv"},
		{"-bsf:v", "h264_metadata=video_full_range_flag=0:colour_primaries=1:transfer_characteristics=1:matrix_coefficients=1"},
		{"-f", "mpegts", "pipe:1"},
		{"-flush_packets", "1", "-f", "mpegts", "pipe:1"},
	} {
		if !containsSequence(args, want) {
			t.Fatalf("args %v does not contain %v", args, want)
		}
	}
	if !containsSequence(args, []string{"-output_ts_offset", "5.500000000"}) {
		t.Fatalf("args %v does not preserve authoritative PTS offset", args)
	}
	if slices.Contains(args, "-f") && slices.Contains(args, "rawvideo") {
		t.Fatal("GPU mux must not accept CPU RGBA/rawvideo input")
	}
}

func TestApplyPlaylistGPUAudioFadePCMUsesAuthoritativeLinearPlan(t *testing.T) {
	samples := make([]byte, 4*2)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint16(samples[i*2:], uint16(int16(1000)))
	}
	faded, err := ApplyPlaylistGPUAudioFadePCM(samples, 0, 4, 1, video.AudioFadePlan{StartPTSNs: 0, DurationNS: 1_000_000_000, Curve: "linear"})
	if err != nil {
		t.Fatal(err)
	}
	for i, expected := range []int16{1000, 750, 500, 250} {
		got := int16(binary.LittleEndian.Uint16(faded[i*2:]))
		if got != expected {
			t.Fatalf("sample %d = %d, want %d", i, got, expected)
		}
	}
}

func TestPlaylistGPUMuxArgsRejectInvalidContract(t *testing.T) {
	cases := [][5]int{{0, 360, 30, 48_000, 2}, {640, 0, 30, 48_000, 2}, {640, 360, 0, 48_000, 2}, {640, 360, 30, 0, 2}, {640, 360, 30, 48_000, 0}}
	for _, tc := range cases {
		if _, err := playlistGPUMuxArgs(tc[0], tc[1], tc[2], tc[3], tc[4], 0); err == nil {
			t.Fatalf("invalid contract %#v was accepted", tc)
		}
	}
}

func TestPlaylistGPUMuxArgsAcceptWindowsSafeLoopbackInputs(t *testing.T) {
	args, err := playlistGPUMuxArgsWithInputs(640, 360, 30, 48_000, 2, 0, "tcp://127.0.0.1:41001", "tcp://127.0.0.1:41002")
	if err != nil {
		t.Fatal(err)
	}
	if !containsSequence(args, []string{"-f", "h264", "-i", "tcp://127.0.0.1:41001"}) {
		t.Fatalf("video loopback input missing: %v", args)
	}
	if !containsSequence(args, []string{"-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "tcp://127.0.0.1:41002"}) {
		t.Fatalf("audio loopback input missing: %v", args)
	}
}

func TestStartPlaylistGPUMuxProcessFailsClosedWithoutFFmpeg(t *testing.T) {
	if _, err := StartPlaylistGPUMuxProcess("", 640, 360, 30, 48_000, 2, 0); err == nil {
		t.Fatal("missing FFmpeg executable must fail closed")
	}
}

func TestCopyPlaylistGPUMuxOutputDrainsMPEGTSOnlyToProvidedSink(t *testing.T) {
	var sink bytes.Buffer
	process := &PlaylistGPUMuxProcess{output: io.NopCloser(strings.NewReader("mpegts"))}
	if err := CopyPlaylistGPUMuxOutput(&sink, process); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "mpegts" {
		t.Fatalf("copied output = %q, want mpegts", sink.String())
	}
}

func TestPlaylistGPUMuxProcessCloseInputsSendsEOFWithoutKillingProcess(t *testing.T) {
	videoPeer, videoInput := net.Pipe()
	audioPeer, audioInput := net.Pipe()
	process := &PlaylistGPUMuxProcess{video: videoInput, audio: audioInput}
	if err := process.CloseInputs(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(videoPeer); err != nil {
		t.Fatalf("video input did not reach EOF: %v", err)
	}
	if _, err := io.ReadAll(audioPeer); err != nil {
		t.Fatalf("audio input did not reach EOF: %v", err)
	}
	_ = videoPeer.Close()
	_ = audioPeer.Close()
}

func TestPlaylistGPUMuxProcessCloseVideoInputKeepsAudioInputOpen(t *testing.T) {
	videoPeer, videoInput := net.Pipe()
	audioPeer, audioInput := net.Pipe()
	process := &PlaylistGPUMuxProcess{video: videoInput, audio: audioInput}

	if err := process.CloseVideoInput(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(videoPeer); err != nil {
		t.Fatalf("video input did not reach EOF: %v", err)
	}

	process.mu.Lock()
	videoClosed := process.video == nil
	audioOpen := process.audio != nil
	inputsClosed := process.inputsClosed
	process.mu.Unlock()
	if !videoClosed || !audioOpen || inputsClosed {
		t.Fatalf("CloseVideoInput state = video_closed=%t audio_open=%t inputs_closed=%t", videoClosed, audioOpen, inputsClosed)
	}

	if err := process.CloseInputs(); err != nil {
		t.Fatal(err)
	}
	_ = videoPeer.Close()
	_ = audioPeer.Close()
}

func TestPlaylistGPUMuxProcessCloseInputsCancelsPendingAudioDial(t *testing.T) {
	var canceled atomic.Bool
	process := &PlaylistGPUMuxProcess{audioCancel: func() { canceled.Store(true) }}
	if err := process.CloseInputs(); err != nil {
		t.Fatal(err)
	}
	if !canceled.Load() {
		t.Fatal("CloseInputs did not cancel the pending audio dial")
	}
}

func TestPlaylistGPUMuxProcessCloseInterruptsBlockedAudioWrite(t *testing.T) {
	peer, input := net.Pipe()
	defer peer.Close()
	process := &PlaylistGPUMuxProcess{
		audio:      input,
		audioReady: make(chan error, 1),
	}
	process.audioReady <- nil

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- process.WriteAudioPCM([]byte{0, 0}, 1)
	}()

	select {
	case err := <-writeDone:
		t.Fatalf("audio write returned before close: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- process.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Close blocked behind an in-flight audio write")
	}

	select {
	case <-writeDone:
	case <-time.After(1 * time.Second):
		t.Fatal("blocked audio write was not interrupted by Close")
	}
}

func TestDialPlaylistGPUMuxURLHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dialPlaylistGPUMuxURL(ctx, "tcp://127.0.0.1:1?listen=1"); err == nil {
		t.Fatal("canceled mux dial unexpectedly succeeded")
	}
}

func TestStartPlaylistGPUMuxBridgeRequiresPublisherSink(t *testing.T) {
	if _, err := StartPlaylistGPUMuxBridge(context.Background(), "ffmpeg", nil, 640, 360, 30, 48_000, 2, 0); err == nil {
		t.Fatal("GPU mux bridge must fail closed without a publisher sink")
	}
}

func containsSequence(haystack, needle []string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}
