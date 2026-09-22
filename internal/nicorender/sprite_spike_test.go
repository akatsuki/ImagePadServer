//go:build nico_sprite_spike

package nicorender

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"imagepadserver/internal/niconico"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type spriteCompareSink struct {
	t             *testing.T
	reference     [][]byte
	diff          int64
	max           int
	total         int64
	output        string
	width, height int
	saved         bool
	reader        io.Reader
}

func (s *spriteCompareSink) WriteRGBA(_ context.Context, seq uint64, p []byte) error {
	if s.reader == nil && int(seq) >= len(s.reference) {
		return fmt.Errorf("extra native frame")
	}
	var ref []byte
	if s.reader != nil {
		ref = make([]byte, len(p))
		if _, err := io.ReadFull(s.reader, ref); err != nil {
			return err
		}
	} else {
		ref = s.reference[seq]
	}
	if len(ref) != len(p) {
		return fmt.Errorf("pixel length mismatch")
	}
	localMax := 0
	for i, b := range ref {
		d := int(b) - int(p[i])
		if d < 0 {
			d = -d
		}
		if d > 0 {
			s.diff++
		}
		if d > s.max {
			s.max = d
		}
		if d > localMax {
			localMax = d
		}
		s.total += int64(d)
	}
	if localMax > 1 && !s.saved && s.output != "" {
		for name, data := range map[string][]byte{"reference": ref, "sprite": p} {
			f, e := os.Create(filepath.Join(s.output, name+".png"))
			if e != nil {
				return e
			}
			e = png.Encode(f, &image.RGBA{Pix: data, Stride: s.width * 4, Rect: image.Rect(0, 0, s.width, s.height)})
			f.Close()
			if e != nil {
				return e
			}
		}
		s.saved = true
	}
	return nil
}

type spriteFileSink struct{ w io.Writer }

func (s spriteFileSink) WriteRGBA(_ context.Context, _ uint64, p []byte) error {
	_, err := s.w.Write(p)
	return err
}

func TestSpriteSpikeRealPixels(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_SPRITE_EXE") == "" || os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT") == "" {
		t.Skip("opt-in real snapshot")
	}
	data, err := os.ReadFile(os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot niconico.Snapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	options := RenderOptions{Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1, Transport: "binary"}
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "reference.rgba"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scene := filepath.Join(dir, "scene.bin")
	stats, err := captureSpriteSpike(ctx, snapshot, options, scene, spriteFileSink{f})
	if err != nil {
		t.Fatal(err)
	}
	if dest := os.Getenv("IMAGEPAD_NICO_SPRITE_SAVE_SCENE"); dest != "" {
		data, e := os.ReadFile(scene)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(dest, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	compare := &spriteCompareSink{t: t, reader: f, output: os.Getenv("IMAGEPAD_NICO_SPRITE_ARTIFACTS"), width: options.Width, height: options.Height}
	if compare.output != "" {
		if err = os.MkdirAll(compare.output, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err = runSpriteSpike(ctx, scene, options, compare); err != nil {
		t.Fatal(err)
	}
	var extra [1]byte
	if n, e := f.Read(extra[:]); n != 0 || e != io.EOF {
		t.Fatal("uncompared reference frames")
	}
	if stats.Frames != 180 {
		t.Fatal("wrong frame count")
	}
	t.Logf("frames=%d textures=%d texture_bytes=%d command_bytes=%d max_delta=%d changed_bytes=%d mean_abs=%.6f", stats.Frames, stats.Textures, stats.TextureBytes, stats.CommandBytes, compare.max, compare.diff, float64(compare.total)/float64(stats.Frames*options.Width*options.Height*4))
	if compare.max > 1 {
		t.Errorf("native pixels exceed existing PNG/binary tolerance (1): %d", compare.max)
	}
}

func TestSpriteSpikePixels(t *testing.T) {
	if os.Getenv("IMAGEPAD_NICO_SPRITE_EXE") == "" {
		t.Skip("opt-in native probe")
	}
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{VideoID: "sm9", Threads: []niconico.Thread{{ID: "t", Fork: "main", Comments: []niconico.Comment{
		{ID: "a", VposMs: 1000, Body: "流れるコメント", Commands: []string{"red"}, PostedAt: "2026-01-01T00:00:00Z"},
		{ID: "b", VposMs: 2000, Body: "上固定\n二行目", Commands: []string{"ue", "blue"}, PostedAt: "2026-01-01T00:00:00Z"},
		{ID: "c", VposMs: 2500, Body: "下固定", Commands: []string{"shita", "green"}, PostedAt: "2026-01-01T00:00:00Z"},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	options := RenderOptions{Width: 321, Height: 181, DurationMs: 8000, FPSNum: 10, FPSDen: 1, Transport: "binary"}
	dir := t.TempDir()
	if value := os.Getenv("IMAGEPAD_NICO_SPRITE_ARTIFACTS"); value != "" {
		dir = value
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	scene := filepath.Join(dir, "scene.bin")
	ref := &collectingSink{}
	stats, err := captureSpriteSpike(ctx, snapshot, options, scene, ref)
	if err != nil {
		t.Fatal(err)
	}
	compare := &spriteCompareSink{t: t, reference: ref.frames, output: dir, width: options.Width, height: options.Height}
	if err = runSpriteSpike(ctx, scene, options, compare); err != nil {
		t.Fatal(err)
	}
	if len(ref.frames) != 80 {
		t.Fatal("wrong frame count")
	}
	visible := false
	for _, f := range ref.frames {
		for i := 3; i < len(f); i += 4 {
			if f[i] != 0 {
				visible = true
				break
			}
		}
	}
	if !visible {
		t.Fatal("all transparent")
	}
	t.Logf("frames=%d textures=%d texture_bytes=%d command_bytes=%d max_delta=%d changed_bytes=%d mean_abs=%.6f", stats.Frames, stats.Textures, stats.TextureBytes, stats.CommandBytes, compare.max, compare.diff, float64(compare.total)/float64(len(ref.frames)*options.Width*options.Height*4))
	if compare.max > 1 {
		t.Errorf("native pixels exceed existing PNG/binary tolerance (1): %d", compare.max)
	}
}
