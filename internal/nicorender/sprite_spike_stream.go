//go:build nico_sprite_spike

package nicorender

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"imagepadserver/internal/niconico"
)

// CaptureSpriteSpikeScene preserves the original, complete NPS1 scene for A/B tests.
func CaptureSpriteSpikeScene(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, scene string) (SpriteSpikeStats, error) {
	return captureSpriteSpike(ctx, snapshot, options, scene, nil)
}

// StreamSpriteSpike emits NPS2: header {magic,width,height,totalFrames}, followed
// by {byteLength, textureCount, textures..., frameCount, frames...} batches.
// A zero byteLength is the mandatory end marker. Texture IDs live to EOF; the
// native reader caps their aggregate memory. Only one batch is generated ahead.
func StreamSpriteSpike(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, w io.Writer) (SpriteSpikeStats, error) {
	clock, err := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	if err != nil {
		return SpriteSpikeStats{}, err
	}
	count := clock.FrameCountForDurationMs(options.DurationMs)
	if count < 1 || count > 1000000 {
		return SpriteSpikeStats{}, fmt.Errorf("sprite frame count bound")
	}
	if err = binary.Write(w, binary.LittleEndian, [4]uint32{0x3253504e, uint32(options.Width), uint32(options.Height), uint32(count)}); err != nil {
		return SpriteSpikeStats{}, err
	}
	stats, err := captureSpriteBatches(ctx, snapshot, options, nil, func(batch spikeBatch) error { return writeSpikeBatch(w, batch) })
	if err == nil {
		err = binary.Write(w, binary.LittleEndian, uint32(0))
	}
	return stats, err
}

func writeSpikeBatch(w io.Writer, batch spikeBatch) error {
	if len(batch.Frames) < 1 || len(batch.Frames) > 30 || len(batch.Textures) > 10000 {
		return fmt.Errorf("sprite batch count bound")
	}
	var b bytes.Buffer
	write := func(v any) error { return binary.Write(&b, binary.LittleEndian, v) }
	_ = write(uint32(len(batch.Textures)))
	for _, tex := range batch.Textures {
		p, err := decodeSpikeTexture(tex)
		if err != nil {
			return err
		}
		if int64(b.Len())+int64(len(p))+12 > 512*1024*1024 {
			return fmt.Errorf("sprite batch byte bound")
		}
		_ = write([3]uint32{tex.ID, tex.Width, tex.Height})
		_, _ = b.Write(p)
	}
	_ = write(uint32(len(batch.Frames)))
	for _, frame := range batch.Frames {
		if len(frame.Commands) > 100000 {
			return fmt.Errorf("sprite command count bound")
		}
		_ = write(uint32(len(frame.Commands)))
		_ = write(frame.Commands)
	}
	if b.Len() > 512*1024*1024 {
		return fmt.Errorf("sprite batch byte bound")
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(b.Len())); err != nil {
		return err
	}
	_, err := b.WriteTo(w)
	return err
}
