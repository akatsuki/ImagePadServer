package nicorender

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"testing"
)

func TestSpriteCodecWritesNPS3BatchWithTimingCommandsAndDeletes(t *testing.T) {
	var out bytes.Buffer
	if err := writeSpriteHeader(&out, 320, 180, 2, 30, 1); err != nil {
		t.Fatal(err)
	}
	pixels := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	batch := spriteBatch{
		Textures: []spriteTexture{{ID: 7, Width: 2, Height: 1, RGBA: pixels}},
		Frames:   []spriteFrame{{Sequence: 1, TimeMs: 33, Commands: []spriteCommand{{ID: 7, Rect: [4]float32{1, 2, 3, 4}, Proj: [16]float32{1}, Color: [4]float32{0.5, 0, 0, 0}}}}},
		Deletes:  []uint32{3, 7},
	}
	if err := writeSpriteBatch(&out, batch); err != nil {
		t.Fatal(err)
	}
	if err := writeSpriteTerminator(&out); err != nil {
		t.Fatal(err)
	}
	data := out.Bytes()
	var header [6]uint32
	if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, &header); err != nil {
		t.Fatal(err)
	}
	if header != [6]uint32{0x3353504e, 320, 180, 2, 30, 1} {
		t.Fatalf("header = %#v", header)
	}
	if !bytes.Contains(data, pixels) {
		t.Fatal("raw RGBA texture was not written")
	}
	if got := len(data) - 24; got < 4+4+12+len(pixels)+4+8+4+100+4+8 {
		t.Fatalf("short NPS3 payload: %d", got)
	}
	if got := binary.LittleEndian.Uint32(data[len(data)-4:]); got != 0 {
		t.Fatalf("terminator = %d", got)
	}
}

func TestSpriteCodecRejectsInvalidBatchesAndWriterErrors(t *testing.T) {
	if err := writeSpriteBatch(&bytes.Buffer{}, spriteBatch{}); err == nil {
		t.Fatal("empty batch accepted")
	}
	tooMany := spriteBatch{Frames: make([]spriteFrame, spriteMaxBatchFrames+1)}
	if err := writeSpriteBatch(&bytes.Buffer{}, tooMany); err == nil {
		t.Fatal("oversized frame batch accepted")
	}
	if err := writeSpriteHeader(errorWriter{}, 1, 1, 1, 1, 1); !errors.Is(err, errWriter) {
		t.Fatalf("writer error = %v", err)
	}
}

var errWriter = errors.New("writer failed")

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errWriter }

func spriteB64(v []byte) string { return base64.StdEncoding.EncodeToString(v) }

func TestSpriteCodecBoundedBase64Decode(t *testing.T) {
	if _, err := decodeSpriteBase64("AAAA\n", 3, "pixels"); err == nil {
		t.Fatal("newline accepted")
	}
	if _, err := decodeSpriteBase64("AAAA", 2, "pixels"); err == nil {
		t.Fatal("wrong decoded length accepted")
	}
}

func TestSpriteDeflateRoundTripRejectsShortOversizedAndTrailingData(t *testing.T) {
	raw := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := compressed.Bytes()
	texture := spriteJSONTexture{Width: 2, Height: 1, Data: spriteB64(encoded), Compression: "deflate"}
	got, packed, err := decodeSpriteTexture(texture)
	if err != nil || !bytes.Equal(got, raw) || packed != int64(len(encoded)) {
		t.Fatalf("round trip = %v, packed=%d, err=%v", got, packed, err)
	}
	trailing := append(append([]byte(nil), encoded...), 0x7f)
	texture.Data = spriteB64(trailing)
	if _, _, err := decodeSpriteTexture(texture); err == nil {
		t.Fatal("trailing deflate data accepted")
	}
	texture.Data = spriteB64(encoded[:len(encoded)-1])
	if _, _, err := decodeSpriteTexture(texture); err == nil {
		t.Fatal("truncated deflate data accepted")
	}
	var bomb bytes.Buffer
	bombWriter := zlib.NewWriter(&bomb)
	if _, err := bombWriter.Write(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if err := bombWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := decodeSpriteTexture(spriteJSONTexture{Width: 1, Height: 1, Data: spriteB64(bomb.Bytes()), Compression: "deflate"}); err == nil {
		t.Fatal("oversized deflate output accepted")
	}
	var indexes bytes.Buffer
	indexWriter := zlib.NewWriter(&indexes)
	if _, err := indexWriter.Write(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if err := indexWriter.Close(); err != nil {
		t.Fatal(err)
	}
	paletteTexture := spriteJSONTexture{Width: 32, Height: 32, Data: spriteB64(indexes.Bytes()), Palette: spriteB64([]byte{0, 0, 0, 255}), Encoding: "palette8", Compression: "deflate"}
	if decoded, packed, err := decodeSpriteTexture(paletteTexture); err != nil || len(decoded) != 32*32*4 || packed != int64(len(indexes.Bytes())+4) {
		t.Fatalf("palette deflate = len=%d packed=%d err=%v", len(decoded), packed, err)
	}
}
