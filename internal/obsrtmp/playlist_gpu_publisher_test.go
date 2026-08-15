package obsrtmp

import (
	"context"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/video"
)

func TestPlaylistGPUEvaluationPublisherContextIsIndependentFromRadioSession(t *testing.T) {
	sessionCtx, cancelSession := context.WithCancel(context.Background())
	publisherCtx, cancelPublisher := newPlaylistGPUEvaluationPublisherContext()
	t.Cleanup(cancelPublisher)

	cancelSession()
	if sessionCtx.Err() == nil {
		t.Fatal("radio session context did not cancel")
	}
	select {
	case <-publisherCtx.Done():
		t.Fatal("GPU evaluation publisher context followed radio session cancellation")
	default:
	}

	cancelPublisher()
	select {
	case <-publisherCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("GPU evaluation publisher context did not cancel on explicit shutdown")
	}
}

type orderedPublisherInput struct {
	events chan<- string
	exit   chan<- error
}

func (w *orderedPublisherInput) Write(payload []byte) (int, error) {
	return len(payload), nil
}

func (w *orderedPublisherInput) Close() error {
	w.events <- "stdin-close"
	w.exit <- nil
	close(w.exit)
	return nil
}

func TestPlaylistGPUEvaluationPublisherCloseCancelsAfterStdinEOS(t *testing.T) {
	events := make(chan string, 2)
	exit := make(chan error, 1)
	publisher := &ffmpegPublisher{
		in:   &orderedPublisherInput{events: events, exit: exit},
		exit: exit,
		cancel: func() {
			events <- "publisher-cancel"
		},
	}

	publisher.close()
	if got := <-events; got != "stdin-close" {
		t.Fatalf("first shutdown event = %q, want stdin-close", got)
	}
	if got := <-events; got != "publisher-cancel" {
		t.Fatalf("second shutdown event = %q, want publisher-cancel", got)
	}
}

func TestPlaylistGPUEvaluationPublisherArgsAreSeparateFromCPUProfile(t *testing.T) {
	gpu := strings.Join(playlistGPUEvaluationPublisherArgs("rtmp://127.0.0.1:1935/eval"), " ")
	if !strings.Contains(gpu, "-map 0:v:0 -map 0:a:0") {
		t.Fatalf("GPU publisher must map both streams explicitly: %s", gpu)
	}
	for _, want := range []string{
		"-fflags +genpts",
		"-avioflags direct",
		"-probesize 64k",
		"-analyzeduration 500000",
		"-max_interleave_delta 100000",
		"-flush_packets 1",
		"-flvflags no_duration_filesize",
		"-f flv",
	} {
		if !strings.Contains(gpu, want) {
			t.Fatalf("GPU publisher args missing %q: %s", want, gpu)
		}
	}
	cpu := strings.Join(video.RadioPublisherArgs("rtsp://127.0.0.1:8554/radio"), " ")
	if strings.Contains(cpu, "-avioflags direct") || strings.Contains(cpu, "-f flv") {
		t.Fatalf("CPU publisher profile drifted into GPU evaluation args: %s", cpu)
	}
	if strings.Contains(gpu, "-max_interleave_delta 0") {
		t.Fatalf("GPU publisher must not use an unbounded interleave window: %s", gpu)
	}
}
