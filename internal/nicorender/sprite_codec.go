package nicorender

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"
)

const (
	spriteMagic           = uint32(0x3353504e)
	spriteMaxBatchFrames  = 30
	spriteMaxBatchBytes   = int64(512 << 20)
	spriteMaxTextureBytes = int64(128 << 20)
	spriteMaxActiveBytes  = int64(512 << 20)
	spriteMaxActiveIDs    = 10000
	spriteMaxFrames       = int64(1000000)
	spriteMaxCommands     = 100000
)

type spriteTexture struct {
	ID, Width, Height uint32
	// RGBA contains tightly packed, exact framebuffer readback bytes.
	RGBA []byte
	// Data, Encoding and Palette are used while decoding the browser payload.
	Data, Encoding, Palette string
	PackedBytes             int64
}

type spriteCommand struct {
	ID    uint32
	Rect  [4]float32
	Proj  [16]float32
	Color [4]float32
}

type spriteFrame struct {
	Sequence uint64
	TimeMs   uint64
	Commands []spriteCommand
}

type spriteBatch struct {
	Textures []spriteTexture
	Frames   []spriteFrame
	Deletes  []uint32
}

func writeSpriteHeader(w io.Writer, width, height, frames, fpsNum, fpsDen uint32) error {
	if width == 0 || height == 0 || frames > uint32(spriteMaxFrames) || fpsNum == 0 || fpsDen == 0 {
		return fmt.Errorf("nps3: invalid header")
	}
	return binary.Write(w, binary.LittleEndian, [6]uint32{spriteMagic, width, height, frames, fpsNum, fpsDen})
}

func writeSpriteTerminator(w io.Writer) error {
	return binary.Write(w, binary.LittleEndian, uint32(0))
}

func writeSpriteBatch(w io.Writer, batch spriteBatch) error {
	return writeSpriteBatchReuse(w, batch, nil)
}

func writeSpriteBatchReuse(w io.Writer, batch spriteBatch, reuse *bytes.Buffer) error {
	return writeSpriteBatchReuseScratch(w, batch, reuse, nil)
}

func writeSpriteBatchReuseScratch(w io.Writer, batch spriteBatch, reuse *bytes.Buffer, scratch *[]byte) error {
	if len(batch.Frames) == 0 || len(batch.Frames) > spriteMaxBatchFrames {
		return fmt.Errorf("nps3: sprite batch frame count bound")
	}
	if len(batch.Textures) > spriteMaxActiveIDs || len(batch.Deletes) > spriteMaxActiveIDs {
		return fmt.Errorf("nps3: sprite batch texture count bound")
	}
	if len(batch.Textures) > int(^uint32(0)) || len(batch.Frames) > int(^uint32(0)) || len(batch.Deletes) > int(^uint32(0)) {
		return fmt.Errorf("nps3: sprite batch count overflow")
	}
	var planned int64 = 4 + 4 + 4 + int64(len(batch.Deletes))*4
	for _, tex := range batch.Textures {
		if tex.Width == 0 || tex.Height == 0 || tex.Width > 16384 || tex.Height > 16384 {
			return fmt.Errorf("nps3: invalid texture dimensions %dx%d", tex.Width, tex.Height)
		}
		expected := uint64(tex.Width) * uint64(tex.Height) * 4
		if expected > uint64(spriteMaxTextureBytes) || expected != uint64(len(tex.RGBA)) {
			return fmt.Errorf("nps3: invalid texture byte length for %d", tex.ID)
		}
		planned += 12 + int64(len(tex.RGBA))
	}
	for _, frame := range batch.Frames {
		if len(frame.Commands) > spriteMaxCommands {
			return fmt.Errorf("nps3: sprite command count bound")
		}
		planned += 20 + int64(len(frame.Commands))*100
	}
	if planned > spriteMaxBatchBytes {
		return fmt.Errorf("nps3: sprite batch byte bound")
	}
	var local bytes.Buffer
	payload := &local
	if reuse != nil {
		payload = reuse
		payload.Reset()
	}
	write := func(v any) error { return binary.Write(payload, binary.LittleEndian, v) }
	if err := write(uint32(len(batch.Textures))); err != nil {
		return err
	}
	for _, tex := range batch.Textures {
		if tex.Width == 0 || tex.Height == 0 || tex.Width > 16384 || tex.Height > 16384 {
			return fmt.Errorf("nps3: invalid texture dimensions %dx%d", tex.Width, tex.Height)
		}
		expected := uint64(tex.Width) * uint64(tex.Height) * 4
		if expected > uint64(spriteMaxTextureBytes) || expected != uint64(len(tex.RGBA)) {
			return fmt.Errorf("nps3: invalid texture byte length for %d", tex.ID)
		}
		if int64(payload.Len())+12+int64(len(tex.RGBA)) > spriteMaxBatchBytes {
			return fmt.Errorf("nps3: sprite batch byte bound")
		}
		if err := write([3]uint32{tex.ID, tex.Width, tex.Height}); err != nil {
			return err
		}
		if _, err := payload.Write(tex.RGBA); err != nil {
			return err
		}
	}
	if err := write(uint32(len(batch.Frames))); err != nil {
		return err
	}
	for _, frame := range batch.Frames {
		if len(frame.Commands) > spriteMaxCommands {
			return fmt.Errorf("nps3: sprite command count bound")
		}
		if err := write(frame.Sequence); err != nil {
			return err
		}
		if err := write(frame.TimeMs); err != nil {
			return err
		}
		if err := write(uint32(len(frame.Commands))); err != nil {
			return err
		}
		var commandData []byte
		if scratch != nil {
			commandData = commandBytesInto((*scratch)[:0], frame.Commands)
			*scratch = commandData
		} else {
			commandData = commandBytes(frame.Commands)
		}
		if _, err := payload.Write(commandData); err != nil {
			return err
		}
	}
	if err := write(uint32(len(batch.Deletes))); err != nil {
		return err
	}
	for _, id := range batch.Deletes {
		if err := write(id); err != nil {
			return err
		}
	}
	if int64(payload.Len()) > spriteMaxBatchBytes {
		return fmt.Errorf("nps3: sprite batch byte bound")
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(payload.Len())); err != nil {
		return err
	}
	n, err := w.Write(payload.Bytes())
	if err == nil && n != payload.Len() {
		return io.ErrShortWrite
	}
	return err
}

func commandBytes(commands []spriteCommand) []byte {
	if len(commands) == 0 {
		return nil
	}
	return commandBytesInto(make([]byte, len(commands)*100), commands)
}

func commandBytesInto(b []byte, commands []spriteCommand) []byte {
	need := len(commands) * 100
	if cap(b) < need {
		b = make([]byte, need)
	} else {
		b = b[:need]
	}
	for i, command := range commands {
		o := b[i*100:]
		binary.LittleEndian.PutUint32(o, command.ID)
		for j, value := range command.Rect {
			binary.LittleEndian.PutUint32(o[4+j*4:], math.Float32bits(value))
		}
		for j, value := range command.Proj {
			binary.LittleEndian.PutUint32(o[20+j*4:], math.Float32bits(value))
		}
		for j, value := range command.Color {
			binary.LittleEndian.PutUint32(o[84+j*4:], math.Float32bits(value))
		}
	}
	return b
}

func decodeSpriteBase64(value string, want int64, name string) ([]byte, error) {
	if want < 0 || want > spriteMaxTextureBytes || strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("nps3: invalid %s base64", name)
	}
	encoded := ((want + 2) / 3) * 4
	if int64(len(value)) != encoded {
		return nil, fmt.Errorf("nps3: invalid %s base64 length %d, want %d", name, len(value), encoded)
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("nps3: invalid %s base64: %w", name, err)
	}
	if int64(len(data)) != want {
		return nil, fmt.Errorf("nps3: invalid %s length %d, want %d", name, len(data), want)
	}
	return data, nil
}

func decodeSpriteBase64Bounded(value string, max int64, name string) ([]byte, error) {
	if max < 0 || strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("nps3: invalid %s base64", name)
	}
	maxEncoded := ((max + 2) / 3) * 4
	if int64(len(value)) > maxEncoded {
		return nil, fmt.Errorf("nps3: %s base64 exceeds bound", name)
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("nps3: invalid %s base64: %w", name, err)
	}
	if int64(len(decoded)) > max {
		return nil, fmt.Errorf("nps3: %s exceeds bound", name)
	}
	return decoded, nil
}
