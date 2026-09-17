package nicorender

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

// Uses the same browser flush for both the reference RGBA and production sprite
// commands. This avoids comparing two independent browser/font initializations.
func TestProductionSpritePixels(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_PRODUCTION_TEST") != "1" {
		t.Skip("opt-in browser/native pixels")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	native, cleanup, err := PrepareNativeCompositor(ctx, os.Getenv("IMAGEPAD_NICO_COMPOSITOR"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{VideoID: "sm9", Threads: []niconico.Thread{{ID: "t", Fork: "main", Comments: []niconico.Comment{
		{ID: "a", VposMs: 1000, Body: "流れるコメント", Commands: []string{"red"}, PostedAt: "2026-01-01T00:00:00Z"},
		{ID: "b", VposMs: 2000, Body: "上固定\n二行目", Commands: []string{"ue", "blue"}, PostedAt: "2026-01-01T00:00:00Z"},
		{ID: "c", VposMs: 2500, Body: "下固定", Commands: []string{"shita", "green"}, PostedAt: "2026-01-01T00:00:00Z"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	o := RenderOptions{Width: 321, Height: 181, DurationMs: 8000, FPSNum: 10, FPSDen: 1}
	if os.Getenv("IMAGEPAD_NICO_PIXEL_REAL") == "1" {
		data, e := os.ReadFile(os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT"))
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(data, &snapshot); e != nil {
			t.Fatal(e)
		}
		o = RenderOptions{Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1}
	}
	clock, _ := niconico.NewFrameClock(o.FPSNum, o.FPSDen)
	count := clock.FrameCountForDurationMs(o.DurationMs)
	bundle, cleanupBundle, err := materializeBundle("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupBundle()
	transport, err := newFrameTransport(o.Width, o.Height)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.close()
	transport.config.SparseFrames = true
	page, err := writeRendererPage(o.Width, o.Height, bundle, snapshot.RendererThreads(), &transport.config)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(page)
	session, err := startBrowser(ctx, "", page)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	if err = session.waitReady(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err = spriteEvaluate(ctx, session, spriteCaptureScript, nil); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("IMAGEPAD_NICO_TEST_COMPRESSION") == "deflate" {
		if err = spriteEvaluate(ctx, session, "window.__nicoSpritesDeflate=true", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err = spriteEvaluate(ctx, session, "window.__nicoSpritesReference=true", nil); err != nil {
		t.Fatal(err)
	}
	conn, err := transport.waitConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	reference, err := os.Create(filepath.Join(dir, "reference.rgba"))
	if err != nil {
		t.Fatal(err)
	}
	defer reference.Close()
	scene, err := os.Create(filepath.Join(dir, "scene.nps3"))
	if err != nil {
		t.Fatal(err)
	}
	defer scene.Close()
	if err = writeSpriteHeader(scene, uint32(o.Width), uint32(o.Height), uint32(count), uint32(o.FPSNum), uint32(o.FPSDen)); err != nil {
		t.Fatal(err)
	}
	visible, commands := int64(0), int64(0)
	for first := int64(0); first < count; first += 30 {
		times := make([]int64, min(30, count-first))
		for i := range times {
			times[i] = clock.CommentTimeMs(first + int64(i))
		}
		if err = requestBinaryBatch(ctx, session, uint64(first), times, true); err != nil {
			t.Fatal(err)
		}
		for j, ts := range times {
			seq := uint64(first + int64(j))
			packet, e := transport.receivePacket(ctx, conn)
			if e != nil {
				t.Fatal(e)
			}
			_, pixels, e := decodeFramePacket(packet, frameHeader{Sequence: seq, TimeMs: uint64(ts), Width: uint32(o.Width), Height: uint32(o.Height)})
			if e != nil {
				t.Fatal(e)
			}
			for k := 3; k < len(pixels); k += 4 {
				if pixels[k] != 0 {
					visible++
				}
			}
			if _, e = reference.Write(pixels); e != nil {
				t.Fatal(e)
			}
			if e = transport.acknowledge(conn, seq); e != nil {
				t.Fatal(e)
			}
		}
		var raw spriteJSONBatch
		if err = spriteEvaluate(ctx, session, "window.__nicoSpritesTake()", &raw); err != nil {
			t.Fatal(err)
		}
		batch, _, _, e := decodeSpriteBatch(raw, uint64(first), times)
		if e != nil {
			t.Fatal(e)
		}
		for _, frame := range batch.Frames {
			commands += int64(len(frame.Commands))
		}
		if err = writeSpriteBatch(scene, batch); err != nil {
			t.Fatal(err)
		}
	}
	transport.stop(conn, "completed")
	if visible == 0 || commands == 0 {
		t.Fatal("blank reference or command capture")
	}
	if err = writeSpriteTerminator(scene); err != nil {
		t.Fatal(err)
	}
	if _, err = scene.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err = reference.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, native, "--stdin")
	hideNativeWindow(cmd)
	cmd.Stdin = scene
	var log strings.Builder
	cmd.Stderr = &log
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	pixels := make([]byte, o.Width*o.Height*4)
	ref := make([]byte, len(pixels))
	maxDelta, total, different := 0, int64(0), int64(0)
	for frame := int64(0); frame < count; frame++ {
		if _, err = io.ReadFull(stdout, pixels); err != nil {
			t.Fatal(err)
		}
		if _, err = io.ReadFull(reference, ref); err != nil {
			t.Fatal(err)
		}
		for i, b := range pixels {
			d := int(b) - int(ref[i])
			if d < 0 {
				d = -d
			}
			if d > 0 {
				different++
			}
			if d > maxDelta {
				maxDelta = d
			}
			total += int64(d)
		}
	}
	var extra [1]byte
	if n, e := stdout.Read(extra[:]); n != 0 || e != io.EOF {
		t.Fatalf("extra native data: %d %v", n, e)
	}
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatalf("%v %s", err, log.String())
	}
	if !strings.Contains(log.String(), fmt.Sprintf("NICO_DONE %d", count)) {
		t.Fatal("missing DONE")
	}
	t.Logf("frames=%d commands=%d visible_pixels=%d max_delta=%d changed_bytes=%d mean_abs=%.8f", count, commands, visible, maxDelta, different, float64(total)/float64(count*int64(len(pixels))))
	if maxDelta > 1 {
		t.Fatalf("exceeds established browser/WARP tolerance: %d", maxDelta)
	}
}
