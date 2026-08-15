package obsrtmp

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

// PlaylistGPUMuxBridge owns one explicit evaluation segment: GPU H.264 frames
// and the explicit PCM/PTS tee enter one mux process, while MPEG-TS stdout is
// drained into the supplied publisher sink.
type PlaylistGPUMuxBridge struct {
	process    *PlaylistGPUMuxProcess
	channels   int
	outputDone chan error
	closeOnce  sync.Once
	outputMu   sync.Mutex
	outputErr  error
	buffer     *playlistMuxPublisherBuffer
}

const playlistMuxPublisherBufferLimit = 4 << 20

type playlistMuxPublisherBuffer struct {
	dst           io.Writer
	tracePath     string
	limit         int
	mu            sync.Mutex
	cond          *sync.Cond
	data          []byte
	acceptedBytes int64
	writtenBytes  int64
	writeCalls    int64
	closed        bool
	err           error
	done          chan struct{}
}

func newPlaylistMuxPublisherBuffer(dst io.Writer, tracePaths ...string) *playlistMuxPublisherBuffer {
	tracePath := ""
	if len(tracePaths) > 0 {
		tracePath = tracePaths[0]
	}
	b := &playlistMuxPublisherBuffer{
		dst:       dst,
		tracePath: tracePath,
		limit:     playlistMuxPublisherBufferLimit,
		done:      make(chan struct{}),
	}
	b.cond = sync.NewCond(&b.mu)
	go b.run()
	return b
}

func (b *playlistMuxPublisherBuffer) Write(payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	for len(b.data)+len(payload) > b.limit && !b.closed && b.err == nil {
		b.cond.Wait()
	}
	if b.err != nil {
		b.mu.Unlock()
		return 0, b.err
	}
	if b.closed {
		b.mu.Unlock()
		return 0, errors.New("playlist GPU publisher buffer is closed")
	}
	b.data = append(b.data, payload...)
	b.acceptedBytes += int64(len(payload))
	acceptedBytes := b.acceptedBytes
	queuedBytes := len(b.data)
	_ = recordPlaylistGPUTrace(b.tracePath, "mux_stdout_chunk", map[string]any{
		"bytes":          len(payload),
		"accepted_bytes": acceptedBytes,
		"queued_bytes":   queuedBytes,
	})
	b.cond.Signal()
	b.mu.Unlock()
	return len(payload), nil
}

func (b *playlistMuxPublisherBuffer) run() {
	defer close(b.done)
	for {
		b.mu.Lock()
		for len(b.data) == 0 && !b.closed && b.err == nil {
			b.cond.Wait()
		}
		if len(b.data) == 0 && (b.closed || b.err != nil) {
			b.mu.Unlock()
			return
		}
		n := len(b.data)
		if n > 64*1024 {
			n = 64 * 1024
		}
		chunk := append([]byte(nil), b.data[:n]...)
		b.data = b.data[n:]
		b.cond.Broadcast()
		b.mu.Unlock()

		written, err := b.dst.Write(chunk)
		b.mu.Lock()
		b.writeCalls++
		if written > 0 {
			b.writtenBytes += int64(written)
		}
		writtenBytes := b.writtenBytes
		writeCalls := b.writeCalls
		b.mu.Unlock()
		_ = recordPlaylistGPUTrace(b.tracePath, "publisher_stdin_write", map[string]any{
			"requested_bytes": len(chunk),
			"completed_bytes": written,
			"written_bytes":   writtenBytes,
			"write_calls":     writeCalls,
		})
		if err == nil && written != len(chunk) {
			err = io.ErrShortWrite
		}
		if err != nil {
			b.mu.Lock()
			if b.err == nil {
				b.err = err
			}
			b.closed = true
			b.cond.Broadcast()
			b.mu.Unlock()
			return
		}
	}
}

type playlistMuxPublisherBufferStats struct {
	AcceptedBytes int64
	WrittenBytes  int64
	QueuedBytes   int
	WriteCalls    int64
	Closed        bool
	Err           error
}

func (b *playlistMuxPublisherBuffer) stats() playlistMuxPublisherBufferStats {
	if b == nil {
		return playlistMuxPublisherBufferStats{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return playlistMuxPublisherBufferStats{
		AcceptedBytes: b.acceptedBytes,
		WrittenBytes:  b.writtenBytes,
		QueuedBytes:   len(b.data),
		WriteCalls:    b.writeCalls,
		Closed:        b.closed,
		Err:           b.err,
	}
}

func (b *playlistMuxPublisherBuffer) CloseAndWait() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
	<-b.done
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// Abort stops accepting mux output without waiting for a blocked publisher
// write. The owning RadioManager closes the publisher stdin during teardown.
func (b *playlistMuxPublisherBuffer) Abort() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.closed = true
	b.data = nil
	b.cond.Broadcast()
	b.mu.Unlock()
}

func StartPlaylistGPUMuxBridge(ctx context.Context, ffmpeg string, publisherSink io.Writer, width, height, fps, sampleRate, channels int, outputPTSNs int64) (*PlaylistGPUMuxBridge, error) {
	if publisherSink == nil {
		return nil, errors.New("playlist GPU mux bridge requires a publisher sink")
	}
	if ctx == nil {
		return nil, errors.New("playlist GPU mux bridge requires a context")
	}
	process, err := StartPlaylistGPUMuxProcess(ffmpeg, width, height, fps, sampleRate, channels, outputPTSNs)
	if err != nil {
		return nil, err
	}
	tracePath := process.tracePath
	buffer := newPlaylistMuxPublisherBuffer(publisherSink, tracePath)
	bridge := &PlaylistGPUMuxBridge{
		process:    process,
		channels:   channels,
		outputDone: make(chan error, 1),
		buffer:     buffer,
	}
	_ = recordPlaylistGPUTrace(tracePath, "gpu_mux_bridge_started", map[string]any{
		"output_pts_ns": outputPTSNs,
	})
	go func() {
		err := CopyPlaylistGPUMuxOutput(buffer, process)
		_ = recordPlaylistGPUTrace(tracePath, "gpu_mux_stdout_eof", map[string]any{
			"error": errorString(err),
		})
		if err == nil {
			err = buffer.CloseAndWait()
		} else {
			buffer.Abort()
		}
		bridge.outputMu.Lock()
		bridge.outputErr = err
		bridge.outputMu.Unlock()
		bridge.outputDone <- err
	}()
	go func() {
		<-ctx.Done()
		_ = bridge.Close()
	}()
	return bridge, nil
}

func (b *PlaylistGPUMuxBridge) OutputError() error {
	if b == nil {
		return errors.New("playlist GPU mux bridge is unavailable")
	}
	b.outputMu.Lock()
	defer b.outputMu.Unlock()
	return b.outputErr
}

func (b *PlaylistGPUMuxBridge) VideoSink(frame video.EncodedH264Frame) error {
	if b == nil || b.process == nil {
		return errors.New("playlist GPU mux bridge is unavailable")
	}
	return b.process.WriteVideoFrame(frame)
}

func (b *PlaylistGPUMuxBridge) AudioTee(samples []byte, pts time.Duration) error {
	if b == nil || b.process == nil {
		return errors.New("playlist GPU mux bridge is unavailable")
	}
	if pts < 0 {
		return errors.New("playlist GPU mux bridge received negative audio PTS")
	}
	return b.process.WriteAudioPCM(samples, b.channels)
}

// CloseInputs sends EOF to FFmpeg without killing the mux process, allowing
// MPEG-TS stdout to drain before publisher ownership is released.
func (b *PlaylistGPUMuxBridge) CloseInputs() error {
	if b == nil || b.process == nil {
		return nil
	}
	return b.process.CloseInputs()
}

// CloseVideoInput sends EOF only to the H.264 input while keeping the PCM
// input, FFmpeg process, and publisher output alive for the audio drain.
func (b *PlaylistGPUMuxBridge) CloseVideoInput() error {
	if b == nil || b.process == nil {
		return nil
	}
	return b.process.CloseVideoInput()
}

func (b *PlaylistGPUMuxBridge) Wait() error {
	if b == nil || b.process == nil {
		return errors.New("playlist GPU mux bridge is unavailable")
	}
	outputErr := <-b.outputDone
	processErr := b.process.Wait()
	if outputErr != nil {
		return outputErr
	}
	return processErr
}

func (b *PlaylistGPUMuxBridge) Close() error {
	if b == nil || b.process == nil {
		return nil
	}
	var err error
	b.closeOnce.Do(func() {
		if b.buffer != nil {
			b.buffer.Abort()
		}
		err = b.process.Close()
	})
	return err
}
