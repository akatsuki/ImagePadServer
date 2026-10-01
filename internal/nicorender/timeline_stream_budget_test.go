package nicorender

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestTimelineStreamBudgetBoundaryAndCancellation(t *testing.T) {
	budget := newTimelineStreamBudget(TimelineStreamGoBudgetBytes)
	first, err := budget.reserve(context.Background(), TimelineStreamGoBudgetBytes)
	if err != nil {
		t.Fatalf("reserve exact budget: %v", err)
	}
	if got := budget.used(); got != TimelineStreamGoBudgetBytes {
		t.Fatalf("used=%d, want %d", got, TimelineStreamGoBudgetBytes)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := budget.reserve(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reserve over budget error=%v, want deadline exceeded", err)
	}
	first.release()
	first.release()
	if got := budget.used(); got != 0 {
		t.Fatalf("used after idempotent release=%d, want 0", got)
	}
	if _, err := budget.reserve(context.Background(), TimelineStreamGoBudgetBytes+1); err == nil {
		t.Fatal("oversized reservation unexpectedly succeeded")
	}
}

func TestTimelineElementPixelBudgetReservesBeforeTextureDecode(t *testing.T) {
	budget := newTimelineStreamBudget(4)
	held, err := budget.reserve(context.Background(), 4)
	if err != nil {
		t.Fatalf("hold budget: %v", err)
	}
	defer held.release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	element := &timelineCaptureElementResult{Samples: []timelineCaptureSample{{Textures: []spriteJSONTexture{{
		ID: 1, Width: 1, Height: 1, Data: "%%%", Encoding: "rgba",
	}}}}}
	if _, err := reserveTimelineElementPixels(ctx, budget, element); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pixel reservation error=%v, want deadline before decode", err)
	}
	if _, _, err := decodeSpriteTexture(element.Samples[0].Textures[0]); err == nil {
		t.Fatal("control texture unexpectedly decoded; malformed data should prove decode would fail")
	}
}

func TestTimelineCaptureReleasesBudgetAfterCanonicalPixelReferences(t *testing.T) {
	t.Setenv("NICO_TIMELINE_ASSET_READBACK", "sync")
	ctx := context.Background()
	var output bytes.Buffer
	budget := newTimelineStreamBudget(TimelineStreamGoBudgetBytes)
	queue := newTimelineStreamOutputQueue(ctx, &output, budget)
	defer func() { _ = queue.abortAndWait() }()
	seed := uint32(0x4e49434f)
	starter, closeCount := timelineCaptureStreamTestStarter(t, &output, nil, nil)
	_, _, err := captureCommentTimelineWithCaptureLimitsAndStarterAndStream(
		ctx, timelineCaptureStreamTestSnapshot(), timelinePoolTestOptions(), &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, starter, queue,
	)
	if err != nil {
		t.Fatalf("stream capture: %v", err)
	}
	if *closeCount != 1 {
		t.Fatalf("browser cleanup count=%d, want 1", *closeCount)
	}
	if err := queue.finishAndWait(ctx); err != nil {
		t.Fatalf("finish output queue: %v", err)
	}
	if got := budget.used(); got != 0 {
		t.Fatalf("budget still has %d bytes after capture and writer join, want 0", got)
	}
}

func TestTimelineStreamOutputQueueCountsInFlightAndQueuedChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	downstream := newGateWriter()
	queue := newTimelineStreamOutputQueue(ctx, downstream)
	defer func() { _ = queue.abortAndWait() }()

	chunk := bytes.Repeat([]byte{0x5a}, TimelineStreamOutputChunkBytes)
	if n, err := queue.Write(chunk); err != nil || n != len(chunk) {
		t.Fatalf("first enqueue=(%d,%v), want %d,nil", n, err, len(chunk))
	}
	<-downstream.started
	if n, err := queue.Write(chunk); err != nil || n != len(chunk) {
		t.Fatalf("second enqueue=(%d,%v), want %d,nil", n, err, len(chunk))
	}
	third := make(chan error, 1)
	go func() { _, err := queue.Write([]byte("third")); third <- err }()
	select {
	case err := <-third:
		t.Fatalf("third write passed while two credits were held: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if got := queue.outstandingChunks(); got != TimelineStreamOutputQueueCredits {
		t.Fatalf("outstanding chunks=%d, want %d (including in-flight)", got, TimelineStreamOutputQueueCredits)
	}
	downstream.unblock()
	select {
	case err := <-third:
		if err != nil {
			t.Fatalf("third enqueue after credit return: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("third write did not resume after downstream completion")
	}
	if err := queue.flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := queue.outstandingChunks(); got != 0 {
		t.Fatalf("outstanding chunks after flush=%d, want 0", got)
	}
}

func TestTimelineStreamOutputQueuePropagatesPriorAndEndErrors(t *testing.T) {
	priorErr := errors.New("downstream record failure")
	ctx := context.Background()
	failing := &failAfterWriter{failAt: 1, err: priorErr}
	queue := newTimelineStreamOutputQueue(ctx, failing)
	if _, err := queue.Write([]byte("header")); err != nil {
		t.Fatalf("enqueue header: %v", err)
	}
	if err := queue.flush(ctx); !errors.Is(err, priorErr) {
		t.Fatalf("pre-End barrier error=%v, want %v", err, priorErr)
	}
	if failing.writes != 1 {
		t.Fatalf("downstream writes=%d, want 1 before sticky failure", failing.writes)
	}
	if err := queue.abortAndWait(); !errors.Is(err, priorErr) {
		t.Fatalf("join error=%v, want sticky prior error %v", err, priorErr)
	}

	endErr := errors.New("End write failure")
	endFailing := &failAfterWriter{failAt: int(^uint(0) >> 1), err: endErr}
	endQueue := newTimelineStreamOutputQueue(ctx, endFailing)
	defer func() { _ = endQueue.abortAndWait() }()
	streamWriter, err := NewIncrementalCommentTimelineStreamWriter(endQueue, TimelineHeader{
		Width: 1, Height: 1, FrameCount: 1, FPSNum: 30, FPSDen: 1,
	}, nil)
	if err != nil {
		t.Fatalf("construct writer: %v", err)
	}
	if err := endQueue.flush(ctx); err != nil {
		t.Fatalf("pre-End flush: %v", err)
	}
	endFailing.failAt = endFailing.writes + 1
	if err := streamWriter.Finish(); err != nil {
		t.Fatalf("enqueue End: %v", err)
	}
	if err := endQueue.flush(ctx); !errors.Is(err, endErr) {
		t.Fatalf("End barrier error=%v, want %v", err, endErr)
	}
}

func TestTimelineStreamOutputQueueCancellationJoinsWorkerWithoutClosingWriter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	downstream := &contextAwareBlockingWriter{started: make(chan struct{}), joined: make(chan struct{})}
	queue := newTimelineStreamOutputQueue(ctx, downstream)
	if _, err := queue.Write([]byte("pending")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	<-downstream.started
	cancel()
	select {
	case <-downstream.joined:
	case <-time.After(time.Second):
		t.Fatal("context-aware downstream write did not observe cancellation")
	}
	if err := queue.abortAndWait(); err == nil {
		t.Fatal("join after cancellation unexpectedly succeeded")
	}
	if downstream.closed {
		t.Fatal("queue closed caller-owned downstream writer")
	}
}

func TestTimelineStreamOutputQueueCancellationWakesSecondCreditWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	downstream := &contextAwareBlockingWriter{started: make(chan struct{}), joined: make(chan struct{})}
	queue := newTimelineStreamOutputQueue(ctx, downstream)
	producerResult := make(chan error, 1)
	go func() {
		_, err := queue.Write(make([]byte, 3*TimelineStreamOutputChunkBytes))
		producerResult <- err
	}()
	<-downstream.started
	deadline := time.After(time.Second)
	for queue.outstandingChunks() != TimelineStreamOutputQueueCredits {
		select {
		case <-deadline:
			t.Fatalf("producer did not fill two credits; outstanding=%d", queue.outstandingChunks())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-producerResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked producer error=%v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("producer remained blocked after cancellation")
	}
	select {
	case <-downstream.joined:
	case <-time.After(time.Second):
		t.Fatal("context-aware writer did not return after cancellation")
	}
	if err := queue.abortAndWait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("queue join error=%v, want external context cancellation", err)
	}
	if got := queue.outstandingChunks(); got != 0 {
		t.Fatalf("outstanding chunks after join=%d, want 0", got)
	}
	if got := len(queue.credits); got != 0 {
		t.Fatalf("returned queue credits=%d held, want 0", got)
	}
	if downstream.closed {
		t.Fatal("queue closed caller-owned downstream writer")
	}
}

type gateWriter struct {
	started chan struct{}
	gate    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	buf     bytes.Buffer
}

func newGateWriter() *gateWriter {
	return &gateWriter{started: make(chan struct{}), gate: make(chan struct{})}
}

func (w *gateWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.gate
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *gateWriter) unblock() { close(w.gate) }

type failAfterWriter struct {
	writes int
	failAt int
	err    error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes >= w.failAt {
		return 0, w.err
	}
	return len(p), nil
}

type contextAwareBlockingWriter struct {
	started chan struct{}
	joined  chan struct{}
	closed  bool
	mu      sync.Mutex
}

func (w *contextAwareBlockingWriter) Write(p []byte) (int, error) {
	return 0, io.ErrNoProgress
}

func (w *contextAwareBlockingWriter) WriteContext(ctx context.Context, p []byte) (int, error) {
	close(w.started)
	<-ctx.Done()
	close(w.joined)
	return 0, ctx.Err()
}

func (w *contextAwareBlockingWriter) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return nil
}
