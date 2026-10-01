package nicorender

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"

	"imagepadserver/internal/niconico"
)

var errIncrementalTimelineWriterState = errors.New("niconico timeline: invalid incremental NCT2 writer state")

type incrementalTimelineAssetDescriptor struct {
	id        uint32
	width     uint32
	height    uint32
	rawLength uint64
	sha256    [32]byte
}

type incrementalTimelineAssetState struct {
	descriptor incrementalTimelineAssetDescriptor
	referenced bool
}

// IncrementalCommentTimelineStreamWriter emits a frozen NCT2 header and
// declaration table, then validated elements and a final commitment.
//
// It retains declarations, bounded asset descriptors, and exact encoded Draw
// records needed for the final commitment. Asset RGBA is only borrowed during
// WriteElement and is never retained by the writer.
type IncrementalCommentTimelineStreamWriter struct {
	out          io.Writer
	header       TimelineHeader
	declarations []TimelineStreamDeclaration
	suffixReady  []int64
	exclusiveEnd int64
	sequence     uint64
	nextOrdinal  uint32
	totalDraws   uint32
	totalRaw     uint64
	assets       map[uint32]incrementalTimelineAssetState
	drawBytes    []byte
	failed       error
	finished     bool
}

// NewIncrementalCommentTimelineStreamWriter validates and writes the immutable
// NCT2 header and declaration records before returning the stateful writer.
func NewIncrementalCommentTimelineStreamWriter(out io.Writer, header TimelineHeader, declarations []TimelineStreamDeclaration) (*IncrementalCommentTimelineStreamWriter, error) {
	if out == nil {
		return nil, fmt.Errorf("niconico timeline: nil incremental NCT2 writer")
	}
	if len(declarations) > TimelineStreamMaxDeclarations {
		return nil, fmt.Errorf("%w: NCT2 declaration count %d", errTimelineLimit, len(declarations))
	}
	if err := ValidateCommentTimeline(CommentTimeline{Header: header}); err != nil {
		return nil, err
	}
	exclusiveEnd, err := timelineStreamFrameClock(header.FrameCount, header.FPSNum, header.FPSDen)
	if err != nil {
		return nil, err
	}
	ownedDeclarations := append([]TimelineStreamDeclaration(nil), declarations...)
	for i := range ownedDeclarations {
		declaration := &ownedDeclarations[i]
		if declaration.Ordinal != uint32(i) {
			return nil, fmt.Errorf("%w: NCT2 declaration ordinal %d, want %d", errTimelineFormat, declaration.Ordinal, i)
		}
		if declaration.StartVPos > declaration.EndVPos {
			return nil, fmt.Errorf("%w: NCT2 declaration %d has reversed interval", errTimelineFormat, i)
		}
		if i > 0 {
			previous := &ownedDeclarations[i-1]
			if declaration.OwnerOrder < previous.OwnerOrder || (declaration.OwnerOrder == previous.OwnerOrder && declaration.CommentIndex <= previous.CommentIndex) {
				return nil, fmt.Errorf("%w: NCT2 declarations are not strictly owner/index ordered", errTimelineFormat)
			}
		}
	}

	writer := &IncrementalCommentTimelineStreamWriter{
		out:          out,
		header:       header,
		declarations: ownedDeclarations,
		suffixReady:  make([]int64, len(ownedDeclarations)+1),
		exclusiveEnd: exclusiveEnd,
		assets:       make(map[uint32]incrementalTimelineAssetState),
	}
	writer.suffixReady[len(ownedDeclarations)] = exclusiveEnd
	for i := len(ownedDeclarations) - 1; i >= 0; i-- {
		start := clipTimelineVPos(ownedDeclarations[i].StartVPos, exclusiveEnd)
		writer.suffixReady[i] = min(start, writer.suffixReady[i+1])
	}
	if err := writer.writeInitialRecords(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (writer *IncrementalCommentTimelineStreamWriter) writeInitialRecords() error {
	var header [TimelineStreamHeaderBytes]byte
	copy(header[:4], "NCT2")
	putU32(header[4:], TimelineStreamHeaderBytes)
	putU32(header[8:], 2)
	putU32(header[12:], writer.header.Width)
	putU32(header[16:], writer.header.Height)
	putU32(header[20:], writer.header.FrameCount)
	putU32(header[24:], writer.header.FPSNum)
	putU32(header[28:], writer.header.FPSDen)
	putU32(header[32:], uint32(len(writer.declarations)))
	putU32(header[36:], TimelineFlags)
	copy(header[40:72], writer.header.BundleSHA256[:])
	if err := writer.writeRecord(TimelineStreamRecordHeader, header[:]); err != nil {
		return err
	}

	declarationPayloadLength := uint32(4 + len(writer.declarations)*TimelineStreamDeclarationBytes)
	var count [4]byte
	putU32(count[:], uint32(len(writer.declarations)))
	err := appendTimelineStreamRecord(writer.out, &writer.sequence, TimelineStreamRecordDeclarations, declarationPayloadLength, func() error {
		if err := writeAll(writer.out, count[:]); err != nil {
			return err
		}
		var entry [TimelineStreamDeclarationBytes]byte
		for i := range writer.declarations {
			declaration := &writer.declarations[i]
			putU32(entry[0:], uint32(i))
			putU32(entry[4:], declaration.OwnerOrder)
			putU32(entry[8:], declaration.CommentIndex)
			putI32(entry[12:], int32(clipTimelineVPos(declaration.StartVPos, writer.exclusiveEnd)))
			putI32(entry[16:], int32(clipTimelineVPos(declaration.EndVPos, writer.exclusiveEnd)))
			if err := writeAll(writer.out, entry[:]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		writer.failed = err
		return err
	}

	var complete [36]byte
	putU32(complete[0:], uint32(len(writer.declarations)))
	declarationSHA := timelineStreamDeclarationsSHA(writer.declarations, writer.exclusiveEnd)
	copy(complete[4:], declarationSHA[:])
	return writer.writeRecord(TimelineStreamRecordDeclarationsComplete, complete[:])
}

// WriteElement writes the next complete declaration's first-use asset
// transfers, one atomic ElementComplete record, and its suffix-min Watermark.
// The assets slice must contain exactly the not-yet-transferred assets first
// referenced by this element's Draws, in their primitive order.
func (writer *IncrementalCommentTimelineStreamWriter) WriteElement(element TimelineStreamElement, assets []TimelineAsset) error {
	if err := writer.stateError(); err != nil {
		return err
	}
	if writer.nextOrdinal >= uint32(len(writer.declarations)) {
		return fmt.Errorf("%w: no remaining NCT2 declaration for element %d", errTimelineFormat, element.Ordinal)
	}
	ordinal := writer.nextOrdinal
	if element.Ordinal != ordinal {
		return fmt.Errorf("%w: NCT2 element ordinal %d, want %d", errTimelineFormat, element.Ordinal, ordinal)
	}
	if len(element.Draws) > TimelineStreamMaxDrawsPerElement {
		return fmt.Errorf("%w: NCT2 element %d has %d Draws", errTimelineLimit, ordinal, len(element.Draws))
	}
	if uint64(writer.totalDraws)+uint64(len(element.Draws)) > MaxTimelineDraws {
		return fmt.Errorf("%w: NCT2 total Draw count exceeds %d", errTimelineLimit, MaxTimelineDraws)
	}
	if len(assets) > MaxTimelineAssets-len(writer.assets) {
		return fmt.Errorf("%w: NCT2 asset count exceeds %d", errTimelineLimit, MaxTimelineAssets)
	}

	declaration := writer.declarations[ordinal]
	declarationStart := clipTimelineVPos(declaration.StartVPos, writer.exclusiveEnd)
	declarationEnd := clipTimelineVPos(declaration.EndVPos, writer.exclusiveEnd)
	clock := niconico.FrameClock{Num: int64(writer.header.FPSNum), Den: int64(writer.header.FPSDen)}
	firstVPos, err := TimelineVPos(clock, 0)
	if err != nil {
		return err
	}
	lastVPos, err := TimelineVPos(clock, int64(writer.header.FrameCount)-1)
	if err != nil {
		return err
	}
	newAssetIDs := make([]uint32, 0, len(assets))
	seenNewAssets := make(map[uint32]struct{}, len(assets))
	for primitive := range element.Draws {
		draw := &element.Draws[primitive]
		if draw.OwnerOrder != declaration.OwnerOrder || draw.CommentIndex != declaration.CommentIndex {
			return fmt.Errorf("%w: NCT2 Draw owner/index differs from declaration %d", errTimelineFormat, ordinal)
		}
		if draw.PrimitiveIndex != uint32(primitive) {
			return fmt.Errorf("%w: NCT2 Draw primitive index %d, want %d", errTimelineFormat, draw.PrimitiveIndex, primitive)
		}
		if clipTimelineVPos(draw.StartVPos, writer.exclusiveEnd) != declarationStart || clipTimelineVPos(draw.EndVPos, writer.exclusiveEnd) != declarationEnd {
			return fmt.Errorf("%w: NCT2 Draw clipped interval differs from declaration %d", errTimelineFormat, ordinal)
		}
		if err := validateTimelineStreamDraw(draw, firstVPos, lastVPos); err != nil {
			return fmt.Errorf("NCT2 element %d Draw %d: %w", ordinal, primitive, err)
		}
		if _, alreadySent := writer.assets[draw.AssetID]; !alreadySent {
			if _, found := seenNewAssets[draw.AssetID]; !found {
				newAssetIDs = append(newAssetIDs, draw.AssetID)
				seenNewAssets[draw.AssetID] = struct{}{}
			}
		}
	}
	if len(newAssetIDs) != len(assets) {
		if len(assets) < len(newAssetIDs) {
			return fmt.Errorf("%w: NCT2 element %d is missing newly referenced asset %d", errTimelineFormat, ordinal, newAssetIDs[len(assets)])
		}
		return fmt.Errorf("%w: NCT2 asset %d is not referenced by element %d", errTimelineFormat, assets[len(newAssetIDs)].ID, ordinal)
	}
	assetByID := make(map[uint32]*TimelineAsset, len(assets))
	for i := range assets {
		asset := &assets[i]
		if asset.ID != newAssetIDs[i] {
			return fmt.Errorf("%w: NCT2 new asset at position %d has ID %d, want first-use ID %d", errTimelineFormat, i, asset.ID, newAssetIDs[i])
		}
		if _, duplicate := assetByID[asset.ID]; duplicate {
			return fmt.Errorf("%w: duplicate NCT2 asset id %d", errTimelineFormat, asset.ID)
		}
		if _, alreadySent := writer.assets[asset.ID]; alreadySent {
			return fmt.Errorf("%w: NCT2 asset %d was already transferred", errTimelineFormat, asset.ID)
		}
		if _, err := validateIncrementalTimelineAsset(asset, writer.totalRaw); err != nil {
			return err
		}
		assetByID[asset.ID] = asset
	}
	for _, id := range newAssetIDs {
		if _, ok := assetByID[id]; !ok {
			return fmt.Errorf("%w: NCT2 element %d is missing newly referenced asset %d", errTimelineFormat, ordinal, id)
		}
	}
	if len(writer.assets)+len(assets) > MaxTimelineAssets {
		return fmt.Errorf("%w: NCT2 asset count exceeds %d", errTimelineLimit, MaxTimelineAssets)
	}
	newRaw := uint64(0)
	for i := range assets {
		rawLength := uint64(len(assets[i].RGBA))
		if newRaw > uint64(MaxTimelineTotalAssetBytes)-rawLength {
			return fmt.Errorf("%w: NCT2 total asset bytes exceed %d", errTimelineLimit, MaxTimelineTotalAssetBytes)
		}
		newRaw += rawLength
	}
	if writer.totalRaw > uint64(MaxTimelineTotalAssetBytes)-newRaw {
		return fmt.Errorf("%w: NCT2 total asset bytes exceed %d", errTimelineLimit, MaxTimelineTotalAssetBytes)
	}

	encodedDraws := make([]byte, len(element.Draws)*TimelineDrawBytes)
	var drawRecord [TimelineDrawBytes]byte
	for i := range element.Draws {
		encodeDraw(drawRecord[:], &element.Draws[i])
		copy(encodedDraws[i*TimelineDrawBytes:], drawRecord[:])
	}
	if err := writer.appendCommitmentDraws(encodedDraws); err != nil {
		return err
	}

	for i := range assets {
		asset := &assets[i]
		if err := writeTimelineStreamAsset(writer.out, asset, &writer.sequence); err != nil {
			return writer.fail(err)
		}
		descriptor := incrementalTimelineAssetDescriptor{
			id: asset.ID, width: asset.Width, height: asset.Height,
			rawLength: uint64(len(asset.RGBA)), sha256: asset.SHA256,
		}
		writer.assets[asset.ID] = incrementalTimelineAssetState{descriptor: descriptor}
		writer.totalRaw += descriptor.rawLength
	}

	payloadLength := uint32(8 + len(encodedDraws))
	if err := appendTimelineStreamRecord(writer.out, &writer.sequence, TimelineStreamRecordElementComplete, payloadLength, func() error {
		var prefix [8]byte
		putU32(prefix[0:], ordinal)
		putU32(prefix[4:], uint32(len(element.Draws)))
		if err := writeAll(writer.out, prefix[:]); err != nil {
			return err
		}
		return writeAll(writer.out, encodedDraws)
	}); err != nil {
		return writer.fail(err)
	}

	nextPrefix := ordinal + 1
	var watermark [12]byte
	putU32(watermark[0:], nextPrefix)
	putU64(watermark[4:], uint64(writer.suffixReady[nextPrefix]))
	if err := writer.writeRecord(TimelineStreamRecordWatermark, watermark[:]); err != nil {
		return err
	}

	for i := range element.Draws {
		state := writer.assets[element.Draws[i].AssetID]
		state.referenced = true
		writer.assets[element.Draws[i].AssetID] = state
	}
	writer.totalDraws += uint32(len(element.Draws))
	writer.nextOrdinal = nextPrefix
	return nil
}

// Finish writes the final End record after every declaration has completed.
func (writer *IncrementalCommentTimelineStreamWriter) Finish() error {
	if err := writer.stateError(); err != nil {
		return err
	}
	if writer.nextOrdinal != uint32(len(writer.declarations)) {
		return fmt.Errorf("%w: NCT2 has completed %d of %d elements", errTimelineFormat, writer.nextOrdinal, len(writer.declarations))
	}
	for id, asset := range writer.assets {
		if !asset.referenced {
			return fmt.Errorf("%w: NCT2 asset %d is not referenced by a Draw", errTimelineFormat, id)
		}
	}
	commitment, err := writer.commitment()
	if err != nil {
		return writer.fail(err)
	}
	var end [64]byte
	putU32(end[0:], uint32(len(writer.declarations)))
	putU32(end[4:], uint32(len(writer.assets)))
	putU32(end[8:], writer.totalDraws)
	putU64(end[12:], writer.totalRaw)
	putU32(end[20:], uint32(len(writer.declarations)))
	putU64(end[24:], uint64(writer.exclusiveEnd))
	copy(end[32:], commitment[:])
	if err := writer.writeRecord(TimelineStreamRecordEnd, end[:]); err != nil {
		return err
	}
	writer.finished = true
	return nil
}

func (writer *IncrementalCommentTimelineStreamWriter) commitment() ([32]byte, error) {
	h := sha256.New()
	if _, err := io.WriteString(h, TimelineStreamSceneDigestDomain); err != nil {
		return [32]byte{}, err
	}
	var header [TimelineHeaderBytes]byte
	if err := fillCanonicalTimelineHeader(header[:], writer.header, uint32(len(writer.assets)), writer.totalDraws, writer.totalRaw); err != nil {
		return [32]byte{}, err
	}
	_, _ = h.Write(header[:])
	descriptors := make([]incrementalTimelineAssetDescriptor, 0, len(writer.assets))
	for _, asset := range writer.assets {
		descriptors = append(descriptors, asset.descriptor)
	}
	sort.Slice(descriptors, func(i, j int) bool { return descriptors[i].id < descriptors[j].id })
	var encodedDescriptor [52]byte
	for i := range descriptors {
		descriptor := &descriptors[i]
		putU32(encodedDescriptor[0:], descriptor.id)
		putU32(encodedDescriptor[4:], descriptor.width)
		putU32(encodedDescriptor[8:], descriptor.height)
		putU64(encodedDescriptor[12:], descriptor.rawLength)
		copy(encodedDescriptor[20:], descriptor.sha256[:])
		_, _ = h.Write(encodedDescriptor[:])
	}
	_, _ = h.Write(writer.drawBytes)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

func (writer *IncrementalCommentTimelineStreamWriter) appendCommitmentDraws(encoded []byte) error {
	maxBytes := int(MaxTimelineDraws) * TimelineDrawBytes
	if len(encoded) > maxBytes-len(writer.drawBytes) {
		return fmt.Errorf("%w: NCT2 commitment Draw bytes exceed %d", errTimelineLimit, maxBytes)
	}
	needed := len(writer.drawBytes) + len(encoded)
	if needed > cap(writer.drawBytes) {
		newCapacity := cap(writer.drawBytes) * 2
		if newCapacity < needed {
			newCapacity = needed
		}
		if newCapacity > maxBytes {
			newCapacity = maxBytes
		}
		grown := make([]byte, len(writer.drawBytes), newCapacity)
		copy(grown, writer.drawBytes)
		writer.drawBytes = grown
	}
	writer.drawBytes = append(writer.drawBytes, encoded...)
	return nil
}

func validateIncrementalTimelineAsset(asset *TimelineAsset, priorRaw uint64) (incrementalTimelineAssetDescriptor, error) {
	if asset == nil {
		return incrementalTimelineAssetDescriptor{}, fmt.Errorf("%w: nil NCT2 asset", errTimelineFormat)
	}
	if asset.Width == 0 || asset.Height == 0 || asset.Width > MaxTimelineAssetDimension || asset.Height > MaxTimelineAssetDimension {
		return incrementalTimelineAssetDescriptor{}, fmt.Errorf("%w: NCT2 asset %d dimensions %dx%d", errTimelineLimit, asset.ID, asset.Width, asset.Height)
	}
	pixels := uint64(asset.Width) * uint64(asset.Height)
	if pixels > math.MaxUint64/4 {
		return incrementalTimelineAssetDescriptor{}, fmt.Errorf("%w: NCT2 asset %d byte length overflow", errTimelineLimit, asset.ID)
	}
	rawLength := pixels * 4
	if rawLength > MaxTimelineAssetBytes {
		return incrementalTimelineAssetDescriptor{}, fmt.Errorf("%w: NCT2 asset %d exceeds %d bytes", errTimelineLimit, asset.ID, MaxTimelineAssetBytes)
	}
	if uint64(len(asset.RGBA)) != rawLength {
		return incrementalTimelineAssetDescriptor{}, fmt.Errorf("%w: NCT2 asset %d has %d pixel bytes, want %d", errTimelineFormat, asset.ID, len(asset.RGBA), rawLength)
	}
	if priorRaw > uint64(MaxTimelineTotalAssetBytes)-rawLength {
		return incrementalTimelineAssetDescriptor{}, fmt.Errorf("%w: NCT2 total asset bytes exceed %d", errTimelineLimit, MaxTimelineTotalAssetBytes)
	}
	if sha256.Sum256(asset.RGBA) != asset.SHA256 {
		return incrementalTimelineAssetDescriptor{}, fmt.Errorf("%w: NCT2 asset %d SHA-256 mismatch", errTimelineFormat, asset.ID)
	}
	return incrementalTimelineAssetDescriptor{
		id: asset.ID, width: asset.Width, height: asset.Height,
		rawLength: rawLength, sha256: asset.SHA256,
	}, nil
}

func (writer *IncrementalCommentTimelineStreamWriter) writeRecord(kind uint32, payload []byte) error {
	err := appendTimelineStreamRecord(writer.out, &writer.sequence, kind, uint32(len(payload)), func() error {
		return writeAll(writer.out, payload)
	})
	if err != nil {
		return writer.fail(err)
	}
	return nil
}

func (writer *IncrementalCommentTimelineStreamWriter) stateError() error {
	if writer == nil {
		return errIncrementalTimelineWriterState
	}
	if writer.failed != nil {
		return writer.failed
	}
	if writer.finished {
		return fmt.Errorf("%w: incremental NCT2 stream already finished", errIncrementalTimelineWriterState)
	}
	return nil
}

func (writer *IncrementalCommentTimelineStreamWriter) fail(err error) error {
	if writer.failed == nil {
		writer.failed = err
	}
	return writer.failed
}
