package airplay

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var errSourceClockReceiverOutputClosed = errors.New("source-clock receiver output is closed")

type sourceClockReceiverOutputFile interface {
	io.Writer
	io.Closer
}

var openSourceClockReceiverOutputFile = func(path string) (sourceClockReceiverOutputFile, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}

type sourceClockReceiverOutputStats struct {
	FramerRejects uint64
}

type sourceClockReceiverOutput struct {
	mu sync.Mutex

	log    *limitedBuffer
	file   sourceClockReceiverOutputFile
	framer *sourceClockLiveLineFramer
	now    func() time.Time

	closed    bool
	closeDone chan struct{}
	closeErr  error
	rejects   uint64
}

func newSourceClockReceiverOutput(log *limitedBuffer, diagnosticPath string, gate *sourceClockMetricsGate, now func() time.Time) (*sourceClockReceiverOutput, error) {
	if log == nil {
		return nil, errors.New("source-clock receiver output log is nil")
	}
	if gate == nil {
		return nil, errors.New("source-clock receiver output metrics gate is nil")
	}
	if now == nil {
		now = time.Now
	}
	var file sourceClockReceiverOutputFile
	if path := strings.TrimSpace(diagnosticPath); path != "" {
		opened, err := openSourceClockReceiverOutputFile(path)
		if err != nil {
			return nil, fmt.Errorf("open source-clock receiver diagnostic log: %w", err)
		}
		file = opened
	}
	output := &sourceClockReceiverOutput{
		log:       log,
		file:      file,
		now:       now,
		closeDone: make(chan struct{}),
	}
	output.framer = newSourceClockLiveLineFramer(func(line string) {
		if gate == nil {
			return
		}
		_, _ = gate.consumeLine(line, output.now())
	}, output.recordFramerReject)
	return output, nil
}

func (o *sourceClockReceiverOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return 0, errSourceClockReceiverOutputClosed
	}

	n := len(p)
	var firstErr error
	write := func(writer io.Writer) {
		if writer == nil {
			return
		}
		written, err := writer.Write(p)
		if written < n {
			n = written
		}
		if err == nil && written != len(p) {
			err = io.ErrShortWrite
		}
		if firstErr == nil && err != nil {
			firstErr = err
		}
	}
	if o.log != nil {
		write(o.log)
	}
	write(o.file)
	write(o.framer)
	return n, firstErr
}

func (o *sourceClockReceiverOutput) Close() error {
	o.mu.Lock()
	if o.closed {
		done := o.closeDone
		o.mu.Unlock()
		<-done
		o.mu.Lock()
		err := o.closeErr
		o.mu.Unlock()
		return err
	}
	o.closed = true
	done := o.closeDone
	o.mu.Unlock()

	framerErr := o.framer.Close()
	var fileErr error
	if o.file != nil {
		fileErr = o.file.Close()
	}
	err := errors.Join(framerErr, fileErr)
	o.mu.Lock()
	o.closeErr = err
	close(done)
	o.mu.Unlock()
	return err
}

func (o *sourceClockReceiverOutput) snapshot() sourceClockReceiverOutputStats {
	o.mu.Lock()
	defer o.mu.Unlock()
	return sourceClockReceiverOutputStats{FramerRejects: o.rejects}
}

func (o *sourceClockReceiverOutput) recordFramerReject() {
	o.mu.Lock()
	if o.rejects != ^uint64(0) {
		o.rejects++
	}
	o.mu.Unlock()
}

var _ io.WriteCloser = (*sourceClockReceiverOutput)(nil)
