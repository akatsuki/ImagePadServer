package nicorender

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func goldenTimeline() CommentTimeline {
	scene := CommentTimeline{
		Header: TimelineHeader{
			Width: 33, Height: 19, FrameCount: 3, FPSNum: 30, FPSDen: 1,
			BundleSHA256: sha256.Sum256([]byte("nico-timeline-fixture-v1")),
		},
		Assets: []TimelineAsset{
			{ID: 1, Width: 1, Height: 1, RGBA: []byte{255, 0, 0, 255}},
			{ID: 2, Width: 1, Height: 1, RGBA: []byte{0, 0, 128, 128}},
		},
		Draws: []TimelineDraw{
			{
				AssetID: 1, StartVPos: -4, EndVPos: 100, AnchorVPos: 0,
				OwnerOrder: 0, CommentIndex: 0, PrimitiveIndex: 0,
				Rect:       [4]float32{0, 0, 33, 19},
				Projection: identityProjection(), Alpha: 1, AnchorX: 0, SpeedX: 0,
			},
			{
				AssetID: 2, StartVPos: -1, EndVPos: 6, AnchorVPos: 2,
				OwnerOrder: 0, CommentIndex: 1, PrimitiveIndex: 0,
				Rect:       [4]float32{4, 5, 10, 3},
				Projection: identityProjection(), Alpha: 0.75, AnchorX: 4.25, SpeedX: -0.125,
			},
		},
	}
	for i := range scene.Assets {
		scene.Assets[i].SHA256 = sha256.Sum256(scene.Assets[i].RGBA)
	}
	return scene
}

func identityProjection() [16]float32 {
	return [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
}

func redRenderTimeline() CommentTimeline {
	asset := TimelineAsset{ID: 1, Width: 1, Height: 1, RGBA: []byte{255, 0, 0, 255}}
	asset.SHA256 = sha256.Sum256(asset.RGBA)
	return CommentTimeline{
		Header: TimelineHeader{
			Width: 33, Height: 19, FrameCount: 3, FPSNum: 30, FPSDen: 1,
			BundleSHA256: sha256.Sum256([]byte("nico-timeline-fixture-v1")),
		},
		Assets: []TimelineAsset{asset},
		Draws: []TimelineDraw{{
			AssetID: 1, StartVPos: 0, EndVPos: 100, AnchorVPos: 0,
			Rect: [4]float32{0, 0, 33, 19},
			Projection: [16]float32{
				2.0 / 33.0, 0, 0, 0,
				0, -2.0 / 19.0, 0, 0,
				0, 0, 1, 0,
				-1, 1, 0, 1,
			},
			Alpha: 1, AnchorX: 0, SpeedX: 0,
		}},
	}
}

func TestTimelineFormatGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "timeline", "wire", "wire-multidraw-33x19-3frames.nct"))
	if err != nil {
		t.Fatal(err)
	}
	want := goldenTimeline()
	var encoded bytes.Buffer
	if err := WriteCommentTimeline(&encoded, want); err != nil {
		t.Fatalf("WriteCommentTimeline: %v", err)
	}
	if !bytes.Equal(encoded.Bytes(), golden) {
		t.Fatalf("Go wire output differs from independent Python golden: got %d bytes, golden %d bytes", encoded.Len(), len(golden))
	}
	got, err := ReadCommentTimeline(bytes.NewReader(golden))
	if err != nil {
		t.Fatalf("ReadCommentTimeline(golden): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read golden differs from expected scene:\n got: %#v\nwant: %#v", got, want)
	}
	var roundTrip bytes.Buffer
	if err := WriteCommentTimeline(&roundTrip, got); err != nil {
		t.Fatalf("write parsed scene: %v", err)
	}
	if !bytes.Equal(roundTrip.Bytes(), golden) {
		t.Fatal("read/write round trip changed wire bytes")
	}
}

func TestTimelineFormatRedRenderGolden(t *testing.T) {
	golden, err := os.ReadFile(filepath.Join("testdata", "timeline", "wire", "red-33x19-3frames.nct"))
	if err != nil {
		t.Fatal(err)
	}
	want := redRenderTimeline()
	got, err := ReadCommentTimeline(bytes.NewReader(golden))
	if err != nil {
		t.Fatalf("ReadCommentTimeline(red render golden): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("red render fixture is not the single opaque full-canvas draw:\n got: %#v\nwant: %#v", got, want)
	}
	var encoded bytes.Buffer
	if err := WriteCommentTimeline(&encoded, want); err != nil {
		t.Fatalf("WriteCommentTimeline(red render): %v", err)
	}
	if !bytes.Equal(encoded.Bytes(), golden) {
		t.Fatal("Go red render output differs from independent Python golden")
	}
}

func TestTimelineFormatRejectsAllTruncationsAndTrailingData(t *testing.T) {
	wire, err := os.ReadFile(filepath.Join("testdata", "timeline", "wire", "wire-multidraw-33x19-3frames.nct"))
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(wire); n++ {
		if _, err := ReadCommentTimeline(bytes.NewReader(wire[:n])); err == nil {
			t.Fatalf("accepted truncated NCT1 at %d/%d bytes", n, len(wire))
		}
	}
	if _, err := ReadCommentTimeline(bytes.NewReader(append(append([]byte(nil), wire...), 0))); err == nil {
		t.Fatal("accepted trailing byte after declared records")
	}
}

func TestTimelineFormatRejectsMalformedFixtures(t *testing.T) {
	for _, name := range []string{"unknown-flags.nct", "missing-asset.nct", "oversized-length.nct"} {
		t.Run(name, func(t *testing.T) {
			wire, err := os.ReadFile(filepath.Join("testdata", "timeline", "wire", name))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReadCommentTimeline(bytes.NewReader(wire)); err == nil {
				t.Fatalf("accepted malformed fixture %s", name)
			}
		})
	}
}

func TestTimelineFormatRejectsDuplicateIDsAndNonFiniteFloats(t *testing.T) {
	wire, err := os.ReadFile(filepath.Join("testdata", "timeline", "wire", "wire-multidraw-33x19-3frames.nct"))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := append([]byte(nil), wire...)
	// Header (80) + first asset (4-byte length + 44-byte metadata + 4 pixels).
	binary.LittleEndian.PutUint32(duplicate[80+52+4:], 1)
	if _, err := ReadCommentTimeline(bytes.NewReader(duplicate)); err == nil {
		t.Fatal("accepted duplicate asset ID")
	}
	nan := append([]byte(nil), wire...)
	drawOffset := 80 + 2*52
	binary.LittleEndian.PutUint32(nan[drawOffset+108:], math.Float32bits(float32(math.NaN())))
	if _, err := ReadCommentTimeline(bytes.NewReader(nan)); err == nil {
		t.Fatal("accepted NaN alpha")
	}
	badHash := append([]byte(nil), wire...)
	badHash[80+16] ^= 0xff
	if _, err := ReadCommentTimeline(bytes.NewReader(badHash)); err == nil {
		t.Fatal("accepted asset with mismatched SHA-256")
	}
	inf := goldenTimeline()
	inf.Draws[0].SpeedX = math.Inf(1)
	if err := ValidateCommentTimeline(inf); err == nil {
		t.Fatal("ValidateCommentTimeline accepted infinite speed")
	}
}

func TestTimelineFormatRejectsOversizedHeaderBeforeRecords(t *testing.T) {
	wire, err := os.ReadFile(filepath.Join("testdata", "timeline", "wire", "wire-multidraw-33x19-3frames.nct"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		offset int
		value  uint32
	}{
		{"asset-count", 28, MaxTimelineAssets + 1},
		{"draw-count", 32, MaxTimelineDraws + 1},
		{"raw-byte-total", 68, MaxTimelineTotalAssetBytes + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			malformed := append([]byte(nil), wire...)
			if tt.offset == 68 {
				binary.LittleEndian.PutUint64(malformed[tt.offset:], uint64(tt.value))
			} else {
				binary.LittleEndian.PutUint32(malformed[tt.offset:], tt.value)
			}
			if _, err := ReadCommentTimeline(bytes.NewReader(malformed)); err == nil {
				t.Fatal("accepted oversized header")
			}
		})
	}
}

func TestReadCommentTimelineRejectsCumulativeAssetLimitBeforeExcessPayload(t *testing.T) {
	type assetSpec struct {
		id, width, height uint32
		rawBytes          int64
	}
	largeAsset := assetSpec{
		id: 1, width: 8192, height: 4096,
		rawBytes: int64(MaxTimelineAssetBytes),
	}
	assets := []assetSpec{
		largeAsset,
		{id: 2, width: largeAsset.width, height: largeAsset.height, rawBytes: largeAsset.rawBytes},
		{id: 3, width: 1, height: 1, rawBytes: 4},
	}

	var header [TimelineHeaderBytes]byte
	copy(header[:4], "NCT1")
	binary.LittleEndian.PutUint32(header[4:], TimelineHeaderBytes)
	binary.LittleEndian.PutUint32(header[8:], 1920)
	binary.LittleEndian.PutUint32(header[12:], 1080)
	binary.LittleEndian.PutUint32(header[16:], 1)
	binary.LittleEndian.PutUint32(header[20:], 30)
	binary.LittleEndian.PutUint32(header[24:], 1)
	binary.LittleEndian.PutUint32(header[28:], uint32(len(assets)))
	binary.LittleEndian.PutUint64(header[68:], uint64(MaxTimelineTotalAssetBytes))
	binary.LittleEndian.PutUint32(header[76:], TimelineFlags)

	largeHash := zeroPixelSHA256(largeAsset.rawBytes)
	lastHash := zeroPixelSHA256(4)
	pixels := &zeroPixelReader{}
	parts := []io.Reader{bytes.NewReader(header[:])}
	for _, asset := range assets {
		var hash [32]byte
		if asset.id == 3 {
			hash = lastHash
		} else {
			hash = largeHash
		}
		var record [48]byte
		binary.LittleEndian.PutUint32(record[0:], uint32(44+asset.rawBytes))
		binary.LittleEndian.PutUint32(record[4:], asset.id)
		binary.LittleEndian.PutUint32(record[8:], asset.width)
		binary.LittleEndian.PutUint32(record[12:], asset.height)
		copy(record[16:], hash[:])
		parts = append(parts, bytes.NewReader(record[:]), io.LimitReader(pixels, asset.rawBytes))
	}

	_, err := ReadCommentTimeline(io.MultiReader(parts...))
	if !errors.Is(err, errTimelineLimit) {
		t.Fatalf("ReadCommentTimeline error = %v, want cumulative asset limit", err)
	}
	if pixels.bytesRead != int64(MaxTimelineTotalAssetBytes) {
		t.Fatalf("pixel reader consumed %d bytes, want exactly the capped %d before rejection", pixels.bytesRead, MaxTimelineTotalAssetBytes)
	}
}

func zeroPixelSHA256(size int64) [32]byte {
	h := sha256.New()
	zeros := make([]byte, 64*1024)
	for size > 0 {
		chunk := int64(len(zeros))
		if size < chunk {
			chunk = size
		}
		_, _ = h.Write(zeros[:chunk])
		size -= chunk
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

type zeroPixelReader struct {
	bytesRead int64
}

func (reader *zeroPixelReader) Read(p []byte) (int, error) {
	clear(p)
	reader.bytesRead += int64(len(p))
	return len(p), nil
}

func TestTimelineFormatValidatesLimitsAndReferences(t *testing.T) {
	base := goldenTimeline()
	tests := []struct {
		name string
		edit func(*CommentTimeline)
	}{
		{"output-width", func(s *CommentTimeline) { s.Header.Width = MaxTimelineWidth + 1 }},
		{"frame-count", func(s *CommentTimeline) { s.Header.FrameCount = MaxTimelineFrames + 1 }},
		{"fps", func(s *CommentTimeline) { s.Header.FPSNum = 61; s.Header.FPSDen = 1 }},
		{"asset-dimensions", func(s *CommentTimeline) { s.Assets[0].Width = MaxTimelineAssetDimension + 1 }},
		{"missing-reference", func(s *CommentTimeline) { s.Draws[0].AssetID = 999 }},
		{"empty-interval", func(s *CommentTimeline) { s.Draws[0].EndVPos = s.Draws[0].StartVPos }},
		{"unrepresentable-visible-delta", func(s *CommentTimeline) {
			s.Draws[0].StartVPos = -1 << 30
			s.Draws[0].EndVPos = 1 << 30
			s.Draws[0].AnchorVPos = -1 << 30
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scene := base
			scene.Assets = append([]TimelineAsset(nil), base.Assets...)
			scene.Draws = append([]TimelineDraw(nil), base.Draws...)
			tt.edit(&scene)
			if err := ValidateCommentTimeline(scene); err == nil {
				t.Fatal("ValidateCommentTimeline accepted invalid scene")
			}
		})
	}
}
