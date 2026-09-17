package nicorender

import (
	"bytes"
	"compress/zlib"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"imagepadserver/internal/niconico"
)

//go:embed assets/sprites.js
var spriteCaptureScript string

// SpriteReport describes an NPS3 capture. RenderReport remains the common
// frame contract used by the regular renderer and downstream encoders.
type SpriteReport struct {
	RenderReport
	TextureBytes       int64
	PackedTextureBytes int64
	Textures           int
}

type spriteJSONTexture struct {
	ID          uint32 `json:"id"`
	Width       uint32 `json:"width"`
	Height      uint32 `json:"height"`
	Data        string `json:"data"`
	Encoding    string `json:"encoding"`
	Palette     string `json:"palette"`
	Compression string `json:"compression"`
}

type spriteJSONCommand struct {
	ID    uint32      `json:"id"`
	Rect  [4]float32  `json:"rect"`
	Proj  [16]float32 `json:"proj"`
	Color [4]float32  `json:"color"`
}

type spriteJSONFrame struct {
	Commands []spriteJSONCommand `json:"commands"`
}

type spriteJSONBatch struct {
	Textures []spriteJSONTexture `json:"textures"`
	Frames   []spriteJSONFrame   `json:"frames"`
	Deletes  []uint32            `json:"deletes"`
}

func validateSpriteOptions(options RenderOptions) error {
	if options.Width <= 0 || options.Height <= 0 || options.Width > 3840 || options.Height > 2160 {
		return fmt.Errorf("niconico: invalid render size %dx%d", options.Width, options.Height)
	}
	if options.DurationMs < 0 {
		return fmt.Errorf("niconico: negative duration %dms", options.DurationMs)
	}
	if options.FPSNum <= 0 || options.FPSDen <= 0 || options.FPSNum > 1000000 || options.FPSDen > 1000000 {
		return fmt.Errorf("nps3: fps numerator and denominator must be <= 1000000")
	}
	clock, err := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	if err != nil {
		return err
	}
	count := clock.FrameCountForDurationMs(options.DurationMs)
	if count < 1 {
		return fmt.Errorf("nps3: at least one output frame is required")
	}
	if count > spriteMaxFrames {
		return fmt.Errorf("nps3: frame count bound")
	}
	return nil
}

// WriteSpriteStream captures the browser's exact WebGL sprite draw stream and
// writes a bounded, length-prefixed NPS3 stream. Capture has no progress side
// effects; native frame delivery reports progress after it consumes frames.
func WriteSpriteStream(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, w io.Writer) (SpriteReport, error) {
	compression := options.SpriteCompression
	if compression == "" {
		compression = "deflate"
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SpriteReport{}, err
	}
	if w == nil {
		return SpriteReport{}, errors.New("nps3: writer is required")
	}
	if err := validateSpriteOptions(options); err != nil {
		return SpriteReport{}, err
	}
	if compression != "" && compression != "none" && compression != "deflate" {
		return SpriteReport{}, fmt.Errorf("nps3: unsupported sprite compression %q", compression)
	}
	clock, _ := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	frameCount := clock.FrameCountForDurationMs(options.DurationMs)
	report := SpriteReport{RenderReport: RenderReport{FrameCount: frameCount, Width: options.Width, Height: options.Height, FPSNum: options.FPSNum, FPSDen: options.FPSDen, RendererLabel: options.RendererLabel}}
	if err := writeSpriteHeader(w, uint32(options.Width), uint32(options.Height), uint32(frameCount), uint32(options.FPSNum), uint32(options.FPSDen)); err != nil {
		return SpriteReport{}, err
	}
	bundle, cleanupBundle, err := materializeBundle(options.BundlePath)
	if err != nil {
		return SpriteReport{}, err
	}
	defer cleanupBundle()
	pagePath, err := writeRendererPage(options.Width, options.Height, bundle, snapshot.RendererThreads(), nil)
	if err != nil {
		return SpriteReport{}, err
	}
	defer os.Remove(pagePath)
	session, err := startBrowser(ctx, options.BrowserPath, pagePath)
	if err != nil {
		return SpriteReport{}, err
	}
	defer session.close()
	if err := session.waitReady(ctx, false); err != nil {
		return SpriteReport{}, err
	}
	if err := spriteEvaluate(ctx, session, spriteCaptureScript, nil); err != nil {
		return SpriteReport{}, err
	}
	if err := spriteEvaluate(ctx, session, fmt.Sprintf("window.__nicoSpritesDeflate = %t", compression == "deflate"), nil); err != nil {
		return SpriteReport{}, err
	}
	var ready bool
	if err := spriteEvaluate(ctx, session, "window.__nicoSpritesReady === true", &ready); err != nil {
		return SpriteReport{}, err
	}
	if !ready {
		return SpriteReport{}, errors.New("nps3: sprite capture page is not ready")
	}

	active := make(map[uint32]int64)
	var activeBytes int64
	var nextID uint32
	var timeScratch [spriteMaxBatchFrames]int64
	var batchBuffer bytes.Buffer
	var commandScratch []byte
	for first := int64(0); first < frameCount; first += spriteMaxBatchFrames {
		if err := ctx.Err(); err != nil {
			return SpriteReport{}, err
		}
		last := first + spriteMaxBatchFrames
		if last > frameCount {
			last = frameCount
		}
		times := timeScratch[:last-first]
		for i := range times {
			times[i] = clock.CommentTimeMs(first + int64(i))
		}
		timesJSON, _ := json.Marshal(times)
		if err := spriteEvaluate(ctx, session, "window.__nicoSpritesDraw("+string(timesJSON)+")", nil); err != nil {
			return SpriteReport{}, fmt.Errorf("nps3: capture frames %d-%d: %w", first, last-1, err)
		}
		var raw spriteJSONBatch
		if err := spriteEvaluate(ctx, session, "window.__nicoSpritesTake()", &raw); err != nil {
			return SpriteReport{}, err
		}
		batch, textureBytes, packedBytes, err := decodeSpriteBatch(raw, uint64(first), times)
		if err != nil {
			return SpriteReport{}, err
		}
		if err := validateSpriteIDs(batch.Textures, batch.Deletes, active, activeBytes, nextID); err != nil {
			return SpriteReport{}, err
		}
		if len(batch.Textures) > 0 {
			nextID = batch.Textures[len(batch.Textures)-1].ID
		}
		if err := writeSpriteBatchReuseScratch(w, batch, &batchBuffer, &commandScratch); err != nil {
			return SpriteReport{}, err
		}
		for _, texture := range batch.Textures {
			bytes := int64(len(texture.RGBA))
			active[texture.ID] = bytes
			activeBytes += bytes
			report.TextureBytes += bytes
			report.PackedTextureBytes += texture.PackedBytes
			report.Textures++
		}
		for _, id := range batch.Deletes {
			activeBytes -= active[id]
			delete(active, id)
		}
		_ = textureBytes
		_ = packedBytes
	}
	if err := writeSpriteTerminator(w); err != nil {
		return SpriteReport{}, err
	}
	report.RenderReport.SpriteTextureBytes = report.TextureBytes
	report.RenderReport.SpritePayloadBytes = report.PackedTextureBytes
	report.RenderReport.SpriteTextures = report.Textures
	return report, nil
}

func spriteEvaluate(ctx context.Context, session *browserSession, expression string, out any) error {
	raw, err := session.call(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return err
	}
	var envelope struct {
		Result struct {
			Value            json.RawMessage `json:"value"`
			ExceptionDetails json.RawMessage `json:"exceptionDetails"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if len(envelope.ExceptionDetails) > 0 && string(envelope.ExceptionDetails) != "null" {
		return fmt.Errorf("nps3: browser evaluate: %s", envelope.ExceptionDetails)
	}
	if len(envelope.Result.ExceptionDetails) > 0 && string(envelope.Result.ExceptionDetails) != "null" {
		return fmt.Errorf("nps3: browser evaluate: %s", envelope.Result.ExceptionDetails)
	}
	if out == nil {
		return nil
	}
	if len(envelope.Result.Value) == 0 || string(envelope.Result.Value) == "null" {
		return errors.New("nps3: browser returned no value")
	}
	if err := json.Unmarshal(envelope.Result.Value, out); err != nil {
		return err
	}
	return nil
}

func decodeSpriteBatch(raw spriteJSONBatch, first uint64, times []int64) (spriteBatch, int64, int64, error) {
	if len(raw.Frames) != len(times) || len(raw.Frames) == 0 || len(raw.Frames) > spriteMaxBatchFrames {
		return spriteBatch{}, 0, 0, fmt.Errorf("nps3: browser frame count %d, want %d", len(raw.Frames), len(times))
	}
	if len(raw.Textures) > spriteMaxActiveIDs || len(raw.Deletes) > spriteMaxActiveIDs {
		return spriteBatch{}, 0, 0, fmt.Errorf("nps3: browser texture count bound")
	}
	var planned int64 = 12 + int64(len(raw.Deletes))*4
	for _, texture := range raw.Textures {
		if texture.Width == 0 || texture.Height == 0 || texture.Width > 16384 || texture.Height > 16384 {
			return spriteBatch{}, 0, 0, fmt.Errorf("nps3: invalid texture dimensions")
		}
		planned += 12 + int64(texture.Width)*int64(texture.Height)*4
		if planned > spriteMaxBatchBytes {
			return spriteBatch{}, 0, 0, fmt.Errorf("nps3: sprite batch byte bound")
		}
	}
	for _, frame := range raw.Frames {
		if len(frame.Commands) > spriteMaxCommands {
			return spriteBatch{}, 0, 0, fmt.Errorf("nps3: sprite command count bound")
		}
		planned += 20 + int64(len(frame.Commands))*100
		if planned > spriteMaxBatchBytes {
			return spriteBatch{}, 0, 0, fmt.Errorf("nps3: sprite batch byte bound")
		}
	}
	batch := spriteBatch{Frames: make([]spriteFrame, len(raw.Frames)), Deletes: append([]uint32(nil), raw.Deletes...)}
	var textureBytes, packedBytes int64
	for _, texture := range raw.Textures {
		decoded, packed, err := decodeSpriteTexture(texture)
		if err != nil {
			return spriteBatch{}, 0, 0, err
		}
		batch.Textures = append(batch.Textures, spriteTexture{ID: texture.ID, Width: texture.Width, Height: texture.Height, RGBA: decoded, PackedBytes: packed})
		textureBytes += int64(len(decoded))
		packedBytes += packed
		if textureBytes > spriteMaxActiveBytes {
			return spriteBatch{}, 0, 0, fmt.Errorf("nps3: sprite batch texture bytes bound")
		}
	}
	for i, frame := range raw.Frames {
		commands := make([]spriteCommand, len(frame.Commands))
		for j, command := range frame.Commands {
			commands[j] = spriteCommand{ID: command.ID, Rect: command.Rect, Proj: command.Proj, Color: command.Color}
		}
		batch.Frames[i] = spriteFrame{Sequence: first + uint64(i), TimeMs: uint64(times[i]), Commands: commands}
	}
	return batch, textureBytes, packedBytes, nil
}

func decodeSpriteTexture(texture spriteJSONTexture) ([]byte, int64, error) {
	if texture.Width == 0 || texture.Height == 0 || texture.Width > 16384 || texture.Height > 16384 {
		return nil, 0, fmt.Errorf("nps3: invalid texture dimensions %dx%d", texture.Width, texture.Height)
	}
	pixels := uint64(texture.Width) * uint64(texture.Height)
	rawLen := pixels * 4
	if rawLen > uint64(spriteMaxTextureBytes) {
		return nil, 0, fmt.Errorf("nps3: sprite texture exceeds %d bytes", spriteMaxTextureBytes)
	}
	if texture.Encoding == "" || texture.Encoding == "rgba" {
		if texture.Palette != "" {
			return nil, 0, fmt.Errorf("nps3: palette supplied for RGBA texture")
		}
		data, packed, err := decodeSpriteDataWithSize(texture.Data, int64(rawLen), texture.Compression, "rgba data")
		return data, packed, err
	}
	if texture.Encoding == "grayalpha8" {
		if texture.Palette != "" {
			return nil, 0, fmt.Errorf("nps3: palette supplied for grayalpha8 texture")
		}
		data, packed, err := decodeSpriteDataWithSize(texture.Data, int64(pixels*2), texture.Compression, "grayalpha8 data")
		if err != nil {
			return nil, 0, err
		}
		out := make([]byte, rawLen)
		for i := uint64(0); i < pixels; i++ {
			out[i*4], out[i*4+1], out[i*4+2], out[i*4+3] = data[i*2], data[i*2], data[i*2], data[i*2+1]
		}
		return out, packed, nil
	}
	if texture.Encoding != "palette8" {
		return nil, 0, fmt.Errorf("nps3: unknown sprite texture encoding %q", texture.Encoding)
	}
	if strings.ContainsAny(texture.Palette, "\r\n") || len(texture.Palette) > 1368 {
		return nil, 0, fmt.Errorf("nps3: invalid palette base64 length")
	}
	palette, err := base64.StdEncoding.DecodeString(texture.Palette)
	if err != nil || len(palette) == 0 || len(palette)%4 != 0 || len(palette) > 256*4 {
		return nil, 0, fmt.Errorf("nps3: invalid palette")
	}
	indexes, packed, err := decodeSpriteDataWithSize(texture.Data, int64(pixels), texture.Compression, "palette8 indexes")
	if err != nil {
		return nil, 0, err
	}
	out := make([]byte, rawLen)
	for i, index := range indexes {
		if int(index)*4+4 > len(palette) {
			return nil, 0, fmt.Errorf("nps3: palette index %d out of range", index)
		}
		copy(out[i*4:], palette[int(index)*4:int(index)*4+4])
	}
	return out, packed + int64(len(palette)), nil
}

func decodeSpriteDataWithSize(data string, expected int64, compression, name string) ([]byte, int64, error) {
	if compression == "" {
		decoded, err := decodeSpriteBase64(data, expected, name)
		return decoded, int64(len(decoded)), err
	}
	if compression != "deflate" {
		return nil, 0, fmt.Errorf("nps3: unknown texture compression %q", compression)
	}
	encoded, err := decodeSpriteBase64Bounded(data, spriteMaxTextureBytes, name)
	if err != nil {
		return nil, 0, err
	}
	source := bytes.NewReader(encoded)
	reader, err := zlib.NewReader(source)
	if err != nil {
		return nil, 0, fmt.Errorf("nps3: invalid %s deflate: %w", name, err)
	}
	out := make([]byte, expected)
	if _, err := io.ReadFull(reader, out); err != nil {
		reader.Close()
		return nil, 0, fmt.Errorf("nps3: short %s deflate: %w", name, err)
	}
	var extra [1]byte
	if n, readErr := reader.Read(extra[:]); n != 0 || readErr != io.EOF {
		reader.Close()
		return nil, 0, fmt.Errorf("nps3: oversized %s deflate", name)
	}
	if err := reader.Close(); err != nil {
		return nil, 0, fmt.Errorf("nps3: invalid %s deflate: %w", name, err)
	}
	if source.Len() != 0 {
		return nil, 0, fmt.Errorf("nps3: trailing %s deflate data", name)
	}
	return out, int64(len(encoded)), nil
}

func validateSpriteIDs(textures []spriteTexture, deletes []uint32, active map[uint32]int64, activeBytes int64, lastID uint32) error {
	if len(active)+len(textures) > spriteMaxActiveIDs {
		return fmt.Errorf("nps3: active texture ID bound")
	}
	batchIDs := make(map[uint32]struct{}, len(textures))
	previousID := lastID
	for _, texture := range textures {
		if texture.ID == 0 || texture.ID <= previousID {
			return fmt.Errorf("nps3: texture IDs must be monotonic")
		}
		if _, ok := active[texture.ID]; ok {
			return fmt.Errorf("nps3: active texture ID reused: %d", texture.ID)
		}
		if _, ok := batchIDs[texture.ID]; ok {
			return fmt.Errorf("nps3: duplicate texture ID: %d", texture.ID)
		}
		batchIDs[texture.ID] = struct{}{}
		previousID = texture.ID
		activeBytes += int64(len(texture.RGBA))
		if activeBytes > spriteMaxActiveBytes {
			return fmt.Errorf("nps3: active texture bytes bound")
		}
	}
	deleteIDs := make(map[uint32]struct{}, len(deletes))
	for _, id := range deletes {
		if _, duplicate := deleteIDs[id]; duplicate {
			return fmt.Errorf("nps3: duplicate texture deletion ID %d", id)
		}
		deleteIDs[id] = struct{}{}
		if _, ok := batchIDs[id]; !ok {
			if _, ok = active[id]; !ok {
				return fmt.Errorf("nps3: deleting unknown texture ID %d", id)
			}
		}
	}
	return nil
}
