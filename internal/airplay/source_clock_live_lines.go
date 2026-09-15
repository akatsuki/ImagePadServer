package airplay

import (
	"errors"
	"sync"
)

const (
	sourceClockLiveLineMaxBytes      = 4096
	sourceClockLiveLineQueueCapacity = 64
)

var errSourceClockLiveLineFramerClosed = errors.New("source-clock live line framer is closed")

type sourceClockLiveLineFramer struct {
	mu        sync.Mutex
	cond      *sync.Cond
	line      []byte
	pendingCR bool
	overlong  bool
	closed    bool

	queue       [sourceClockLiveLineQueueCapacity]sourceClockLiveLineEvent
	queueHead   int
	queueTail   int
	queueSize   int
	dropPending bool
	workerDone  chan struct{}

	onLine   func(string)
	onReject func()
}

type sourceClockLiveLineEvent struct {
	line    string
	reject  bool
	barrier chan struct{}
}

// Callbacks may re-enter Write. Close and Flush from a callback are unsupported
// because both wait for this worker to finish.
func newSourceClockLiveLineFramer(onLine func(string), onReject func()) *sourceClockLiveLineFramer {
	framer := &sourceClockLiveLineFramer{
		onLine:     onLine,
		onReject:   onReject,
		workerDone: make(chan struct{}),
	}
	framer.cond = sync.NewCond(&framer.mu)
	go framer.run()
	return framer
}

func (f *sourceClockLiveLineFramer) Write(p []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, errSourceClockLiveLineFramerClosed
	}
	for _, b := range p {
		f.consumeByteLocked(b)
	}
	f.cond.Signal()
	f.mu.Unlock()
	return len(p), nil
}

func (f *sourceClockLiveLineFramer) Flush() {
	f.mu.Lock()
	if f.closed {
		workerDone := f.workerDone
		f.mu.Unlock()
		<-workerDone
		return
	}
	f.finishPartialLocked()
	barrier := f.enqueueBarrierLocked()
	workerDone := f.workerDone
	f.mu.Unlock()
	if barrier == nil {
		<-workerDone
		return
	}
	<-barrier
}

func (f *sourceClockLiveLineFramer) Close() error {
	f.mu.Lock()
	if f.closed {
		workerDone := f.workerDone
		f.mu.Unlock()
		<-workerDone
		return nil
	}
	f.finishPartialLocked()
	f.closed = true
	f.cond.Signal()
	workerDone := f.workerDone
	f.mu.Unlock()
	<-workerDone
	return nil
}

func (f *sourceClockLiveLineFramer) run() {
	defer close(f.workerDone)
	for {
		f.mu.Lock()
		for f.queueSize == 0 && !f.dropPending && !f.closed {
			f.cond.Wait()
		}
		if f.queueSize == 0 && !f.dropPending && f.closed {
			f.mu.Unlock()
			return
		}
		if f.queueSize != 0 {
			event := f.dequeueLocked()
			f.cond.Broadcast()
			f.mu.Unlock()
			if event.barrier != nil {
				close(event.barrier)
			} else {
				f.dispatchEvent(event)
			}
			continue
		}
		f.dropPending = false
		f.cond.Broadcast()
		f.mu.Unlock()
		f.dispatchEvent(sourceClockLiveLineEvent{reject: true})
	}
}

func (f *sourceClockLiveLineFramer) enqueueBarrierLocked() <-chan struct{} {
	for !f.closed && (f.queueSize == sourceClockLiveLineQueueCapacity || f.dropPending) {
		f.cond.Wait()
	}
	if f.closed {
		return nil
	}
	barrier := make(chan struct{})
	f.queue[f.queueTail] = sourceClockLiveLineEvent{barrier: barrier}
	f.queueTail = (f.queueTail + 1) % sourceClockLiveLineQueueCapacity
	f.queueSize++
	f.cond.Signal()
	return barrier
}

func (f *sourceClockLiveLineFramer) enqueueDataLocked(event sourceClockLiveLineEvent) {
	if f.dropPending || f.queueSize == sourceClockLiveLineQueueCapacity {
		f.dropPending = true
		f.cond.Signal()
		return
	}
	f.queue[f.queueTail] = event
	f.queueTail = (f.queueTail + 1) % sourceClockLiveLineQueueCapacity
	f.queueSize++
	f.cond.Signal()
}

func (f *sourceClockLiveLineFramer) dequeueLocked() sourceClockLiveLineEvent {
	event := f.queue[f.queueHead]
	f.queue[f.queueHead] = sourceClockLiveLineEvent{}
	f.queueHead = (f.queueHead + 1) % sourceClockLiveLineQueueCapacity
	f.queueSize--
	return event
}

func (f *sourceClockLiveLineFramer) consumeByteLocked(b byte) {
	if f.overlong {
		if b == '\n' {
			f.enqueueDataLocked(sourceClockLiveLineEvent{reject: true})
			f.resetLineLocked()
		}
		return
	}

	if f.pendingCR {
		if b == '\n' {
			f.pendingCR = false
			f.emitLineLocked()
			return
		}
		f.pendingCR = false
		f.appendByteLocked('\r')
		if f.overlong {
			return
		}
	}

	if b == '\n' {
		f.emitLineLocked()
		return
	}
	if b == '\r' {
		f.pendingCR = true
		return
	}
	f.appendByteLocked(b)
}

func (f *sourceClockLiveLineFramer) appendByteLocked(b byte) {
	if len(f.line) >= sourceClockLiveLineMaxBytes {
		f.line = nil
		f.overlong = true
		return
	}
	f.line = append(f.line, b)
}

func (f *sourceClockLiveLineFramer) finishPartialLocked() {
	if f.overlong {
		f.enqueueDataLocked(sourceClockLiveLineEvent{reject: true})
		f.resetLineLocked()
		return
	}
	if f.pendingCR {
		f.pendingCR = false
		f.appendByteLocked('\r')
	}
	if f.overlong {
		f.enqueueDataLocked(sourceClockLiveLineEvent{reject: true})
		f.resetLineLocked()
		return
	}
	if len(f.line) != 0 {
		f.emitLineLocked()
	}
}

func (f *sourceClockLiveLineFramer) emitLineLocked() {
	f.enqueueDataLocked(sourceClockLiveLineEvent{line: string(f.line)})
	f.resetLineLocked()
}

func (f *sourceClockLiveLineFramer) resetLineLocked() {
	f.line = nil
	f.pendingCR = false
	f.overlong = false
}

func (f *sourceClockLiveLineFramer) dispatchEvent(event sourceClockLiveLineEvent) {
	if event.reject {
		if f.onReject != nil {
			f.onReject()
		}
		return
	}
	if f.onLine != nil {
		f.onLine(event.line)
	}
}
