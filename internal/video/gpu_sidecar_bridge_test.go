package video

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSidecar struct {
	health chan error
	closed chan struct{}
}

func (f *fakeSidecar) Health(context.Context) error { return <-f.health }
func (f *fakeSidecar) Close() error {
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}
func TestGPUSidecarBridgeHeartbeatFailureUnblocks(t *testing.T) {
	tr, _ := NewGPUFrameTransport(1)
	f := &fakeSidecar{health: make(chan error, 1), closed: make(chan struct{})}
	b, _ := NewGPUSidecarBridge(tr, f)
	done := make(chan error)
	go func() { _, e := tr.Receive(context.Background()); done <- e }()
	f.health <- errors.New("heartbeat timeout")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Monitor(ctx, time.Millisecond, time.Millisecond)
	select {
	case e := <-done:
		if e == nil || e.Error() != "heartbeat timeout" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter remained blocked")
	}
}
