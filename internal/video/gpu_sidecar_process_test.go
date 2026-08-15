package video

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestSidecarHelper(t *testing.T) {
	if os.Getenv("IMAGEPAD_SIDECAR_HELPER") != "1" {
		return
	}
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	var pendingPlaylistFrames []any
	var preparedTimelineFrames []any
	encodePlaylistFrames := func(rawFrames []any) []EncodedH264Frame {
		encoded := make([]EncodedH264Frame, 0, len(rawFrames))
		for _, raw := range rawFrames {
			item, _ := raw.(map[string]any)
			seq, _ := item["sequence"].(float64)
			pts, _ := item["pts_ns"].(float64)
			encoded = append(encoded, EncodedH264Frame{
				Schema: GPUContractVersion, Sequence: uint64(seq), PTSNs: int64(pts), Width: 640, Height: 360,
				Codec: "h264", Profile: "High", Backend: "dx12", PixelReadbackBytes: 0,
				AssetCacheReady: true, AssetReceipt: H264AssetReceipt{ArtworkHash: strings.Repeat("a", 64), GlyphHash: strings.Repeat("b", 64)},
				Payload: []byte{0, 0, 0, 1, 9},
			})
		}
		return encoded
	}
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
			ack := map[string]any{"type": "hello_ack", "version": 1, "session": req["session"], "adapter": "fixture-adapter", "backend": "vulkan", "toolchain": "wgpu-fixture-1"}
			if os.Getenv("IMAGEPAD_SIDECAR_ADVERTISE_H264") == "1" {
				ack["outputs"] = GPUOutputCapabilities{Schema: GPUContractVersion, Formats: []GPUOutputFormat{GPUOutputH264Bitstream}, MaxWidth: GPUMaxDimension, MaxHeight: GPUMaxDimension, RowAlignment: 256}
			}
			_ = enc.Encode(ack)
		case "health":
			_ = enc.Encode(map[string]any{"type": "health", "ready": true, "protocol": 1, "adapter": "fixture-adapter", "backend": "vulkan", "toolchain": "wgpu-fixture-1"})
		case "prepare_track":
			assets, _ := req["assets"].(map[string]any)
			pendingPlaylistFrames = nil
			preparedTimelineFrames = nil
			_ = enc.Encode(map[string]any{"type": "track_ready", "epoch": req["epoch"], "assets_hash": assets["assets_hash"]})
		case "prepare_timeline":
			timeline, _ := req["timeline"].(map[string]any)
			preparedTimelineFrames, _ = timeline["frames"].([]any)
			_ = enc.Encode(map[string]any{"type": "timeline_prepared", "epoch": req["epoch"], "timeline_version": req["timeline_version"], "frame_count": len(preparedTimelineFrames)})
		case "render_timeline_batch":
			timeline, _ := req["timeline"].(map[string]any)
			rawFrames, _ := timeline["frames"].([]any)
			if indices, ok := req["frame_indices"].([]any); ok && len(rawFrames) == 0 {
				rawFrames = make([]any, 0, len(indices))
				for _, idx := range indices {
					want, _ := idx.(float64)
					for _, frame := range preparedTimelineFrames {
						frameMap, _ := frame.(map[string]any)
						if fi, _ := frameMap["frame_index"].(float64); fi == want {
							rawFrames = append(rawFrames, frame)
							break
						}
					}
				}
			}
			batchID := req["batch_id"]
			epoch := req["epoch"]
			timelineVersion := req["timeline_version"]
			submitOnly, _ := req["submit_only"].(bool)
			if submitOnly {
				pendingPlaylistFrames = rawFrames
				_ = enc.Encode(map[string]any{"type": "timeline_submitted", "batch_id": batchID, "epoch": epoch, "timeline_version": timelineVersion})
			} else {
				_ = enc.Encode(map[string]any{"type": "encoded_timeline_batch", "batch_id": batchID, "epoch": epoch, "timeline_version": timelineVersion, "frames": encodePlaylistFrames(rawFrames)})
			}
		case "drain_timeline_batch":
			frames := encodePlaylistFrames(pendingPlaylistFrames)
			if os.Getenv("IMAGEPAD_SIDECAR_BAD_DRAIN_FRAME") == "1" && len(frames) > 0 {
				frames[0].Sequence++
			}
			_ = enc.Encode(map[string]any{"type": "encoded_timeline_batch", "batch_id": req["batch_id"], "epoch": req["epoch"], "timeline_version": req["timeline_version"], "frames": frames})
			pendingPlaylistFrames = nil
		case "render_batch":
			rawFrames, _ := req["frames"].([]any)
			encoded := make([]EncodedH264Frame, 0, len(rawFrames))
			for _, raw := range rawFrames {
				item, _ := raw.(map[string]any)
				seq, _ := item["sequence"].(float64)
				pts, _ := item["pts_ns"].(float64)
				encoded = append(encoded, EncodedH264Frame{
					Schema: GPUContractVersion, Sequence: uint64(seq), PTSNs: int64(pts), Width: 640, Height: 360,
					Codec: "h264", Profile: "High", Backend: "dx12", PixelReadbackBytes: 0,
					AssetCacheReady: true, AssetReceipt: H264AssetReceipt{ArtworkHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", GlyphHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
					Payload: []byte{0, 0, 0, 1, 9},
				})
			}
			batchID := req["batch_id"]
			if os.Getenv("IMAGEPAD_SIDECAR_BAD_BATCH_ID") == "1" {
				batchID = float64(999999)
			}
			_ = enc.Encode(map[string]any{"type": "encoded_batch", "batch_id": batchID, "frames": encoded})
		case "render":
			if output, _ := req["output"].(string); output == string(GPUOutputH264Bitstream) {
				seq, _ := req["sequence"].(float64)
				pts, _ := req["pts_ns"].(float64)
				_ = enc.Encode(map[string]any{"type": "encoded_frame", "frame": EncodedH264Frame{
					Schema: GPUContractVersion, Sequence: uint64(seq), PTSNs: int64(pts), Width: 640, Height: 360,
					Codec: "h264", Profile: "High", Backend: "dx12", PixelReadbackBytes: 0, Payload: []byte{0, 0, 0, 1, 9},
					AssetCacheReady: true, AssetReceipt: H264AssetReceipt{ArtworkHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", GlyphHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
				}})
				continue
			}
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

func TestSidecarResponseUnmarshalH264FrameFailClosed(t *testing.T) {
	var resp sidecarResponse
	if err := json.Unmarshal([]byte(`{"type":"encoded_frame","frame":{"schema":1,"sequence":7,"pts_ns":11,"width":640,"height":360,"codec":"h264","profile":"High","backend":"dx12","pixel_readback_bytes":0,"asset_cache_ready":true,"asset_receipt":{"artwork_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","glyph_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},"payload":"AAABCQ=="}}`), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.EncodedFrame == nil {
		t.Fatal("encoded frame missing")
	}
	if err := resp.EncodedFrame.Validate(); err != nil {
		t.Fatalf("valid encoded frame rejected: %v", err)
	}

	var readback sidecarResponse
	if err := json.Unmarshal([]byte(`{"type":"encoded_frame","frame":{"schema":1,"sequence":7,"pts_ns":11,"width":640,"height":360,"codec":"h264","backend":"dx12","pixel_readback_bytes":4,"payload":"AAABCQ=="}}`), &readback); err == nil {
		t.Fatal("pixel readback was accepted")
	}
}

func TestSidecarProcessRenderSceneH264RequiresNegotiationAndValidates(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1", "IMAGEPAD_SIDECAR_ADVERTISE_H264=1")
	p, err := startSidecarCommand(context.Background(), cmd, "h264-session")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Hello(ctx, "h264-session"); err != nil {
		t.Fatal(err)
	}
	frame, err := p.RenderSceneH264(ctx, 640, 360, 7, 1234, nil)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Sequence != 7 || frame.PTSNs != 1234 || len(frame.Payload) == 0 {
		t.Fatalf("unexpected h264 frame: %+v", frame)
	}
}

func TestSidecarProcessRenderSceneH264BatchPreservesOrder(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1", "IMAGEPAD_SIDECAR_ADVERTISE_H264=1")
	p, err := startSidecarCommand(context.Background(), cmd, "h264-batch-session")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Hello(ctx, "h264-batch-session"); err != nil {
		t.Fatal(err)
	}
	scene := &MusicScenePayload{
		Artwork:    &ArtworkMetadata{AssetHash: strings.Repeat("a", 64)},
		GlyphAtlas: &GlyphAtlasMetadata{AssetHash: strings.Repeat("b", 64)},
	}
	frames, err := p.RenderSceneH264Batch(ctx, 640, 360, []H264RenderBatchFrame{
		{Sequence: 10, PTSNS: 333_333_333, Scene: scene},
		{Sequence: 11, PTSNS: 366_666_666, Scene: scene},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].Sequence != 10 || frames[1].Sequence != 11 {
		t.Fatalf("unexpected H264 batch response: %+v", frames)
	}
}

func TestSidecarProcessPlaylistTimelineSubmitDrainCommitsThreeBoundedChunks(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1", "IMAGEPAD_SIDECAR_ADVERTISE_H264=1")
	p, err := startSidecarCommand(context.Background(), cmd, "playlist-multichunk-session")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Hello(ctx, "playlist-multichunk-session"); err != nil {
		t.Fatal(err)
	}

	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Playlist", Artist: "Artist", Album: "Album"},
		Analysis: AudioAnalysis{
			FPS:      30,
			Duration: float64(3*GPUH264MaxBatchFrames) / 30,
			Frames:   make([]AudioFrame, 3*GPUH264MaxBatchFrames),
		},
	}
	assets, timeline, err := CompilePlaylistGPUTrack(input, "track-multichunk")
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline.Frames) != 3*GPUH264MaxBatchFrames {
		t.Fatalf("timeline frame count = %d, want %d", len(timeline.Frames), 3*GPUH264MaxBatchFrames)
	}
	if err := p.PreparePlaylistTrack(ctx, 9, assets, 640, 360, 30); err != nil {
		t.Fatal(err)
	}

	const batchSize = GPUH264MaxBatchFrames
	for batchID := uint64(0); batchID < 3; batchID++ {
		start := int(batchID) * batchSize
		chunk := TimelineChunk{
			ScrollProfiles: timeline.ScrollProfiles,
			Frames:         append([]FrameDirective(nil), timeline.Frames[start:start+batchSize]...),
		}
		p.mu.Lock()
		cursorBeforeSubmit := p.playlistTimelineCursor
		p.mu.Unlock()
		if err := p.SubmitPlaylistTimelineBatch(ctx, batchID, 9, assets.AssetsHash, timeline.TimelineVersion, chunk, nil); err != nil {
			t.Fatalf("submit batch %d: %v", batchID, err)
		}
		p.mu.Lock()
		cursorAfterSubmit := p.playlistTimelineCursor
		pending := p.playlistPendingBatch != nil
		p.mu.Unlock()
		if cursorAfterSubmit != cursorBeforeSubmit {
			t.Fatalf("submit batch %d advanced the committed cursor before drain: before=%#v after=%#v", batchID, cursorBeforeSubmit, cursorAfterSubmit)
		}
		if !pending {
			t.Fatalf("submit batch %d did not retain pending metadata", batchID)
		}

		frames, err := p.DrainPlaylistTimelineBatch(ctx, batchID, 9, timeline.TimelineVersion)
		if err != nil {
			t.Fatalf("drain batch %d: %v", batchID, err)
		}
		if len(frames) != batchSize {
			t.Fatalf("drain batch %d returned %d frames, want %d", batchID, len(frames), batchSize)
		}
		p.mu.Lock()
		cursor := p.playlistTimelineCursor
		pending = p.playlistPendingBatch != nil
		p.mu.Unlock()
		if !cursor.initialized || cursor.nextBatchID != batchID+1 {
			t.Fatalf("cursor after drain batch %d = %#v", batchID, cursor)
		}
		if pending {
			t.Fatalf("drain batch %d left pending metadata", batchID)
		}
	}
}

func TestSidecarProcessPlaylistTimelineFailedDrainLeavesCursorUnchangedAndPoisons(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1", "IMAGEPAD_SIDECAR_ADVERTISE_H264=1", "IMAGEPAD_SIDECAR_BAD_DRAIN_FRAME=1")
	p, err := startSidecarCommand(context.Background(), cmd, "playlist-failed-drain-session")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.Hello(ctx, "playlist-failed-drain-session"); err != nil {
		t.Fatal(err)
	}

	input := AudioRenderInput{
		Metadata: AudioMetadata{Title: "Playlist", Artist: "Artist", Album: "Album"},
		Analysis: AudioAnalysis{FPS: 30, Duration: 8.0 / 30.0, Frames: make([]AudioFrame, 8)},
	}
	assets, timeline, err := CompilePlaylistGPUTrack(input, "track-failed-drain")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.PreparePlaylistTrack(ctx, 10, assets, 640, 360, 30); err != nil {
		t.Fatal(err)
	}
	chunk := TimelineChunk{ScrollProfiles: timeline.ScrollProfiles, Frames: append([]FrameDirective(nil), timeline.Frames...)}
	p.mu.Lock()
	before := p.playlistTimelineCursor
	p.mu.Unlock()
	if err := p.SubmitPlaylistTimelineBatch(ctx, 0, 10, assets.AssetsHash, timeline.TimelineVersion, chunk, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DrainPlaylistTimelineBatch(ctx, 0, 10, timeline.TimelineVersion); err == nil {
		t.Fatal("drain metadata mismatch was accepted")
	}
	p.mu.Lock()
	after := p.playlistTimelineCursor
	closed := p.closed
	p.mu.Unlock()
	if after != before {
		t.Fatalf("failed drain advanced committed cursor: before=%#v after=%#v", before, after)
	}
	if !closed {
		t.Fatal("failed drain did not poison the sidecar session")
	}
}

func TestSidecarProcessPoisonsSessionAfterBatchContractFailure(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestSidecarHelper")
	cmd.Env = append(os.Environ(), "IMAGEPAD_SIDECAR_HELPER=1", "IMAGEPAD_SIDECAR_ADVERTISE_H264=1", "IMAGEPAD_SIDECAR_BAD_BATCH_ID=1")
	p, err := startSidecarCommand(context.Background(), cmd, "h264-poison-session")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.Hello(ctx, "h264-poison-session"); err != nil {
		t.Fatal(err)
	}
	scene := &MusicScenePayload{
		Artwork:    &ArtworkMetadata{AssetHash: strings.Repeat("a", 64)},
		GlyphAtlas: &GlyphAtlasMetadata{AssetHash: strings.Repeat("b", 64)},
	}
	if _, err := p.RenderSceneH264Batch(ctx, 640, 360, []H264RenderBatchFrame{{Sequence: 10, PTSNS: 333_333_333, Scene: scene}}); err == nil {
		t.Fatal("contract failure should be returned")
	}
	p.mu.Lock()
	poisoned := p.closed
	p.mu.Unlock()
	if !poisoned {
		t.Fatal("sidecar remained reusable after a batch contract failure")
	}
	if _, err := p.RenderSceneH264Batch(ctx, 640, 360, []H264RenderBatchFrame{{Sequence: 10, PTSNS: 333_333_333, Scene: scene}}); !errors.Is(err, ErrGPUUnavailable) {
		t.Fatalf("poisoned session accepted a retry: %v", err)
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

func TestSidecarDiagnosticsUsesExtendedTraceLimitOnlyWhenRequested(t *testing.T) {
	t.Setenv("IMAGEPAD_GPU_STAGE_TRACE_FULL", "")
	if got := sidecarStderrCaptureLimit(); got != sidecarStderrLimit {
		t.Fatalf("default sidecar stderr limit=%d want=%d", got, sidecarStderrLimit)
	}
	t.Setenv("IMAGEPAD_GPU_STAGE_TRACE_FULL", "1")
	if got := sidecarStderrCaptureLimit(); got != sidecarExtendedStderrLimit {
		t.Fatalf("extended sidecar stderr limit=%d want=%d", got, sidecarExtendedStderrLimit)
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

func TestSetPlaylistTransitionPlanCopiesClearsAndRejectsInvalidState(t *testing.T) {
	plan, err := CompilePlaylistTransition(
		TransitionRequest{
			Schema:               GPUPlaylistTimelineSchema,
			Epoch:                7,
			Reason:               PlaylistTransitionTrackChange,
			SourceTrackID:        "track-a",
			PlaybackHeadSequence: 10,
			PlaybackHeadPTSNs:    333_333_330,
		},
		"track-b",
		15,
		500_000_000,
		2,
		6,
		33_333_333,
		AudioFadePlan{Curve: "linear", DurationNS: 199_999_998},
	)
	if err != nil {
		t.Fatal(err)
	}

	p := &SidecarProcess{}
	if err := p.SetPlaylistTransitionPlan(&plan); err != nil {
		t.Fatal(err)
	}
	plan.PlaybackHeadSequence = 999
	if p.playlistTransition == nil || p.playlistTransition.PlaybackHeadSequence != 10 {
		t.Fatalf("transition plan was not copied: %#v", p.playlistTransition)
	}
	if err := p.SetPlaylistTransitionPlan(nil); err != nil {
		t.Fatal(err)
	}
	if p.playlistTransition != nil {
		t.Fatal("nil transition plan did not clear the pending plan")
	}

	invalid := plan
	invalid.Schema = 0
	if err := p.SetPlaylistTransitionPlan(&invalid); err == nil {
		t.Fatal("invalid transition plan was accepted")
	}
	p.closed = true
	if err := p.SetPlaylistTransitionPlan(&plan); !errors.Is(err, ErrGPUUnavailable) {
		t.Fatalf("closed sidecar error = %v", err)
	}
}
