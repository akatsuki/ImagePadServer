//go:build nico_mask_probe

package nicorender

// This file is deliberately a test-only browser fixture. It is excluded from
// normal builds and never changes the production renderer or its startup path.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

const (
	nicoMaskMagic          = uint32(0x31464d4e) // NMF1, little endian
	nicoMaskMaxFileBytes   = int64(1 << 30)
	nicoMaskMaxTextures    = uint32(10000)
	nicoMaskMaxTextureSize = int64(128 << 20)
	nicoMaskMaxTotalBytes  = int64(512 << 20)
	nicoMaskMaxCommands    = uint64(10000000)
)

type nicoMaskTexture struct {
	ID            uint32  `json:"id"`
	Width         uint32  `json:"width"`
	Height        uint32  `json:"height"`
	Fill          string  `json:"fill"`
	RGBA          string  `json:"rgba"`
	FillRGBA      uint32  `json:"fillRGBA"`
	OutlineRGBA   uint32  `json:"outlineRGBA"`
	OutlineRadius float32 `json:"outlineRadiusPx"`
	Fallback      bool    `json:"fallback"`
}

type nicoMaskCommand struct {
	ID    uint32      `json:"id"`
	Rect  [4]float32  `json:"rect"`
	Proj  [16]float32 `json:"proj"`
	Color [4]float32  `json:"color"`
}

type nicoMaskFrame struct {
	Sequence uint64            `json:"sequence"`
	TimeMs   uint64            `json:"timeMs"`
	Commands []nicoMaskCommand `json:"commands"`
}

type nicoMaskBatch struct {
	Textures []nicoMaskTexture `json:"textures"`
	Frames   []nicoMaskFrame   `json:"frames"`
}

type nicoMaskFixtureMetadata struct {
	Schema               int      `json:"schema"`
	Format               string   `json:"format"`
	Fixture              string   `json:"fixture"`
	Width                int      `json:"width"`
	Height               int      `json:"height"`
	FPSNum               int64    `json:"fpsNum"`
	FPSDen               int64    `json:"fpsDen"`
	DurationMs           int64    `json:"durationMs"`
	Density              int      `json:"density"`
	ExpectedConcurrent   int      `json:"expectedConcurrent"`
	ActualCommandsMin    int      `json:"actualCommandsMin"`
	ActualCommandsMax    int      `json:"actualCommandsMax"`
	CommentCount         int      `json:"commentCount"`
	Capture              string   `json:"capture"`
	StrokeTextCalls      int      `json:"strokeTextCalls"`
	FillTextCalls        int      `json:"fillTextCalls"`
	StrokeTextSuppressed bool     `json:"strokeTextSuppressed"`
	TextureCount         int      `json:"textureCount"`
	FallbackTextures     int      `json:"fallbackTextures"`
	FontHashKind         string   `json:"fontHashKind"`
	SourceHash           string   `json:"sourceHash"`
	FixtureHash          string   `json:"fixtureHash"`
	FontHash             string   `json:"fontHash"`
	Fonts                []string `json:"fonts,omitempty"`
}

type nicoMaskProbeResult struct {
	Metadata nicoMaskFixtureMetadata
	Textures []nicoMaskTexture
	Frames   []nicoMaskFrame
}

type nicoMaskFixtureSpec struct {
	name               string
	width, height      int
	fpsNum, fpsDen     int64
	durationMs         int64
	density            int
	expectedConcurrent int
	snapshot           niconico.Snapshot
}

type nicoMaskCaptureMetadata struct {
	StrokeTextCalls      int      `json:"strokeTextCalls"`
	FillTextCalls        int      `json:"fillTextCalls"`
	StrokeTextSuppressed bool     `json:"strokeTextSuppressed"`
	Fonts                []string `json:"fonts"`
}

func TestNicoMaskProbe(t *testing.T) {
	if strings.TrimSpace(os.Getenv("NICO_MASK_PROBE_OUT")) == "" {
		t.Skip("set NICO_MASK_PROBE_OUT to run the opt-in browser fixture")
	}
	if err := runNicoMaskProbe(); err != nil {
		t.Fatal(err)
	}
}

func runNicoMaskProbe() error {
	out := strings.TrimSpace(os.Getenv("NICO_MASK_PROBE_OUT"))
	if out == "" {
		return errors.New("nico mask probe: NICO_MASK_PROBE_OUT is required")
	}
	fixture, err := nicoMaskFixture()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("nico mask probe: output directory: %w", err)
	}
	bundle, cleanupBundle, err := materializeBundle("")
	if err != nil {
		return err
	}
	defer cleanupBundle()
	bundleBytes, err := os.ReadFile(bundle)
	if err != nil {
		return fmt.Errorf("nico mask probe: read renderer bundle: %w", err)
	}
	capturePath, err := nicoMaskCapturePath()
	if err != nil {
		return err
	}
	captureScript, err := os.ReadFile(capturePath)
	if err != nil {
		return fmt.Errorf("nico mask probe: read capture script: %w", err)
	}
	transport, err := newFrameTransport(fixture.width, fixture.height)
	if err != nil {
		return err
	}
	defer transport.close()
	transport.config.SparseFrames = false
	page, err := writeRendererPage(fixture.width, fixture.height, bundle, fixture.snapshot.RendererThreads(), &transport.config)
	if err != nil {
		return err
	}
	defer os.Remove(page)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	session, err := startBrowser(ctx, "", page)
	if err != nil {
		return err
	}
	defer session.close()
	if err := session.waitReady(ctx, true); err != nil {
		return err
	}
	if err := spriteEvaluate(ctx, session, spriteCaptureScript, nil); err != nil {
		return err
	}
	if err := spriteEvaluate(ctx, session, string(captureScript), nil); err != nil {
		return fmt.Errorf("nico mask probe: install capture hooks: %w", err)
	}
	var ready bool
	if err := spriteEvaluate(ctx, session, "window.__nicoMaskReady === true", &ready); err != nil || !ready {
		if err != nil {
			return err
		}
		return errors.New("nico mask probe: capture hooks are not ready")
	}
	conn, err := transport.waitConn(ctx)
	if err != nil {
		return err
	}
	clock, err := niconico.NewFrameClock(fixture.fpsNum, fixture.fpsDen)
	if err != nil {
		return err
	}
	frameCount := clock.FrameCountForDurationMs(fixture.durationMs)
	result := nicoMaskProbeResult{Metadata: nicoMaskFixtureMetadata{
		Schema: 1, Format: "NMF1", Fixture: fixture.name,
		Width: fixture.width, Height: fixture.height, FPSNum: fixture.fpsNum,
		FPSDen: fixture.fpsDen, DurationMs: fixture.durationMs, Density: fixture.density,
		ExpectedConcurrent: fixture.expectedConcurrent,
		CommentCount:       fixture.snapshot.CommentCount,
		Capture:            "Canvas2D fillText A8 + original RGBA; strokeText preserved for reference",
		SourceHash:         digestHex(bundleBytes), FixtureHash: digestSnapshot(fixture.snapshot),
	}}
	dumpAllFrames := strings.TrimSpace(os.Getenv("NICO_MASK_PROBE_DUMP_ALL")) == "1"
	selectedFrames := map[int]struct{}{0: {}, 30: {}, 75: {}, 180: {}, 300: {}, int(frameCount - 1): {}}
	browserFrames := make(map[int][]byte, len(selectedFrames))
	browserFrameStem := filepath.Join(out, strings.ToLower(fixture.name))
	lastFull := []byte(nil)
	for first := int64(0); first < frameCount; first += 30 {
		last := first + 30
		if last > frameCount {
			last = frameCount
		}
		times := make([]int64, last-first)
		for i := range times {
			times[i] = clock.CommentTimeMs(first + int64(i))
		}
		if err := requestBinaryBatch(ctx, session, uint64(first), times, true); err != nil {
			return fmt.Errorf("nico mask probe: request frames %d-%d: %w", first, last-1, err)
		}
		for j, timeMs := range times {
			sequence := uint64(first + int64(j))
			packet, err := transport.receivePacket(ctx, conn)
			if err != nil {
				return fmt.Errorf("nico mask probe: receive frame %d: %w", sequence, err)
			}
			header, pixels, err := decodeFramePacket(packet, frameHeader{Sequence: sequence, TimeMs: uint64(timeMs), Width: uint32(fixture.width), Height: uint32(fixture.height)})
			if err != nil {
				return fmt.Errorf("nico mask probe: decode frame %d: %w", sequence, err)
			}
			if header.Kind == frameKindRepeat {
				if lastFull == nil {
					return fmt.Errorf("nico mask probe: repeat frame %d has no reference", sequence)
				}
				pixels = lastFull
			} else {
				lastFull = pixels
			}
			if dumpAllFrames {
				path := fmt.Sprintf("%s.browser.frame-%03d.rgba", browserFrameStem, sequence)
				if err := os.WriteFile(path, pixels, 0o644); err != nil {
					return fmt.Errorf("nico mask probe: write all-frame pixel %d: %w", sequence, err)
				}
			} else if _, ok := selectedFrames[int(sequence)]; ok {
				browserFrames[int(sequence)] = append([]byte(nil), pixels...)
			}
			if err := transport.acknowledge(conn, sequence); err != nil {
				return err
			}
		}
		var batch nicoMaskBatch
		if err := spriteEvaluate(ctx, session, "window.__nicoMaskTake()", &batch); err != nil {
			return fmt.Errorf("nico mask probe: take frames %d-%d: %w", first, last-1, err)
		}
		if len(batch.Frames) != len(times) {
			return fmt.Errorf("nico mask probe: captured %d frames, want %d", len(batch.Frames), len(times))
		}
		result.Textures = append(result.Textures, batch.Textures...)
		result.Frames = append(result.Frames, batch.Frames...)
	}
	transport.stop(conn, "completed")
	var captureMeta nicoMaskCaptureMetadata
	if err := spriteEvaluate(ctx, session, "window.__nicoMaskMetadata()", &captureMeta); err != nil {
		return fmt.Errorf("nico mask probe: metadata: %w", err)
	}
	result.Metadata.StrokeTextCalls = captureMeta.StrokeTextCalls
	result.Metadata.FillTextCalls = captureMeta.FillTextCalls
	result.Metadata.StrokeTextSuppressed = captureMeta.StrokeTextSuppressed
	result.Metadata.Fonts = append([]string(nil), captureMeta.Fonts...)
	sort.Strings(result.Metadata.Fonts)
	result.Metadata.FontHash = digestHex([]byte(strings.Join(result.Metadata.Fonts, "\n")))
	result.Metadata.TextureCount = len(result.Textures)
	result.Metadata.FontHashKind = "sha256 of CSS font descriptions, not font file bytes"
	for _, texture := range result.Textures {
		if texture.Fallback {
			result.Metadata.FallbackTextures++
		}
	}
	if fixture.name != "F0" && result.Metadata.FallbackTextures == len(result.Textures) {
		return errors.New("nico mask probe: no eligible fill-only mask textures")
	}
	result.Metadata.ActualCommandsMin, result.Metadata.ActualCommandsMax = commandRange(result.Frames)
	if err := validateNicoMaskResult(result, uint32(frameCount)); err != nil {
		return err
	}
	stem := filepath.Join(out, strings.ToLower(result.Metadata.Fixture))
	if err := writeNicoMaskNMF1(stem+".mask.nmf1", fixture, result, true); err != nil {
		return err
	}
	if err := writeNicoMaskNMF1(stem+".reference.nmf1", fixture, result, false); err != nil {
		return err
	}
	for frame, pixels := range browserFrames {
		if err := os.WriteFile(fmt.Sprintf("%s.browser.frame-%03d.rgba", stem, frame), pixels, 0o644); err != nil {
			return err
		}
	}
	metadata, err := json.MarshalIndent(result.Metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(stem+".metadata.json", append(metadata, '\n'), 0o644); err != nil {
		return err
	}
	snapshotJSON, err := json.MarshalIndent(fixture.snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(stem+".snapshot.json", append(snapshotJSON, '\n'), 0o644); err != nil {
		return err
	}
	return nil
}

func nicoMaskFixture() (nicoMaskFixtureSpec, error) {
	name := strings.ToUpper(strings.TrimSpace(os.Getenv("NICO_MASK_PROBE_FIXTURE")))
	if name == "" {
		name = "F1"
	}
	spec := nicoMaskFixtureSpec{name: name, width: 1920, height: 1080, fpsNum: 60, fpsDen: 1, durationMs: 10000, density: 30, expectedConcurrent: 30}
	if snapshotPath := strings.TrimSpace(os.Getenv("NICO_MASK_SNAPSHOT")); snapshotPath != "" {
		data, err := os.ReadFile(snapshotPath)
		if err != nil {
			return nicoMaskFixtureSpec{}, fmt.Errorf("nico mask probe: read snapshot: %w", err)
		}
		if err := json.Unmarshal(data, &spec.snapshot); err != nil {
			return nicoMaskFixtureSpec{}, fmt.Errorf("nico mask probe: decode snapshot: %w", err)
		}
		spec.name, spec.durationMs, spec.density, spec.expectedConcurrent = "REAL", 6000, spec.snapshot.CommentCount, spec.snapshot.CommentCount
	} else if name == "F0" {
		spec.density, spec.expectedConcurrent = 0, 0
		spec.snapshot = niconico.Snapshot{VideoID: "nico-mask-f0", SelectedForks: []string{"main"}}
	} else if name == "F1" {
		spec.snapshot = nicoMaskFixedSnapshot(30, "nico-mask-f1")
	} else if name == "DENSITY100" {
		spec.density = 100
		spec.snapshot = nicoMaskFixedSnapshot(100, "nico-mask-density100")
	} else {
		return nicoMaskFixtureSpec{}, fmt.Errorf("nico mask probe: unsupported fixture %q", name)
	}
	normalized, err := niconico.NormalizeSnapshot(spec.snapshot)
	if err != nil {
		return nicoMaskFixtureSpec{}, err
	}
	spec.snapshot = normalized
	return spec, nil
}

func nicoMaskFixedSnapshot(count int, videoID string) niconico.Snapshot {
	comments := make([]niconico.Comment, 0, count*4)
	for batch := 0; batch < 4; batch++ {
		for i := 0; i < count; i++ {
			commands := []string{"white"}
			switch {
			case i < 10:
				commands = append(commands, "ue")
			case i < 20:
				commands = append(commands, "shita")
			}
			comments = append(comments, niconico.Comment{
				ID: fmt.Sprintf("%s-%d-%03d", videoID, batch, i+1), No: int64(batch*count + i + 1), VposMs: int64(batch * 3000),
				Body: "白コメント", Commands: commands,
				PostedAt: "2026-01-01T00:00:00Z",
			})
		}
	}
	return niconico.Snapshot{VideoID: videoID, SelectedForks: []string{"main"}, Threads: []niconico.Thread{{ID: videoID + "-main", Fork: "main", Comments: comments}}}
}

func commandRange(frames []nicoMaskFrame) (int, int) {
	if len(frames) == 0 {
		return 0, 0
	}
	minCount, maxCount := len(frames[0].Commands), len(frames[0].Commands)
	for _, frame := range frames[1:] {
		if len(frame.Commands) < minCount {
			minCount = len(frame.Commands)
		}
		if len(frame.Commands) > maxCount {
			maxCount = len(frame.Commands)
		}
	}
	return minCount, maxCount
}

func nicoMaskCapturePath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("NICO_MASK_CAPTURE")); configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return "", fmt.Errorf("nico mask probe: configured capture.js not found at %s: %w", configured, err)
		}
		return configured, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	candidates := []string{
		filepath.Join(wd, "scripts", "experiments", "nico-mask-probe", "capture.js"),
		filepath.Join(wd, "..", "..", "scripts", "experiments", "nico-mask-probe", "capture.js"),
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("nico mask probe: capture.js not found from %s", wd)
}

func validateNicoMaskResult(result nicoMaskProbeResult, frameCount uint32) error {
	if result.Metadata.Width <= 0 || result.Metadata.Height <= 0 || result.Metadata.Width > 3840 || result.Metadata.Height > 2160 {
		return errors.New("nico mask probe: invalid output dimensions")
	}
	if uint32(len(result.Textures)) > nicoMaskMaxTextures || uint32(len(result.Frames)) != frameCount {
		return fmt.Errorf("nico mask probe: texture/frame count out of bounds: %d/%d", len(result.Textures), len(result.Frames))
	}
	seen := make(map[uint32]struct{}, len(result.Textures))
	var total int64
	for _, texture := range result.Textures {
		if texture.ID == 0 || texture.Width == 0 || texture.Height == 0 || texture.Width > 16384 || texture.Height > 16384 {
			return fmt.Errorf("nico mask probe: invalid texture %d", texture.ID)
		}
		if _, ok := seen[texture.ID]; ok {
			return fmt.Errorf("nico mask probe: duplicate texture %d", texture.ID)
		}
		seen[texture.ID] = struct{}{}
		pixels := uint64(texture.Width) * uint64(texture.Height)
		if pixels > uint64(nicoMaskMaxTextureSize) || pixels*4 > uint64(nicoMaskMaxTextureSize) {
			return fmt.Errorf("nico mask probe: texture %d exceeds size bound", texture.ID)
		}
		fill, err := decodeNicoMaskBase64(texture.Fill, int64(pixels))
		if err != nil {
			return fmt.Errorf("nico mask probe: texture %d fill: %w", texture.ID, err)
		}
		if len(fill) == 0 {
			return fmt.Errorf("nico mask probe: texture %d has empty fill mask", texture.ID)
		}
		if texture.OutlineRadius < 0 || !finiteNicoMask(float64(texture.OutlineRadius)) {
			return fmt.Errorf("nico mask probe: texture %d has invalid outline radius", texture.ID)
		}
		rgba, err := decodeNicoMaskBase64(texture.RGBA, int64(pixels*4))
		if err != nil {
			return fmt.Errorf("nico mask probe: texture %d rgba: %w", texture.ID, err)
		}
		if len(rgba) == 0 {
			return fmt.Errorf("nico mask probe: texture %d has empty rgba reference", texture.ID)
		}
		total += int64(len(fill)) + int64(len(rgba))
		if total > nicoMaskMaxTotalBytes {
			return errors.New("nico mask probe: total texture bytes exceed bound")
		}
	}
	var commands uint64
	for i, frame := range result.Frames {
		wantTime := uint64((int64(i) * result.Metadata.FPSDen * 1000) / result.Metadata.FPSNum)
		if frame.Sequence != uint64(i) || frame.TimeMs != wantTime {
			return fmt.Errorf("nico mask probe: frame %d timestamp mismatch", i)
		}
		commands += uint64(len(frame.Commands))
		if commands > nicoMaskMaxCommands {
			return errors.New("nico mask probe: command count exceeds bound")
		}
		for _, command := range frame.Commands {
			if command.ID == 0 {
				continue
			}
			if _, ok := seen[command.ID]; !ok {
				return fmt.Errorf("nico mask probe: command references unknown texture %d", command.ID)
			}
			for _, value := range command.Rect {
				if !finiteNicoMask(float64(value)) {
					return errors.New("nico mask probe: non-finite command rectangle")
				}
			}
			for _, value := range command.Proj {
				if !finiteNicoMask(float64(value)) {
					return errors.New("nico mask probe: non-finite command projection")
				}
			}
			for _, value := range command.Color {
				if !finiteNicoMask(float64(value)) {
					return errors.New("nico mask probe: non-finite command color")
				}
			}
		}
	}
	if result.Metadata.Fixture == "F1" && commands == 0 {
		return errors.New("nico mask probe: F1 produced no draw commands")
	}
	return nil
}

func finiteNicoMask(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func decodeNicoMaskBase64(value string, want int64) ([]byte, error) {
	if want < 0 || len(value) == 0 || strings.ContainsAny(value, "\r\n") {
		return nil, errors.New("invalid base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || int64(len(decoded)) != want {
		return nil, fmt.Errorf("payload length %d, want %d", len(decoded), want)
	}
	return decoded, nil
}

func writeNicoMaskNMF1(path string, fixture nicoMaskFixtureSpec, result nicoMaskProbeResult, mask bool) error {
	var payload bytes.Buffer
	header := [7]uint32{nicoMaskMagic, uint32(fixture.width), uint32(fixture.height), uint32(fixture.fpsNum), uint32(fixture.fpsDen), uint32(len(result.Textures)), uint32(len(result.Frames))}
	if err := binary.Write(&payload, binary.LittleEndian, header); err != nil {
		return err
	}
	for _, texture := range result.Textures {
		value, kind := texture.RGBA, uint32(0)
		want := int64(texture.Width) * int64(texture.Height) * 4
		if mask && !texture.Fallback {
			value, kind, want = texture.Fill, 1, int64(texture.Width)*int64(texture.Height)
		}
		data, err := decodeNicoMaskBase64(value, want)
		if err != nil {
			return fmt.Errorf("nico mask probe: %s texture %d: %w", path, texture.ID, err)
		}
		if int64(len(data)) > nicoMaskMaxTextureSize {
			return errors.New("nico mask probe: texture payload exceeds bound")
		}
		entry := [8]uint32{texture.ID, texture.Width, texture.Height, kind, texture.FillRGBA, texture.OutlineRGBA, uint32(math.Round(float64(texture.OutlineRadius))), uint32(len(data))}
		if err := binary.Write(&payload, binary.LittleEndian, entry); err != nil {
			return err
		}
		if _, err := payload.Write(data); err != nil {
			return err
		}
	}
	for _, frame := range result.Frames {
		if err := binary.Write(&payload, binary.LittleEndian, uint32(len(frame.Commands))); err != nil {
			return err
		}
		for _, command := range frame.Commands {
			if err := writeNicoMaskCommand(&payload, command); err != nil {
				return err
			}
		}
	}
	if int64(payload.Len()) > nicoMaskMaxFileBytes {
		return errors.New("nico mask probe: NMF1 exceeds file bound")
	}
	return os.WriteFile(path, payload.Bytes(), 0o644)
}

func writeNicoMaskCommand(w io.Writer, command nicoMaskCommand) error {
	if err := binary.Write(w, binary.LittleEndian, command.ID); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, command.Rect); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, command.Proj); err != nil {
		return err
	}
	return binary.Write(w, binary.LittleEndian, command.Color)
}

func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func digestSnapshot(snapshot niconico.Snapshot) string {
	data, _ := json.Marshal(snapshot)
	return digestHex(data)
}
