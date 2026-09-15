package iphonemodel

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestControlSequenceContainsInfoFairPlayAndSetup(t *testing.T) {
	got := ControlSequence()
	names := make([]string, len(got))
	for i := range got {
		names[i] = got[i].Name
	}
	want := []string{"info", "fp-setup-1", "fp-setup-2", "setup"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names=%v want=%v", names, want)
	}
	if got[0].Method != "GET" || got[1].Method != "POST" || got[3].Method != "SETUP" {
		t.Fatalf("methods=%q,%q,%q", got[0].Method, got[1].Method, got[3].Method)
	}
}

func TestSequenceForSetupSkipsSessionBoundFairPlayReplay(t *testing.T) {
	got, err := SequenceFor("setup")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(got))
	for i := range got {
		names[i] = got[i].Name
	}
	if want := []string{"info", "setup", "teardown"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names=%v want=%v", names, want)
	}
	if _, err := SequenceFor("unknown"); err == nil {
		t.Fatal("unknown sequence was accepted")
	}
}

func TestSetupFixtureIsSanitizedAndStable(t *testing.T) {
	sequence := ControlSequence()
	setup := sequence[len(sequence)-1]
	if bytes.Contains(setup.Body, []byte("D6:97:74:63:B5:CE")) {
		t.Fatal("SETUP fixture contains the real device identifier")
	}
	if !bytes.Contains(setup.Body, []byte("00:11:22:33:44:55")) ||
		!bytes.Contains(setup.Body, []byte("ImagePad Test")) {
		t.Fatalf("SETUP fixture is not the sanitized reproduction fixture: %x", setup.Body)
	}
	sum := sha256.Sum256(setup.Body)
	if got, want := hex.EncodeToString(sum[:]), "ea47d414dda460e26c67639963ac18fb33f413e5a584b8ea813307ba718d7892"; got != want {
		t.Fatalf("SETUP fixture sha256=%s want=%s", got, want)
	}
}

func TestWriteRequestUsesExactContentLengthAndFragmentSize(t *testing.T) {
	out := &recordingWriter{}
	req := Request{
		Name:        "setup",
		Method:      "SETUP",
		URI:         "rtsp://127.0.0.1/stream",
		ContentType: "application/x-apple-binary-plist",
		Body:        []byte{1, 2, 3},
	}
	if err := writeRequest(out, req, 4, 7); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("Content-Length: 3\r\n")) {
		t.Fatalf("request=%q", out.Bytes())
	}
	if !bytes.HasSuffix(out.Bytes(), req.Body) {
		t.Fatalf("request does not end with body: %x", out.Bytes())
	}
	for i, size := range out.writeSizes {
		if size > 7 {
			t.Fatalf("write %d size=%d exceeds fragment size", i, size)
		}
	}
}

func TestRunControlSequenceRepeatsOnFreshConnections(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	const repeats = 3
	var (
		mu       sync.Mutex
		observed []string
	)
	serverErr := make(chan error, 1)
	go func() {
		for connection := 0; connection < repeats; connection++ {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverErr <- acceptErr
				return
			}
			reader := bufio.NewReader(conn)
			for range ControlSequence() {
				name, readErr := readTestRequest(reader)
				if readErr != nil {
					conn.Close()
					serverErr <- readErr
					return
				}
				mu.Lock()
				observed = append(observed, name)
				mu.Unlock()
				if _, writeErr := io.WriteString(conn, "RTSP/1.0 200 OK\r\nContent-Length: 0\r\n\r\n"); writeErr != nil {
					conn.Close()
					serverErr <- writeErr
					return
				}
			}
			conn.Close()
		}
		serverErr <- nil
	}()

	report, err := RunControlSequence(context.Background(), listener.Addr().String(), RunOptions{
		Repeat:              repeats,
		Seed:                1,
		FragmentSize:        1,
		Timeout:             2 * time.Second,
		InterIterationDelay: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if report.RequestsSent != repeats*len(ControlSequence()) || report.Responses != report.RequestsSent {
		t.Fatalf("report=%+v", report)
	}
	if report.LastStatus != 200 {
		t.Fatalf("last status=%d", report.LastStatus)
	}
	if report.Duration < 20*time.Millisecond {
		t.Fatalf("duration=%s does not include reconnect delays", report.Duration)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(observed) != repeats*len(ControlSequence()) {
		t.Fatalf("observed=%v", observed)
	}
}

func TestRunControlSequenceRejectsInvalidOptions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		address string
		options RunOptions
	}{
		{name: "non-loopback", address: "192.0.2.1:7000", options: RunOptions{Repeat: 1, Timeout: time.Second}},
		{name: "zero-repeat", address: "127.0.0.1:7000", options: RunOptions{Repeat: 0, Timeout: time.Second}},
		{name: "negative-fragment", address: "127.0.0.1:7000", options: RunOptions{Repeat: 1, FragmentSize: -1, Timeout: time.Second}},
		{name: "zero-timeout", address: "127.0.0.1:7000", options: RunOptions{Repeat: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RunControlSequence(context.Background(), tc.address, tc.options); err == nil {
				t.Fatal("invalid options were accepted")
			}
		})
	}
}

type recordingWriter struct {
	bytes.Buffer
	writeSizes []int
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.writeSizes = append(w.writeSizes, len(p))
	return w.Buffer.Write(p)
}

func readTestRequest(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	parts := strings.Fields(strings.TrimSpace(line))
	if len(parts) != 3 {
		return "", fmt.Errorf("bad request line %q", line)
	}
	contentLength := 0
	for {
		header, readErr := reader.ReadString('\n')
		if readErr != nil {
			return "", readErr
		}
		if header == "\r\n" {
			break
		}
		name, value, ok := strings.Cut(strings.TrimSpace(header), ":")
		if ok && strings.EqualFold(name, "Content-Length") {
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return "", err
			}
		}
	}
	if _, err := io.CopyN(io.Discard, reader, int64(contentLength)); err != nil {
		return "", err
	}
	switch parts[0] + " " + parts[1] {
	case "GET /info":
		return "info", nil
	case "POST /fp-setup":
		if contentLength == 16 {
			return "fp-setup-1", nil
		}
		return "fp-setup-2", nil
	case "SETUP rtsp://127.0.0.1/stream":
		return "setup", nil
	default:
		return "", fmt.Errorf("unexpected request %s %s", parts[0], parts[1])
	}
}
