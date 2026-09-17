package nicorender

import (
	"context"
	"sync"
)

// FramePipe is a bounded bridge between browser rendering and FFmpeg. The
// capacity is intentionally small so a slow encoder cannot cause unbounded
// RGBA memory growth.
type FramePipe struct {
	frames chan []byte
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func NewFramePipe(capacity int) *FramePipe {
	if capacity < 1 {
		capacity = 1
	}
	return &FramePipe{frames: make(chan []byte, capacity), done: make(chan struct{})}
}

func (p *FramePipe) WriteRGBA(ctx context.Context, _ uint64, pixels []byte) error {
	if p == nil {
		return context.Canceled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		if err != nil {
			return err
		}
		return context.Canceled
	case p.frames <- pixels:
		return nil
	}
}

func (p *FramePipe) Next(ctx context.Context) ([]byte, bool, error) {
	if p == nil {
		return nil, false, context.Canceled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case pixels, ok := <-p.frames:
		if ok {
			return pixels, true, nil
		}
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		return nil, false, err
	}
}

// Close signals that no more frames will be produced. It must be called only
// after the final WriteRGBA returns, typically from the renderer goroutine.
func (p *FramePipe) Close(err error) {
	if p == nil {
		return
	}
	p.once.Do(func() {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.frames)
		close(p.done)
	})
}
