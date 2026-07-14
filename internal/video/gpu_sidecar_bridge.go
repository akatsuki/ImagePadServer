package video

import (
	"context"
	"errors"
	"sync"
	"time"
)

// SidecarHealth is intentionally small so the production process transport
// and tests can share the same lifecycle/failure semantics.
type SidecarHealth interface {
	Health(context.Context) error
	Close() error
}

// GPUSidecarBridge connects a sidecar health channel to the bounded frame
// transport. A missed heartbeat closes the transport, which unblocks FFmpeg
// feeder goroutines instead of leaving them waiting forever.
type GPUSidecarBridge struct {
	transport *GPUFrameTransport
	client    SidecarHealth
	mu        sync.Mutex
	closed    bool
}

func NewGPUSidecarBridge(transport *GPUFrameTransport, client SidecarHealth) (*GPUSidecarBridge, error) {
	if transport == nil || client == nil {
		return nil, errors.New("gpu sidecar bridge requires transport and client")
	}
	return &GPUSidecarBridge{transport: transport, client: client}, nil
}

func (b *GPUSidecarBridge) Monitor(ctx context.Context, interval, timeout time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = interval * 2
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			b.Close(ctx.Err())
			return
		case <-t.C:
			hctx, cancel := context.WithTimeout(ctx, timeout)
			err := b.client.Health(hctx)
			cancel()
			if err != nil {
				b.Close(err)
				return
			}
		}
	}
}

func (b *GPUSidecarBridge) Transport() *GPUFrameTransport { return b.transport }

// RenderYUV is the delivery adapter used by the existing FFmpeg rawvideo
// input. Rendering remains GPU-side; only the required final colorspace pack
// happens here.
func (b *GPUSidecarBridge) RenderYUV(ctx context.Context, width, height uint32, sequence uint64, ptsNS int64) ([]byte, error) {
	client, ok := b.client.(*SidecarProcess)
	if !ok { return nil, errors.New("sidecar client does not support frame rendering") }
	frame, err := client.Render(ctx, width, height, sequence, ptsNS); if err != nil { b.Close(err); return nil, err }
	if err := b.transport.Submit(ctx, frame); err != nil { return nil, err }
	ordered, err := b.transport.Receive(ctx); if err != nil { return nil, err }
	return RGBA8ToYUV420P(ordered)
}

func (b *GPUSidecarBridge) Close(cause error) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	b.transport.Close(cause)
	return b.client.Close()
}
