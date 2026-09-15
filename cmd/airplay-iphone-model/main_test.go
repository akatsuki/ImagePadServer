package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"imagepadserver/internal/airplay/iphonemodel"
)

func TestRunRejectsNonLoopbackAddress(t *testing.T) {
	err := run([]string{"-address", "192.0.2.1:7000"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunRejectsInvalidRepeatAndFragmentSize(t *testing.T) {
	for _, args := range [][]string{
		{"-address", "127.0.0.1:7000", "-repeat", "0"},
		{"-address", "127.0.0.1:7000", "-fragment-size", "-1"},
	} {
		if err := run(args, io.Discard, io.Discard); err == nil {
			t.Fatalf("args=%v", args)
		}
	}
}

func TestRunWritesPassingControlResult(t *testing.T) {
	listener := startRTSPTestServer(t)
	defer listener.Close()
	outPath := filepath.Join(t.TempDir(), "control-result.json")
	var stdout bytes.Buffer
	err := run([]string{
		"-address", listener.Addr().String(),
		"-sequence", "full",
		"-repeat", "1",
		"-seed", "7",
		"-fragment-size", "1",
		"-timeout", "2s",
		"-out", outPath,
	}, &stdout, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var result controlResult
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "PASS" || result.Layer != "uxplay-control" || result.Seed != 7 ||
		result.RequestsSent != len(iphonemodel.ControlSequence()) {
		t.Fatalf("result=%+v", result)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"PASS"`)) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func startRTSPTestServer(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for range iphonemodel.ControlSequence() {
			if _, readErr := reader.ReadString('\n'); readErr != nil {
				return
			}
			contentLength := 0
			for {
				line, readErr := reader.ReadString('\n')
				if readErr != nil {
					return
				}
				if line == "\r\n" {
					break
				}
				name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
				if ok && strings.EqualFold(name, "Content-Length") {
					contentLength, _ = strconv.Atoi(strings.TrimSpace(value))
				}
			}
			if _, readErr := io.CopyN(io.Discard, reader, int64(contentLength)); readErr != nil {
				return
			}
			if _, writeErr := io.WriteString(conn, "RTSP/1.0 200 OK\r\nContent-Length: 0\r\n\r\n"); writeErr != nil {
				return
			}
		}
	}()
	return listener
}
