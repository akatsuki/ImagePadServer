package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"imagepadserver/internal/airplay/sourceclock"
)

const captureRecordHeaderBytes = 12

type captureWriter struct {
	mu           sync.Mutex
	file         *os.File
	metadataOnly bool
	frames       uint64
	bytes        uint64
}

func main() {
	var videoPort, audioPort int
	var tokenHex, outputDir string
	var duration time.Duration
	var metadataOnly bool
	flag.IntVar(&videoPort, "video-listen-port", 42001, "loopback TCP port for source-clock video")
	flag.IntVar(&audioPort, "audio-listen-port", 42003, "loopback TCP port for source-clock audio")
	flag.StringVar(&tokenHex, "token", "", "16-byte source-clock session token in hex")
	flag.StringVar(&outputDir, "out", "", "capture output directory")
	flag.DurationVar(&duration, "duration", 30*time.Second, "capture duration")
	flag.BoolVar(&metadataOnly, "metadata-only", false, "store headers without media payloads")
	flag.Parse()
	if len(tokenHex) != sourceclock.SessionTokenBytes*2 || outputDir == "" ||
		videoPort < 1 || videoPort > 65534 || audioPort < 1 || audioPort > 65534 || duration <= 0 {
		flag.Usage()
		os.Exit(2)
	}
	token, err := hex.DecodeString(tokenHex)
	if err != nil || len(token) != sourceclock.SessionTokenBytes {
		fatalf("invalid source-clock token")
	}
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		fatalf("create output directory: %v", err)
	}
	videoWriter, err := newCaptureWriter(filepath.Join(outputDir, "video.ipaf"), metadataOnly)
	if err != nil {
		fatalf("open video capture: %v", err)
	}
	defer videoWriter.close()
	audioWriter, err := newCaptureWriter(filepath.Join(outputDir, "audio.ipaf"), metadataOnly)
	if err != nil {
		fatalf("open audio capture: %v", err)
	}
	defer audioWriter.close()
	videoListener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: videoPort})
	if err != nil {
		fatalf("listen video: %v", err)
	}
	defer videoListener.Close()
	audioListener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: audioPort})
	if err != nil {
		fatalf("listen audio: %v", err)
	}
	defer audioListener.Close()
	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		acceptCapture(videoListener, sourceclock.StreamVideo, token, videoWriter, deadline)
	}()
	go func() {
		defer wg.Done()
		acceptCapture(audioListener, sourceclock.StreamAudio, token, audioWriter, deadline)
	}()
	<-time.After(duration)
	_ = videoListener.Close()
	_ = audioListener.Close()
	wg.Wait()
	fmt.Printf("source-clock capture complete: video=%d frames/%d bytes audio=%d frames/%d bytes metadataOnly=%t\n",
		videoWriter.frames, videoWriter.bytes, audioWriter.frames, audioWriter.bytes, metadataOnly)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func newCaptureWriter(path string, metadataOnly bool) (*captureWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return nil, err
	}
	return &captureWriter{file: file, metadataOnly: metadataOnly}, nil
}

func (w *captureWriter) close() error {
	if w == nil || w.file == nil {
		return nil
	}
	return w.file.Close()
}

func acceptCapture(listener *net.TCPListener, expected sourceclock.StreamKind, token []byte, writer *captureWriter, deadline time.Time) {
	for time.Now().Before(deadline) {
		_ = listener.SetDeadline(time.Now().Add(500 * time.Millisecond))
		conn, err := listener.AcceptTCP()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			return
		}
		_ = conn.SetDeadline(deadline)
		captureConnection(conn, expected, token, writer)
		_ = conn.Close()
	}
}

func captureConnection(conn net.Conn, expected sourceclock.StreamKind, token []byte, writer *captureWriter) {
	first := true
	for {
		arrival := uint64(time.Now().UnixNano())
		rawHeader := make([]byte, sourceclock.HeaderSize)
		if _, err := io.ReadFull(conn, rawHeader); err != nil {
			return
		}
		header, err := sourceclock.DecodeHeader(rawHeader)
		if err != nil {
			return
		}
		if first {
			if header.StreamKind != sourceclock.StreamControl || header.Codec != sourceclock.ControlHello {
				return
			}
			payload := make([]byte, header.PayloadBytes)
			if _, err := io.ReadFull(conn, payload); err != nil || !bytesEqual(payload, token) {
				return
			}
			first = false
			if err := writer.writeRecord(arrival, rawHeader, payload); err != nil {
				return
			}
			continue
		}
		if sourceclock.StreamKind(header.StreamKind) != expected && header.StreamKind != sourceclock.StreamControl {
			return
		}
		payload := make([]byte, header.PayloadBytes)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		if err := writer.writeRecord(arrival, rawHeader, payload); err != nil {
			return
		}
	}
}

func (w *captureWriter) writeRecord(arrival uint64, rawHeader, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	recordPayload := payload
	if w.metadataOnly {
		recordPayload = nil
	}
	recordBytes := uint32(len(rawHeader) + len(recordPayload))
	var prefix [captureRecordHeaderBytes]byte
	binary.LittleEndian.PutUint64(prefix[0:8], arrival)
	binary.LittleEndian.PutUint32(prefix[8:12], recordBytes)
	if _, err := w.file.Write(prefix[:]); err != nil {
		return err
	}
	if _, err := w.file.Write(rawHeader); err != nil {
		return err
	}
	if len(recordPayload) > 0 {
		if _, err := w.file.Write(recordPayload); err != nil {
			return err
		}
	}
	w.frames++
	w.bytes += uint64(len(payload))
	return nil
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
