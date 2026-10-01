package nicorender

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type nct2ReadyVector struct {
	Name              string    `json:"name"`
	ExclusiveEnd      int64     `json:"exclusive_end"`
	ClippedStarts     []int32   `json:"clipped_starts"`
	OriginalIntervals [][]int32 `json:"original_intervals"`
	ClippedIntervals  [][]int32 `json:"clipped_intervals"`
	DrawIntervals     [][]int32 `json:"draw_intervals_remain"`
	ReadyByPrefix     []int64   `json:"ready_by_prefix"`
	InvalidClaims     []struct {
		Prefix uint32 `json:"prefix"`
		Ready  int64  `json:"ready"`
	} `json:"invalid_claims"`
}

type nct2BoundaryVector struct {
	Name                  string          `json:"name"`
	Ready                 int64           `json:"ready"`
	FrameVPos             json.RawMessage `json:"frame_vpos"`
	MayRender             bool            `json:"may_render"`
	AtStartVPos           int64           `json:"at_start_vpos"`
	MayRenderAtStart      bool            `json:"may_render_at_start"`
	DrawInterval          []int32         `json:"draw_interval"`
	VisibleAtStart        bool            `json:"visible_at_start"`
	VisibleAtEnd          bool            `json:"visible_at_end"`
	FrameIndices          []uint32        `json:"frame_indices"`
	ExclusiveEndAtFrame61 int64           `json:"exclusive_end_at_frame_61"`
}

type nct2TestVectors struct {
	ReadyVectors         []nct2ReadyVector    `json:"ready_vectors"`
	Boundaries           []nct2BoundaryVector `json:"boundaries"`
	CompletionRejections []string             `json:"completion_and_terminal_rejections"`
}

type nct2TestRecord struct {
	kind    uint32
	payload []byte
}

func readNCT2TestVectors(t *testing.T) nct2TestVectors {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "timeline", "stream", "vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors nct2TestVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatalf("decode shared NCT2 vectors: %v", err)
	}
	return vectors
}

func nct2ReadyVectorStream(vector nct2ReadyVector) CommentTimelineStream {
	if len(vector.OriginalIntervals) != 0 {
		stream := nct2FixtureParityStream()
		stream.Header.FPSNum = 1
		stream.Header.FPSDen = 1
		for ordinal, interval := range vector.OriginalIntervals {
			stream.Declarations[ordinal].StartVPos = interval[0]
			stream.Declarations[ordinal].EndVPos = interval[1]
			stream.Elements[ordinal].Draws[0].StartVPos = interval[0]
			stream.Elements[ordinal].Draws[0].EndVPos = interval[1]
		}
		return stream
	}

	stream := nct2FixtureInversionStream()
	stream.Header.FPSNum = 1
	stream.Header.FPSDen = 1
	stream.Declarations = make([]TimelineStreamDeclaration, len(vector.ClippedStarts))
	stream.Elements = make([]TimelineStreamElement, len(vector.ClippedStarts))
	for ordinal, start := range vector.ClippedStarts {
		stream.Declarations[ordinal] = TimelineStreamDeclaration{
			Ordinal: uint32(ordinal), OwnerOrder: 0, CommentIndex: uint32(ordinal),
			StartVPos: start, EndVPos: int32(vector.ExclusiveEnd),
		}
		stream.Elements[ordinal] = TimelineStreamElement{Ordinal: uint32(ordinal)}
	}
	return stream
}

func nct2TestRecords(t *testing.T, bytes []byte) []nct2TestRecord {
	t.Helper()
	var records []nct2TestRecord
	for offset := 0; offset+TimelineStreamEnvelopeBytes <= len(bytes); {
		kind := binary.LittleEndian.Uint32(bytes[offset : offset+4])
		payloadLen := int(binary.LittleEndian.Uint32(bytes[offset+4 : offset+8]))
		payloadStart := offset + TimelineStreamEnvelopeBytes
		payloadEnd := payloadStart + payloadLen
		if payloadEnd > len(bytes) {
			t.Fatalf("record %d kind %d is truncated", len(records), kind)
		}
		records = append(records, nct2TestRecord{kind: kind, payload: bytes[payloadStart:payloadEnd]})
		offset = payloadEnd
	}
	return records
}

func nct2FixtureParityStream() CommentTimelineStream {
	scene := goldenTimeline()
	return CommentTimelineStream{
		Header: scene.Header,
		Declarations: []TimelineStreamDeclaration{
			{Ordinal: 0, OwnerOrder: 0, CommentIndex: 0, StartVPos: -4, EndVPos: 100},
			{Ordinal: 1, OwnerOrder: 0, CommentIndex: 1, StartVPos: -1, EndVPos: 6},
		},
		Assets: scene.Assets,
		Elements: []TimelineStreamElement{
			{Ordinal: 0, Draws: []TimelineDraw{scene.Draws[0]}},
			{Ordinal: 1, Draws: []TimelineDraw{scene.Draws[1]}},
		},
	}
}

func nct2FixtureEmptyStream() CommentTimelineStream {
	return CommentTimelineStream{Header: goldenTimeline().Header}
}

func nct2FixtureInversionStream() CommentTimelineStream {
	return CommentTimelineStream{
		Header: TimelineHeader{
			Width: 1, Height: 1, FrameCount: 3, FPSNum: 1, FPSDen: 1,
			BundleSHA256: sha256.Sum256([]byte("nct2-watermark-v1")),
		},
		Declarations: []TimelineStreamDeclaration{
			{Ordinal: 0, OwnerOrder: 0, CommentIndex: 0, StartVPos: 0, EndVPos: 300},
			{Ordinal: 1, OwnerOrder: 0, CommentIndex: 1, StartVPos: 200, EndVPos: 300},
			{Ordinal: 2, OwnerOrder: 0, CommentIndex: 2, StartVPos: 0, EndVPos: 300},
		},
		Elements: []TimelineStreamElement{
			{Ordinal: 0},
			{Ordinal: 1},
			{Ordinal: 2},
		},
	}
}

func TestNCT2WriterMatchesCheckedInGoldens(t *testing.T) {
	tests := []struct {
		name  string
		input CommentTimelineStream
	}{
		{"empty-scene.nct2", nct2FixtureEmptyStream()},
		{"nct1-compat-multidraw.nct2", nct2FixtureParityStream()},
		{"owner-time-inversion.nct2", nct2FixtureInversionStream()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "timeline", "stream", tt.name))
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := WriteCommentTimelineStream(&got, tt.input); err != nil {
				t.Fatalf("WriteCommentTimelineStream: %v", err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("NCT2 output differs from %s: got %d bytes, want %d", tt.name, got.Len(), len(want))
			}
		})
	}
}

func TestNCT2WriterRejectsInvalidRequestsBeforeWriting(t *testing.T) {
	base := nct2FixtureParityStream()
	tests := []struct {
		name string
		edit func(*CommentTimelineStream)
	}{
		{"unknown-asset-reference", func(s *CommentTimelineStream) { s.Elements[0].Draws[0].AssetID = 99 }},
		{"draw-owner-index-mismatch", func(s *CommentTimelineStream) { s.Elements[0].Draws[0].CommentIndex = 1 }},
		{"draw-interval-mismatch", func(s *CommentTimelineStream) { s.Elements[0].Draws[0].StartVPos = 1 }},
		{"primitive-order", func(s *CommentTimelineStream) { s.Elements[0].Draws[0].PrimitiveIndex = 1 }},
		{"declaration-order", func(s *CommentTimelineStream) { s.Declarations[1].CommentIndex = 0 }},
		{"missing-element-completion", func(s *CommentTimelineStream) { s.Elements = s.Elements[:1] }},
		{"asset-dimension-limit", func(s *CommentTimelineStream) { s.Assets[0].Width = MaxTimelineAssetDimension + 1 }},
		{"asset-length-overflow-or-limit", func(s *CommentTimelineStream) {
			s.Assets[0].Width = ^uint32(0)
			s.Assets[0].Height = ^uint32(0)
		}},
		{"per-element-draw-limit", func(s *CommentTimelineStream) {
			many := make([]TimelineDraw, TimelineStreamMaxDrawsPerElement+1)
			for i := range many {
				many[i] = s.Elements[0].Draws[0]
				many[i].PrimitiveIndex = uint32(i)
			}
			s.Elements[0].Draws = many
			s.Elements = s.Elements[:1]
			s.Declarations = s.Declarations[:1]
		}},
		{"total-draw-limit", func(s *CommentTimelineStream) {
			baseDraw := s.Elements[0].Draws[0]
			s.Declarations = make([]TimelineStreamDeclaration, 98)
			s.Elements = make([]TimelineStreamElement, len(s.Declarations))
			for i := range s.Declarations {
				s.Declarations[i] = TimelineStreamDeclaration{
					Ordinal: uint32(i), OwnerOrder: 0, CommentIndex: uint32(i), StartVPos: 0, EndVPos: 100,
				}
				draws := make([]TimelineDraw, TimelineStreamMaxDrawsPerElement)
				for primitive := range draws {
					draws[primitive] = baseDraw
					draws[primitive].CommentIndex = uint32(i)
					draws[primitive].PrimitiveIndex = uint32(primitive)
				}
				s.Elements[i] = TimelineStreamElement{Ordinal: uint32(i), Draws: draws}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := cloneCommentTimelineStream(base)
			tt.edit(&input)
			var output bytes.Buffer
			if err := WriteCommentTimelineStream(&output, input); err == nil {
				t.Fatal("writer accepted invalid request")
			}
			if output.Len() != 0 {
				t.Fatalf("writer emitted %d bytes before rejecting request", output.Len())
			}
		})
	}
}

func TestNCT2WriterRejectsAssetSHAMismatchBeforeWriting(t *testing.T) {
	stream := nct2FixtureParityStream()
	stream.Assets[0].RGBA[0] ^= 0xff

	var output bytes.Buffer
	if err := WriteCommentTimelineStream(&output, stream); err == nil {
		t.Fatal("writer accepted asset pixels that do not match the declared SHA-256")
	}
	if output.Len() != 0 {
		t.Fatalf("writer emitted %d bytes before rejecting the asset hash mismatch", output.Len())
	}
}

func TestNCT2WriterPropagatesWriterFailures(t *testing.T) {
	writeErr := errors.New("test writer failure")
	if err := WriteCommentTimelineStream(failingTimelineWriter{err: writeErr}, nct2FixtureEmptyStream()); !errors.Is(err, writeErr) {
		t.Fatalf("writer error = %v, want %v", err, writeErr)
	}
	if err := WriteCommentTimelineStream(zeroTimelineWriter{}, nct2FixtureEmptyStream()); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress writer error = %v, want %v", err, io.ErrShortWrite)
	}
}

func TestNCT2WriterHandlesPositiveShortWrites(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "timeline", "stream", "nct1-compat-multidraw.nct2"))
	if err != nil {
		t.Fatal(err)
	}
	output := &limitedTimelineWriter{maxWrite: 3}
	if err := WriteCommentTimelineStream(output, nct2FixtureParityStream()); err != nil {
		t.Fatalf("WriteCommentTimelineStream with positive short writes: %v", err)
	}
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("short-writing output differs from golden: got %d bytes, want %d", output.Len(), len(want))
	}
}

type failingTimelineWriter struct{ err error }

func (w failingTimelineWriter) Write([]byte) (int, error) { return 0, w.err }

type zeroTimelineWriter struct{}

func (zeroTimelineWriter) Write([]byte) (int, error) { return 0, nil }

type limitedTimelineWriter struct {
	bytes.Buffer
	maxWrite int
}

func (w *limitedTimelineWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		p = p[:w.maxWrite]
	}
	return w.Buffer.Write(p)
}

func TestTimelineFormatWriterKeepsNCT1Goldens(t *testing.T) {
	tests := []struct {
		name  string
		scene CommentTimeline
	}{
		{"wire-multidraw-33x19-3frames.nct", goldenTimeline()},
		{"red-33x19-3frames.nct", redRenderTimeline()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "timeline", "wire", tt.name))
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := WriteCommentTimeline(&got, tt.scene); err != nil {
				t.Fatalf("WriteCommentTimeline: %v", err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatal("NCT1 writer bytes changed")
			}
		})
	}
}

func TestNCT2ParityNormalizesToNCT1LegacyGolden(t *testing.T) {
	stream := nct2FixtureParityStream()
	var encodedNCT2 bytes.Buffer
	if err := WriteCommentTimelineStream(&encodedNCT2, stream); err != nil {
		t.Fatalf("write fixed NCT2 parity stream: %v", err)
	}
	wantNCT2, err := os.ReadFile(filepath.Join("testdata", "timeline", "stream", "nct1-compat-multidraw.nct2"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encodedNCT2.Bytes(), wantNCT2) {
		t.Fatal("NCT2 parity model differs from the checked-in fixed stream")
	}

	wantNCT1, err := os.ReadFile(filepath.Join("testdata", "timeline", "wire", "wire-multidraw-33x19-3frames.nct"))
	if err != nil {
		t.Fatal(err)
	}
	wantNCT1SHA := sha256.Sum256(wantNCT1)
	if got := fmt.Sprintf("%x", wantNCT1SHA); got != "d506db27b68a294e8e8a9d5db1f512d274da8b2c62543b5213c121e26f379948" {
		t.Fatalf("legacy NCT1 golden SHA-256=%s", got)
	}
	legacyScene, err := ReadCommentTimeline(bytes.NewReader(wantNCT1))
	if err != nil {
		t.Fatalf("read legacy NCT1 golden: %v", err)
	}

	normalized := CommentTimeline{Header: stream.Header, Assets: stream.Assets}
	for _, element := range stream.Elements {
		normalized.Draws = append(normalized.Draws, element.Draws...)
	}
	var normalizedNCT1 bytes.Buffer
	if err := WriteCommentTimeline(&normalizedNCT1, normalized); err != nil {
		t.Fatalf("write NCT1-normalized NCT2 parity scene: %v", err)
	}
	if !bytes.Equal(normalizedNCT1.Bytes(), wantNCT1) {
		t.Fatal("NCT2 parity scene does not normalize to the legacy NCT1 bytes")
	}
	if !reflect.DeepEqual(normalized, legacyScene) {
		t.Fatal("NCT2 parity scene does not normalize to the parsed legacy NCT1 scene")
	}

	plan, err := validateTimelineStream(stream)
	if err != nil {
		t.Fatalf("validate NCT2 parity scene commitment: %v", err)
	}
	if got := fmt.Sprintf("%x", plan.commitment); got != "c3f6fa5024466409879965d7cc0471aa33d5d3fb1b5842185bee5e923f785350" {
		t.Fatalf("NCT2 scene commitment=%s", got)
	}
}

func cloneCommentTimelineStream(s CommentTimelineStream) CommentTimelineStream {
	out := s
	out.Declarations = append([]TimelineStreamDeclaration(nil), s.Declarations...)
	out.Assets = append([]TimelineAsset(nil), s.Assets...)
	for i := range out.Assets {
		out.Assets[i].RGBA = append([]byte(nil), s.Assets[i].RGBA...)
	}
	out.Elements = make([]TimelineStreamElement, len(s.Elements))
	for i := range s.Elements {
		out.Elements[i] = s.Elements[i]
		out.Elements[i].Draws = append([]TimelineDraw(nil), s.Elements[i].Draws...)
	}
	return out
}

func TestNCT2SharedReadyVectorsDriveWriterWatermarks(t *testing.T) {
	vectors := readNCT2TestVectors(t)
	if len(vectors.ReadyVectors) != 4 {
		t.Fatalf("shared readiness vector count = %d, want 4", len(vectors.ReadyVectors))
	}
	for _, vector := range vectors.ReadyVectors {
		t.Run(vector.Name, func(t *testing.T) {
			stream := nct2ReadyVectorStream(vector)
			if got, want := len(vector.ReadyByPrefix), len(stream.Declarations)+1; got != want {
				t.Fatalf("ready vector prefixes = %d, want %d", got, want)
			}
			for prefix, want := range vector.ReadyByPrefix {
				if prefix > 0 && want < vector.ReadyByPrefix[prefix-1] {
					t.Errorf("ready prefix regressed from %d to %d at prefix %d", vector.ReadyByPrefix[prefix-1], want, prefix)
				}
				if got := readyTimelineStream(stream.Declarations, uint32(prefix), vector.ExclusiveEnd); got != want {
					t.Errorf("ready(%d) = %d, want %d", prefix, got, want)
				}
			}
			for _, claim := range vector.InvalidClaims {
				if got := readyTimelineStream(stream.Declarations, claim.Prefix, vector.ExclusiveEnd); got == claim.Ready {
					t.Errorf("invalid ready claim %d at prefix %d matched recomputed readiness", claim.Ready, claim.Prefix)
				}
			}

			var encoded bytes.Buffer
			if err := WriteCommentTimelineStream(&encoded, stream); err != nil {
				t.Fatalf("write stream: %v", err)
			}
			var serializedReady []int64
			for _, record := range nct2TestRecords(t, encoded.Bytes()) {
				if record.kind == TimelineStreamRecordWatermark {
					prefix := binary.LittleEndian.Uint32(record.payload[:4])
					ready := int64(binary.LittleEndian.Uint64(record.payload[4:12]))
					if int(prefix) >= len(vector.ReadyByPrefix) {
						t.Fatalf("serialized watermark prefix %d is out of range", prefix)
					}
					if want := vector.ReadyByPrefix[prefix]; ready != want {
						t.Errorf("serialized ready(%d) = %d, want %d", prefix, ready, want)
					}
					serializedReady = append(serializedReady, ready)
				}
			}
			if !reflect.DeepEqual(serializedReady, vector.ReadyByPrefix[1:]) {
				t.Errorf("serialized watermarks = %v, want %v", serializedReady, vector.ReadyByPrefix[1:])
			}

			if len(vector.OriginalIntervals) != 0 {
				var declarationPayload []byte
				var serializedDraws [][]int32
				for _, record := range nct2TestRecords(t, encoded.Bytes()) {
					if record.kind == TimelineStreamRecordDeclarations {
						declarationPayload = record.payload
					}
					if record.kind == TimelineStreamRecordElementComplete {
						drawCount := binary.LittleEndian.Uint32(record.payload[4:8])
						for draw := uint32(0); draw < drawCount; draw++ {
							start := 8 + int(draw)*TimelineDrawBytes
							serializedDraws = append(serializedDraws, []int32{
								int32(binary.LittleEndian.Uint32(record.payload[start+4 : start+8])),
								int32(binary.LittleEndian.Uint32(record.payload[start+8 : start+12])),
							})
						}
					}
				}
				if len(declarationPayload) == 0 {
					t.Fatal("serialized stream omitted Declarations")
				}
				for ordinal, expected := range vector.ClippedIntervals {
					entry := declarationPayload[4+ordinal*TimelineStreamDeclarationBytes:]
					start := int32(binary.LittleEndian.Uint32(entry[12:16]))
					end := int32(binary.LittleEndian.Uint32(entry[16:20]))
					if start != expected[0] || end != expected[1] {
						t.Errorf("serialized declaration %d interval = [%d,%d], want %v", ordinal, start, end, expected)
					}
				}
				if !reflect.DeepEqual(serializedDraws, vector.DrawIntervals) {
					t.Errorf("serialized original Draw intervals = %v, want %v", serializedDraws, vector.DrawIntervals)
				}
			}
		})
	}
}

func TestNCT2SharedFrameBoundaryAndFractionalFPSVectors(t *testing.T) {
	vectors := readNCT2TestVectors(t)
	for _, vector := range vectors.Boundaries {
		t.Run(vector.Name, func(t *testing.T) {
			switch vector.Name {
			case "exact-start-waits":
				var frame int64
				if err := json.Unmarshal(vector.FrameVPos, &frame); err != nil {
					t.Fatal(err)
				}
				if got := frame < vector.Ready; got != vector.MayRender {
					t.Fatalf("FrameClock %d < ready %d = %v, want %v", frame, vector.Ready, got, vector.MayRender)
				}
				if got := vector.AtStartVPos < vector.Ready; got != vector.MayRenderAtStart {
					t.Fatalf("frame at ready boundary is allowed = %v, want %v", got, vector.MayRenderAtStart)
				}
			case "draw-end-is-exclusive":
				// T5.5 has no runtime Draw visibility gate; T6.7 will test that path.
				if len(vector.DrawInterval) != 2 || vector.DrawInterval[0] != 100 || vector.DrawInterval[1] != 200 || !vector.VisibleAtStart || vector.VisibleAtEnd {
					t.Fatalf("unexpected shared half-open interval vector: %+v", vector)
				}
			case "fractional-fps-60000-1001":
				var want []int64
				if err := json.Unmarshal(vector.FrameVPos, &want); err != nil {
					t.Fatal(err)
				}
				if len(want) != len(vector.FrameIndices) {
					t.Fatalf("fractional frame vector length mismatch")
				}
				for i, frame := range vector.FrameIndices {
					got, err := timelineStreamFrameClock(frame, 60_000, 1_001)
					if err != nil || got != want[i] {
						t.Errorf("FrameClock(%d,60000,1001) = %d, %v, want %d", frame, got, err, want[i])
					}
				}
				gotEnd, err := timelineStreamFrameClock(61, 60_000, 1_001)
				if err != nil || gotEnd != vector.ExclusiveEndAtFrame61 {
					t.Errorf("E at frame 61 = %d, %v, want %d", gotEnd, err, vector.ExclusiveEndAtFrame61)
				}
			default:
				t.Fatalf("unhandled shared boundary vector %q", vector.Name)
			}
		})
	}
}

func TestNCT2ZeroDrawStillRequiresExplicitElementCompletion(t *testing.T) {
	vectors := readNCT2TestVectors(t)
	found := false
	for _, rejection := range vectors.CompletionRejections {
		if strings.Contains(rejection, "zero-primitive declaration without an explicit ElementComplete") {
			found = true
		}
	}
	if !found {
		t.Fatal("shared vectors omit the zero-primitive completion rejection")
	}

	stream := nct2FixtureInversionStream()
	var encoded bytes.Buffer
	if err := WriteCommentTimelineStream(&encoded, stream); err != nil {
		t.Fatalf("write completed zero-Draw elements: %v", err)
	}
	elements, watermarks := 0, 0
	for _, record := range nct2TestRecords(t, encoded.Bytes()) {
		if record.kind == TimelineStreamRecordElementComplete {
			elements++
			if got := binary.LittleEndian.Uint32(record.payload[4:8]); got != 0 {
				t.Errorf("zero-Draw element reports %d primitives", got)
			}
		}
		if record.kind == TimelineStreamRecordWatermark {
			watermarks++
		}
	}
	if elements != len(stream.Declarations) || watermarks != len(stream.Declarations) {
		t.Fatalf("serialized %d ElementComplete and %d watermarks for %d zero-Draw declarations", elements, watermarks, len(stream.Declarations))
	}

	stream.Elements = stream.Elements[:len(stream.Elements)-1]
	encoded.Reset()
	if err := WriteCommentTimelineStream(&encoded, stream); err == nil {
		t.Fatal("writer accepted a zero-Draw declaration without an explicit ElementComplete")
	}
	if encoded.Len() != 0 {
		t.Fatalf("writer emitted %d bytes before rejecting the incomplete declaration", encoded.Len())
	}
}
