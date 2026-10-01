//go:build nico_sprite_spike

package nicorender

// Throwaway, opt-in feasibility probe. Never built into the normal server.
import (
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"imagepadserver/internal/niconico"
)

//go:embed sprite_spike.js
var spriteSpikeScript string

type spikeTexture struct {
	ID, Width, Height uint32
	Data              string
	Encoding          string
	Palette           string
}
type spikeCommand struct {
	ID    uint32
	Rect  [4]float32
	Proj  [16]float32
	Color [4]float32
}
type spikeFrame struct {
	Sequence uint64
	TimeMs   int64
	Commands []spikeCommand
}
type spikeBatch struct {
	Textures []spikeTexture
	Frames   []spikeFrame
}
type SpriteSpikeStats struct {
	CaptureMs, CompositeMs                          float64
	TextureBytes, CommandBytes                      int64
	Textures, Frames                                int
	Backend                                         string
	PackedTextureBytes                              int64
	PaletteTextures, GrayAlphaTextures, RawTextures int
}

func spikeEvaluate(ctx context.Context, s *browserSession, expression string, out any) error {
	raw, err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true})
	if err != nil {
		return err
	}
	var envelope struct {
		Result           struct{ Value json.RawMessage }
		ExceptionDetails json.RawMessage
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if len(envelope.ExceptionDetails) > 0 {
		return fmt.Errorf("sprite probe JS: %s", envelope.ExceptionDetails)
	}
	if out != nil {
		return json.Unmarshal(envelope.Result.Value, out)
	}
	return nil
}

func captureSpriteBatches(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, reference FrameSink, consume func(spikeBatch) error) (SpriteSpikeStats, error) {
	var stats SpriteSpikeStats
	started := time.Now()
	clock, err := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	if err != nil {
		return stats, err
	}
	if options.Width <= 0 || options.Height <= 0 || options.Width > 3840 || options.Height > 2160 || options.DurationMs <= 0 {
		return stats, fmt.Errorf("invalid sprite geometry/duration")
	}
	bundle, cleanup, err := materializeBundle(options.BundlePath)
	if err != nil {
		return stats, err
	}
	defer cleanup()
	transport, err := newFrameTransport(options.Width, options.Height)
	if err != nil {
		return stats, err
	}
	defer transport.close()
	transport.config.SparseFrames = true
	page, err := writeRendererPage(options.Width, options.Height, bundle, snapshot.RendererThreads(), &transport.config)
	if err != nil {
		return stats, err
	}
	defer os.Remove(page)
	session, err := startBrowser(ctx, options.BrowserPath, page)
	if err != nil {
		return stats, err
	}
	defer session.close()
	if err = session.waitReady(ctx, true); err != nil {
		return stats, err
	}
	if err = spikeEvaluate(ctx, session, spriteSpikeScript, nil); err != nil {
		return stats, err
	}
	if os.Getenv("IMAGEPAD_NICO_SPRITE_PACK") == "1" {
		if err = spikeEvaluate(ctx, session, "window.__spikePack=true", nil); err != nil {
			return stats, err
		}
	}
	if reference != nil {
		if err = spikeEvaluate(ctx, session, "window.__spikeReference=true", nil); err != nil {
			return stats, err
		}
	}
	conn, err := transport.waitConn(ctx)
	if err != nil {
		return stats, err
	}
	count := clock.FrameCountForDurationMs(options.DurationMs)
	for i := int64(0); i < count; {
		times := make([]int64, min(30, count-i))
		for j := range times {
			times[j] = clock.CommentTimeMs(i + int64(j))
		}
		if reference != nil {
			if err = requestBinaryBatch(ctx, session, uint64(i), times, true); err != nil {
				return stats, err
			}
			for j, ts := range times {
				seq := uint64(i + int64(j))
				packet, e := transport.receivePacket(ctx, conn)
				if e != nil {
					return stats, e
				}
				_, pixels, e := decodeFramePacket(packet, frameHeader{Sequence: seq, TimeMs: uint64(ts), Width: uint32(options.Width), Height: uint32(options.Height)})
				if e != nil {
					return stats, e
				}
				if e = reference.WriteRGBA(ctx, seq, pixels); e != nil {
					return stats, e
				}
				if e = transport.acknowledge(conn, seq); e != nil {
					return stats, e
				}
			}
		} else {
			data, _ := json.Marshal(times)
			if err = spikeEvaluate(ctx, session, fmt.Sprintf("window.__spikeDraw(%d,%s)", i, data), nil); err != nil {
				return stats, err
			}
		}
		var batch spikeBatch
		if err = spikeEvaluate(ctx, session, "window.__spikeTake()", &batch); err != nil {
			return stats, err
		}
		if len(batch.Frames) != len(times) {
			return stats, fmt.Errorf("sprite frame count %d, want %d", len(batch.Frames), len(times))
		}
		for j := range batch.Frames {
			batch.Frames[j].Sequence = uint64(i + int64(j))
			batch.Frames[j].TimeMs = times[j]
		}
		// Consume before the next browser request. The pipe applies backpressure,
		// and __spikeTake releases its per-batch arrays.
		if err = consume(batch); err != nil {
			return stats, err
		}
		for _, tex := range batch.Textures {
			stats.TextureBytes += int64(tex.Width) * int64(tex.Height) * 4
			stats.PackedTextureBytes += int64(decodedBase64Size(tex.Data) + decodedBase64Size(tex.Palette))
			switch tex.Encoding {
			case "palette8":
				stats.PaletteTextures++
			case "grayalpha8":
				stats.GrayAlphaTextures++
			default:
				stats.RawTextures++
			}
		}
		for _, frame := range batch.Frames {
			stats.CommandBytes += int64(len(frame.Commands)) * 100
		}
		stats.Textures += len(batch.Textures)
		stats.Frames += len(batch.Frames)
		i += int64(len(times))
	}
	stats.CaptureMs = float64(time.Since(started).Microseconds()) / 1000
	transport.stop(conn, "completed")
	return stats, nil
}

func decodedBase64Size(s string) int {
	n := len(s) / 4 * 3
	if len(s) > 0 && s[len(s)-1] == '=' {
		n--
	}
	if len(s) > 1 && s[len(s)-2] == '=' {
		n--
	}
	return n
}

func captureSpriteSpike(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, scene string, reference FrameSink) (SpriteSpikeStats, error) {
	var frames []spikeFrame
	var textures []spikeTexture
	stats, err := captureSpriteBatches(ctx, snapshot, options, reference, func(batch spikeBatch) error {
		textures = append(textures, batch.Textures...)
		frames = append(frames, batch.Frames...)
		return nil
	})
	if err != nil {
		return stats, err
	}
	started := time.Now()
	f, err := os.Create(scene)
	if err != nil {
		return stats, err
	}
	defer f.Close()
	write := func(v any) error { return binary.Write(f, binary.LittleEndian, v) }
	if err = write([4]uint32{0x3153504e, uint32(options.Width), uint32(options.Height), uint32(len(textures))}); err != nil {
		return stats, err
	}
	for _, tex := range textures {
		pixels, e := decodeSpikeTexture(tex)
		if e != nil {
			return stats, e
		}
		if tex.Width == 0 || tex.Height == 0 || len(pixels) != int(tex.Width)*int(tex.Height)*4 {
			return stats, fmt.Errorf("invalid sprite texture size")
		}
		if err = write([3]uint32{tex.ID, tex.Width, tex.Height}); err != nil {
			return stats, err
		}
		if _, err = f.Write(pixels); err != nil {
			return stats, err
		}
	}
	if err = write(uint32(len(frames))); err != nil {
		return stats, err
	}
	for _, frame := range frames {
		if err = write(uint32(len(frame.Commands))); err != nil {
			return stats, err
		}
		for _, cmd := range frame.Commands {
			if err = write(cmd); err != nil {
				return stats, err
			}
		}
	}
	if err = f.Close(); err != nil {
		return stats, err
	}
	stats.CaptureMs += float64(time.Since(started).Microseconds()) / 1000
	return stats, nil
}

// RenderSpriteSpike uses the same source library's commands and a separate
// D3D11 compositor, followed by the caller's unchanged frame sink/encoder.
func RenderSpriteSpike(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, sink FrameSink) (SpriteSpikeStats, error) {
	if scene := os.Getenv("IMAGEPAD_NICO_SPRITE_SCENE"); scene != "" {
		started := time.Now()
		err := runSpriteSpike(ctx, scene, options, sink)
		return SpriteSpikeStats{Backend: spriteSpikeDriver(), CompositeMs: float64(time.Since(started).Microseconds()) / 1000}, err
	}
	dir, err := os.MkdirTemp("", "nico-sprite-spike-*")
	if err != nil {
		return SpriteSpikeStats{}, err
	}
	defer os.RemoveAll(dir)
	scene := filepath.Join(dir, "scene.bin")
	stats, err := captureSpriteSpike(ctx, snapshot, options, scene, nil)
	if err != nil {
		return stats, err
	}
	stats.Backend = spriteSpikeDriver()
	started := time.Now()
	err = runSpriteSpike(ctx, scene, options, sink)
	stats.CompositeMs = float64(time.Since(started).Microseconds()) / 1000
	return stats, err
}

func spriteSpikeDriver() string {
	if value := os.Getenv("IMAGEPAD_NICO_SPRITE_DRIVER"); value != "" {
		return value
	}
	return "warp"
}

func runSpriteSpike(ctx context.Context, scene string, options RenderOptions, sink FrameSink) error {
	exe := os.Getenv("IMAGEPAD_NICO_SPRITE_EXE")
	if exe == "" {
		return fmt.Errorf("set IMAGEPAD_NICO_SPRITE_EXE")
	}
	driver := spriteSpikeDriver()
	if driver != "warp" && driver != "hardware" {
		return fmt.Errorf("unknown sprite probe driver %q", driver)
	}
	depth := os.Getenv("IMAGEPAD_NICO_SPRITE_READBACK")
	if depth == "" {
		depth = "1"
	}
	cmd := exec.CommandContext(ctx, exe, scene, driver, depth)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	waited := false
	defer func() {
		if !waited && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	clock, _ := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	for frame := int64(0); frame < clock.FrameCountForDurationMs(options.DurationMs); frame++ {
		pixels := make([]byte, options.Width*options.Height*4)
		if _, err = io.ReadFull(stdout, pixels); err != nil {
			return fmt.Errorf("sprite output frame %d: %w", frame, err)
		}
		if err = sink.WriteRGBA(ctx, uint64(frame), pixels); err != nil {
			return err
		}
	}
	var extra [1]byte
	n, e := stdout.Read(extra[:])
	if n != 0 || e != io.EOF {
		return fmt.Errorf("unexpected extra sprite output: %d, %v", n, e)
	}
	err = cmd.Wait()
	waited = true
	return err
}
