package nicorender

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

const (
	TimelineStreamGoBudgetBytes      int64 = 512 << 20
	TimelineStreamOutputChunkBytes         = 4 << 20
	TimelineStreamOutputQueueCredits       = 2
)

var errTimelineStreamQueueStopped = errors.New("niconico timeline: output queue stopped")

// timelineStreamBudget accounts for live Go-side allocations in the NCT2
// producer. Callers reserve before allocating and release after the final
// reference is gone.
type timelineStreamBudget struct {
	mu      sync.Mutex
	limit   int64
	usedNow int64
	changed chan struct{}
}

func newTimelineStreamBudget(limit int64) *timelineStreamBudget {
	if limit <= 0 || limit > TimelineStreamGoBudgetBytes {
		limit = TimelineStreamGoBudgetBytes
	}
	return &timelineStreamBudget{limit: limit, changed: make(chan struct{})}
}

type timelineStreamReservation struct {
	budget *timelineStreamBudget
	bytes  int64
	once   sync.Once
}

func (budget *timelineStreamBudget) reserve(ctx context.Context, amount int64) (*timelineStreamReservation, error) {
	if budget == nil {
		return &timelineStreamReservation{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if amount < 0 || amount > budget.limit {
		return nil, fmt.Errorf("niconico timeline: Go budget reservation %d exceeds limit %d", amount, budget.limit)
	}
	for {
		budget.mu.Lock()
		if budget.usedNow <= budget.limit-amount {
			budget.usedNow += amount
			budget.mu.Unlock()
			return &timelineStreamReservation{budget: budget, bytes: amount}, nil
		}
		changed := budget.changed
		budget.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (reservation *timelineStreamReservation) release() {
	if reservation == nil {
		return
	}
	reservation.once.Do(func() {
		if reservation.budget == nil || reservation.bytes == 0 {
			return
		}
		budget := reservation.budget
		budget.mu.Lock()
		budget.usedNow -= reservation.bytes
		close(budget.changed)
		budget.changed = make(chan struct{})
		budget.mu.Unlock()
	})
}

func (budget *timelineStreamBudget) used() int64 {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.usedNow
}

// timelineContextWriter is optional. A plain io.Writer cannot be interrupted
// while Write is blocked; the caller must arrange for it to return. The queue
// never closes caller-owned writers.
type timelineContextWriter interface {
	WriteContext(context.Context, []byte) (int, error)
}

type timelineStreamBudgetProvider interface {
	timelineStreamBudget() *timelineStreamBudget
}

type timelineOutputRequest struct {
	data        []byte
	reservation *timelineStreamReservation
	barrier     chan error
	stop        bool
}

// timelineStreamOutputQueue serializes copied stream bytes on one worker. The
// two semaphore credits include both queued and currently-writing chunks.
type timelineStreamOutputQueue struct {
	ctx           context.Context
	parentCtx     context.Context
	cancel        context.CancelFunc
	out           io.Writer
	budget        *timelineStreamBudget
	credits       chan struct{}
	requests      chan timelineOutputRequest
	done          chan struct{}
	mu            sync.Mutex
	sendMu        sync.Mutex
	sticky        error
	stopped       bool
	stopSent      bool
	internalAbort atomic.Bool
	outstanding   atomic.Int32
}

func newTimelineStreamOutputQueue(parent context.Context, out io.Writer, budget ...*timelineStreamBudget) *timelineStreamOutputQueue {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	var memoryBudget *timelineStreamBudget
	if len(budget) > 0 {
		memoryBudget = budget[0]
	}
	queue := &timelineStreamOutputQueue{
		ctx: ctx, parentCtx: parent, cancel: cancel, out: out, budget: memoryBudget,
		credits:  make(chan struct{}, TimelineStreamOutputQueueCredits),
		requests: make(chan timelineOutputRequest, TimelineStreamOutputQueueCredits), done: make(chan struct{}),
	}
	go queue.run()
	return queue
}

func (queue *timelineStreamOutputQueue) timelineStreamBudget() *timelineStreamBudget {
	return queue.budget
}

func (queue *timelineStreamOutputQueue) Write(p []byte) (int, error) {
	if queue == nil || queue.out == nil {
		return 0, errors.New("niconico timeline: output queue requires a writer")
	}
	if len(p) == 0 {
		return 0, nil
	}
	written := 0
	for written < len(p) {
		end := min(written+TimelineStreamOutputChunkBytes, len(p))
		if err := queue.acquireCredit(); err != nil {
			return written, err
		}
		var reservation *timelineStreamReservation
		var err error
		if queue.budget != nil {
			reservation, err = queue.budget.reserve(queue.ctx, int64(end-written))
			if err != nil {
				<-queue.credits
				return written, err
			}
		}
		copyOfChunk := make([]byte, end-written)
		copy(copyOfChunk, p[written:end])
		queue.outstanding.Add(1)
		request := timelineOutputRequest{data: copyOfChunk, reservation: reservation}
		if err := queue.enqueue(request); err != nil {
			queue.releaseRequest(request)
			return written, err
		} else {
			written = end
		}
	}
	return written, nil
}

func (queue *timelineStreamOutputQueue) enqueue(request timelineOutputRequest) error {
	queue.sendMu.Lock()
	defer queue.sendMu.Unlock()
	queue.mu.Lock()
	if queue.stopped {
		err := queue.errorOrStoppedLocked()
		queue.mu.Unlock()
		return err
	}
	queue.mu.Unlock()
	select {
	case queue.requests <- request:
		return nil
	case <-queue.ctx.Done():
		return queue.ctx.Err()
	case <-queue.done:
		return queue.errorOrStoppedLocked()
	}
}

func (queue *timelineStreamOutputQueue) acquireCredit() error {
	select {
	case queue.credits <- struct{}{}:
		if err := queue.ctx.Err(); err != nil {
			<-queue.credits
			return err
		}
		if err := queue.error(); err != nil {
			<-queue.credits
			return err
		}
		return nil
	case <-queue.ctx.Done():
		return queue.ctx.Err()
	case <-queue.done:
		return queue.errorOrStopped()
	}
}

func (queue *timelineStreamOutputQueue) flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := queue.error(); err != nil {
		return err
	}
	response := make(chan error, 1)
	if err := queue.enqueue(timelineOutputRequest{barrier: response}); err != nil {
		return err
	}
	select {
	case err := <-response:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-queue.ctx.Done():
		return queue.ctx.Err()
	case <-queue.done:
		return queue.errorOrStopped()
	}
}

func (queue *timelineStreamOutputQueue) finishAndWait(ctx context.Context) error {
	if err := queue.flush(ctx); err != nil {
		_ = queue.abortAndWait()
		return err
	}
	if err := queue.requestStop(ctx); err != nil {
		_ = queue.abortAndWait()
		return err
	}
	<-queue.done
	queue.cancel()
	return queue.error()
}

func (queue *timelineStreamOutputQueue) abortAndWait() error {
	if queue == nil {
		return nil
	}
	queue.internalAbort.Store(true)
	queue.cancel()
	_ = queue.requestStop(context.Background())
	<-queue.done
	return queue.error()
}

func (queue *timelineStreamOutputQueue) outstandingChunks() int {
	if queue == nil {
		return 0
	}
	return int(queue.outstanding.Load())
}

func (queue *timelineStreamOutputQueue) run() {
	defer close(queue.done)
	ctxDone := queue.ctx.Done()
	for {
		select {
		case request := <-queue.requests:
			if request.stop {
				return
			}
			if request.barrier != nil {
				request.barrier <- queue.error()
				continue
			}
			if err := queue.ctx.Err(); err != nil {
				queue.setError(queue.externalCancellation())
			} else if prior := queue.error(); prior != nil {
				// Retire later chunks after the first write error without writing them.
			} else if err := queue.writeDownstream(request.data); err != nil {
				if !queue.suppressInternalCancellation(err) {
					queue.setError(err)
				}
			}
			queue.releaseRequest(request)
		case <-ctxDone:
			queue.setError(queue.externalCancellation())
			ctxDone = nil
		}
	}
}

func (queue *timelineStreamOutputQueue) writeDownstream(data []byte) error {
	if writer, ok := queue.out.(timelineContextWriter); ok {
		for len(data) > 0 {
			n, err := writer.WriteContext(queue.ctx, data)
			if n < 0 || n > len(data) {
				return fmt.Errorf("niconico timeline: invalid context writer count %d", n)
			}
			data = data[n:]
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
		}
		return nil
	}
	return writeAll(queue.out, data)
}

func (queue *timelineStreamOutputQueue) releaseRequest(request timelineOutputRequest) {
	if request.reservation != nil {
		request.reservation.release()
	}
	if request.data != nil {
		queue.outstanding.Add(-1)
		<-queue.credits
	}
}

func (queue *timelineStreamOutputQueue) setError(err error) {
	if err == nil {
		return
	}
	queue.mu.Lock()
	if queue.sticky == nil {
		queue.sticky = err
	}
	queue.mu.Unlock()
}

func (queue *timelineStreamOutputQueue) error() error {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.sticky
}

func (queue *timelineStreamOutputQueue) errorOrStopped() error {
	if err := queue.error(); err != nil {
		return err
	}
	return errTimelineStreamQueueStopped
}

func (queue *timelineStreamOutputQueue) errorOrStoppedLocked() error {
	if queue.sticky != nil {
		return queue.sticky
	}
	return errTimelineStreamQueueStopped
}

func (queue *timelineStreamOutputQueue) externalCancellation() error {
	if queue == nil || queue.parentCtx == nil {
		return nil
	}
	return queue.parentCtx.Err()
}

func (queue *timelineStreamOutputQueue) suppressInternalCancellation(err error) bool {
	return queue != nil && queue.internalAbort.Load() && queue.externalCancellation() == nil && errors.Is(err, context.Canceled)
}

func (queue *timelineStreamOutputQueue) requestStop(ctx context.Context) error {
	if queue == nil {
		return nil
	}
	queue.sendMu.Lock()
	defer queue.sendMu.Unlock()
	queue.mu.Lock()
	queue.stopped = true
	if queue.stopSent {
		queue.mu.Unlock()
		return nil
	}
	queue.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case queue.requests <- timelineOutputRequest{stop: true}:
		queue.mu.Lock()
		queue.stopSent = true
		queue.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-queue.done:
		return nil
	}
}
