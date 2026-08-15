package obsrtmp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

// PlaylistGPUMuxProcess is the explicit evaluation-only H.264/PCM to MPEG-TS
// bridge. Loopback TCP is used instead of extra OS file descriptors so the
// process works on Windows as well as Unix.
type PlaylistGPUMuxProcess struct {
	cmd         *exec.Cmd
	video       net.Conn
	audio       net.Conn
	output      io.ReadCloser
	stderr      *bytes.Buffer
	done        chan error
	audioReady  chan error
	audioCancel context.CancelFunc

	mu             sync.Mutex
	videoWriteMu   sync.Mutex
	audioWriteMu   sync.Mutex
	closed         bool
	inputsClosed   bool
	hasFrame       bool
	sequence       uint64
	ptsNS          int64
	closeOnce      sync.Once
	audioReadyOnce sync.Once
	audioReadyErr  error
	tracePath      string
}

func StartPlaylistGPUMuxProcess(ffmpeg string, width, height, fps, sampleRate, channels int, outputPTSNs int64) (*PlaylistGPUMuxProcess, error) {
	if strings.TrimSpace(ffmpeg) == "" {
		return nil, errors.New("playlist GPU mux requires an FFmpeg executable")
	}
	videoURL, err := reservePlaylistGPUMuxTCPURL()
	if err != nil {
		return nil, fmt.Errorf("reserve playlist GPU video input: %w", err)
	}
	audioURL, err := reservePlaylistGPUMuxTCPURL()
	if err != nil {
		return nil, fmt.Errorf("reserve playlist GPU audio input: %w", err)
	}
	args, err := playlistGPUMuxArgsWithInputs(width, height, fps, sampleRate, channels, outputPTSNs, videoURL, audioURL)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(ffmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create playlist GPU mux stdout: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start playlist GPU mux: %w", err)
	}
	videoCtx, cancelVideo := context.WithTimeout(context.Background(), 15*time.Second)
	videoConn, err := dialPlaylistGPUMuxURL(videoCtx, videoURL)
	cancelVideo()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("connect playlist GPU video input: %w", err)
	}
	audioCtx, cancelAudio := context.WithTimeout(context.Background(), 120*time.Second)
	p := &PlaylistGPUMuxProcess{
		cmd:         cmd,
		video:       videoConn,
		output:      stdout,
		stderr:      stderr,
		done:        make(chan error, 1),
		audioReady:  make(chan error, 1),
		audioCancel: cancelAudio,
		tracePath:   playlistGPUTracePathFromEnv(),
	}
	_ = recordPlaylistGPUTrace(p.tracePath, "gpu_mux_process_started", map[string]any{
		"pid": cmd.Process.Pid,
	})
	go func() {
		err := cmd.Wait()
		cancelAudio()
		_ = recordPlaylistGPUTrace(p.tracePath, "gpu_mux_process_exit", map[string]any{
			"error": errorString(err),
		})
		p.done <- err
	}()
	go func() {
		audioConn, dialErr := dialPlaylistGPUMuxURL(audioCtx, audioURL)
		p.mu.Lock()
		if dialErr == nil && !p.closed && !p.inputsClosed {
			p.audio = audioConn
		} else if audioConn != nil {
			_ = audioConn.Close()
		}
		p.mu.Unlock()
		p.audioReady <- dialErr
	}()
	return p, nil
}

func reservePlaylistGPUMuxTCPURL() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return fmt.Sprintf("tcp://127.0.0.1:%d?listen=1", port), nil
}

func dialPlaylistGPUMuxURL(ctx context.Context, rawURL string) (net.Conn, error) {
	address := strings.TrimPrefix(rawURL, "tcp://")
	address = strings.TrimSuffix(address, "?listen=1")
	var lastErr error
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, lastErr
		case <-timer.C:
		}
	}
}

func (p *PlaylistGPUMuxProcess) WriteVideoFrame(frame video.EncodedH264Frame) error {
	if p == nil {
		return errors.New("playlist GPU mux video input is unavailable")
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	p.videoWriteMu.Lock()
	defer p.videoWriteMu.Unlock()
	p.mu.Lock()
	if p.closed || p.inputsClosed || p.video == nil {
		p.mu.Unlock()
		return errors.New("playlist GPU mux is closed")
	}
	if p.hasFrame && (frame.Sequence != p.sequence+1 || frame.PTSNs <= p.ptsNS) {
		p.mu.Unlock()
		return errors.New("playlist GPU mux video sequence or PTS disorder")
	}
	videoConn := p.video
	p.mu.Unlock()
	if _, err := videoConn.Write(frame.Payload); err != nil {
		return fmt.Errorf("write playlist GPU H264 access unit: %w", err)
	}
	p.mu.Lock()
	p.sequence = frame.Sequence
	p.ptsNS = frame.PTSNs
	p.hasFrame = true
	p.mu.Unlock()
	_ = recordPlaylistGPUTrace(p.tracePath, "gpu_h264_input_write", map[string]any{
		"sequence": frame.Sequence,
		"pts_ns":   frame.PTSNs,
		"bytes":    len(frame.Payload),
	})
	return nil
}

func (p *PlaylistGPUMuxProcess) WriteAudioPCM(samples []byte, channels int) error {
	if p == nil {
		return errors.New("playlist GPU mux audio input is unavailable")
	}
	if channels <= 0 || len(samples) == 0 || len(samples)%(channels*2) != 0 {
		return errors.New("playlist GPU mux PCM payload is not aligned to s16le channels")
	}
	p.audioWriteMu.Lock()
	defer p.audioWriteMu.Unlock()
	p.audioReadyOnce.Do(func() {
		p.audioReadyErr = <-p.audioReady
	})
	if p.audioReadyErr != nil {
		return fmt.Errorf("connect playlist GPU audio input: %w", p.audioReadyErr)
	}
	p.mu.Lock()
	if p.closed || p.inputsClosed || p.audio == nil {
		p.mu.Unlock()
		return errors.New("playlist GPU mux is closed")
	}
	audioConn := p.audio
	p.mu.Unlock()
	if _, err := audioConn.Write(samples); err != nil {
		return fmt.Errorf("write playlist GPU PCM: %w", err)
	}
	_ = recordPlaylistGPUTrace(p.tracePath, "gpu_pcm_input_write", map[string]any{
		"bytes":    len(samples),
		"channels": channels,
	})
	return nil
}

func (p *PlaylistGPUMuxProcess) Output() io.Reader {
	if p == nil {
		return nil
	}
	return p.output
}

// CopyPlaylistGPUMuxOutput drains only the MPEG-TS stdout of the explicit GPU
// mux process into the supplied publisher sink. It never reads CPU RGBA or
// decoded pixels.
func CopyPlaylistGPUMuxOutput(dst io.Writer, p *PlaylistGPUMuxProcess) error {
	if dst == nil || p == nil || p.Output() == nil {
		return errors.New("playlist GPU MPEG-TS output sink is unavailable")
	}
	if _, err := io.Copy(dst, p.Output()); err != nil {
		return fmt.Errorf("copy playlist GPU MPEG-TS output: %w", err)
	}
	return nil
}

func (p *PlaylistGPUMuxProcess) Wait() error {
	if p == nil || p.done == nil {
		return errors.New("playlist GPU mux is unavailable")
	}
	return <-p.done
}

func (p *PlaylistGPUMuxProcess) Close() error {
	if p == nil {
		return nil
	}
	var closeErr error
	p.closeOnce.Do(func() {
		if p.audioCancel != nil {
			p.audioCancel()
		}
		p.mu.Lock()
		p.closed = true
		p.inputsClosed = true
		videoConn := p.video
		audioConn := p.audio
		p.video = nil
		p.audio = nil
		p.mu.Unlock()
		if videoConn != nil {
			_ = videoConn.Close()
		}
		if audioConn != nil {
			_ = audioConn.Close()
		}
		if p.output != nil {
			_ = p.output.Close()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			if err := p.cmd.Process.Kill(); err != nil {
				closeErr = err
			}
		}
	})
	return closeErr
}

func (p *PlaylistGPUMuxProcess) CloseInputs() error {
	if p == nil {
		return nil
	}
	if p.audioCancel != nil {
		p.audioCancel()
	}
	p.mu.Lock()
	videoConn := p.video
	audioConn := p.audio
	p.inputsClosed = true
	p.video = nil
	p.audio = nil
	p.mu.Unlock()
	_ = recordPlaylistGPUTrace(p.tracePath, "gpu_mux_inputs_closed", nil)
	var firstErr error
	if videoConn != nil {
		if err := videoConn.Close(); err != nil {
			firstErr = err
		}
	}
	if audioConn != nil {
		if err := audioConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// CloseVideoInput sends EOF to only the H.264 loopback input. FFmpeg keeps the
// PCM input and MPEG-TS output alive, allowing a live publisher to progress
// after the GPU video access units have been submitted. This is an explicit
// playlist-GPU lifecycle operation; it is not used by the normal CPU route.
func (p *PlaylistGPUMuxProcess) CloseVideoInput() error {
	if p == nil {
		return nil
	}
	p.videoWriteMu.Lock()
	defer p.videoWriteMu.Unlock()
	p.mu.Lock()
	videoConn := p.video
	p.video = nil
	p.mu.Unlock()
	if videoConn == nil {
		return nil
	}
	return videoConn.Close()
}
