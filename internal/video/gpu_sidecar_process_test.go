package video

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSidecarHelper(t *testing.T) {
	if os.Getenv("IMAGEPAD_SIDECAR_HELPER") != "1" {
		return
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	if os.Getenv("IMAGEPAD_SIDECAR_WRITE_STDERR") == "1" {
		_, _ = os.Stderr.WriteString("sidecar diagnostic: adapter init failed\n" + string(make([]byte, 40*1024)))
	}
	for {
		var req map[string]any
		if dec.Decode(&req) != nil {
			return
		}
		typ, _ := req["type"].(string)
		switch typ {
		case "hello":
			_ = enc.Encode(map[string]any{"type": "hello_ack", "version": 1, "session": req["session"], "adapter": "fixture-adapter", "backend": "vulkan", "toolchain": "wgpu-fixture-1"})
		case "health":
			_ = enc.Encode(map[string]any{"type": "health", "ready": true, "protocol": 1, "adapter": "fixture-adapter", "backend": "vulkan", "toolchain": "wgpu-fixture-1"})
		case "render":
			if output, _ := req["output"].(string); output == string(GPUOutputYUV420P) {
				w, _ := req["width"].(float64)
				h, _ := req["height"].(float64)
				seq, _ := req["sequence"].(float64)
				pts, _ := req["pts_ns"].(float64)
				width, height := uint32(w), uint32(h)
				cw, ch := (width+1)/2, (height+1)/2
				_ = enc.Encode(map[string]any{"type": "yuv_frame", "frame": YUV420PFrame{
					Schema: GPUContractVersion, Sequence: uint64(seq), PTSNs: int64(pts), Width: width, Height: height,
					YStride: width, UStride: cw, VStride: cw, ColorSpace: ColorSRGB, Ownership: "OwnedByTransport",
					Y: make([]byte, int(width*height)), U: make([]byte, int(cw*ch)), V: make([]byte, int(cw*ch)),
				}})
				continue
			}
			if os.Getenv("IMAGEPAD_SIDECAR_MALFORMED_FRAME") == "1" {
				_ = enc.Encode(map[string]any{"type": "frame", "frame": map[string]any{"width": 0, "height": 0, "stride": 0, "format": "rgba8", "data": "!!!"}})
				continue
			}
			if os.Getenv("IMAGEPAD_SIDECAR_DIE_ON_RENDER") == "1" {
				return
			}
		case "shutdown":
			_ = enc.Encode(map[string]any{"type": "bye"})
			return
		}
	}
}

func TestSidecarProcessRenderSceneYUVRequestsAndValidates(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1")
	p, err := startSidecarCommand(context.Background(), cmd, "yuv-session")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := p.RenderSceneYUV(ctx, 4, 2, 7, 1234, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	if frame.Sequence != 7 || frame.PTSNs != 1234 || len(frame.Y) != 8 {
		t.Fatalf("unexpected yuv frame: %+v", frame)
	}
}

func TestSidecarDiagnosticsCaptureBoundedStderrAndExit(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1", "IMAGEPAD_SIDECAR_WRITE_STDERR=1")
	p, err := startSidecarCommand(context.Background(), cmd, "diagnostic-session")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Hello(ctx, "diagnostic-session"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	d := p.Diagnostics()
	if d.Executable == "" || len(d.Args) == 0 {
		t.Fatalf("missing executable evidence: %+v", d)
	}
	if d.FinishedAt.IsZero() || d.ExitError != nil {
		t.Fatalf("missing clean exit evidence: %+v", d)
	}
	if len(d.Stderr) == 0 || len(d.Stderr) > sidecarStderrLimit+32 {
		t.Fatalf("stderr capture not bounded: %d", len(d.Stderr))
	}
	if d.Stderr == "" || d.Stderr[:len("sidecar diagnostic")] != "sidecar diagnostic" {
		t.Fatalf("stderr evidence missing prefix: %q", d.Stderr[:min(len(d.Stderr), 64)])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestSidecarProcessRenderUnblocksOnDeath(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1", "IMAGEPAD_SIDECAR_DIE_ON_RENDER=1")
	p, err := startSidecarCommand(context.Background(), cmd, "death-session")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Hello(ctx, "death-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Render(ctx, 360, 360, 1, 33333333); err == nil {
		t.Fatal("render should fail when sidecar exits without a frame")
	}
}

func TestSidecarProcessHelloHealthShutdown(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1")
	// StartSidecar accepts an executable only, so launch through a tiny helper
	// command wrapper is not portable; this test exercises the same JSONL client
	// with the test binary via an environment-selected executable path.
	p, err := startSidecarCommand(context.Background(), cmd, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Hello(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := p.Health(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSidecarDiagnosticsIncludesRuntimeFingerprint(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1")
	p, err := startSidecarCommand(context.Background(), cmd, "fingerprint-session")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Hello(ctx, "fingerprint-session"); err != nil {
		t.Fatal(err)
	}
	if err := p.Health(ctx); err != nil {
		t.Fatal(err)
	}
	_ = p.Close()
	d := p.Diagnostics()
	if d.Adapter != "fixture-adapter" || d.Backend != "vulkan" || d.Toolchain != "wgpu-fixture-1" {
		t.Fatalf("fingerprint=%+v", d)
	}
}

func TestSidecarCloseIsBoundedAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1")
	p, err := startSidecarCommand(ctx, cmd, "cancel-session")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	started := time.Now()
	_ = p.Close()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("close exceeded bounded cleanup: %s", elapsed)
	}
}
