package obsrtmp

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestProgramClockMonotonicAcrossTransitionsAndIdle(t *testing.T) {
	clock := NewProgramClock()

	var previous ProgramTick
	for frame := 0; frame < 180; frame++ {
		tick := clock.Next()
		if frame > 0 {
			if tick.VideoPTS <= previous.VideoPTS {
				t.Fatalf("video PTS did not advance at frame %d: previous=%s current=%s", frame, previous.VideoPTS, tick.VideoPTS)
			}
			if tick.AudioPTS <= previous.AudioPTS {
				t.Fatalf("audio PTS did not advance at frame %d: previous=%s current=%s", frame, previous.AudioPTS, tick.AudioPTS)
			}
		}
		if tick.AudioSamples != 1600 {
			t.Fatalf("frame %d audio samples = %d, want 1600", frame, tick.AudioSamples)
		}
		previous = tick

		// Track boundaries and idle periods do not create a new clock.
		if frame == 59 || frame == 119 {
			clock.MarkSourceTransition()
		}
	}

	if got, want := previous.VideoPTS, time.Duration(179)*time.Second/30; got != want {
		t.Fatalf("last video PTS = %s, want %s", got, want)
	}
	if got, want := previous.AudioPTS, time.Duration(179*1600)*time.Second/48000; got != want {
		t.Fatalf("last audio PTS = %s, want %s", got, want)
	}
}

func TestPlaylistAudioTeeRemovalWaitsForInFlightWrite(t *testing.T) {
	m := &RadioManager{}
	started := make(chan struct{})
	release := make(chan struct{})
	m.SetPlaylistAudioTee(func([]byte, time.Duration) error {
		close(started)
		<-release
		return nil
	})

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- m.writePlaylistAudioTee([]byte{0, 0}, 0)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("playlist audio tee did not start")
	}

	removeDone := make(chan struct{})
	go func() {
		m.SetPlaylistAudioTee(nil)
		close(removeDone)
	}()
	select {
	case <-removeDone:
		t.Fatal("playlist audio tee removal returned while a write was in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("playlist audio tee write: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("playlist audio tee write did not finish")
	}
	select {
	case <-removeDone:
	case <-time.After(time.Second):
		t.Fatal("playlist audio tee removal did not finish after the write")
	}
}

type recordingProgramEncoder struct {
	videoPTS []time.Duration
	audioPTS []time.Duration
	video    [][]byte
	audio    [][]byte
	videoErr error
}

func (e *recordingProgramEncoder) WriteVideoRGBA(frame []byte, pts time.Duration) error {
	e.videoPTS = append(e.videoPTS, pts)
	e.video = append(e.video, append([]byte(nil), frame...))
	return e.videoErr
}

func (e *recordingProgramEncoder) WriteAudioPCM(samples []byte, pts time.Duration) error {
	e.audioPTS = append(e.audioPTS, pts)
	e.audio = append(e.audio, append([]byte(nil), samples...))
	return nil
}

func TestProgramCompositorFallsBackAndDisablesOverlayAfterOneRetry(t *testing.T) {
	encoder := &recordingProgramEncoder{}
	renders := 0
	compositor := NewProgramCompositor(2, 1, encoder, func(width, height int, elapsed time.Duration, snapshot OverlaySnapshot) ([]byte, error) {
		renders++
		return nil, errors.New("overlay failed")
	})
	compositor.SetOverlay(OverlaySnapshot{
		Mode:          OverlayModeNotifications,
		Next:          &OverlayTrack{ID: "next", Title: "Next"},
		ShowNextFrom:  0,
		ShowNextUntil: time.Minute,
		Revision:      1,
	})

	source := ProgramSourceFrame{
		VideoRGBA: bytes.Repeat([]byte{0xff}, 2*1*4),
		AudioPCM:  bytes.Repeat([]byte{0x7f}, 1600*2*2),
	}
	if err := compositor.WriteTick(NewProgramClock().Next(), source); err != nil {
		t.Fatal(err)
	}
	if err := compositor.WriteTick(ProgramTick{VideoPTS: time.Second / 30, AudioPTS: time.Second / 30, AudioSamples: 1600}, source); err != nil {
		t.Fatal(err)
	}
	if err := compositor.WriteTick(ProgramTick{VideoPTS: 2 * time.Second / 30, AudioPTS: 2 * time.Second / 30, AudioSamples: 1600}, source); err != nil {
		t.Fatal(err)
	}

	if renders != 2 {
		t.Fatalf("overlay renders = %d, want one attempt plus one retry", renders)
	}
	if !compositor.OverlayDisabled() {
		t.Fatal("overlay remained enabled after retry failed")
	}
	black := make([]byte, 2*1*4)
	if !bytes.Equal(encoder.video[0], black) || !bytes.Equal(encoder.video[1], black) {
		t.Fatal("overlay failures did not bridge with generated black frames")
	}
	if !bytes.Equal(encoder.video[2], source.VideoRGBA) {
		t.Fatal("output did not continue without the disabled overlay")
	}
	if len(encoder.audioPTS) != 3 {
		t.Fatalf("audio writes = %d, want 3", len(encoder.audioPTS))
	}
}

func TestProgramCompositorTreatsEncoderFailureAsFatal(t *testing.T) {
	want := errors.New("encoder dead")
	encoder := &recordingProgramEncoder{videoErr: want}
	compositor := NewProgramCompositor(2, 1, encoder, nil)

	err := compositor.WriteTick(NewProgramClock().Next(), ProgramSourceFrame{})
	if !errors.Is(err, want) {
		t.Fatalf("WriteTick error = %v, want encoder failure", err)
	}
}

func TestProgramCompositorUsesGPUFrameRenderer(t *testing.T) {
	encoder := &recordingProgramEncoder{}
	compositor := NewProgramCompositor(2, 1, encoder, nil)
	compositor.SetGPUFrameRenderer(func(width, height int, _ ProgramTick, _ ProgramSourceFrame) ([]byte, error) {
		return []byte{1, 2, 3, 255, 4, 5, 6, 255}, nil
	})
	if err := compositor.WriteTick(NewProgramClock().Next(), ProgramSourceFrame{}); err != nil {
		t.Fatal(err)
	}
	got := encoder.video[0]
	if len(got) != 8 || got[0] != 1 || got[4] != 4 {
		t.Fatalf("gpu frame not forwarded: %v", got)
	}
}

func TestProgramCompositorAudioTeeIsExplicitAndPreservesPTS(t *testing.T) {
	encoder := &recordingProgramEncoder{}
	var teeSamples []byte
	var teePTS time.Duration
	compositor := NewProgramCompositor(2, 1, encoder, nil)
	compositor.SetAudioTee(func(samples []byte, pts time.Duration) error {
		teeSamples = append([]byte(nil), samples...)
		teePTS = pts
		return nil
	})
	source := ProgramSourceFrame{VideoRGBA: bytes.Repeat([]byte{0x11}, 8), AudioPCM: bytes.Repeat([]byte{0x22}, 16)}
	if err := compositor.WriteTick(ProgramTick{VideoPTS: 7 * time.Second, AudioPTS: 9 * time.Second, AudioSamples: 4}, source); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(teeSamples, source.AudioPCM) || teePTS != 9*time.Second {
		t.Fatalf("audio tee = (%x, %s), want (%x, %s)", teeSamples, teePTS, source.AudioPCM, 9*time.Second)
	}
}

func TestRadioManagerSuppressesCPUProgramOutputWhileGPUOwnsPublisher(t *testing.T) {
	m := NewRadioManager(t.TempDir(), "127.0.0.1", nil, RadioCallbacks{})
	var sink bytes.Buffer
	if n, err := m.writeProgramOutput(&sink, []byte("cpu-before")); err != nil || n != len("cpu-before") {
		t.Fatalf("normal output = (%d, %v)", n, err)
	}
	m.SetPlaylistGPUOutputActive(true)
	if n, err := m.writeProgramOutput(&sink, []byte("cpu-during-gpu")); err != nil || n != len("cpu-during-gpu") {
		t.Fatalf("suppressed output = (%d, %v)", n, err)
	}
	if sink.String() != "cpu-before" {
		t.Fatalf("CPU bytes leaked while GPU owned publisher: %q", sink.String())
	}
	m.SetPlaylistGPUOutputActive(false)
	if _, err := m.writeProgramOutput(&sink, []byte("cpu-after")); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "cpu-beforecpu-after" {
		t.Fatalf("CPU output was not restored: %q", sink.String())
	}
}

func TestProgramCompositorTranslatesSourcePTSToProgramClock(t *testing.T) {
	encoder := &recordingProgramEncoder{}
	compositor := NewProgramCompositor(2, 1, encoder, nil)
	source := ProgramSourceFrame{
		VideoRGBA:      bytes.Repeat([]byte{0xff}, 2*1*4),
		AudioPCM:       bytes.Repeat([]byte{0x7f}, 1600*2*2),
		SourceVideoPTS: 12 * time.Hour,
		SourceAudioPTS: 9 * time.Hour,
	}
	if err := compositor.WriteTick(NewProgramClock().Next(), source); err != nil {
		t.Fatal(err)
	}
	if encoder.videoPTS[0] != 0 || encoder.audioPTS[0] != 0 {
		t.Fatalf("source PTS leaked to output: video=%s audio=%s", encoder.videoPTS[0], encoder.audioPTS[0])
	}
}

func TestProgramCompositorGeneratesFadeToBlackAndSilenceDuringSourceTransition(t *testing.T) {
	encoder := &recordingProgramEncoder{}
	compositor := NewProgramCompositor(2, 1, encoder, nil)
	clock := NewProgramClock()
	white := bytes.Repeat([]byte{0xff}, 2*1*4)
	loud := bytes.Repeat([]byte{0x7f}, 1600*2*2)
	if err := compositor.WriteTick(clock.Next(), ProgramSourceFrame{VideoRGBA: white, AudioPCM: loud}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < programTransitionFadeFrames; i++ {
		if err := compositor.WriteTick(clock.Next(), ProgramSourceFrame{}); err != nil {
			t.Fatal(err)
		}
	}

	firstFade := encoder.video[1]
	if bytes.Equal(firstFade, white) || bytes.Equal(firstFade, make([]byte, len(firstFade))) {
		t.Fatal("first transition frame was not a generated fade")
	}
	if got := encoder.video[len(encoder.video)-1]; !bytes.Equal(got, make([]byte, len(got))) {
		t.Fatal("transition did not reach black")
	}
	for _, audio := range encoder.audio[1:] {
		if !bytes.Equal(audio, make([]byte, len(audio))) {
			t.Fatal("transition fallback did not emit clock-owned silence")
		}
	}
}
