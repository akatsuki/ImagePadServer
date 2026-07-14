package video

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrGPUUnavailable is returned to all blocked producers/consumers when the
// sidecar disappears. It is deliberately distinct from a cancelled context.
var ErrGPUUnavailable = errors.New("gpu renderer unavailable")

// GPUFrameTransport is the Go-side bounded bridge for frames produced by the
// wgpu sidecar. It owns no GPU resources; ownership ends when Receive returns.
type GPUFrameTransport struct {
	mu       sync.Mutex
	capacity int
	queue    []GpuFrame
	closed   bool
	err      error
	notify   chan struct{}
}

func NewGPUFrameTransport(capacity int) (*GPUFrameTransport, error) {
	if capacity <= 0 {
		return nil, errors.New("gpu frame transport capacity must be positive")
	}
	return &GPUFrameTransport{capacity: capacity, notify: make(chan struct{})}, nil
}

func (t *GPUFrameTransport) signal() { close(t.notify); t.notify = make(chan struct{}) }

func (t *GPUFrameTransport) Submit(ctx context.Context, frame GpuFrame) error {
	if err := frame.Validate(); err != nil {
		return fmt.Errorf("submit gpu frame: %w", err)
	}
	for {
		t.mu.Lock()
		if t.closed {
			err := t.err
			t.mu.Unlock()
			if err == nil {
				return ErrGPUUnavailable
			}
			return err
		}
		if len(t.queue) < t.capacity {
			t.queue = append(t.queue, frame)
			t.signal()
			t.mu.Unlock()
			return nil
		}
		wait := t.notify
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		}
	}
}

func (t *GPUFrameTransport) Receive(ctx context.Context) (GpuFrame, error) {
	for {
		t.mu.Lock()
		if len(t.queue) > 0 {
			f := t.queue[0]
			t.queue = t.queue[1:]
			t.signal()
			t.mu.Unlock()
			return f, nil
		}
		if t.closed {
			err := t.err
			t.mu.Unlock()
			if err == nil {
				return GpuFrame{}, ErrGPUUnavailable
			}
			return GpuFrame{}, err
		}
		wait := t.notify
		t.mu.Unlock()
		select {
		case <-ctx.Done():
			return GpuFrame{}, ctx.Err()
		case <-wait:
		}
	}
}

// Close unblocks every waiter. Calling it more than once is safe.
func (t *GPUFrameTransport) Close(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	if err == nil {
		err = ErrGPUUnavailable
	}
	t.err = err
	t.signal()
}

func (t *GPUFrameTransport) Len() int { t.mu.Lock(); defer t.mu.Unlock(); return len(t.queue) }

// RGBA8ToYUV420P performs the only CPU-side conversion required before the
// existing FFmpeg H.264 path. It accepts padded GPU rows and emits planar
// 4:2:0 (Y, U, V) with tightly packed planes.
func RGBA8ToYUV420P(frame GpuFrame) ([]byte, error) {
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	w, h := int(frame.Width), int(frame.Height)
	ySize, cw, ch := w*h, (w+1)/2, (h+1)/2
	out := make([]byte, ySize+2*cw*ch)
	read := func(x, y int) (uint8, uint8, uint8) {
		i := y*int(frame.RowStride) + x*4
		r, g, b := frame.Payload[i], frame.Payload[i+1], frame.Payload[i+2]
		if frame.Format == PixelBGRA8 {
			b, r = frame.Payload[i], frame.Payload[i+2]
		}
		return r, g, b
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := read(x, y)
			out[y*w+x] = uint8((66*int(r) + 129*int(g) + 25*int(b) + 128>>8) + 16)
		}
	}
	for by := 0; by < ch; by++ {
		for bx := 0; bx < cw; bx++ {
			var sr, sg, sb, n int
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					x, y := bx*2+dx, by*2+dy
					if x < w && y < h {
						r, g, b := read(x, y)
						sr += int(r)
						sg += int(g)
						sb += int(b)
						n++
					}
				}
			}
			r, g, b := sr/n, sg/n, sb/n
			i := ySize + by*cw + bx
			out[i] = uint8((-38*r - 74*g + 112*b + 128>>8) + 128)
			out[ySize+cw*ch+by*cw+bx] = uint8((112*r - 94*g - 18*b + 128>>8) + 128)
		}
	}
	return out, nil
}
