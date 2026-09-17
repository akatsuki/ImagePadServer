package nicorender

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

type batchFailureSink struct {
	calls int
	err   error
}

func (s *batchFailureSink) WriteRGBA(context.Context, uint64, []byte) error {
	s.calls++
	if s.calls == 3 {
		return s.err
	}
	return nil
}

func TestRenderBatchStopsOnSinkFailure(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_RENDER_TEST") != "1" {
		t.Skip("real browser test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	want := errors.New("test sink failure")
	sink := &batchFailureSink{err: want}
	_, err := Render(ctx, niconico.Snapshot{}, RenderOptions{Width: 64, Height: 36, DurationMs: 2000, FPSNum: 30, FPSDen: 1, Transport: "binary", ReuseUnchanged: true, BatchFrames: 30, SparseFrames: true}, sink)
	if !errors.Is(err, want) || sink.calls != 3 {
		t.Fatalf("err=%v calls=%d", err, sink.calls)
	}
}

func TestRenderBatchMatchesSingleAndBoundsReadAhead(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_RENDER_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_RENDER_TEST=1 for the real browser test")
	}
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{VideoID: "sm9", Threads: []niconico.Thread{{ID: "t", Fork: "main", Comments: []niconico.Comment{
		{ID: "a", VposMs: 100, Body: "まとめて描画", Commands: []string{"red"}, PostedAt: "2026-01-01T00:00:00Z"},
		{ID: "b", VposMs: 500, Body: "上固定", Commands: []string{"ue"}, PostedAt: "2026-01-01T00:00:00Z"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const width, height, count = 321, 181, 45
	ref := &collectingSink{}
	if _, err := Render(ctx, snapshot, RenderOptions{Width: width, Height: height, DurationMs: 1500, FPSNum: 30, FPSDen: 1, Transport: "binary", ReuseUnchanged: true}, ref); err != nil {
		t.Fatal(err)
	}
	bundle, cleanup, err := materializeBundle("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	transport, err := newFrameTransport(width, height)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.close()
	transport.config.SparseFrames = true
	page, err := writeRendererPage(width, height, bundle, snapshot.RendererThreads(), &transport.config)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(page)
	session, err := startBrowser(ctx, "", page)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if err := session.waitReady(ctx, true); err != nil {
		t.Fatal(err)
	}
	conn, err := transport.waitConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock, _ := niconico.NewFrameClock(30, 1)
	times := make([]int64, count)
	for i := range times {
		times[i] = clock.CommentTimeMs(int64(i))
	}
	encoded, _ := json.Marshal(times)
	result, err := session.call(ctx, "Runtime.evaluate", map[string]any{"expression": fmt.Sprintf("window.__requestBatch(0,%s,false)", encoded)})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ExceptionDetails) > 0 {
		t.Fatalf("batch draw failed: %s", result)
	}
	var last []byte
	sparse := 0
	for frame := 0; frame < count; frame++ {
		packet, err := transport.receivePacket(ctx, conn)
		if err != nil {
			t.Fatal(err)
		}
		h, pixels, err := decodeFramePacket(packet, frameHeader{Sequence: uint64(frame), TimeMs: uint64(times[frame]), Width: width, Height: height})
		if err != nil {
			t.Fatal(err)
		}
		if h.Kind == frameKindSparseRuns {
			sparse++
		}
		if h.Kind == frameKindRepeat {
			pixels = last
		} else {
			last = pixels
		}
		if !bytes.Equal(pixels, ref.frames[frame]) {
			t.Fatalf("batch pixels differ at frame %d", frame)
		}
		if frame == 0 {
			continue
		} // Deliberately hold the first two acknowledgements.
		if frame == 1 {
			blocked, stop := context.WithTimeout(ctx, 100*time.Millisecond)
			_, err := transport.receivePacket(blocked, conn)
			stop()
			if err == nil {
				t.Fatal("batch exceeded the two-frame read-ahead limit")
			}
			var timeout interface{ Timeout() bool }
			if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &timeout) && timeout.Timeout()) {
				t.Fatal(err)
			}
			if err := transport.acknowledge(conn, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := transport.acknowledge(conn, uint64(frame)); err != nil {
			t.Fatal(err)
		}
	}
	if sparse == 0 {
		t.Fatal("sparse transport was not exercised")
	}
	// A solid image must use the ordinary full packet instead of expanding.
	if err := evaluateBinaryRequest(ctx, session, `(()=>{window.__niconi.drawCanvas=()=>{const gl=document.getElementById('canvas').getContext('webgl2');gl.clearColor(1,0,0,1);gl.clear(gl.COLOR_BUFFER_BIT);return true;};window.__requestDraw(45,1500,true);})()`); err != nil {
		t.Fatal(err)
	}
	packet, err := transport.receivePacket(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	_, pixels, err := decodeFramePacket(packet, frameHeader{Kind: frameKindFull, Sequence: 45, TimeMs: 1500, Width: width, Height: height})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pixels, bytes.Repeat([]byte{255, 0, 0, 255}, width*height)) {
		t.Fatal("solid full-frame fallback changed pixels")
	}
}
