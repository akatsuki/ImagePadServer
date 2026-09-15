package iphonemodel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type Request struct {
	Name        string
	Method      string
	URI         string
	ContentType string
	Body        []byte
}

type RunOptions struct {
	Repeat              int
	Seed                uint64
	Sequence            string
	FragmentSize        int
	Timeout             time.Duration
	InterIterationDelay time.Duration
}

type ControlReport struct {
	RequestsSent int           `json:"requestsSent"`
	Responses    int           `json:"responses"`
	LastStatus   int           `json:"lastStatus"`
	Duration     time.Duration `json:"duration"`
}

func ControlSequence() []Request {
	return []Request{
		{Name: "info", Method: "GET", URI: "/info", ContentType: "application/x-apple-binary-plist"},
		{Name: "fp-setup-1", Method: "POST", URI: "/fp-setup", ContentType: "application/octet-stream", Body: mustDecodeFixture(fairPlaySetup1Base64)},
		{Name: "fp-setup-2", Method: "POST", URI: "/fp-setup", ContentType: "application/octet-stream", Body: mustDecodeFixture(fairPlaySetup2Base64)},
		{Name: "setup", Method: "SETUP", URI: "rtsp://127.0.0.1/stream", ContentType: "application/x-apple-binary-plist", Body: mustDecodeFixture(setupBase64)},
	}
}

func SequenceFor(name string) ([]Request, error) {
	full := ControlSequence()
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "full":
		return full, nil
	case "setup":
		return []Request{
			full[0],
			full[len(full)-1],
			{Name: "teardown", Method: "TEARDOWN", URI: "rtsp://127.0.0.1/stream"},
		}, nil
	default:
		return nil, fmt.Errorf("unknown AirPlay control sequence %q", name)
	}
}

func RunControlSequence(ctx context.Context, address string, options RunOptions) (ControlReport, error) {
	var report ControlReport
	if err := validateRunOptions(address, options); err != nil {
		return report, err
	}
	started := time.Now()
	sequence, err := SequenceFor(options.Sequence)
	if err != nil {
		return report, err
	}
	dialer := net.Dialer{Timeout: options.Timeout}
	for iteration := 0; iteration < options.Repeat; iteration++ {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return report, fmt.Errorf("iteration %d connect: %w", iteration+1, err)
		}
		reader := bufio.NewReader(conn)
		for index, request := range sequence {
			if err := conn.SetDeadline(time.Now().Add(options.Timeout)); err != nil {
				conn.Close()
				return report, fmt.Errorf("iteration %d %s deadline: %w", iteration+1, request.Name, err)
			}
			if err := writeRequest(conn, request, index, options.FragmentSize); err != nil {
				conn.Close()
				return report, fmt.Errorf("iteration %d %s write: %w", iteration+1, request.Name, err)
			}
			report.RequestsSent++
			status, err := readResponse(reader)
			if err != nil {
				conn.Close()
				return report, fmt.Errorf("iteration %d %s response: %w", iteration+1, request.Name, err)
			}
			report.Responses++
			report.LastStatus = status
			if status < 200 || status >= 300 {
				conn.Close()
				return report, fmt.Errorf("iteration %d %s returned RTSP status %d", iteration+1, request.Name, status)
			}
		}
		if err := conn.Close(); err != nil {
			return report, fmt.Errorf("iteration %d close: %w", iteration+1, err)
		}
		if iteration+1 < options.Repeat && options.InterIterationDelay > 0 {
			timer := time.NewTimer(options.InterIterationDelay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return report, ctx.Err()
			case <-timer.C:
			}
		}
	}
	report.Duration = time.Since(started)
	return report, nil
}

func validateRunOptions(address string, options RunOptions) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid AirPlay model address: %w", err)
	}
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return errors.New("AirPlay model address must be loopback")
	}
	if options.Repeat < 1 {
		return errors.New("repeat must be positive")
	}
	if options.FragmentSize < 0 {
		return errors.New("fragment size must not be negative")
	}
	if options.Timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if options.InterIterationDelay < 0 {
		return errors.New("inter-iteration delay must not be negative")
	}
	return nil
}

func writeRequest(writer io.Writer, request Request, cseq int, fragmentSize int) error {
	if writer == nil {
		return errors.New("request writer is nil")
	}
	var data bytes.Buffer
	fmt.Fprintf(&data, "%s %s RTSP/1.0\r\n", request.Method, request.URI)
	fmt.Fprintf(&data, "X-Apple-ProtocolVersion: 1\r\n")
	fmt.Fprintf(&data, "CSeq: %d\r\n", cseq)
	fmt.Fprintf(&data, "User-Agent: AirPlay/960.13.1\r\n")
	fmt.Fprintf(&data, "DACP-ID: 0000000000000000\r\n")
	fmt.Fprintf(&data, "Active-Remote: 1\r\n")
	if request.ContentType != "" {
		fmt.Fprintf(&data, "Content-Type: %s\r\n", request.ContentType)
	}
	fmt.Fprintf(&data, "Content-Length: %d\r\n\r\n", len(request.Body))
	data.Write(request.Body)
	return writeFragments(writer, data.Bytes(), fragmentSize)
}

func writeFragments(writer io.Writer, data []byte, fragmentSize int) error {
	if fragmentSize == 0 || fragmentSize > len(data) {
		fragmentSize = len(data)
	}
	for offset := 0; offset < len(data); {
		end := offset + fragmentSize
		if end > len(data) {
			end = len(data)
		}
		written, err := writer.Write(data[offset:end])
		if err != nil {
			return err
		}
		if written <= 0 || written > end-offset {
			return io.ErrShortWrite
		}
		offset += written
	}
	return nil
}

func readResponse(reader *bufio.Reader) (int, error) {
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, err
	}
	parts := strings.Fields(strings.TrimSpace(statusLine))
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "RTSP/") {
		return 0, fmt.Errorf("invalid RTSP status line %q", statusLine)
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid RTSP status %q: %w", parts[1], err)
	}
	contentLength := 0
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			return 0, readErr
		}
		if line == "\r\n" {
			break
		}
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && strings.EqualFold(name, "Content-Length") {
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil || contentLength < 0 {
				return 0, fmt.Errorf("invalid Content-Length %q", value)
			}
		}
	}
	if contentLength > 0 {
		if _, err := io.CopyN(io.Discard, reader, int64(contentLength)); err != nil {
			return 0, err
		}
	}
	return status, nil
}
