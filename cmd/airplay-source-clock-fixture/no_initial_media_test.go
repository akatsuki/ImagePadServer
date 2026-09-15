package main

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"imagepadserver/internal/airplay/sourceclock"
)

// Catch an adapter that requires audio or accidentally sends an AU while
// modelling a connected receiver with no initial media.
func TestNoInitialMediaOnlySendsSessionControl(t *testing.T) {
	env := map[string]string{
		"IMAGEPAD_AIRPLAY_FIXTURE_VIDEO_FILE": "unused.h264",
		"IMAGEPAD_AIRPLAY_FIXTURE_SCENARIO":   "no-initial-media",
		"IMAGEPAD_AIRPLAY_FIXTURE_DURATION":   "150ms",
	}
	args, err := receiverAdapterArgs([]string{
		"-ipscv", "127.0.0.1:41001", "-ipsca", "127.0.0.1:41002",
		"-ipsct", "00112233445566778899aabbccddeeff00",
	}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	opts, err := parseOptions(args)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.noAudio {
		t.Fatal("no-initial-media enabled audio")
	}
	scenario, err := buildScenario(opts)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	type received struct {
		data []byte
		err  error
	}
	done := make(chan received, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- received{err: err}
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		data, err := io.ReadAll(conn)
		done <- received{data, err}
	}()
	item := frame{payload: []byte{0, 0, 0, 1, 0x65, 0x88}}
	report, err := sendVideo(listener.Addr().String(), make([]byte, sourceclock.SessionTokenBytes), []frame{item}, []frame{item}, 100, scenario)
	if err != nil {
		t.Fatal(err)
	}
	if report.Sent != 0 || report.InputFrames != 0 || report.Connections != 1 {
		t.Fatalf("unexpected media or missing connection: %+v", report)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	r := bytes.NewReader(got.data)
	for _, want := range []sourceclock.Codec{sourceclock.ControlHello, sourceclock.ControlSessionStart} {
		h, _, err := sourceclock.ReadFrame(r, sourceclock.StreamControl)
		if err != nil || h.Codec != want {
			t.Fatalf("control=%+v want=%v err=%v", h, want, err)
		}
	}
	if r.Len() != 0 {
		t.Fatalf("unexpected bytes after session controls: %d", r.Len())
	}
}
