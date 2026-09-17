package nicorender

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

// This opt-in diagnostic uses an existing local snapshot; it performs no
// downloads and does not touch the running server or its process registry.
func TestNicoRenderPerformance(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICONICO_PERF_TEST") != "1" {
		t.Skip("set IMAGEPAD_NICONICO_PERF_TEST=1 and IMAGEPAD_NICO_PERF_SNAPSHOT")
	}
	data, err := os.ReadFile(os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"png", "binary-full", "binary-reuse"} {
		if selected := os.Getenv("IMAGEPAD_NICO_PERF_MODE"); selected != "" && selected != mode {
			continue
		}
		t.Run(mode, func(t *testing.T) { measureNicoRender(t, snapshot, mode) })
	}
}

func measureNicoRender(t *testing.T, snapshot niconico.Snapshot, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const width, height, count = 1920, 1080, 180
	clock, _ := niconico.NewFrameClock(30, 1)
	start := time.Now()
	bundle, cleanup, err := materializeBundle("")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var transport *frameTransport
	var config *frameTransportConfig
	if mode != "png" {
		transport, err = newFrameTransport(width, height)
		if err != nil {
			t.Fatal(err)
		}
		defer transport.close()
		transport.config.SparseFrames = os.Getenv("IMAGEPAD_NICO_PERF_SPARSE") == "1"
		config = &transport.config
	}
	page, err := writeRendererPage(width, height, bundle, snapshot.RendererThreads(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(page)
	session, err := startBrowser(ctx, "", page)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { session.close() }()
	if err := session.waitReady(ctx, mode != "png"); err != nil {
		t.Fatal(err)
	}
	// Small diagnostics are collected once at the end; image bytes still use
	// the actual production transport and code paths.
	_, err = session.call(ctx, "Runtime.evaluate", map[string]any{"expression": `(()=>{
window.__perf={drawMs:0,readMs:0,requestMs:0,sendMs:0,sendCount:0,bytes:0};
const p=window.__perf,n=window.__niconi,draw=n.drawCanvas.bind(n);
n.drawCanvas=(...args)=>{const t=performance.now();try{return draw(...args);}finally{p.drawMs+=performance.now()-t;}};
const gl=document.getElementById('canvas').getContext('webgl2');
if(gl){const info=gl.getExtension('WEBGL_debug_renderer_info');p.renderer=gl.getParameter(info?info.UNMASKED_RENDERER_WEBGL:gl.RENDERER);}
if(gl){const read=gl.readPixels.bind(gl);gl.readPixels=(...args)=>{const t=performance.now();try{return read(...args);}finally{p.readMs+=performance.now()-t;}};}
if(window.__requestDraw){const req=window.__requestDraw;window.__requestDraw=(...args)=>{const t=performance.now();try{return req(...args);}finally{p.requestMs+=performance.now()-t;}};}
const send=WebSocket.prototype.send;WebSocket.prototype.send=function(data){const t=performance.now();try{return send.call(this,data);}finally{p.sendMs+=performance.now()-t;p.sendCount++;p.bytes+=data.byteLength||0;}};
})()`})
	if err != nil {
		t.Fatal(err)
	}
	initTime := time.Since(start)
	var drawTime, receiveTime, decodeTime, ackTime time.Duration
	var full, repeat int
	var payloadBytes int64
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	renderStart := time.Now()
	if transport == nil {
		for frame := int64(0); frame < count; frame++ {
			ts := time.Now()
			png, err := session.drawPNG(ctx, clock.CommentTimeMs(frame)/10)
			drawTime += time.Since(ts)
			if err != nil {
				t.Fatal(err)
			}
			payloadBytes += int64(len(png))
			ts = time.Now()
			pixels, err := pngToRGBA(png, width, height)
			decodeTime += time.Since(ts)
			if err != nil || len(pixels) != width*height*4 {
				t.Fatalf("invalid PNG output: %v", err)
			}
			full++
		}
	} else {
		conn, err := transport.waitConn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var last []byte
		for frame := int64(0); frame < count; frame++ {
			timeMs := uint64(clock.CommentTimeMs(frame))
			ts := time.Now()
			err := requestBinaryDraw(ctx, session, uint64(frame), timeMs, mode == "binary-full" || frame == 0)
			drawTime += time.Since(ts)
			if err != nil {
				t.Fatal(err)
			}
			ts = time.Now()
			packet, err := transport.receivePacket(ctx, conn)
			receiveTime += time.Since(ts)
			if err != nil {
				t.Fatal(err)
			}
			payloadBytes += int64(len(packet))
			ts = time.Now()
			h, pixels, err := decodeFramePacket(packet, frameHeader{Sequence: uint64(frame), TimeMs: timeMs, Width: width, Height: height})
			decodeTime += time.Since(ts)
			if err != nil {
				t.Fatal(err)
			}
			if h.Kind == frameKindRepeat {
				repeat++
				pixels = last
			} else {
				full++
				last = pixels
			}
			if len(pixels) != width*height*4 {
				t.Fatal("missing output frame")
			}
			ts = time.Now()
			if err := transport.acknowledge(conn, uint64(frame)); err != nil {
				t.Fatal(err)
			}
			ackTime += time.Since(ts)
		}
	}
	elapsed := time.Since(renderStart)
	runtime.ReadMemStats(&after)
	diagnostic, err := session.call(ctx, "Runtime.evaluate", map[string]any{"expression": "window.__perf", "returnByValue": true})
	if err != nil {
		t.Fatal(err)
	}
	closeStart := time.Now()
	session.close()
	session = nil
	closeTime := time.Since(closeStart)
	t.Logf("mode=%s frames=%d init_ms=%.1f total_ms=%.1f close_ms=%.1f fps=%.2f request_ms=%.1f receive_ms=%.1f decode_ms=%.1f ack_ms=%.1f full=%d repeat=%d wire_MB=%.1f go_alloc_MB=%.1f gc=%d browser=%s", mode, count, float64(initTime.Microseconds())/1000, float64(elapsed.Microseconds())/1000, float64(closeTime.Microseconds())/1000, count/elapsed.Seconds(), float64(drawTime.Microseconds())/1000, float64(receiveTime.Microseconds())/1000, float64(decodeTime.Microseconds())/1000, float64(ackTime.Microseconds())/1000, full, repeat, float64(payloadBytes)/1e6, float64(after.TotalAlloc-before.TotalAlloc)/1e6, after.NumGC-before.NumGC, diagnostic)
}
