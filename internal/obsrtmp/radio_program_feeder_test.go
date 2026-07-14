package obsrtmp

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestProgramTrackFeederTranslatesSourcePTSFromDecodedCounts(t *testing.T) {
	videoPTS, audioPTS := programSourcePTS(30)
	if videoPTS != time.Second || audioPTS != time.Second {
		t.Fatalf("frame 30 source PTS = video %s audio %s, want 1s/1s", videoPTS, audioPTS)
	}
}

func TestProgramTrackFeederDecodesCanonicalMP4ToRawInputs(t *testing.T) {
	videoArgs := strings.Join(programVideoDecoderArgs("radio-track-id.mp4", 12, 640, 360), "\x00")
	audioArgs := strings.Join(programAudioDecoderArgs("radio-track-id.mp4", 12), "\x00")
	for label, args := range map[string]string{"video": videoArgs, "audio": audioArgs} {
		if !strings.Contains(args, "-ss\x0012") || !strings.Contains(args, "-i\x00radio-track-id.mp4") {
			t.Fatalf("%s decoder lost canonical input/resume position: %s", label, args)
		}
		if strings.Contains(args, "copy") || strings.Contains(args, "-re") {
			t.Fatalf("%s decoder attempted copy/pacing instead of program-clock ingestion: %s", label, args)
		}
	}
	if !strings.Contains(videoArgs, "format=rgba") || !strings.Contains(videoArgs, "-f\x00rawvideo") {
		t.Fatalf("video decoder is not RGBA rawvideo: %s", videoArgs)
	}
	if !strings.Contains(audioArgs, "-ar\x0048000") || !strings.Contains(audioArgs, "-ac\x002") || !strings.Contains(audioArgs, "-f\x00s16le") {
		t.Fatalf("audio decoder is not 48 kHz stereo PCM: %s", audioArgs)
	}
}

func TestProgramTrackFeederPadsFinalPartialPCMFrameFromRealFFmpeg(t *testing.T) {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		t.Fatal(err)
	}
	fixture := writeUnalignedProgramFixture(t, ffmpeg)
	feeder := NewProgramTrackFeeder(t.TempDir(), 8, 8)
	feeder.ensureFFmpeg = func() (string, error) { return ffmpeg, nil }
	feeder.started = func(*exec.Cmd) func() { return func() {} }
	frames := make(chan ProgramSourceFrame, 8)

	if err := feeder.Run(context.Background(), fixture, 0, frames); err != nil {
		t.Fatalf("Run: %v", err)
	}
	close(frames)
	var got []ProgramSourceFrame
	for frame := range frames {
		got = append(got, frame)
	}
	if len(got) != 4 {
		t.Fatalf("decoded frame count = %d, want 4", len(got))
	}
	finalAudio := got[len(got)-1].AudioPCM
	partialBytes := 480 * programAudioChannels * programAudioBytes
	if allZero(finalAudio[:partialBytes]) {
		t.Fatal("final partial PCM samples were not emitted")
	}
	if !allZero(finalAudio[partialBytes:]) {
		t.Fatal("final partial PCM frame was not padded with silence")
	}
}

func TestProgramTrackFeederFailsWhenDecoderExitsAfterFullFrames(t *testing.T) {
	feeder := newProgramTrackFeederWithFailingVideoDecoder(t, 1, 1)
	frames := make(chan ProgramSourceFrame, 2)

	err := feeder.Run(context.Background(), "track.mp4", 0, frames)
	if err == nil {
		t.Fatal("Run succeeded after video decoder exited nonzero")
	}
	if !strings.Contains(err.Error(), "program video decoder") {
		t.Fatalf("Run error = %v, want video decoder failure", err)
	}
	close(frames)
	var got []ProgramSourceFrame
	for frame := range frames {
		got = append(got, frame)
	}
	if len(got) != 1 {
		t.Fatalf("decoded frames before decoder exit = %d, want 1", len(got))
	}
}

func TestProgramTrackFeederStopsPipeBlockedVideoWhenAudioEndsFirst(t *testing.T) {
	feeder := newProgramTrackFeederWithDecoderScenario(t, 1, 1, "audio-shorter-video-block")
	assertProgramTrackFeederCompletes(t, feeder)
}

func TestProgramTrackFeederStopsPipeBlockedAudioWhenVideoEndsFirst(t *testing.T) {
	feeder := newProgramTrackFeederWithDecoderScenario(t, 1, 1, "video-shorter-audio-block")
	assertProgramTrackFeederCompletes(t, feeder)
}

func TestProgramTrackFeederPreservesAudioFailureRacingVideoEOF(t *testing.T) {
	for iteration := 0; iteration < 128; iteration++ {
		feeder := newProgramTrackFeederWithDecoderScenario(t, 1, 1, "video-eof-audio-fails")
		assertProgramTrackFeederFailure(t, feeder, "program audio decoder")
	}
}

func TestProgramTrackFeederPreservesVideoFailureRacingAudioEOF(t *testing.T) {
	for iteration := 0; iteration < 128; iteration++ {
		feeder := newProgramTrackFeederWithDecoderScenario(t, 1, 1, "audio-eof-video-fails")
		assertProgramTrackFeederFailure(t, feeder, "program video decoder")
	}
}

func assertProgramTrackFeederCompletes(t *testing.T, feeder *ProgramTrackFeeder) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan ProgramSourceFrame, 4)
	result := make(chan error, 1)
	go func() { result <- feeder.Run(ctx, "track.mp4", 0, frames) }()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	// The helper intentionally fills a pipe beyond its platform buffer. Under
	// Windows process scheduling, draining and reaping the peer can exceed
	// 500ms even when cancellation is progressing normally.
	case <-time.After(2 * time.Second):
		cancel()
		<-result
		t.Fatal("Run waited for the pipe-blocked peer decoder")
	}
}

func assertProgramTrackFeederFailure(t *testing.T, feeder *ProgramTrackFeeder, want string) {
	t.Helper()
	frames := make(chan ProgramSourceFrame, 4)
	err := feeder.Run(context.Background(), "track.mp4", 0, frames)
	if err == nil {
		t.Fatal("Run succeeded after peer decoder exited nonzero")
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Run error = %v, want %s", err, want)
	}
}

func newProgramTrackFeederWithFailingVideoDecoder(t *testing.T, width, height int) *ProgramTrackFeeder {
	return newProgramTrackFeederWithDecoderScenario(t, width, height, "video-fails")
}

func newProgramTrackFeederWithDecoderScenario(t *testing.T, width, height int, scenario string) *ProgramTrackFeeder {
	t.Helper()
	feeder := NewProgramTrackFeeder(t.TempDir(), width, height)
	feeder.ensureFFmpeg = func() (string, error) { return "test-helper", nil }
	feeder.started = func(*exec.Cmd) func() { return func() {} }
	feeder.command = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		role := "audio"
		for _, arg := range args {
			if arg == "0:v:0" {
				role = "video"
				break
			}
		}
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestProgramTrackFeederDecoderExitHelperProcess", "--")
		cmd.Env = append(os.Environ(),
			"IMAGEPAD_PROGRAM_DECODER_HELPER=1",
			"IMAGEPAD_PROGRAM_DECODER_ROLE="+role,
			"IMAGEPAD_PROGRAM_DECODER_SCENARIO="+scenario,
			"IMAGEPAD_PROGRAM_DECODER_VIDEO_BYTES="+strconv.Itoa(width*height*4),
		)
		return cmd
	}
	return feeder
}

func TestProgramTrackFeederDecoderExitHelperProcess(t *testing.T) {
	if os.Getenv("IMAGEPAD_PROGRAM_DECODER_HELPER") != "1" {
		return
	}
	role := os.Getenv("IMAGEPAD_PROGRAM_DECODER_ROLE")
	scenario := os.Getenv("IMAGEPAD_PROGRAM_DECODER_SCENARIO")
	if role == "video" {
		videoBytes, err := strconv.Atoi(os.Getenv("IMAGEPAD_PROGRAM_DECODER_VIDEO_BYTES"))
		if err != nil || videoBytes <= 0 {
			os.Exit(2)
		}
		switch scenario {
		case "audio-shorter-video-block":
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{0x7f}, videoBytes*2))
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{0x7f}, 128*1024))
		case "video-shorter-audio-block", "video-fails", "video-eof-audio-fails":
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{0x7f}, videoBytes))
		case "audio-eof-video-fails":
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{0x7f}, videoBytes*2))
		}
		if scenario == "video-fails" || scenario == "audio-eof-video-fails" {
			os.Exit(17)
		}
		os.Exit(0)
	}
	if role == "audio" {
		audioFrameBytes := programSamplesPerFrame * programAudioChannels * programAudioBytes
		switch scenario {
		case "video-shorter-audio-block":
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{0x01}, audioFrameBytes*2))
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{0x01}, 128*1024))
		default:
			_, _ = os.Stdout.Write(bytes.Repeat([]byte{0x01}, audioFrameBytes))
		}
		if scenario == "video-eof-audio-fails" {
			os.Exit(17)
		}
		os.Exit(0)
	}
	_, _ = io.WriteString(os.Stderr, "unknown program decoder helper role")
	os.Exit(2)
}

func writeUnalignedProgramFixture(t *testing.T, ffmpeg string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "unaligned.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=8x8:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=0.11",
		"-map", "0:v:0", "-map", "1:a:0", "-frames:v", "4",
		"-c:v", "ffv1", "-c:a", "pcm_s16le", path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate unaligned fixture: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return path
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}
