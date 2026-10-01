package nicoexportbudget

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSessionProcessCloseIsConcurrentAndAtMostOnce(t *testing.T) {
	backend := newFakeSessionBackend()
	backend.closeGate = make(chan struct{})
	wantErr := errors.New("close failed")
	backend.closeErr = wantErr
	p, err := newSessionProcess(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 24
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Close(); err != wantErr {
				t.Errorf("Close error = %v, want cached %v", err, wantErr)
			}
		}()
	}
	<-backend.closeStarted
	close(backend.closeGate)
	wg.Wait()
	if got := backend.closeCalls(); got != 1 {
		t.Fatalf("backend Close calls=%d, want 1", got)
	}
}

func TestSessionProcessCancellationUsesCloseAndWaitIsCached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	backend := newFakeSessionBackend()
	wantErr := errors.New("worker exit")
	backend.waitErr = wantErr
	backend.waitReport = Report{ExitCode: 7, CPUSeconds: 2.5, PIDs: []uint32{10}, Samples: []Sample{{PIDs: []uint32{11}}}}
	p, err := newSessionProcess(ctx, backend)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-backend.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not call Close")
	}
	close(backend.closeGate)
	const callers = 12
	results := make(chan struct {
		r   Report
		err error
	}, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := p.Wait()
			results <- struct {
				r   Report
				err error
			}{r, e}
		}()
	}
	<-backend.waitStarted
	close(backend.waitGate)
	wg.Wait()
	close(results)
	for got := range results {
		if got.err != wantErr || got.r.ExitCode != 7 || got.r.CPUSeconds != 2.5 {
			t.Fatalf("Wait=(%+v,%v)", got.r, got.err)
		}
	}
	if got := backend.waitCalls(); got != 1 {
		t.Fatalf("backend Wait calls=%d, want 1", got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if got := backend.closeCalls(); got != 1 {
		t.Fatalf("backend Close calls=%d, want 1", got)
	}
}

func TestSessionProcessSnapshotReturnsDeepCopy(t *testing.T) {
	backend := newFakeSessionBackend()
	backend.snapshot = Report{PIDs: []uint32{1, 2}, Samples: []Sample{{At: time.Second, PIDs: []uint32{3, 4}}}}
	p, err := newSessionProcess(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	first.PIDs[0] = 99
	first.Samples[0].PIDs[0] = 88
	second, err := p.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if second.PIDs[0] != 1 || second.PIDs[1] != 2 || second.Samples[0].PIDs[0] != 3 || second.Samples[0].PIDs[1] != 4 {
		t.Fatalf("snapshot aliases backend: %+v", second)
	}
}

func TestSessionProcessSnapshotAndWaitReportsAreDetached(t *testing.T) {
	backend := newFakeSessionBackend()
	backend.snapshot = Report{PIDs: []uint32{5}, Samples: []Sample{{PIDs: []uint32{6}}}}
	backend.waitReport = Report{PIDs: []uint32{7}, Samples: []Sample{{PIDs: []uint32{8}}}}
	p, err := newSessionProcess(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := p.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snap.PIDs[0] = 50
	snap.Samples[0].PIDs[0] = 60
	close(backend.waitGate)
	first, err := p.Wait()
	if err != nil {
		t.Fatal(err)
	}
	first.PIDs[0] = 70
	first.Samples[0].PIDs[0] = 80
	second, err := p.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if second.PIDs[0] != 7 || second.Samples[0].PIDs[0] != 8 {
		t.Fatalf("cached Wait report aliases prior result: %+v", second)
	}
	after, err := p.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.PIDs[0] != 5 || after.Samples[0].PIDs[0] != 6 {
		t.Fatalf("post-Wait Snapshot = %+v", after)
	}
	if backend.snapshot.PIDs[0] != 5 || backend.snapshot.Samples[0].PIDs[0] != 6 {
		t.Fatalf("Snapshot mutated backend: %+v", backend.snapshot)
	}
}

func TestSessionProcessSnapshotWhileWaitBlockedAndAfter(t *testing.T) {
	backend := newFakeSessionBackend()
	backend.snapshot = Report{CPUSeconds: 1, PIDs: []uint32{15}, Samples: []Sample{{PIDs: []uint32{16}}}}
	backend.waitReport = Report{CPUSeconds: 2, PIDs: []uint32{25}, Samples: []Sample{{PIDs: []uint32{26}}}}
	p, err := newSessionProcess(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan error, 1)
	go func() { _, err := p.Wait(); waitDone <- err }()
	<-backend.waitStarted
	whileWaiting, err := p.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	whileWaiting.PIDs[0] = 99
	whileWaiting.Samples[0].PIDs[0] = 98
	close(backend.waitGate)
	if err := <-waitDone; err != nil {
		t.Fatal(err)
	}
	afterWait, err := p.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if afterWait.CPUSeconds != 1 || afterWait.PIDs[0] != 15 || afterWait.Samples[0].PIDs[0] != 16 {
		t.Fatalf("snapshot while waiting or after Wait was not detached: %+v", afterWait)
	}
}

func TestSessionProcessCloseOverlapsWaitWithSingleBackendCalls(t *testing.T) {
	backend := newFakeSessionBackend()
	p, err := newSessionProcess(context.Background(), backend)
	if err != nil {
		t.Fatal(err)
	}
	closeDone := make(chan error, 1)
	waitDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()
	go func() { _, err := p.Wait(); waitDone <- err }()
	<-backend.closeStarted
	<-backend.waitStarted
	close(backend.closeGate)
	close(backend.waitGate)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-waitDone; err != nil {
		t.Fatal(err)
	}
	if got := backend.closeCalls(); got != 1 {
		t.Fatalf("backend Close calls=%d, want 1", got)
	}
	if got := backend.waitCalls(); got != 1 {
		t.Fatalf("backend Wait calls=%d, want 1", got)
	}
}

type fakeSessionBackend struct {
	mu                                   sync.Mutex
	closeCount, waitCount, snapshotCount int
	closeStarted, waitStarted            chan struct{}
	closeGate, waitGate                  chan struct{}
	closeErr, waitErr                    error
	snapshot, waitReport                 Report
}

func newFakeSessionBackend() *fakeSessionBackend {
	return &fakeSessionBackend{closeStarted: make(chan struct{}), waitStarted: make(chan struct{}), closeGate: make(chan struct{}), waitGate: make(chan struct{})}
}
func (b *fakeSessionBackend) Close() error {
	b.mu.Lock()
	b.closeCount++
	if b.closeCount == 1 {
		close(b.closeStarted)
	}
	gate, err := b.closeGate, b.closeErr
	b.mu.Unlock()
	<-gate
	return err
}
func (b *fakeSessionBackend) Wait() (Report, error) {
	b.mu.Lock()
	b.waitCount++
	if b.waitCount == 1 {
		close(b.waitStarted)
	}
	gate, report, err := b.waitGate, cloneReport(b.waitReport), b.waitErr
	b.mu.Unlock()
	<-gate
	return report, err
}
func (b *fakeSessionBackend) Snapshot() (Report, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snapshotCount++
	return b.snapshot, nil
}
func (b *fakeSessionBackend) closeCalls() int { b.mu.Lock(); defer b.mu.Unlock(); return b.closeCount }
func (b *fakeSessionBackend) waitCalls() int  { b.mu.Lock(); defer b.mu.Unlock(); return b.waitCount }
