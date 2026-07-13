package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

type ffmpegProgramEncoder struct {
	cmd           *exec.Cmd
	cancel        context.CancelFunc
	videoConn     net.Conn
	audioConn     net.Conn
	audioListener net.Listener
	audioReady    <-chan programAcceptResult
	stdout        io.ReadCloser
	stderr        *synchronizedBuffer
	done          chan struct{}

	waitMu  sync.Mutex
	waitErr error
	close   sync.Once

	videoMu      sync.Mutex
	audioMu      sync.Mutex
	haveVideoPTS bool
	haveAudioPTS bool
	lastVideoPTS time.Duration
	lastAudioPTS time.Duration
}

type programAcceptResult struct {
	conn net.Conn
	err  error
}

type synchronizedBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	b.buf = append(b.buf, data...)
	b.mu.Unlock()
	return len(data), nil
}

func (b *synchronizedBuffer) Tail(limit int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := 0
	if limit > 0 && len(b.buf) > limit {
		start = len(b.buf) - limit
	}
	return string(b.buf[start:])
}

func (m *RadioManager) startFFmpegProgramEncoder(ctx context.Context, contract RadioActiveSessionContract) (ProgramEncoder, error) {
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return nil, err
	}
	encoder := video.SelectVideoEncoder(ctx, ffmpeg, video.EncoderLowLatency)
	if err := video.PreflightVideoEncoder(ctx, ffmpeg, encoder); err != nil {
		return nil, fmt.Errorf("program encoder preflight: %w", err)
	}
	width, height := radioProgramOutputSize(contract.FallbackPreset)
	return newFFmpegProgramEncoder(ctx, m.outDir, ffmpeg, contract.FallbackPreset, encoder, width, height)
}

func newFFmpegProgramEncoder(ctx context.Context, outDir, ffmpeg string, preset video.QualityPreset, encoder video.VideoEncoderProfile, width, height int) (ProgramEncoder, error) {
	videoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer videoListener.Close()
	audioListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	setProgramAcceptDeadline(videoListener, 8*time.Second)
	setProgramAcceptDeadline(audioListener, 8*time.Second)

	procCtx, cancel := context.WithCancel(ctx)
	videoInput := "tcp://" + videoListener.Addr().String()
	audioInput := "tcp://" + audioListener.Addr().String()
	cmd := exec.CommandContext(procCtx, ffmpeg, video.RadioProgramEncoderArgs(preset, encoder, width, height, videoInput, audioInput)...)
	hideWindow(cmd)
	cmd.Dir = outDir
	stderr := &synchronizedBuffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = audioListener.Close()
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = audioListener.Close()
		cancel()
		return nil, err
	}
	untrack := video.TrackStartedFFmpeg(cmd)
	program := &ffmpegProgramEncoder{
		cmd:           cmd,
		cancel:        cancel,
		stdout:        stdout,
		stderr:        stderr,
		done:          make(chan struct{}),
		audioListener: audioListener,
	}
	go func() {
		err := cmd.Wait()
		untrack()
		program.waitMu.Lock()
		program.waitErr = err
		program.waitMu.Unlock()
		close(program.done)
	}()

	videoReady := make(chan programAcceptResult, 1)
	audioReady := make(chan programAcceptResult, 1)
	program.audioReady = audioReady
	go func() {
		conn, err := videoListener.Accept()
		videoReady <- programAcceptResult{conn: conn, err: err}
	}()
	go func() {
		conn, err := audioListener.Accept()
		audioReady <- programAcceptResult{conn: conn, err: err}
	}()
	select {
	case result := <-videoReady:
		if result.err != nil {
			program.Close()
			return nil, fmt.Errorf("program video input: %w", result.err)
		}
		program.videoConn = result.conn
	case <-program.done:
		program.Close()
		return nil, fmt.Errorf("program encoder exited during startup: %s", program.stderrTail())
	case <-ctx.Done():
		program.Close()
		return nil, ctx.Err()
	}
	return program, nil
}

func setProgramAcceptDeadline(listener net.Listener, timeout time.Duration) {
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(timeout))
	}
}

func (e *ffmpegProgramEncoder) WriteVideoRGBA(frame []byte, pts time.Duration) error {
	e.videoMu.Lock()
	defer e.videoMu.Unlock()
	if e.haveVideoPTS && pts <= e.lastVideoPTS {
		return fmt.Errorf("non-monotonic program video PTS: %s after %s", pts, e.lastVideoPTS)
	}
	if err := writeProgramBytes(e.videoConn, frame); err != nil {
		return err
	}
	e.haveVideoPTS = true
	e.lastVideoPTS = pts
	return nil
}

func (e *ffmpegProgramEncoder) WriteAudioPCM(samples []byte, pts time.Duration) error {
	e.audioMu.Lock()
	defer e.audioMu.Unlock()
	if e.audioConn == nil {
		select {
		case result := <-e.audioReady:
			if result.err != nil {
				return fmt.Errorf("program audio input: %w", result.err)
			}
			e.audioConn = result.conn
			if e.audioListener != nil {
				_ = e.audioListener.Close()
				e.audioListener = nil
			}
		case <-e.done:
			return fmt.Errorf("program encoder exited before audio input opened: %s", e.stderrTail())
		}
	}
	if e.haveAudioPTS && pts <= e.lastAudioPTS {
		return fmt.Errorf("non-monotonic program audio PTS: %s after %s", pts, e.lastAudioPTS)
	}
	if err := writeProgramBytes(e.audioConn, samples); err != nil {
		return err
	}
	e.haveAudioPTS = true
	e.lastAudioPTS = pts
	return nil
}

func writeProgramBytes(writer io.Writer, data []byte) error {
	if writer == nil {
		return errors.New("program encoder input is closed")
	}
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func (e *ffmpegProgramEncoder) Output() io.Reader { return e.stdout }

func (e *ffmpegProgramEncoder) Healthy() error {
	select {
	case <-e.done:
		e.waitMu.Lock()
		err := e.waitErr
		e.waitMu.Unlock()
		if err == nil {
			err = errors.New("program encoder exited")
		}
		return fmt.Errorf("%w: %s", err, e.stderrTail())
	default:
		return nil
	}
}

func (e *ffmpegProgramEncoder) stderrTail() string {
	if e.stderr == nil {
		return ""
	}
	return e.stderr.Tail(400)
}

func (e *ffmpegProgramEncoder) Close() {
	e.close.Do(func() {
		if e.videoConn != nil {
			_ = e.videoConn.Close()
		}
		if e.audioConn != nil {
			_ = e.audioConn.Close()
		}
		if e.audioListener != nil {
			_ = e.audioListener.Close()
		}
		if e.stdout != nil {
			_ = e.stdout.Close()
		}
		e.cancel()
		select {
		case <-e.done:
		case <-time.After(3 * time.Second):
			if e.cmd.Process != nil {
				_ = e.cmd.Process.Kill()
			}
			<-e.done
		}
	})
}
