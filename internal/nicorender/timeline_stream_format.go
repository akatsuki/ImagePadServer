package nicorender

import (
	"crypto/sha256"
	"fmt"
	"io"
	"math"
	"sort"

	"imagepadserver/internal/niconico"
)

const (
	TimelineStreamHeaderBytes                = 80
	TimelineStreamEnvelopeBytes              = 16
	TimelineStreamDeclarationBytes           = 20
	TimelineStreamMaxDeclarations            = 100_000
	TimelineStreamMaxDrawsPerElement         = 1_024
	TimelineStreamMaxRecordPayload           = 16 << 20
	TimelineStreamMaxChunkBytes              = 4 << 20
	TimelineStreamSceneDigestDomain          = "NCT2-SCENE-DIGEST-v1\x00"
	TimelineStreamRecordHeader               = 1
	TimelineStreamRecordDeclarations         = 2
	TimelineStreamRecordDeclarationsComplete = 3
	TimelineStreamRecordAssetBegin           = 4
	TimelineStreamRecordAssetChunk           = 5
	TimelineStreamRecordAssetEnd             = 6
	TimelineStreamRecordElementComplete      = 7
	TimelineStreamRecordWatermark            = 8
	TimelineStreamRecordEnd                  = 9
)

// CommentTimelineStream is a frozen NCT2 output request. Elements has exactly
// one entry per declaration, including entries whose Draws slice is empty.
type CommentTimelineStream struct {
	Header       TimelineHeader
	Declarations []TimelineStreamDeclaration
	Assets       []TimelineAsset
	Elements     []TimelineStreamElement
}

// TimelineStreamDeclaration identifies one frozen owner/index item and its
// original half-open visible interval. The writer clips only the wire copy.
type TimelineStreamDeclaration struct {
	Ordinal, OwnerOrder, CommentIndex uint32
	StartVPos, EndVPos                int32
}

// TimelineStreamElement explicitly completes one declaration ordinal.
type TimelineStreamElement struct {
	Ordinal uint32
	Draws   []TimelineDraw
}

type timelineStreamPlan struct {
	exclusiveEnd   int64
	totalRaw       uint64
	totalDraws     uint32
	declarationSHA [32]byte
	commitment     [32]byte
	assetByID      map[uint32]int
	assetsByID     []int
	usedAssets     []bool
}

// WriteCommentTimelineStream writes the canonical NCT2 envelope sequence.
// Validation and commitment calculation finish before the first output byte.
// Pixel data and Draw records are then sent directly from the request slices;
// no second scene-sized encoded buffer is retained.
func WriteCommentTimelineStream(w io.Writer, stream CommentTimelineStream) error {
	plan, err := validateTimelineStream(stream)
	if err != nil {
		return err
	}
	if w == nil {
		return fmt.Errorf("niconico timeline: nil NCT2 writer")
	}

	var sequence uint64
	writeRecord := func(kind uint32, payload []byte) error {
		if len(payload) > TimelineStreamMaxRecordPayload {
			return fmt.Errorf("niconico timeline: NCT2 record payload %d exceeds limit", len(payload))
		}
		return appendTimelineStreamRecord(w, &sequence, kind, uint32(len(payload)), func() error {
			return writeAll(w, payload)
		})
	}

	var header [TimelineStreamHeaderBytes]byte
	copy(header[:4], "NCT2")
	putU32(header[4:], TimelineStreamHeaderBytes)
	putU32(header[8:], 2)
	putU32(header[12:], stream.Header.Width)
	putU32(header[16:], stream.Header.Height)
	putU32(header[20:], stream.Header.FrameCount)
	putU32(header[24:], stream.Header.FPSNum)
	putU32(header[28:], stream.Header.FPSDen)
	putU32(header[32:], uint32(len(stream.Declarations)))
	putU32(header[36:], TimelineFlags)
	copy(header[40:72], stream.Header.BundleSHA256[:])
	if err := writeRecord(TimelineStreamRecordHeader, header[:]); err != nil {
		return err
	}

	declarationPayloadBytes := uint32(4 + len(stream.Declarations)*TimelineStreamDeclarationBytes)
	var declarationCount [4]byte
	putU32(declarationCount[:], uint32(len(stream.Declarations)))
	if err := appendTimelineStreamRecord(w, &sequence, TimelineStreamRecordDeclarations, declarationPayloadBytes, func() error {
		if err := writeAll(w, declarationCount[:]); err != nil {
			return err
		}
		for i := range stream.Declarations {
			declaration := stream.Declarations[i]
			var entry [TimelineStreamDeclarationBytes]byte
			putU32(entry[0:], uint32(i))
			putU32(entry[4:], declaration.OwnerOrder)
			putU32(entry[8:], declaration.CommentIndex)
			putI32(entry[12:], int32(clipTimelineVPos(declaration.StartVPos, plan.exclusiveEnd)))
			putI32(entry[16:], int32(clipTimelineVPos(declaration.EndVPos, plan.exclusiveEnd)))
			if err := writeAll(w, entry[:]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	declarationsComplete := make([]byte, 36)
	putU32(declarationsComplete[0:], uint32(len(stream.Declarations)))
	copy(declarationsComplete[4:], plan.declarationSHA[:])
	if err := writeRecord(TimelineStreamRecordDeclarationsComplete, declarationsComplete); err != nil {
		return err
	}

	sentAssets := make([]bool, len(stream.Assets))
	for ordinal := range stream.Elements {
		element := &stream.Elements[ordinal]
		for drawIndex := range element.Draws {
			assetIndex := plan.assetByID[element.Draws[drawIndex].AssetID]
			if sentAssets[assetIndex] {
				continue
			}
			if err := writeTimelineStreamAsset(w, &stream.Assets[assetIndex], &sequence); err != nil {
				return err
			}
			sentAssets[assetIndex] = true
		}

		payloadLen := uint32(8 + len(element.Draws)*TimelineDrawBytes)
		if err := appendTimelineStreamRecord(w, &sequence, TimelineStreamRecordElementComplete, payloadLen, func() error {
			var prefix [8]byte
			putU32(prefix[0:], uint32(ordinal))
			putU32(prefix[4:], uint32(len(element.Draws)))
			if err := writeAll(w, prefix[:]); err != nil {
				return err
			}
			for i := range element.Draws {
				var record [TimelineDrawBytes]byte
				encodeDraw(record[:], &element.Draws[i])
				if err := writeAll(w, record[:]); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}

		ready := readyTimelineStream(stream.Declarations, uint32(ordinal+1), plan.exclusiveEnd)
		var watermark [12]byte
		putU32(watermark[0:], uint32(ordinal+1))
		putU64(watermark[4:], uint64(ready))
		if err := writeRecord(TimelineStreamRecordWatermark, watermark[:]); err != nil {
			return err
		}
	}

	var end [64]byte
	putU32(end[0:], uint32(len(stream.Declarations)))
	putU32(end[4:], uint32(len(stream.Assets)))
	putU32(end[8:], plan.totalDraws)
	putU64(end[12:], plan.totalRaw)
	putU32(end[20:], uint32(len(stream.Declarations)))
	putU64(end[24:], uint64(plan.exclusiveEnd))
	copy(end[32:], plan.commitment[:])
	return writeRecord(TimelineStreamRecordEnd, end[:])
}

func writeTimelineStreamAsset(w io.Writer, asset *TimelineAsset, sequence *uint64) error {
	writeParts := func(kind uint32, payloadLen uint32, parts ...[]byte) error {
		var written uint64
		for _, part := range parts {
			written += uint64(len(part))
		}
		if written != uint64(payloadLen) {
			return fmt.Errorf("niconico timeline: NCT2 payload parts have length %d, want %d", written, payloadLen)
		}
		return appendTimelineStreamRecord(w, sequence, kind, payloadLen, func() error {
			for _, part := range parts {
				if err := writeAll(w, part); err != nil {
					return err
				}
			}
			return nil
		})
	}

	var begin [52]byte
	putU32(begin[0:], asset.ID)
	putU32(begin[4:], asset.Width)
	putU32(begin[8:], asset.Height)
	putU64(begin[12:], uint64(len(asset.RGBA)))
	copy(begin[20:], asset.SHA256[:])
	if err := writeParts(TimelineStreamRecordAssetBegin, uint32(len(begin)), begin[:]); err != nil {
		return err
	}
	for offset := 0; offset < len(asset.RGBA); {
		end := offset + TimelineStreamMaxChunkBytes
		if end > len(asset.RGBA) {
			end = len(asset.RGBA)
		}
		var metadata [12]byte
		putU32(metadata[0:], asset.ID)
		putU64(metadata[4:], uint64(offset))
		payloadLen := uint32(len(metadata) + end - offset)
		if err := writeParts(TimelineStreamRecordAssetChunk, payloadLen, metadata[:], asset.RGBA[offset:end]); err != nil {
			return err
		}
		offset = end
	}
	var end [44]byte
	putU32(end[0:], asset.ID)
	putU64(end[4:], uint64(len(asset.RGBA)))
	copy(end[12:], asset.SHA256[:])
	return writeParts(TimelineStreamRecordAssetEnd, uint32(len(end)), end[:])
}

func validateTimelineStream(stream CommentTimelineStream) (*timelineStreamPlan, error) {
	if len(stream.Declarations) > TimelineStreamMaxDeclarations {
		return nil, fmt.Errorf("%w: NCT2 declaration count %d", errTimelineLimit, len(stream.Declarations))
	}
	if len(stream.Elements) != len(stream.Declarations) {
		return nil, fmt.Errorf("%w: NCT2 has %d declarations and %d completed elements", errTimelineFormat, len(stream.Declarations), len(stream.Elements))
	}
	if len(stream.Assets) > MaxTimelineAssets {
		return nil, fmt.Errorf("%w: NCT2 asset count %d", errTimelineLimit, len(stream.Assets))
	}
	if err := ValidateCommentTimeline(CommentTimeline{Header: stream.Header}); err != nil {
		return nil, err
	}
	exclusiveEnd, err := timelineStreamFrameClock(stream.Header.FrameCount, stream.Header.FPSNum, stream.Header.FPSDen)
	if err != nil {
		return nil, err
	}

	plan := &timelineStreamPlan{
		exclusiveEnd: exclusiveEnd,
		assetByID:    make(map[uint32]int, len(stream.Assets)),
		assetsByID:   make([]int, len(stream.Assets)),
		usedAssets:   make([]bool, len(stream.Assets)),
	}
	for i := range stream.Declarations {
		declaration := &stream.Declarations[i]
		if declaration.Ordinal != uint32(i) {
			return nil, fmt.Errorf("%w: NCT2 declaration ordinal %d, want %d", errTimelineFormat, declaration.Ordinal, i)
		}
		if declaration.StartVPos > declaration.EndVPos {
			return nil, fmt.Errorf("%w: NCT2 declaration %d has reversed interval", errTimelineFormat, i)
		}
		if i > 0 {
			previous := &stream.Declarations[i-1]
			if declaration.OwnerOrder < previous.OwnerOrder || (declaration.OwnerOrder == previous.OwnerOrder && declaration.CommentIndex <= previous.CommentIndex) {
				return nil, fmt.Errorf("%w: NCT2 declarations are not strictly owner/index ordered", errTimelineFormat)
			}
		}
	}

	var totalRaw uint64
	for i := range stream.Assets {
		asset := &stream.Assets[i]
		if _, exists := plan.assetByID[asset.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate NCT2 asset id %d", errTimelineFormat, asset.ID)
		}
		plan.assetByID[asset.ID] = i
		pixels := uint64(asset.Width) * uint64(asset.Height)
		if pixels > math.MaxUint64/4 {
			return nil, fmt.Errorf("%w: NCT2 asset %d byte length overflow", errTimelineLimit, asset.ID)
		}
		rawBytes := pixels * 4
		if asset.Width == 0 || asset.Height == 0 || asset.Width > MaxTimelineAssetDimension || asset.Height > MaxTimelineAssetDimension {
			return nil, fmt.Errorf("%w: NCT2 asset %d dimensions %dx%d", errTimelineLimit, asset.ID, asset.Width, asset.Height)
		}
		if rawBytes > MaxTimelineAssetBytes {
			return nil, fmt.Errorf("%w: NCT2 asset %d exceeds %d bytes", errTimelineLimit, asset.ID, MaxTimelineAssetBytes)
		}
		if uint64(len(asset.RGBA)) != rawBytes {
			return nil, fmt.Errorf("%w: NCT2 asset %d has %d pixel bytes, want %d", errTimelineFormat, asset.ID, len(asset.RGBA), rawBytes)
		}
		if totalRaw > MaxTimelineTotalAssetBytes-rawBytes {
			return nil, fmt.Errorf("%w: NCT2 total asset bytes exceed %d", errTimelineLimit, MaxTimelineTotalAssetBytes)
		}
		totalRaw += rawBytes
		if sha256.Sum256(asset.RGBA) != asset.SHA256 {
			return nil, fmt.Errorf("%w: NCT2 asset %d SHA-256 mismatch", errTimelineFormat, asset.ID)
		}
		plan.assetsByID[i] = i
	}
	plan.totalRaw = totalRaw
	sort.Slice(plan.assetsByID, func(i, j int) bool {
		return stream.Assets[plan.assetsByID[i]].ID < stream.Assets[plan.assetsByID[j]].ID
	})

	clock := niconico.FrameClock{Num: int64(stream.Header.FPSNum), Den: int64(stream.Header.FPSDen)}
	firstVPos, err := TimelineVPos(clock, 0)
	if err != nil {
		return nil, err
	}
	lastVPos, err := TimelineVPos(clock, int64(stream.Header.FrameCount)-1)
	if err != nil {
		return nil, err
	}
	for ordinal := range stream.Elements {
		element := &stream.Elements[ordinal]
		if element.Ordinal != uint32(ordinal) {
			return nil, fmt.Errorf("%w: NCT2 element ordinal %d, want %d", errTimelineFormat, element.Ordinal, ordinal)
		}
		if len(element.Draws) > TimelineStreamMaxDrawsPerElement {
			return nil, fmt.Errorf("%w: NCT2 element %d has %d Draws", errTimelineLimit, ordinal, len(element.Draws))
		}
		if uint64(plan.totalDraws)+uint64(len(element.Draws)) > MaxTimelineDraws {
			return nil, fmt.Errorf("%w: NCT2 total Draw count exceeds %d", errTimelineLimit, MaxTimelineDraws)
		}
		declaration := stream.Declarations[ordinal]
		declarationStart := clipTimelineVPos(declaration.StartVPos, exclusiveEnd)
		declarationEnd := clipTimelineVPos(declaration.EndVPos, exclusiveEnd)
		for primitive := range element.Draws {
			draw := &element.Draws[primitive]
			assetIndex, exists := plan.assetByID[draw.AssetID]
			if !exists {
				return nil, fmt.Errorf("%w: NCT2 Draw references missing asset %d", errTimelineFormat, draw.AssetID)
			}
			plan.usedAssets[assetIndex] = true
			if draw.OwnerOrder != declaration.OwnerOrder || draw.CommentIndex != declaration.CommentIndex {
				return nil, fmt.Errorf("%w: NCT2 Draw owner/index differs from declaration %d", errTimelineFormat, ordinal)
			}
			if draw.PrimitiveIndex != uint32(primitive) {
				return nil, fmt.Errorf("%w: NCT2 Draw primitive index %d, want %d", errTimelineFormat, draw.PrimitiveIndex, primitive)
			}
			if clipTimelineVPos(draw.StartVPos, exclusiveEnd) != declarationStart || clipTimelineVPos(draw.EndVPos, exclusiveEnd) != declarationEnd {
				return nil, fmt.Errorf("%w: NCT2 Draw clipped interval differs from declaration %d", errTimelineFormat, ordinal)
			}
			if err := validateTimelineStreamDraw(draw, firstVPos, lastVPos); err != nil {
				return nil, fmt.Errorf("NCT2 element %d Draw %d: %w", ordinal, primitive, err)
			}
		}
		plan.totalDraws += uint32(len(element.Draws))
	}
	for i, used := range plan.usedAssets {
		if !used {
			return nil, fmt.Errorf("%w: NCT2 asset %d is not referenced by a Draw", errTimelineFormat, stream.Assets[i].ID)
		}
	}

	plan.declarationSHA = timelineStreamDeclarationsSHA(stream.Declarations, exclusiveEnd)
	plan.commitment, err = timelineStreamCommitment(stream, *plan)
	if err != nil {
		return nil, err
	}
	return plan, nil
}

func validateTimelineStreamDraw(draw *TimelineDraw, firstVPos, lastVPos int32) error {
	if draw.StartVPos >= draw.EndVPos {
		return fmt.Errorf("%w: empty or reversed interval", errTimelineFormat)
	}
	for _, value := range draw.Rect {
		if !finite32(value) {
			return fmt.Errorf("%w: non-finite rect", errTimelineFormat)
		}
	}
	for _, value := range draw.Projection {
		if !finite32(value) {
			return fmt.Errorf("%w: non-finite projection", errTimelineFormat)
		}
	}
	if !finite32(draw.Alpha) || draw.Alpha < 0 || draw.Alpha > 1 || !finite64(draw.AnchorX) || !finite64(draw.SpeedX) {
		return fmt.Errorf("%w: invalid alpha or motion", errTimelineFormat)
	}
	visibleStart := max(int64(draw.StartVPos), int64(firstVPos))
	visibleEnd := min(int64(draw.EndVPos)-1, int64(lastVPos))
	if visibleStart <= visibleEnd {
		minDelta := visibleStart - int64(draw.AnchorVPos)
		maxDelta := visibleEnd - int64(draw.AnchorVPos)
		if minDelta < -maxExactFloat32Integer || maxDelta > maxExactFloat32Integer {
			return fmt.Errorf("%w: visible vpos delta is not exactly representable in float32", errTimelineLimit)
		}
	}
	return nil
}

func timelineStreamFrameClock(frame, fpsNum, fpsDen uint32) (int64, error) {
	product := uint64(frame)
	if fpsDen != 0 && product > math.MaxInt64/uint64(fpsDen) {
		return 0, fmt.Errorf("%w: NCT2 FrameClock multiplication overflow", errTimelineLimit)
	}
	product *= uint64(fpsDen)
	if product > math.MaxInt64/100 {
		return 0, fmt.Errorf("%w: NCT2 FrameClock multiplication overflow", errTimelineLimit)
	}
	product *= 100
	if fpsNum == 0 {
		return 0, fmt.Errorf("%w: NCT2 zero FPS numerator", errTimelineFormat)
	}
	return int64(product / uint64(fpsNum)), nil
}

func readyTimelineStream(declarations []TimelineStreamDeclaration, prefix uint32, exclusiveEnd int64) int64 {
	if prefix >= uint32(len(declarations)) {
		return exclusiveEnd
	}
	ready := clipTimelineVPos(declarations[prefix].StartVPos, exclusiveEnd)
	for i := int(prefix) + 1; i < len(declarations); i++ {
		start := clipTimelineVPos(declarations[i].StartVPos, exclusiveEnd)
		if start < ready {
			ready = start
		}
	}
	return ready
}

func clipTimelineVPos(value int32, exclusiveEnd int64) int64 {
	if value < 0 {
		return 0
	}
	if int64(value) > exclusiveEnd {
		return exclusiveEnd
	}
	return int64(value)
}

func timelineStreamDeclarationsSHA(declarations []TimelineStreamDeclaration, exclusiveEnd int64) [32]byte {
	h := sha256.New()
	var entry [TimelineStreamDeclarationBytes]byte
	for ordinal := range declarations {
		declaration := &declarations[ordinal]
		putU32(entry[0:], uint32(ordinal))
		putU32(entry[4:], declaration.OwnerOrder)
		putU32(entry[8:], declaration.CommentIndex)
		putI32(entry[12:], int32(clipTimelineVPos(declaration.StartVPos, exclusiveEnd)))
		putI32(entry[16:], int32(clipTimelineVPos(declaration.EndVPos, exclusiveEnd)))
		_, _ = h.Write(entry[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

func timelineStreamCommitment(stream CommentTimelineStream, plan timelineStreamPlan) ([32]byte, error) {
	h := sha256.New()
	_, _ = io.WriteString(h, TimelineStreamSceneDigestDomain)
	var header [TimelineHeaderBytes]byte
	if err := fillCanonicalTimelineHeader(header[:], stream.Header, uint32(len(stream.Assets)), plan.totalDraws, plan.totalRaw); err != nil {
		return [32]byte{}, err
	}
	_, _ = h.Write(header[:])
	var descriptor [52]byte
	for _, assetIndex := range plan.assetsByID {
		asset := &stream.Assets[assetIndex]
		putU32(descriptor[0:], asset.ID)
		putU32(descriptor[4:], asset.Width)
		putU32(descriptor[8:], asset.Height)
		putU64(descriptor[12:], uint64(len(asset.RGBA)))
		copy(descriptor[20:], asset.SHA256[:])
		_, _ = h.Write(descriptor[:])
	}
	var drawRecord [TimelineDrawBytes]byte
	for element := range stream.Elements {
		for draw := range stream.Elements[element].Draws {
			encodeDraw(drawRecord[:], &stream.Elements[element].Draws[draw])
			_, _ = h.Write(drawRecord[:])
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func fillCanonicalTimelineHeader(dst []byte, header TimelineHeader, assetCount, drawCount uint32, totalRaw uint64) error {
	if len(dst) != TimelineHeaderBytes {
		return fmt.Errorf("niconico timeline: canonical NCT1 header destination has %d bytes", len(dst))
	}
	copy(dst[0:4], "NCT1")
	putU32(dst[4:], TimelineHeaderBytes)
	putU32(dst[8:], header.Width)
	putU32(dst[12:], header.Height)
	putU32(dst[16:], header.FrameCount)
	putU32(dst[20:], header.FPSNum)
	putU32(dst[24:], header.FPSDen)
	putU32(dst[28:], assetCount)
	putU32(dst[32:], drawCount)
	copy(dst[36:68], header.BundleSHA256[:])
	putU64(dst[68:], totalRaw)
	putU32(dst[76:], TimelineFlags)
	return nil
}

func appendTimelineStreamRecord(w io.Writer, sequence *uint64, kind uint32, payloadLen uint32, writePayload func() error) error {
	if uint64(payloadLen) > TimelineStreamMaxRecordPayload {
		return fmt.Errorf("niconico timeline: NCT2 record payload %d exceeds limit", payloadLen)
	}
	var envelope [TimelineStreamEnvelopeBytes]byte
	putU32(envelope[0:], kind)
	putU32(envelope[4:], payloadLen)
	putU64(envelope[8:], *sequence)
	if *sequence == math.MaxUint64 {
		return fmt.Errorf("niconico timeline: NCT2 sequence overflow")
	}
	if err := writeAll(w, envelope[:]); err != nil {
		return err
	}
	if err := writePayload(); err != nil {
		return err
	}
	*sequence++
	return nil
}
