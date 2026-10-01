package nicorender

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"

	"imagepadserver/internal/niconico"
)

func TestValidateTimelineSnapshotAcceptsLegacyPayloadWithoutSummaryFields(t *testing.T) {
	snapshot := niconico.Snapshot{
		VideoID: "sm9",
		Threads: []niconico.Thread{{Fork: "main", Comments: []niconico.Comment{{ID: "c1", Body: "legacy comment"}}}},
	}
	if err := validateTimelineSnapshot(snapshot); err != nil {
		t.Fatalf("legacy snapshot without summary fields: %v", err)
	}
}

func TestValidateTimelineSnapshotRejectsMissingSummaryForCurrentSchema(t *testing.T) {
	snapshot := niconico.Snapshot{
		SchemaVersion: 1, VideoID: "sm9",
		Threads: []niconico.Thread{{Fork: "main", Comments: []niconico.Comment{{ID: "c1", Body: "comment"}}}},
	}
	if err := validateTimelineSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "invalid snapshot comment status") {
		t.Fatalf("missing current-schema summary error=%v", err)
	}
}

func TestTimelineCaptureNoComments(t *testing.T) {
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{VideoID: "sm9"})
	if err != nil {
		t.Fatal(err)
	}
	options := RenderOptions{Width: 640, Height: 360, DurationMs: 6000, FPSNum: 30, FPSDen: 1}
	scene, report, err := CaptureCommentTimeline(context.Background(), snapshot, options)
	if err != nil {
		t.Fatal(err)
	}
	if scene.Header.FrameCount != 180 || len(scene.Assets) != 0 || len(scene.Draws) != 0 {
		t.Fatalf("empty timeline = header=%+v assets=%d draws=%d", scene.Header, len(scene.Assets), len(scene.Draws))
	}
	if report.EligibleComments != 0 || report.UnsupportedComments != 0 || report.DrawCanvasCalls != 0 || report.ElementDrawCalls != 0 {
		t.Fatalf("empty capture report = %+v", report)
	}
}

func TestTimelineCaptureNoVisibleComments(t *testing.T) {
	meta := timelineCommentMeta{StartVPos: 60_000, EndVPos: 60_600}
	_, active, err := clipTimelineCaptureInterval(meta, 0, 597)
	if err != nil || active {
		t.Fatalf("outside comment active=%v err=%v, want inactive", active, err)
	}
}

func TestTimelineAssetReadbackMode(t *testing.T) {
	t.Run("default-sync", func(t *testing.T) {
		t.Setenv("NICO_TIMELINE_ASSET_READBACK", "")
		got, err := timelineAssetReadbackMode()
		if err != nil || got != "sync" {
			t.Fatalf("mode=%q err=%v, want sync", got, err)
		}
	})
	t.Run("pbo", func(t *testing.T) {
		t.Setenv("NICO_TIMELINE_ASSET_READBACK", " PBO ")
		got, err := timelineAssetReadbackMode()
		if err != nil || got != "pbo" {
			t.Fatalf("mode=%q err=%v, want pbo", got, err)
		}
	})
	t.Run("rejects-unknown", func(t *testing.T) {
		t.Setenv("NICO_TIMELINE_ASSET_READBACK", "gpu-magic")
		if _, err := timelineAssetReadbackMode(); err == nil || !strings.Contains(err.Error(), "NICO_TIMELINE_ASSET_READBACK") {
			t.Fatalf("unknown mode error=%v", err)
		}
	})
}

func TestTimelineCaptureRejectsUnsupportedSpriteCompressionBeforeBrowser(t *testing.T) {
	snapshot, err := niconico.NormalizeSnapshot(niconico.Snapshot{
		VideoID: "sm9",
		Threads: []niconico.Thread{{Fork: "main", Comments: []niconico.Comment{{ID: "c1", VposMs: 1000, Body: "comment"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	options := RenderOptions{
		Width: 640, Height: 360, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		BrowserPath: "browser-must-not-start", SpriteCompression: "unsupported",
	}
	_, _, err = CaptureCommentTimeline(context.Background(), snapshot, options)
	if err == nil || !strings.Contains(err.Error(), "unsupported sprite compression") {
		t.Fatalf("capture error=%v, want unsupported sprite compression before browser startup", err)
	}
}

func TestTimelineCaptureClipsDrawsToBrowserStageVisibility(t *testing.T) {
	texture := timelineTestTexture(1, []byte{7, 8, 9, 255})
	meta := timelineCommentMeta{
		Index: 4, Loc: "naka", StartVPos: 0, EndVPos: 50,
		Width: 100, Height: 20,
	}
	samples := []timelineCaptureSample{
		// At vpos 10 the comment origin is just beyond the right edge. The
		// browser culls it even though the sprite's transparent padding overlaps
		// the canvas. It becomes visible at vpos 11.
		timelineTestSample(10, 640.5, []spriteJSONCommand{timelineTestCommand(1, 636.5)}),
		timelineTestSample(11, 636.5, []spriteJSONCommand{timelineTestCommand(1, 632.5)}),
		timelineTestSample(12, 632.5, []spriteJSONCommand{timelineTestCommand(1, 628.5)}),
	}
	scene, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta: meta, Samples: samples,
	}}, map[uint32]timelineCaptureTexture{1: texture})
	if err != nil {
		t.Fatal(err)
	}
	if len(scene.Draws) != 1 {
		t.Fatalf("draw count=%d, want 1", len(scene.Draws))
	}
	if got := scene.Draws[0]; got.StartVPos != 11 || got.EndVPos != 50 {
		t.Fatalf("visible interval=[%d,%d), want [11,50)", got.StartVPos, got.EndVPos)
	}
}

func TestTimelineCaptureDropsCommentsOutsideTheStageForTheirWholeInterval(t *testing.T) {
	texture := timelineTestTexture(1, []byte{7, 8, 9, 255})
	scene, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta: timelineCommentMeta{
			Index: 4, Loc: "ue", StartVPos: 0, EndVPos: 50,
			Width: 100, Height: 20,
		},
		Samples: []timelineCaptureSample{
			timelineTestSample(10, 700, []spriteJSONCommand{timelineTestCommand(1, 696)}),
		},
	}}, map[uint32]timelineCaptureTexture{1: texture})
	if err != nil {
		t.Fatal(err)
	}
	if len(scene.Draws) != 0 || len(scene.Assets) != 0 {
		t.Fatalf("off-stage comment retained %d draws and %d assets", len(scene.Draws), len(scene.Assets))
	}
}

func TestTimelineCaptureAssetDedupePreservesPrimitiveOrder(t *testing.T) {
	red := timelineTestTexture(1, []byte{255, 0, 0, 255})
	blue := timelineTestTexture(2, []byte{0, 0, 255, 255})
	redAgain := timelineTestTexture(3, []byte{255, 0, 0, 255})
	sample := timelineTestSample(50, 20, []spriteJSONCommand{
		timelineTestCommand(1, 20), timelineTestCommand(2, 24), timelineTestCommand(3, 28),
	})
	scene, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta:    timelineCommentMeta{Index: 0, Loc: "ue", StartVPos: 0, EndVPos: 100, Width: 10, Height: 10},
		Samples: []timelineCaptureSample{sample},
	}}, map[uint32]timelineCaptureTexture{1: red, 2: blue, 3: redAgain})
	if err != nil {
		t.Fatal(err)
	}
	if len(scene.Assets) != 2 || len(scene.Draws) != 3 {
		t.Fatalf("assets/draws=%d/%d, want 2/3", len(scene.Assets), len(scene.Draws))
	}
	got := []uint32{scene.Draws[0].AssetID, scene.Draws[1].AssetID, scene.Draws[2].AssetID}
	if got[0] != got[2] || got[0] == got[1] {
		t.Fatalf("A→B→A order/dedupe assets=%v", got)
	}
	for i, draw := range scene.Draws {
		if draw.PrimitiveIndex != uint32(i) {
			t.Errorf("primitive[%d]=%d", i, draw.PrimitiveIndex)
		}
	}
}

func TestTimelineCaptureAssetReusesDecodedPixelBuffer(t *testing.T) {
	pixels := []byte{7, 8, 9, 255}
	texture := timelineTestTexture(1, pixels)
	scene, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta:    timelineCommentMeta{Index: 0, Loc: "ue", StartVPos: 0, EndVPos: 100, Width: 10, Height: 10},
		Samples: []timelineCaptureSample{timelineTestSample(10, 20, []spriteJSONCommand{timelineTestCommand(1, 20)})},
	}}, map[uint32]timelineCaptureTexture{1: texture})
	if err != nil {
		t.Fatal(err)
	}
	if len(scene.Assets) != 1 || len(scene.Assets[0].RGBA) == 0 {
		t.Fatalf("assets=%d, want one non-empty asset", len(scene.Assets))
	}
	if &scene.Assets[0].RGBA[0] != &pixels[0] {
		t.Fatal("assembled asset copied the decoded RGBA pixel buffer")
	}
}

func TestTimelineCaptureTextureDecodeReleasesEncodedPayload(t *testing.T) {
	sample := timelineCaptureSample{Textures: []spriteJSONTexture{{
		ID: 1, Width: 1, Height: 1, Encoding: "rgba", Data: "AQIDBA==",
	}}}
	textures := make(map[uint32]timelineCaptureTexture)
	var lastTextureID uint32
	var rawAssetBytes int64
	var browserAssetPayloadBytes int64
	if err := decodeAndReleaseTimelineSampleTextures(&sample, textures, &lastTextureID, &rawAssetBytes, &browserAssetPayloadBytes); err != nil {
		t.Fatal(err)
	}
	if sample.Textures != nil {
		t.Fatal("encoded browser texture payload remains attached after decode")
	}
	texture, ok := textures[1]
	if !ok || string(texture.RGBA) != string([]byte{1, 2, 3, 4}) || lastTextureID != 1 || rawAssetBytes != 4 || browserAssetPayloadBytes != 4 {
		t.Fatalf("decoded texture=%+v lastID=%d rawBytes=%d browserPayloadBytes=%d", texture, lastTextureID, rawAssetBytes, browserAssetPayloadBytes)
	}
}

func TestTimelineCaptureAffineMotionKeepsTileOffsets(t *testing.T) {
	pixels := []byte{7, 8, 9, 255}
	texture := timelineTestTexture(1, pixels)
	samples := []timelineCaptureSample{
		timelineTestSample(10, 100, []spriteJSONCommand{timelineTestCommand(1, 104)}),
		timelineTestSample(11, 97, []spriteJSONCommand{timelineTestCommand(1, 101)}),
	}
	scene, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta: timelineCommentMeta{Index: 4, Loc: "naka", StartVPos: 0, EndVPos: 50, Width: 10, Height: 10}, Samples: samples,
	}}, map[uint32]timelineCaptureTexture{1: texture})
	if err != nil {
		t.Fatal(err)
	}
	if len(scene.Draws) != 1 || scene.Draws[0].AnchorVPos != 10 || scene.Draws[0].AnchorX != 100 || scene.Draws[0].SpeedX != -3 {
		t.Fatalf("draw motion = %+v", scene.Draws)
	}
	if scene.Draws[0].Rect[0] != 104 {
		t.Fatalf("tile-local X offset lost: rect=%v", scene.Draws[0].Rect)
	}
}

func TestTimelineCaptureAnalyticSpeedMatchesPositionSamples(t *testing.T) {
	texture := timelineTestTexture(1, []byte{7, 8, 9, 255})
	meta := timelineCommentMeta{Index: 4, Loc: "naka", StartVPos: 0, EndVPos: 50, Width: 10, Height: 10}
	positionSamples := []timelineCaptureSample{
		timelineTestSample(10, 100, []spriteJSONCommand{timelineTestCommand(1, 104)}),
		timelineTestSample(11, 97, []spriteJSONCommand{timelineTestCommand(1, 101)}),
	}
	positionScene, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta: meta, Samples: positionSamples,
	}}, map[uint32]timelineCaptureTexture{1: texture})
	if err != nil {
		t.Fatal(err)
	}

	declaredSpeed := -3.0
	meta.SpeedX = &declaredSpeed
	analyticScene, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta: meta, Samples: positionSamples[:1],
	}}, map[uint32]timelineCaptureTexture{1: texture})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(analyticScene, positionScene) {
		t.Fatalf("single-sample scene differs from position-derived scene:\nanalytic=%+v\nposition=%+v", analyticScene, positionScene)
	}
}

func TestTimelineCaptureRejectsUnknownPrimitiveWithTypedError(t *testing.T) {
	texture := timelineTestTexture(1, []byte{1, 2, 3, 255})
	_, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta:    timelineCommentMeta{Index: 0, Loc: "ue", StartVPos: 0, EndVPos: 10, Width: 10, Height: 10},
		Samples: []timelineCaptureSample{timelineTestSample(2, 10, []spriteJSONCommand{{ID: 0}})},
	}}, map[uint32]timelineCaptureTexture{1: texture})
	if !errors.Is(err, ErrTimelineUnsupported) {
		t.Fatalf("got %v, want ErrTimelineUnsupported", err)
	}
}

func TestTimelineCaptureEnforcesTextureBoundsBeforeDecode(t *testing.T) {
	if _, err := preflightTimelineSpriteTextures([]spriteJSONTexture{{Width: 16_384, Height: 16_384}}); err == nil {
		t.Fatal("oversized RGBA allocation accepted")
	}
	tooMany := make([]spriteJSONTexture, timelineCapturePerElementIDs+1)
	if _, err := preflightTimelineSpriteTextures(tooMany); err == nil {
		t.Fatal("oversized texture batch accepted")
	}
	if _, err := preflightTimelineSpriteTextures([]spriteJSONTexture{{Width: 1, Height: 1, Data: strings.Repeat("A", 5000)}}); err == nil {
		t.Fatal("oversized encoded payload accepted")
	}
}

func TestTimelineCaptureUsesOneAnchorSample(t *testing.T) {
	for _, tc := range []struct{ start, end int32 }{{0, 1}, {0, 2}, {0, 100}, {-4, 9}} {
		probes := timelineProbeVPos(tc.start, tc.end)
		if len(probes) != 1 {
			t.Fatalf("interval [%d,%d) probes=%v", tc.start, tc.end, probes)
		}
		seen := make(map[int32]bool, len(probes))
		for _, probe := range probes {
			if probe < tc.start || probe >= tc.end || seen[probe] {
				t.Fatalf("invalid probes for [%d,%d): %v", tc.start, tc.end, probes)
			}
			seen[probe] = true
		}
	}
}

func TestTimelineCaptureRejectsNonAffineMotion(t *testing.T) {
	texture := timelineTestTexture(1, []byte{1, 2, 3, 255})
	samples := []timelineCaptureSample{
		timelineTestSample(10, 100, []spriteJSONCommand{timelineTestCommand(1, 100)}),
		timelineTestSample(11, 97, []spriteJSONCommand{timelineTestCommand(1, 97)}),
		timelineTestSample(12, 91, []spriteJSONCommand{timelineTestCommand(1, 91)}),
	}
	_, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{{
		Meta: timelineCommentMeta{Index: 0, Loc: "naka", StartVPos: 0, EndVPos: 40, Width: 10, Height: 10}, Samples: samples,
	}}, map[uint32]timelineCaptureTexture{1: texture})
	if !errors.Is(err, ErrTimelineUnsupported) {
		t.Fatalf("got %v, want affine-motion ErrTimelineUnsupported", err)
	}
}

func timelineTestHeader() TimelineHeader {
	return TimelineHeader{Width: 640, Height: 360, FrameCount: 180, FPSNum: 30, FPSDen: 1}
}

func timelineTestTexture(id uint32, pixels []byte) timelineCaptureTexture {
	return timelineCaptureTexture{Width: 1, Height: 1, RGBA: pixels, SHA256: sha256.Sum256(pixels)}
}

func timelineTestSample(vpos int32, x float64, commands []spriteJSONCommand) timelineCaptureSample {
	return timelineCaptureSample{VPos: vpos, X: x, Y: 10, Commands: commands}
}

func timelineTestCommand(id uint32, x float32) spriteJSONCommand {
	var projection [16]float32
	projection[0], projection[5], projection[10], projection[15] = 1, 1, 1, 1
	return spriteJSONCommand{ID: id, Rect: [4]float32{x, 10, 2, 2}, Proj: projection, Color: [4]float32{1, 0, 0, 0}}
}
