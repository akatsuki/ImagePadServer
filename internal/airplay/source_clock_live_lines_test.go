package airplay

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

const testSourceClockLiveLineQueueCapacity = 64

func TestSourceClockLiveLineFramerSplitsChunksAndEmitsEmptyLines(t *testing.T) {
	var got []string
	var rejects int
	framer := newSourceClockLiveLineFramer(func(line string) {
		got = append(got, line)
	}, func() {
		rejects++
	})

	for _, chunk := range []string{"first", "\nsecond\r", "\n\nthird"} {
		if n, err := framer.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", chunk, n, err, len(chunk))
		}
	}
	framer.Flush()

	if want := []string{"first", "second", "", "third"}; !equalStrings(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
	if rejects != 0 {
		t.Fatalf("rejects = %d, want 0", rejects)
	}
}

func TestSourceClockLiveLineFramerBoundsOverlongLinesAndRecovers(t *testing.T) {
	var got []string
	rejects := 0
	framer := newSourceClockLiveLineFramer(func(line string) {
		got = append(got, line)
	}, func() {
		rejects++
	})

	exact := bytes.Repeat([]byte{'a'}, sourceClockLiveLineMaxBytes)
	if _, err := framer.Write(append(exact, '\n')); err != nil {
		t.Fatalf("Write(exact line) error = %v", err)
	}
	exactCRLF := bytes.Repeat([]byte{'c'}, sourceClockLiveLineMaxBytes)
	if _, err := framer.Write(append(exactCRLF, '\r', '\n')); err != nil {
		t.Fatalf("Write(exact CRLF line) error = %v", err)
	}
	overlong := bytes.Repeat([]byte{'b'}, sourceClockLiveLineMaxBytes+1)
	if _, err := framer.Write(append(overlong, '\n')); err != nil {
		t.Fatalf("Write(overlong line) error = %v", err)
	}
	if _, err := framer.Write([]byte("recovered\n")); err != nil {
		t.Fatalf("Write(recovered line) error = %v", err)
	}
	framer.Flush()

	if want := []string{string(exact), string(exactCRLF), "recovered"}; !equalStrings(got, want) {
		t.Fatalf("lines = %#v, want exact line and recovery line", got)
	}
	if rejects != 1 {
		t.Fatalf("rejects = %d, want 1", rejects)
	}
}

func TestSourceClockLiveLineFramerWriteReturnsWhileCallbackIsBlockedAndDropsBurst(t *testing.T) {
	firstCallbackStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	lines := make(chan string, testSourceClockLiveLineQueueCapacity+1)
	rejects := make(chan struct{}, testSourceClockLiveLineQueueCapacity*2)
	framer := newSourceClockLiveLineFramer(func(line string) {
		lines <- line
		if line == "first" {
			close(firstCallbackStarted)
			<-releaseFirst
		}
	}, func() {
		rejects <- struct{}{}
	})

	firstDone := make(chan struct{})
	go func() {
		_, _ = framer.Write([]byte("first\n"))
		close(firstDone)
	}()
	<-firstCallbackStarted
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first Write remained blocked by callback")
	}

	burstLines := testSourceClockLiveLineQueueCapacity * 1024
	burst := bytes.NewBuffer(nil)
	for i := 0; i < burstLines; i++ {
		fmt.Fprintf(burst, "burst-%02d\n", i)
	}
	if n, err := framer.Write(burst.Bytes()); err != nil || n != burst.Len() {
		t.Fatalf("Write(burst) = (%d, %v), want (%d, nil)", n, err, burst.Len())
	}

	close(releaseFirst)
	if err := framer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if got := len(rejects); got != 1 {
		t.Fatalf("queue-full burst reject notifications = %d, want 1 coalesced burst notification", got)
	}
	if got := len(lines); got > testSourceClockLiveLineQueueCapacity+1 {
		t.Fatalf("delivered lines = %d, queue bound exceeded", got)
	}
}

func TestSourceClockLiveLineFramerCloseWaitsWithFullQueueAndConcurrentWrite(t *testing.T) {
	firstCallbackStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	framer := newSourceClockLiveLineFramer(func(line string) {
		if line == "first" {
			close(firstCallbackStarted)
			<-releaseFirst
		}
	}, nil)

	firstWriteDone := make(chan struct{})
	go func() {
		_, _ = framer.Write([]byte("first\n"))
		close(firstWriteDone)
	}()
	<-firstCallbackStarted
	select {
	case <-firstWriteDone:
	case <-time.After(time.Second):
		t.Fatal("first Write remained blocked by callback")
	}

	burst := bytes.NewBuffer(nil)
	for i := 0; i <= testSourceClockLiveLineQueueCapacity; i++ {
		fmt.Fprintf(burst, "queued-%02d\n", i)
	}
	if _, err := framer.Write(burst.Bytes()); err != nil {
		t.Fatalf("Write(full queue burst) error = %v", err)
	}

	closeDone := make(chan struct{})
	go func() {
		_ = framer.Close()
		close(closeDone)
	}()
	writeDone := make(chan error, 1)
	go func() {
		_, err := framer.Write([]byte("concurrent write\n"))
		writeDone <- err
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned while first callback was blocked")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not complete after blocked callback was released")
	}
	select {
	case <-writeDone:
	default:
		t.Fatal("concurrent Write did not return")
	}
}

func TestSourceClockLiveLineFramerFlushWaitsForEventsQueuedBeforeIt(t *testing.T) {
	firstCallbackStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	callbackStarted := make(chan string, 2)
	framer := newSourceClockLiveLineFramer(func(line string) {
		callbackStarted <- line
		if line == "slow" {
			close(firstCallbackStarted)
			<-releaseFirst
		}
	}, nil)

	go func() {
		_, _ = framer.Write([]byte("slow\n"))
	}()
	<-firstCallbackStarted
	if _, err := framer.Write([]byte("queued\n")); err != nil {
		t.Fatalf("Write(queued) error = %v", err)
	}

	flushDone := make(chan struct{})
	go func() {
		framer.Flush()
		close(flushDone)
	}()
	select {
	case <-flushDone:
		t.Fatal("Flush returned before queued callback completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-flushDone:
	case <-time.After(time.Second):
		t.Fatal("Flush did not complete after queued callback was released")
	}
	close(callbackStarted)
	var got []string
	for line := range callbackStarted {
		got = append(got, line)
	}
	if want := []string{"slow", "queued"}; !equalStrings(got, want) {
		t.Fatalf("callback start order = %#v, want %#v", got, want)
	}
}

func TestSourceClockLiveLineFramerDoesNotEnqueueFlushBarrierAfterClose(t *testing.T) {
	framer := newSourceClockLiveLineFramer(nil, nil)

	framer.mu.Lock()
	framer.closed = true
	barrier := framer.enqueueBarrierLocked()
	framer.cond.Signal()
	framer.mu.Unlock()

	if barrier != nil {
		t.Fatal("flush barrier was enqueued after the worker was allowed to close")
	}
	<-framer.workerDone
}

func TestSourceClockLiveLineFramerConcurrentFlushAndCloseComplete(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		framer := newSourceClockLiveLineFramer(func(line string) {
			if line == "first" {
				close(firstStarted)
				<-releaseFirst
			}
		}, nil)
		if _, err := framer.Write([]byte("first\n")); err != nil {
			t.Fatal(err)
		}
		<-firstStarted

		burst := bytes.Repeat([]byte("queued\n"), sourceClockLiveLineQueueCapacity+1)
		if _, err := framer.Write(burst); err != nil {
			t.Fatal(err)
		}
		flushDone := make(chan struct{})
		go func() {
			framer.Flush()
			close(flushDone)
		}()
		time.Sleep(time.Millisecond)
		closeDone := make(chan struct{})
		go func() {
			_ = framer.Close()
			close(closeDone)
		}()

		deadline := time.Now().Add(time.Second)
		for {
			framer.mu.Lock()
			closed := framer.closed
			framer.mu.Unlock()
			if closed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("Close did not mark the framer closed")
			}
			time.Sleep(time.Millisecond)
		}
		close(releaseFirst)
		for name, done := range map[string]<-chan struct{}{"Flush": flushDone, "Close": closeDone} {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatalf("iteration %d: %s did not complete", iteration, name)
			}
		}
	}
}

func TestSourceClockLiveLineFramerCloseWaitsForFinalPartialAndHasNoCallbacksAfterReturn(t *testing.T) {
	firstCallbackStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	partialCallbackStarted := make(chan struct{})
	releasePartial := make(chan struct{})
	callbackStarted := make(chan string, 2)
	framer := newSourceClockLiveLineFramer(func(line string) {
		callbackStarted <- line
		switch line {
		case "slow":
			close(firstCallbackStarted)
			<-releaseFirst
		case "final partial":
			close(partialCallbackStarted)
			<-releasePartial
		}
	}, nil)

	firstWriteDone := make(chan struct{})
	go func() {
		_, _ = framer.Write([]byte("slow\nfinal partial"))
		close(firstWriteDone)
	}()
	<-firstCallbackStarted

	closeDone := make(chan struct{})
	go func() {
		_ = framer.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("Close returned while callback delivery was blocked")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	<-partialCallbackStarted
	select {
	case <-closeDone:
		t.Fatal("Close returned before final partial callback completed")
	default:
	}
	close(releasePartial)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not complete after all callbacks were released")
	}

	var got []string
	for len(got) < 2 {
		got = append(got, <-callbackStarted)
	}
	if want := []string{"slow", "final partial"}; !equalStrings(got, want) {
		t.Fatalf("callback start order = %#v, want %#v", got, want)
	}
	select {
	case line := <-callbackStarted:
		t.Fatalf("callback %q occurred after Close returned", line)
	default:
	}
	if n, err := framer.Write([]byte("after close\n")); n != 0 || err == nil {
		t.Fatalf("Write(after close) = (%d, %v), want (0, error)", n, err)
	}
	if err := framer.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	select {
	case <-firstWriteDone:
	default:
		t.Fatal("initial Write did not complete")
	}
}

func TestSourceClockLiveLineFramerRejectsOverlongFinalPartialOnce(t *testing.T) {
	rejects := 0
	framer := newSourceClockLiveLineFramer(nil, func() {
		rejects++
	})

	if _, err := framer.Write(bytes.Repeat([]byte{'x'}, sourceClockLiveLineMaxBytes+1)); err != nil {
		t.Fatalf("Write(overlong partial) error = %v", err)
	}
	framer.Flush()
	framer.Flush()

	if rejects != 1 {
		t.Fatalf("rejects = %d, want 1", rejects)
	}
}

func TestSourceClockLiveLineFramerFlushesFinalPartialAndCloseRejectsWrites(t *testing.T) {
	var got []string
	framer := newSourceClockLiveLineFramer(func(line string) {
		got = append(got, line)
	}, nil)

	if _, err := framer.Write([]byte("final partial")); err != nil {
		t.Fatalf("Write(partial) error = %v", err)
	}
	if err := framer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := framer.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	if want := []string{"final partial"}; !equalStrings(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
	if n, err := framer.Write([]byte("after close\n")); n != 0 || err == nil {
		t.Fatalf("Write(after close) = (%d, %v), want (0, error)", n, err)
	}
}

func TestSourceClockLiveLineFramerCallbacksMayReenter(t *testing.T) {
	var framer *sourceClockLiveLineFramer
	var got []string
	reentryResult := make(chan error, 1)
	innerDone := make(chan struct{})
	framer = newSourceClockLiveLineFramer(func(line string) {
		got = append(got, line)
		if line == "outer" {
			_, err := framer.Write([]byte("inner\n"))
			reentryResult <- err
		}
		if line == "inner" {
			close(innerDone)
		}
	}, nil)

	if _, err := framer.Write([]byte("outer\n")); err != nil {
		t.Fatalf("Write(outer) error = %v", err)
	}
	reentryErr := <-reentryResult
	<-innerDone
	framer.Flush()
	if reentryErr != nil {
		t.Fatalf("reentrant Write error = %v", reentryErr)
	}
	if want := []string{"outer", "inner"}; !equalStrings(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestSourceClockLiveLineFramerPreservesCallbackStartOrderWhenFirstBlocks(t *testing.T) {
	firstCallbackStarted := make(chan struct{})
	secondCallbackStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	var releaseFirstOnce sync.Once
	var releaseSecondOnce sync.Once
	releaseFirstCallback := func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
	}
	releaseSecondCallback := func() {
		releaseSecondOnce.Do(func() { close(releaseSecond) })
	}
	callbackStarted := make(chan string, 2)
	framer := newSourceClockLiveLineFramer(func(line string) {
		callbackStarted <- line
		switch line {
		case "first":
			close(firstCallbackStarted)
			<-releaseFirst
		case "second":
			close(secondCallbackStarted)
			<-releaseSecond
		}
	}, nil)
	defer releaseFirstCallback()
	defer releaseSecondCallback()

	firstDone := make(chan struct{})
	go func() {
		_, _ = framer.Write([]byte("first\n"))
		close(firstDone)
	}()
	<-firstCallbackStarted

	secondDone := make(chan struct{})
	go func() {
		_, _ = framer.Write([]byte("second\n"))
		close(secondDone)
	}()

	select {
	case <-secondCallbackStarted:
		t.Fatal("second callback started while first callback was blocked")
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second Write did not return while first callback was blocked")
	}
	select {
	case <-secondCallbackStarted:
		t.Fatal("second callback started before first callback completed")
	default:
	}

	releaseFirstCallback()
	releaseSecondCallback()
	<-firstDone
	<-secondDone
	framer.Flush()
	close(callbackStarted)

	var got []string
	for line := range callbackStarted {
		got = append(got, line)
	}
	if want := []string{"first", "second"}; !equalStrings(got, want) {
		t.Fatalf("callback start order = %#v, want %#v", got, want)
	}
}

func TestSourceClockLiveLineFramerAcceptsConcurrentCompleteLines(t *testing.T) {
	const workers = 64
	lines := make(chan string, workers)
	framer := newSourceClockLiveLineFramer(func(line string) {
		lines <- line
	}, nil)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			line := fmt.Sprintf("concurrent-%02d\n", i)
			if n, err := framer.Write([]byte(line)); err != nil || n != len(line) {
				t.Errorf("Write(%q) = (%d, %v), want (%d, nil)", line, n, err, len(line))
			}
		}(i)
	}
	wg.Wait()
	if err := framer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(lines)

	var got []string
	for line := range lines {
		got = append(got, line)
	}
	sort.Strings(got)
	want := make([]string, workers)
	for i := range want {
		want[i] = fmt.Sprintf("concurrent-%02d", i)
	}
	sort.Strings(want)
	if !equalStrings(got, want) {
		t.Fatalf("concurrent lines = %#v, want %#v", got, want)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
