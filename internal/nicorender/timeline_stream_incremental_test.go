package nicorender

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
)

func TestIncrementalTimelineStreamWriterMatchesWholeSceneFixtures(t *testing.T) {
	tests := []struct {
		name   string
		stream CommentTimelineStream
	}{
		{name: "empty", stream: nct2FixtureEmptyStream()},
		{name: "parity", stream: nct2FixtureParityStream()},
		{name: "owner-time-inversion", stream: nct2FixtureInversionStream()},
	}
	reversedAssetUse := nct2FixtureParityStream()
	firstAssetID := reversedAssetUse.Elements[0].Draws[0].AssetID
	reversedAssetUse.Elements[0].Draws[0].AssetID = reversedAssetUse.Elements[1].Draws[0].AssetID
	reversedAssetUse.Elements[1].Draws[0].AssetID = firstAssetID
	tests = append(tests, struct {
		name   string
		stream CommentTimelineStream
	}{name: "asset-transfer-order-differs-from-commitment-order", stream: reversedAssetUse})
	for _, vector := range readNCT2TestVectors(t).ReadyVectors {
		tests = append(tests, struct {
			name   string
			stream CommentTimelineStream
		}{name: "readiness-" + vector.Name, stream: nct2ReadyVectorStream(vector)})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var want bytes.Buffer
			if err := WriteCommentTimelineStream(&want, tt.stream); err != nil {
				t.Fatalf("write whole-scene reference: %v", err)
			}

			var got bytes.Buffer
			writer, err := NewIncrementalCommentTimelineStreamWriter(&got, tt.stream.Header, tt.stream.Declarations)
			if err != nil {
				t.Fatalf("create incremental writer: %v", err)
			}
			if err := writeIncrementalTestElements(writer, tt.stream); err != nil {
				t.Fatalf("write incremental elements: %v", err)
			}
			if err := writer.Finish(); err != nil {
				t.Fatalf("finish incremental writer: %v", err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("incremental bytes differ from whole-scene writer: got %d, want %d", got.Len(), want.Len())
			}
		})
	}
}

func TestIncrementalTimelineStreamWriterValidatesElementBeforeItsRecords(t *testing.T) {
	tests := []struct {
		name      string
		stream    CommentTimelineStream
		assets    []TimelineAsset
		noAssets  bool
		edit      func(*TimelineStreamElement)
		wantError string
	}{
		{
			name:      "missing asset",
			stream:    nct2FixtureParityStream(),
			noAssets:  true,
			wantError: "missing newly referenced asset",
		},
		{
			name:   "invalid draw owner/index",
			stream: nct2FixtureParityStream(),
			edit: func(element *TimelineStreamElement) {
				element.Draws[0].OwnerOrder++
			},
			wantError: "owner/index",
		},
		{
			name:   "invalid draw comment index",
			stream: nct2FixtureParityStream(),
			edit: func(element *TimelineStreamElement) {
				element.Draws[0].CommentIndex++
			},
			wantError: "owner/index",
		},
		{
			name:   "invalid clipped interval",
			stream: nct2FixtureParityStream(),
			edit: func(element *TimelineStreamElement) {
				element.Draws[0].StartVPos = 1
			},
			wantError: "clipped interval",
		},
		{
			name:      "invalid asset pixels",
			stream:    nct2FixtureParityStream(),
			assets:    []TimelineAsset{{ID: 1, Width: 1, Height: 1, RGBA: []byte{9, 9, 9, 9}}},
			wantError: "SHA-256",
		},
		{
			name:   "unreferenced asset",
			stream: nct2FixtureParityStream(),
			assets: []TimelineAsset{
				{ID: 1, Width: 1, Height: 1, RGBA: []byte{1, 2, 3, 4}, SHA256: sha256.Sum256([]byte{1, 2, 3, 4})},
				{ID: 99, Width: 1, Height: 1, RGBA: []byte{5, 6, 7, 8}, SHA256: sha256.Sum256([]byte{5, 6, 7, 8})},
			},
			wantError: "not referenced",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := cloneCommentTimelineStream(tt.stream)
			var output bytes.Buffer
			writer, err := NewIncrementalCommentTimelineStreamWriter(&output, stream.Header, stream.Declarations)
			if err != nil {
				t.Fatalf("create writer: %v", err)
			}
			before := output.Len()
			element := stream.Elements[0]
			if tt.edit != nil {
				tt.edit(&element)
			}
			assets := tt.assets
			if assets == nil && !tt.noAssets {
				assets = []TimelineAsset{stream.Assets[0]}
			}
			if err := writer.WriteElement(element, assets); err == nil || !bytes.Contains([]byte(err.Error()), []byte(tt.wantError)) {
				t.Fatalf("WriteElement error = %v, want substring %q", err, tt.wantError)
			}
			if output.Len() != before {
				t.Fatalf("invalid element wrote %d bytes before rejection", output.Len()-before)
			}
		})
	}
}

func TestIncrementalTimelineStreamWriterRejectsOrdinalAndDuplicateAssets(t *testing.T) {
	stream := nct2FixtureParityStream()
	var output bytes.Buffer
	writer, err := NewIncrementalCommentTimelineStreamWriter(&output, stream.Header, stream.Declarations)
	if err != nil {
		t.Fatal(err)
	}
	before := output.Len()
	wrong := stream.Elements[0]
	wrong.Ordinal = 1
	if err := writer.WriteElement(wrong, nil); err == nil {
		t.Fatal("writer accepted an element with the wrong ordinal")
	}
	if output.Len() != before {
		t.Fatal("wrong ordinal emitted element records")
	}

	asset := stream.Assets[0]
	first := stream.Elements[0]
	if err := writer.WriteElement(first, []TimelineAsset{asset}); err != nil {
		t.Fatalf("write first element: %v", err)
	}
	before = output.Len()
	second := stream.Elements[1]
	second.Draws[0].AssetID = first.Draws[0].AssetID
	if err := writer.WriteElement(second, []TimelineAsset{asset}); err == nil {
		t.Fatal("writer accepted an asset already emitted in an earlier element")
	}
	if output.Len() != before {
		t.Fatal("duplicate asset failure emitted second-element records")
	}
}

func TestIncrementalTimelineStreamWriterFinishesEmptyAndZeroDrawScenes(t *testing.T) {
	for _, tt := range []struct {
		name   string
		stream CommentTimelineStream
	}{
		{name: "empty", stream: nct2FixtureEmptyStream()},
		{name: "zero-draw", stream: nct2FixtureInversionStream()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			writer, err := NewIncrementalCommentTimelineStreamWriter(&output, tt.stream.Header, tt.stream.Declarations)
			if err != nil {
				t.Fatal(err)
			}
			if len(tt.stream.Declarations) != 0 {
				if err := writer.Finish(); err == nil {
					t.Fatal("Finish accepted missing zero-Draw completions")
				}
				for _, record := range nct2TestRecords(t, output.Bytes()) {
					if record.kind == TimelineStreamRecordEnd {
						t.Fatal("Finish emitted End while declarations remained incomplete")
					}
				}
				return
			}
			if err := writer.Finish(); err != nil {
				t.Fatalf("finish empty scene: %v", err)
			}
			var whole bytes.Buffer
			if err := WriteCommentTimelineStream(&whole, tt.stream); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), whole.Bytes()) {
				t.Fatal("empty incremental stream differs from whole-scene output")
			}
			end := lastNCT2TestRecord(t, output.Bytes(), TimelineStreamRecordEnd)
			if prefix := binary.LittleEndian.Uint32(end[:4]); prefix != 0 {
				t.Fatalf("empty final prefix=%d, want 0", prefix)
			}
			wantEnd, err := timelineStreamFrameClock(tt.stream.Header.FrameCount, tt.stream.Header.FPSNum, tt.stream.Header.FPSDen)
			if err != nil {
				t.Fatal(err)
			}
			if ready := int64(binary.LittleEndian.Uint64(end[24:32])); ready != wantEnd {
				t.Fatalf("empty final ready=%d, want E=%d", ready, wantEnd)
			}
		})
	}
}

func TestIncrementalTimelineStreamWriterRejectsInvalidInitializationBeforeWriting(t *testing.T) {
	stream := nct2FixtureInversionStream()
	badDeclarations := append([]TimelineStreamDeclaration(nil), stream.Declarations...)
	badDeclarations[1].CommentIndex = 0
	var output bytes.Buffer
	if _, err := NewIncrementalCommentTimelineStreamWriter(&output, stream.Header, badDeclarations); err == nil {
		t.Fatal("writer accepted duplicate/out-of-order declarations")
	}
	if output.Len() != 0 {
		t.Fatalf("invalid initialization emitted %d bytes", output.Len())
	}

	tooMany := make([]TimelineStreamDeclaration, TimelineStreamMaxDeclarations+1)
	for i := range tooMany {
		tooMany[i] = TimelineStreamDeclaration{Ordinal: uint32(i), OwnerOrder: 0, CommentIndex: uint32(i), StartVPos: 0, EndVPos: 300}
	}
	if _, err := NewIncrementalCommentTimelineStreamWriter(&output, stream.Header, tooMany); err == nil {
		t.Fatal("writer accepted too many declarations")
	}
	if output.Len() != 0 {
		t.Fatalf("over-limit initialization emitted %d bytes", output.Len())
	}
}

func TestIncrementalTimelineStreamWriterStopsBeforeEndOnEarlierWriterError(t *testing.T) {
	writeErr := errors.New("stop at watermark")
	output := &recordFailTimelineWriter{failKind: TimelineStreamRecordWatermark, err: writeErr}
	stream := nct2FixtureParityStream()
	writer, err := NewIncrementalCommentTimelineStreamWriter(output, stream.Header, stream.Declarations)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteElement(stream.Elements[0], []TimelineAsset{stream.Assets[0]}); !errors.Is(err, writeErr) {
		t.Fatalf("WriteElement error = %v, want %v", err, writeErr)
	}
	if output.endAttempted {
		t.Fatal("writer initiated End after an earlier writer failure")
	}
}

func TestIncrementalTimelineStreamWriterPropagatesEndError(t *testing.T) {
	writeErr := errors.New("fail End")
	output := &recordFailTimelineWriter{failKind: TimelineStreamRecordEnd, err: writeErr}
	writer, err := NewIncrementalCommentTimelineStreamWriter(output, nct2FixtureEmptyStream().Header, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Finish(); !errors.Is(err, writeErr) {
		t.Fatalf("Finish error = %v, want %v", err, writeErr)
	}
	if !output.endAttempted {
		t.Fatal("writer did not attempt final End")
	}
}

func TestIncrementalTimelineStreamWriterUsesPositiveShortWrites(t *testing.T) {
	stream := nct2FixtureParityStream()
	var want bytes.Buffer
	if err := WriteCommentTimelineStream(&want, stream); err != nil {
		t.Fatal(err)
	}
	short := &limitedTimelineWriter{maxWrite: 7}
	writer, err := NewIncrementalCommentTimelineStreamWriter(short, stream.Header, stream.Declarations)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIncrementalTestElements(writer, stream); err != nil {
		t.Fatal(err)
	}
	if err := writer.Finish(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(short.Bytes(), want.Bytes()) {
		t.Fatalf("short-writing incremental stream differs: got %d bytes, want %d", short.Len(), want.Len())
	}
}

func TestIncrementalTimelineStreamWriterDoesNotRetainAssetPixels(t *testing.T) {
	stream := nct2FixtureParityStream()
	var want bytes.Buffer
	if err := WriteCommentTimelineStream(&want, stream); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	writer, err := NewIncrementalCommentTimelineStreamWriter(&output, stream.Header, stream.Declarations)
	if err != nil {
		t.Fatal(err)
	}
	firstAsset := stream.Assets[0]
	if err := writer.WriteElement(stream.Elements[0], []TimelineAsset{firstAsset}); err != nil {
		t.Fatalf("write first element: %v", err)
	}
	firstAsset.RGBA[0] ^= 0xff
	if err := writer.WriteElement(stream.Elements[1], []TimelineAsset{stream.Assets[1]}); err != nil {
		t.Fatalf("write second element: %v", err)
	}
	if err := writer.Finish(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), want.Bytes()) {
		t.Fatal("writer retained or reread the first asset RGBA after WriteElement returned")
	}
}

func TestIncrementalTimelineStreamWriterRejectsPerElementDrawLimit(t *testing.T) {
	stream := nct2FixtureParityStream()
	var output bytes.Buffer
	writer, err := NewIncrementalCommentTimelineStreamWriter(&output, stream.Header, stream.Declarations)
	if err != nil {
		t.Fatal(err)
	}
	draws := make([]TimelineDraw, TimelineStreamMaxDrawsPerElement+1)
	for i := range draws {
		draws[i] = stream.Elements[0].Draws[0]
		draws[i].PrimitiveIndex = uint32(i)
	}
	element := TimelineStreamElement{Ordinal: 0, Draws: draws}
	before := output.Len()
	if err := writer.WriteElement(element, []TimelineAsset{stream.Assets[0]}); err == nil {
		t.Fatal("writer accepted too many draws in one element")
	}
	if output.Len() != before {
		t.Fatal("over-limit element emitted records")
	}
}

func TestIncrementalTimelineStreamWriterRejectsTotalDrawLimitBeforeElementRecords(t *testing.T) {
	declarations := make([]TimelineStreamDeclaration, 98)
	for i := range declarations {
		declarations[i] = TimelineStreamDeclaration{Ordinal: uint32(i), OwnerOrder: 0, CommentIndex: uint32(i), StartVPos: 0, EndVPos: 300}
	}
	stream := nct2FixtureInversionStream()
	stream.Header.FrameCount = 3
	stream.Header.FPSNum = 1
	stream.Header.FPSDen = 1
	stream.Declarations = declarations
	assetBytes := []byte{1, 2, 3, 4}
	asset := TimelineAsset{ID: 1, Width: 1, Height: 1, RGBA: assetBytes, SHA256: sha256.Sum256(assetBytes)}
	var output bytes.Buffer
	writer, err := NewIncrementalCommentTimelineStreamWriter(&output, stream.Header, declarations)
	if err != nil {
		t.Fatal(err)
	}
	for ordinal := 0; ordinal < len(declarations)-1; ordinal++ {
		draws := make([]TimelineDraw, TimelineStreamMaxDrawsPerElement)
		for primitive := range draws {
			draws[primitive] = TimelineDraw{
				AssetID: asset.ID, StartVPos: 0, EndVPos: 300, OwnerOrder: 0,
				CommentIndex: uint32(ordinal), PrimitiveIndex: uint32(primitive), Alpha: 1,
			}
		}
		var newAssets []TimelineAsset
		if ordinal == 0 {
			newAssets = []TimelineAsset{asset}
		}
		if err := writer.WriteElement(TimelineStreamElement{Ordinal: uint32(ordinal), Draws: draws}, newAssets); err != nil {
			t.Fatalf("write element %d: %v", ordinal, err)
		}
	}
	before := output.Len()
	draws := make([]TimelineDraw, TimelineStreamMaxDrawsPerElement)
	for primitive := range draws {
		draws[primitive] = TimelineDraw{
			AssetID: asset.ID, StartVPos: 0, EndVPos: 300, OwnerOrder: 0,
			CommentIndex: uint32(len(declarations) - 1), PrimitiveIndex: uint32(primitive), Alpha: 1,
		}
	}
	if err := writer.WriteElement(TimelineStreamElement{Ordinal: uint32(len(declarations) - 1), Draws: draws}, nil); err == nil {
		t.Fatal("writer accepted a total Draw count above 100,000")
	}
	if output.Len() != before {
		t.Fatal("over-limit total Draw element emitted records")
	}
}

func TestIncrementalTimelineStreamWriterRejectsAssetCountLimitBeforeElementRecords(t *testing.T) {
	declarations := make([]TimelineStreamDeclaration, 11)
	for i := range declarations {
		declarations[i] = TimelineStreamDeclaration{Ordinal: uint32(i), OwnerOrder: 0, CommentIndex: uint32(i), StartVPos: 0, EndVPos: 300}
	}
	stream := nct2FixtureInversionStream()
	stream.Header.FrameCount = 3
	stream.Header.FPSNum = 1
	stream.Header.FPSDen = 1
	var output bytes.Buffer
	writer, err := NewIncrementalCommentTimelineStreamWriter(&output, stream.Header, declarations)
	if err != nil {
		t.Fatal(err)
	}
	nextAssetID := uint32(1)
	for ordinal := 0; ordinal < len(declarations); ordinal++ {
		count := TimelineStreamMaxDrawsPerElement
		if ordinal == 9 {
			count = MaxTimelineAssets - 9*TimelineStreamMaxDrawsPerElement
		} else if ordinal == 10 {
			count = 1
		}
		draws := make([]TimelineDraw, count)
		assets := make([]TimelineAsset, count)
		for primitive := range draws {
			id := nextAssetID + uint32(primitive)
			pixels := []byte{byte(id), byte(id >> 8), byte(id >> 16), byte(id >> 24)}
			assets[primitive] = TimelineAsset{ID: id, Width: 1, Height: 1, RGBA: pixels, SHA256: sha256.Sum256(pixels)}
			draws[primitive] = TimelineDraw{
				AssetID: id, StartVPos: 0, EndVPos: 300, OwnerOrder: 0,
				CommentIndex: uint32(ordinal), PrimitiveIndex: uint32(primitive), Alpha: 1,
			}
		}
		before := output.Len()
		err := writer.WriteElement(TimelineStreamElement{Ordinal: uint32(ordinal), Draws: draws}, assets)
		if ordinal == len(declarations)-1 {
			if err == nil {
				t.Fatal("writer accepted more than 10,000 unique assets")
			}
			if output.Len() != before {
				t.Fatal("over-limit asset element emitted records")
			}
			return
		}
		if err != nil {
			t.Fatalf("write element %d: %v", ordinal, err)
		}
		nextAssetID += uint32(count)
	}
}

func writeIncrementalTestElements(writer *IncrementalCommentTimelineStreamWriter, stream CommentTimelineStream) error {
	assetByID := make(map[uint32]TimelineAsset, len(stream.Assets))
	for i := range stream.Assets {
		assetByID[stream.Assets[i].ID] = stream.Assets[i]
	}
	sent := make(map[uint32]bool, len(stream.Assets))
	for _, element := range stream.Elements {
		var newAssets []TimelineAsset
		for _, draw := range element.Draws {
			if sent[draw.AssetID] {
				continue
			}
			asset, ok := assetByID[draw.AssetID]
			if !ok {
				return fmt.Errorf("test fixture draw references missing asset %d", draw.AssetID)
			}
			newAssets = append(newAssets, asset)
			sent[draw.AssetID] = true
		}
		if err := writer.WriteElement(element, newAssets); err != nil {
			return err
		}
	}
	return nil
}

func lastNCT2TestRecord(t *testing.T, encoded []byte, kind uint32) []byte {
	t.Helper()
	records := nct2TestRecords(t, encoded)
	if len(records) == 0 || records[len(records)-1].kind != kind {
		t.Fatalf("last NCT2 record is not kind %d", kind)
	}
	return records[len(records)-1].payload
}

type recordFailTimelineWriter struct {
	bytes.Buffer
	failKind     uint32
	err          error
	endAttempted bool
}

func (w *recordFailTimelineWriter) Write(p []byte) (int, error) {
	if len(p) >= TimelineStreamEnvelopeBytes {
		kind := binary.LittleEndian.Uint32(p[:4])
		if kind == TimelineStreamRecordEnd {
			w.endAttempted = true
		}
		if kind == w.failKind {
			return 0, w.err
		}
	}
	return w.Buffer.Write(p)
}
