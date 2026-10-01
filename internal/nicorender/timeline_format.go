package nicorender

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"

	"imagepadserver/internal/niconico"
)

const (
	TimelineHeaderBytes        = 80
	TimelineDrawBytes          = 128
	TimelineFlags              = 3 // premultiplied alpha and top-left row origin
	MaxTimelineWidth           = 3840
	MaxTimelineHeight          = 2160
	MaxTimelineFrames          = 1_000_000
	MaxTimelineFPSComponent    = 1_000_000
	MaxTimelineAssetDimension  = 16_384
	MaxTimelineAssets          = 10_000
	MaxTimelineDraws           = 100_000
	MaxTimelineAssetBytes      = 128 << 20
	MaxTimelineTotalAssetBytes = 256 << 20
	maxExactFloat32Integer     = 1 << 24
)

var (
	errTimelineFormat = errors.New("niconico timeline: invalid NCT1 stream")
	errTimelineLimit  = errors.New("niconico timeline: NCT1 limit exceeded")
)

// TimelineHeader is the fixed metadata prefix of an NCT1 stream.
type TimelineHeader struct {
	Width, Height, FrameCount, FPSNum, FPSDen uint32
	BundleSHA256                              [32]byte
}

// TimelineAsset stores premultiplied RGBA8 pixels in top-left row order.
type TimelineAsset struct {
	ID, Width, Height uint32
	SHA256            [32]byte
	RGBA              []byte
}

// TimelineDraw describes one half-open vpos interval using an asset.
type TimelineDraw struct {
	AssetID                                  uint32
	StartVPos, EndVPos, AnchorVPos           int32
	OwnerOrder, CommentIndex, PrimitiveIndex uint32
	Rect                                     [4]float32
	Projection                               [16]float32
	Alpha                                    float32
	AnchorX, SpeedX                          float64
}

// CommentTimeline is the validated scene exchanged over the NCT1 wire format.
type CommentTimeline struct {
	Header TimelineHeader
	Assets []TimelineAsset
	Draws  []TimelineDraw
}

// TimelineVPos returns floor(frame * fpsDen * 100 / fpsNum), including for
// negative frames. Big integers keep the checked result independent of the
// machine word size; only the final signed vpos range is accepted.
func TimelineVPos(clock niconico.FrameClock, frame int64) (int32, error) {
	if clock.Num <= 0 || clock.Den <= 0 || clock.Num > MaxTimelineFPSComponent || clock.Den > MaxTimelineFPSComponent || clock.Num > 60*clock.Den {
		return 0, fmt.Errorf("%w: invalid frame clock %d/%d", errTimelineFormat, clock.Num, clock.Den)
	}
	n := new(big.Int).SetInt64(frame)
	n.Mul(n, big.NewInt(clock.Den))
	n.Mul(n, big.NewInt(100))
	d := big.NewInt(clock.Num)
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(n, d, r)
	if n.Sign() < 0 && r.Sign() != 0 {
		q.Sub(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, fmt.Errorf("%w: vpos overflow", errTimelineLimit)
	}
	v := q.Int64()
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("%w: vpos %d is outside int32", errTimelineLimit, v)
	}
	return int32(v), nil
}

// ValidateCommentTimeline checks semantic limits before serialization or use.
func ValidateCommentTimeline(scene CommentTimeline) error {
	h := scene.Header
	if h.Width == 0 || h.Width > MaxTimelineWidth || h.Height == 0 || h.Height > MaxTimelineHeight {
		return fmt.Errorf("%w: output dimensions %dx%d", errTimelineLimit, h.Width, h.Height)
	}
	if h.FrameCount == 0 || h.FrameCount > MaxTimelineFrames {
		return fmt.Errorf("%w: frame count %d", errTimelineLimit, h.FrameCount)
	}
	if h.FPSNum == 0 || h.FPSDen == 0 || h.FPSNum > MaxTimelineFPSComponent || h.FPSDen > MaxTimelineFPSComponent || uint64(h.FPSNum) > uint64(h.FPSDen)*60 {
		return fmt.Errorf("%w: frame rate %d/%d", errTimelineLimit, h.FPSNum, h.FPSDen)
	}
	if len(scene.Assets) > MaxTimelineAssets || len(scene.Draws) > MaxTimelineDraws {
		return fmt.Errorf("%w: %d assets, %d draws", errTimelineLimit, len(scene.Assets), len(scene.Draws))
	}

	clock := niconico.FrameClock{Num: int64(h.FPSNum), Den: int64(h.FPSDen)}
	firstVPos, err := TimelineVPos(clock, 0)
	if err != nil {
		return err
	}
	lastVPos, err := TimelineVPos(clock, int64(h.FrameCount)-1)
	if err != nil {
		return fmt.Errorf("%w: output frame range: %v", errTimelineLimit, err)
	}

	assets := make(map[uint32]struct{}, len(scene.Assets))
	var totalRaw uint64
	for i := range scene.Assets {
		a := &scene.Assets[i]
		if _, exists := assets[a.ID]; exists {
			return fmt.Errorf("%w: duplicate asset id %d", errTimelineFormat, a.ID)
		}
		assets[a.ID] = struct{}{}
		if a.Width == 0 || a.Height == 0 || a.Width > MaxTimelineAssetDimension || a.Height > MaxTimelineAssetDimension {
			return fmt.Errorf("%w: asset %d dimensions %dx%d", errTimelineLimit, a.ID, a.Width, a.Height)
		}
		pixels := uint64(a.Width) * uint64(a.Height)
		if pixels > uint64(MaxTimelineAssetBytes)/4 {
			return fmt.Errorf("%w: asset %d pixel bytes exceed %d", errTimelineLimit, a.ID, MaxTimelineAssetBytes)
		}
		rawBytes := pixels * 4
		if uint64(len(a.RGBA)) != rawBytes {
			return fmt.Errorf("%w: asset %d has %d RGBA bytes, want %d", errTimelineFormat, a.ID, len(a.RGBA), rawBytes)
		}
		if totalRaw > uint64(MaxTimelineTotalAssetBytes)-rawBytes {
			return fmt.Errorf("%w: total asset bytes exceed %d", errTimelineLimit, MaxTimelineTotalAssetBytes)
		}
		totalRaw += rawBytes
		if sha256.Sum256(a.RGBA) != a.SHA256 {
			return fmt.Errorf("%w: asset %d SHA-256 mismatch", errTimelineFormat, a.ID)
		}
	}

	for i := range scene.Draws {
		d := &scene.Draws[i]
		if _, exists := assets[d.AssetID]; !exists {
			return fmt.Errorf("%w: draw %d references missing asset %d", errTimelineFormat, i, d.AssetID)
		}
		if d.StartVPos >= d.EndVPos {
			return fmt.Errorf("%w: draw %d has empty or reversed interval", errTimelineFormat, i)
		}
		for _, value := range d.Rect {
			if !finite32(value) {
				return fmt.Errorf("%w: draw %d has non-finite rect", errTimelineFormat, i)
			}
		}
		for _, value := range d.Projection {
			if !finite32(value) {
				return fmt.Errorf("%w: draw %d has non-finite projection", errTimelineFormat, i)
			}
		}
		if !finite32(d.Alpha) || d.Alpha < 0 || d.Alpha > 1 || !finite64(d.AnchorX) || !finite64(d.SpeedX) {
			return fmt.Errorf("%w: draw %d has invalid alpha or motion", errTimelineFormat, i)
		}
		visibleStart := max(int64(d.StartVPos), int64(firstVPos))
		visibleEnd := min(int64(d.EndVPos)-1, int64(lastVPos))
		if visibleStart <= visibleEnd {
			minDelta := visibleStart - int64(d.AnchorVPos)
			maxDelta := visibleEnd - int64(d.AnchorVPos)
			if minDelta < -maxExactFloat32Integer || maxDelta > maxExactFloat32Integer {
				return fmt.Errorf("%w: draw %d visible vpos delta is not exactly representable in float32", errTimelineLimit, i)
			}
		}
	}
	return nil
}

func finite32(value float32) bool { return !float32NaNOrInf(value) }

func float32NaNOrInf(value float32) bool {
	return math.IsNaN(float64(value)) || math.IsInf(float64(value), 0)
}

func finite64(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// WriteCommentTimeline writes the canonical, explicitly little-endian NCT1 form.
func WriteCommentTimeline(w io.Writer, scene CommentTimeline) error {
	if err := ValidateCommentTimeline(scene); err != nil {
		return err
	}
	var header [TimelineHeaderBytes]byte
	copy(header[:4], "NCT1")
	putU32(header[4:], TimelineHeaderBytes)
	h := scene.Header
	putU32(header[8:], h.Width)
	putU32(header[12:], h.Height)
	putU32(header[16:], h.FrameCount)
	putU32(header[20:], h.FPSNum)
	putU32(header[24:], h.FPSDen)
	putU32(header[28:], uint32(len(scene.Assets)))
	putU32(header[32:], uint32(len(scene.Draws)))
	copy(header[36:68], h.BundleSHA256[:])
	var totalRaw uint64
	for i := range scene.Assets {
		totalRaw += uint64(len(scene.Assets[i].RGBA))
	}
	putU64(header[68:], totalRaw)
	putU32(header[76:], TimelineFlags)
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	for i := range scene.Assets {
		a := &scene.Assets[i]
		rawBytes := uint64(len(a.RGBA))
		var recordHeader [48]byte // record length plus id, dimensions and hash
		putU32(recordHeader[0:], uint32(44+rawBytes))
		putU32(recordHeader[4:], a.ID)
		putU32(recordHeader[8:], a.Width)
		putU32(recordHeader[12:], a.Height)
		copy(recordHeader[16:48], a.SHA256[:])
		if err := writeAll(w, recordHeader[:]); err != nil {
			return err
		}
		if err := writeAll(w, a.RGBA); err != nil {
			return err
		}
	}
	for i := range scene.Draws {
		var record [TimelineDrawBytes]byte
		encodeDraw(record[:], &scene.Draws[i])
		if err := writeAll(w, record[:]); err != nil {
			return err
		}
	}
	return nil
}

// ReadCommentTimeline parses NCT1 while checking all lengths before allocation.
func ReadCommentTimeline(r io.Reader) (CommentTimeline, error) {
	var scene CommentTimeline
	var header [TimelineHeaderBytes]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return scene, fmt.Errorf("%w: read header: %v", errTimelineFormat, err)
	}
	if string(header[:4]) != "NCT1" || u32(header[4:]) != TimelineHeaderBytes {
		return scene, fmt.Errorf("%w: bad magic or header size", errTimelineFormat)
	}
	h := TimelineHeader{
		Width: u32(header[8:]), Height: u32(header[12:]), FrameCount: u32(header[16:]),
		FPSNum: u32(header[20:]), FPSDen: u32(header[24:]),
	}
	copy(h.BundleSHA256[:], header[36:68])
	assetCount, drawCount := u32(header[28:]), u32(header[32:])
	totalRawExpected, flags := u64(header[68:]), u32(header[76:])
	if flags != TimelineFlags {
		return scene, fmt.Errorf("%w: unsupported flags %#x", errTimelineFormat, flags)
	}
	if assetCount > MaxTimelineAssets || drawCount > MaxTimelineDraws || totalRawExpected > MaxTimelineTotalAssetBytes {
		return scene, fmt.Errorf("%w: header counts or raw byte total", errTimelineLimit)
	}
	// Validate header limits before allocating even the bounded record slices.
	if err := ValidateCommentTimeline(CommentTimeline{Header: h}); err != nil {
		return scene, err
	}
	scene.Header = h
	scene.Assets = make([]TimelineAsset, 0, int(assetCount))
	seen := make(map[uint32]struct{}, int(assetCount))
	var totalRaw uint64
	for i := uint32(0); i < assetCount; i++ {
		var lenBytes [4]byte
		if _, err := io.ReadFull(r, lenBytes[:]); err != nil {
			return CommentTimeline{}, fmt.Errorf("%w: asset %d record length: %v", errTimelineFormat, i, err)
		}
		recordBytes := uint64(u32(lenBytes[:]))
		if recordBytes < 44 {
			return CommentTimeline{}, fmt.Errorf("%w: asset %d record too short", errTimelineFormat, i)
		}
		var meta [44]byte
		if _, err := io.ReadFull(r, meta[:]); err != nil {
			return CommentTimeline{}, fmt.Errorf("%w: asset %d metadata: %v", errTimelineFormat, i, err)
		}
		a := TimelineAsset{ID: u32(meta[0:]), Width: u32(meta[4:]), Height: u32(meta[8:])}
		copy(a.SHA256[:], meta[12:44])
		if _, exists := seen[a.ID]; exists {
			return CommentTimeline{}, fmt.Errorf("%w: duplicate asset id %d", errTimelineFormat, a.ID)
		}
		seen[a.ID] = struct{}{}
		if a.Width == 0 || a.Height == 0 || a.Width > MaxTimelineAssetDimension || a.Height > MaxTimelineAssetDimension {
			return CommentTimeline{}, fmt.Errorf("%w: asset %d dimensions", errTimelineLimit, a.ID)
		}
		pixels := uint64(a.Width) * uint64(a.Height)
		if pixels > uint64(MaxTimelineAssetBytes)/4 {
			return CommentTimeline{}, fmt.Errorf("%w: asset %d byte limit", errTimelineLimit, a.ID)
		}
		rawBytes := pixels * 4
		if recordBytes != 44+rawBytes {
			return CommentTimeline{}, fmt.Errorf("%w: asset %d record length %d, expected %d", errTimelineFormat, a.ID, recordBytes, 44+rawBytes)
		}
		if totalRaw > totalRawExpected || rawBytes > totalRawExpected-totalRaw || totalRaw > uint64(MaxTimelineTotalAssetBytes)-rawBytes {
			return CommentTimeline{}, fmt.Errorf("%w: asset byte total", errTimelineLimit)
		}
		a.RGBA = make([]byte, int(rawBytes))
		if _, err := io.ReadFull(r, a.RGBA); err != nil {
			return CommentTimeline{}, fmt.Errorf("%w: asset %d pixels: %v", errTimelineFormat, a.ID, err)
		}
		if sha256.Sum256(a.RGBA) != a.SHA256 {
			return CommentTimeline{}, fmt.Errorf("%w: asset %d SHA-256 mismatch", errTimelineFormat, a.ID)
		}
		totalRaw += rawBytes
		scene.Assets = append(scene.Assets, a)
	}
	if totalRaw != totalRawExpected {
		return CommentTimeline{}, fmt.Errorf("%w: header says %d asset bytes, read %d", errTimelineFormat, totalRawExpected, totalRaw)
	}
	scene.Draws = make([]TimelineDraw, int(drawCount))
	for i := range scene.Draws {
		var record [TimelineDrawBytes]byte
		if _, err := io.ReadFull(r, record[:]); err != nil {
			return CommentTimeline{}, fmt.Errorf("%w: draw %d: %v", errTimelineFormat, i, err)
		}
		decodeDraw(&scene.Draws[i], record[:])
	}
	var trailing [1]byte
	n, err := io.ReadFull(r, trailing[:])
	if n != 0 {
		return CommentTimeline{}, fmt.Errorf("%w: trailing data after declared records", errTimelineFormat)
	}
	if err != io.EOF {
		return CommentTimeline{}, fmt.Errorf("%w: check stream end: %v", errTimelineFormat, err)
	}
	if err := ValidateCommentTimeline(scene); err != nil {
		return CommentTimeline{}, err
	}
	return scene, nil
}

func encodeDraw(dst []byte, d *TimelineDraw) {
	putU32(dst[0:], d.AssetID)
	putI32(dst[4:], d.StartVPos)
	putI32(dst[8:], d.EndVPos)
	putI32(dst[12:], d.AnchorVPos)
	putU32(dst[16:], d.OwnerOrder)
	putU32(dst[20:], d.CommentIndex)
	putU32(dst[24:], d.PrimitiveIndex)
	for i, value := range d.Rect {
		putF32(dst[28+i*4:], value)
	}
	for i, value := range d.Projection {
		putF32(dst[44+i*4:], value)
	}
	putF32(dst[108:], d.Alpha)
	putF64(dst[112:], d.AnchorX)
	putF64(dst[120:], d.SpeedX)
}

func decodeDraw(d *TimelineDraw, src []byte) {
	d.AssetID = u32(src[0:])
	d.StartVPos = i32(src[4:])
	d.EndVPos = i32(src[8:])
	d.AnchorVPos = i32(src[12:])
	d.OwnerOrder = u32(src[16:])
	d.CommentIndex = u32(src[20:])
	d.PrimitiveIndex = u32(src[24:])
	for i := range d.Rect {
		d.Rect[i] = f32(src[28+i*4:])
	}
	for i := range d.Projection {
		d.Projection[i] = f32(src[44+i*4:])
	}
	d.Alpha = f32(src[108:])
	d.AnchorX = f64(src[112:])
	d.SpeedX = f64(src[120:])
}

func putU32(dst []byte, value uint32)  { binary.LittleEndian.PutUint32(dst, value) }
func putU64(dst []byte, value uint64)  { binary.LittleEndian.PutUint64(dst, value) }
func putI32(dst []byte, value int32)   { binary.LittleEndian.PutUint32(dst, uint32(value)) }
func putF32(dst []byte, value float32) { putU32(dst, math.Float32bits(value)) }
func putF64(dst []byte, value float64) { putU64(dst, math.Float64bits(value)) }
func u32(src []byte) uint32            { return binary.LittleEndian.Uint32(src) }
func u64(src []byte) uint64            { return binary.LittleEndian.Uint64(src) }
func i32(src []byte) int32             { return int32(u32(src)) }
func f32(src []byte) float32           { return math.Float32frombits(u32(src)) }
func f64(src []byte) float64           { return math.Float64frombits(u64(src)) }

func writeAll(w io.Writer, src []byte) error {
	for len(src) > 0 {
		n, err := w.Write(src)
		if n > 0 {
			src = src[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
