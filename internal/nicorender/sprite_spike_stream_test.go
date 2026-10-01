//go:build nico_sprite_spike

package nicorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestSpriteSpikeStreamAndRing(t *testing.T) {
	exe := os.Getenv("IMAGEPAD_NICO_SPRITE_EXE")
	if exe == "" {
		t.Skip("opt-in native probe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, n := range []int{1, 2, 3, 5, 31} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			frames := make([]spikeFrame, n)
			for i := range frames {
				c := spikeCommand{Rect: [4]float32{-1, -1, 2, 2}, Proj: [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}, Color: [4]float32{0, 0, 0, 1}}
				c.Color[i%3] = 1
				frames[i].Commands = []spikeCommand{c}
			}
			var file, stream bytes.Buffer
			_ = binary.Write(&file, binary.LittleEndian, [5]uint32{0x3153504e, 33, 19, 0, uint32(n)})
			for _, f := range frames {
				_ = binary.Write(&file, binary.LittleEndian, uint32(1))
				_ = binary.Write(&file, binary.LittleEndian, f.Commands)
			}
			_ = binary.Write(&stream, binary.LittleEndian, [4]uint32{0x3253504e, 33, 19, uint32(n)})
			for start := 0; start < n; start += 30 {
				if err := writeSpikeBatch(&stream, spikeBatch{Frames: frames[start:min(n, start+30)]}); err != nil {
					t.Fatal(err)
				}
			}
			_ = binary.Write(&stream, binary.LittleEndian, uint32(0))
			path := filepath.Join(t.TempDir(), "scene.bin")
			if err := os.WriteFile(path, file.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			var reference []byte
			for _, variant := range []struct{ input, depth string }{{path, "1"}, {path, "3"}, {"-", "1"}, {"-", "3"}} {
				cmd := exec.CommandContext(ctx, exe, variant.input, "warp", variant.depth)
				if variant.input == "-" {
					cmd.Stdin = bytes.NewReader(stream.Bytes())
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				p, err := cmd.Output()
				if err != nil {
					t.Fatalf("%v %s", err, stderr.String())
				}
				if len(p) != n*33*19*4 {
					t.Fatalf("bytes %d", len(p))
				}
				for i := 0; i < n; i++ {
					at := i * 33 * 19 * 4
					if p[at+i%3] != 255 || p[at+3] != 255 {
						t.Fatalf("frame %d order/color", i)
					}
				}
				if reference == nil {
					reference = p
				} else if !bytes.Equal(reference, p) {
					t.Fatalf("file/stream or 1/3 readback differs: %+v", variant)
				}
			}
			for label, broken := range map[string][]byte{"truncated": stream.Bytes()[:stream.Len()-5], "missing-end": stream.Bytes()[:stream.Len()-4], "trailing": append(append([]byte{}, stream.Bytes()...), 1)} {
				cmd := exec.CommandContext(ctx, exe, "-", "warp", "3")
				cmd.Stdin = bytes.NewReader(broken)
				if err := cmd.Run(); err == nil {
					t.Fatalf("accepted %s stream", label)
				}
			}
		})
	}
}

func TestSpriteSpikeSavedSceneTransportParity(t *testing.T) {
	exe, path := os.Getenv("IMAGEPAD_NICO_SPRITE_EXE"), os.Getenv("IMAGEPAD_NICO_SPRITE_SCENE")
	if exe == "" || path == "" {
		t.Skip("opt-in saved scene")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	read := func(v any) {
		t.Helper()
		if err := binary.Read(f, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	var header [4]uint32
	read(&header)
	if header[0] != 0x3153504e || header[3] > 10000 {
		t.Fatal("bad fixture header")
	}
	textures := make([]spikeTexture, header[3])
	for i := range textures {
		var h [3]uint32
		read(&h)
		if uint64(h[1])*uint64(h[2])*4 > 128<<20 {
			t.Fatal("fixture texture bound")
		}
		p := make([]byte, int(h[1])*int(h[2])*4)
		if _, err = io.ReadFull(f, p); err != nil {
			t.Fatal(err)
		}
		textures[i] = spikeTexture{ID: h[0], Width: h[1], Height: h[2], Data: base64.StdEncoding.EncodeToString(p)}
	}
	var count uint32
	read(&count)
	if count > 1000000 {
		t.Fatal("fixture frame bound")
	}
	var stream bytes.Buffer
	_ = binary.Write(&stream, binary.LittleEndian, [4]uint32{0x3253504e, header[1], header[2], count})
	for i := uint32(0); i < count; {
		batch := spikeBatch{Textures: textures}
		textures = nil
		for j := uint32(0); j < 30 && i < count; j++ {
			var n uint32
			read(&n)
			if n > 100000 {
				t.Fatal("fixture command bound")
			}
			frame := spikeFrame{Commands: make([]spikeCommand, n)}
			read(&frame.Commands)
			batch.Frames = append(batch.Frames, frame)
			i++
		}
		if err = writeSpikeBatch(&stream, batch); err != nil {
			t.Fatal(err)
		}
	}
	_ = binary.Write(&stream, binary.LittleEndian, uint32(0))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var reference []byte
	for _, variant := range []struct{ input, depth string }{{path, "1"}, {path, "3"}, {"-", "1"}, {"-", "3"}} {
		hash := sha256.New()
		cmd := exec.CommandContext(ctx, exe, variant.input, "warp", variant.depth)
		if variant.input == "-" {
			cmd.Stdin = bytes.NewReader(stream.Bytes())
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		r, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		n, copyErr := io.Copy(hash, r)
		waitErr := cmd.Wait()
		if copyErr != nil || waitErr != nil {
			t.Fatalf("copy=%v wait=%v %s", copyErr, waitErr, stderr.String())
		}
		if n != int64(count)*int64(header[1])*int64(header[2])*4 {
			t.Fatalf("bad byte count %d", n)
		}
		sum := hash.Sum(nil)
		if reference == nil {
			reference = sum
		} else if !bytes.Equal(reference, sum) {
			t.Fatalf("raw pixels changed: %+v", variant)
		}
		t.Logf("input=%s depth=%s frames=%d raw_bytes=%d sha256=%x", variant.input, variant.depth, count, n, sum)
	}
}
