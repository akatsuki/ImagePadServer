package nicorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

const (
	timelineCaptureParityOptIn       = "NICO_TIMELINE_CAPTURE_PARITY"
	timelineCaptureParitySnapshot    = "imagepad-nico-perf-20260916/snapshot.json"
	timelineCaptureParitySnapshotSHA = "b7be86d86016175c99925f5f1b5651d43279eeffcc54deb1c22c5b2e6e15e946"
	timelineCaptureParitySceneSHA    = "9e0e0c88f63a1dffc54256e6248d2b64a0561679bb74a260d575ab33bc592373"
	timelineCaptureParitySeed        = uint32(0x4e49434f)
)

func TestTimelineCapturePreservedSnapshotNCT1Parity(t *testing.T) {
	if os.Getenv(timelineCaptureParityOptIn) != "1" {
		t.Skipf("set %s=1 to capture the pinned snapshot with the current browser", timelineCaptureParityOptIn)
	}
	browserPath := os.Getenv("NICO_TIMELINE_BROWSER")
	if browserPath == "" {
		t.Fatal("NICO_TIMELINE_BROWSER must point to the Chrome executable")
	}
	snapshotPath := filepath.Join(os.TempDir(), filepath.FromSlash(timelineCaptureParitySnapshot))
	snapshotBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read preserved snapshot: %v", err)
	}
	snapshotDigest := sha256.Sum256(snapshotBytes)
	if got := hex.EncodeToString(snapshotDigest[:]); got != timelineCaptureParitySnapshotSHA {
		t.Fatalf("snapshot SHA-256=%s, want %s", got, timelineCaptureParitySnapshotSHA)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotBytes, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	snapshot, err = niconico.NormalizeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("normalize snapshot: %v", err)
	}
	options := RenderOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		Transport: "binary", BrowserPath: browserPath,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	scene, _, err := captureCommentTimelineSeededForTest(ctx, snapshot, options, timelineCaptureParitySeed)
	if err != nil {
		t.Fatalf("capture pinned snapshot: %v", err)
	}
	var encoded bytes.Buffer
	if err := WriteCommentTimeline(&encoded, scene); err != nil {
		t.Fatalf("serialize NCT1 in memory: %v", err)
	}
	digest := sha256.Sum256(encoded.Bytes())
	if got := hex.EncodeToString(digest[:]); got != timelineCaptureParitySceneSHA {
		t.Fatalf("NCT1 SHA-256=%s (%d bytes), want %s", got, encoded.Len(), timelineCaptureParitySceneSHA)
	}
	if encoded.Len() != 1_903_788 {
		t.Fatalf("NCT1 size=%d, want 1903788", encoded.Len())
	}
	t.Logf("NCT1 preserved-snapshot bytes=%d SHA-256=%s", encoded.Len(), hex.EncodeToString(digest[:]))
}

func TestTimelineCaptureStreamSceneAssemblerReturnsElementDrawsAndFirstUseAssets(t *testing.T) {
	red := timelineTestTexture(1, []byte{255, 0, 0, 255})
	blue := timelineTestTexture(2, []byte{0, 0, 255, 255})
	redAgain := timelineTestTexture(3, []byte{255, 0, 0, 255})
	textures := map[uint32]timelineCaptureTexture{1: red, 2: blue, 3: redAgain}
	elements := []timelineCapturedElement{
		{
			Meta: timelineCommentMeta{OwnerOrder: 0, Index: 0, Loc: "ue", StartVPos: 0, EndVPos: 100, Width: 10, Height: 10},
			Samples: []timelineCaptureSample{timelineTestSample(20, 10, []spriteJSONCommand{
				timelineTestCommand(1, 10), timelineTestCommand(2, 12),
			})},
		},
		{
			Meta: timelineCommentMeta{OwnerOrder: 0, Index: 1, Loc: "ue", StartVPos: 0, EndVPos: 100, Width: 10, Height: 10},
			Samples: []timelineCaptureSample{timelineTestSample(30, 20, []spriteJSONCommand{
				timelineTestCommand(3, 20),
			})},
		},
	}

	assembler := newTimelineCapturedSceneAssembler(timelineTestHeader())
	var streamDraws []TimelineDraw
	newAssetsByElement := make([][]TimelineAsset, 0, len(elements))
	for _, element := range elements {
		draws, assets, err := assembler.addElement(element, textures)
		if err != nil {
			t.Fatalf("add element %d: %v", element.Meta.Index, err)
		}
		streamDraws = append(streamDraws, draws...)
		newAssetsByElement = append(newAssetsByElement, assets)
	}
	if got := []int{len(newAssetsByElement[0]), len(newAssetsByElement[1])}; !reflect.DeepEqual(got, []int{2, 0}) {
		t.Fatalf("new assets per element=%v, want [2 0]", got)
	}
	if got := []uint32{
		newAssetsByElement[0][0].ID,
		newAssetsByElement[0][1].ID,
		streamDraws[0].AssetID,
		streamDraws[1].AssetID,
		streamDraws[2].AssetID,
	}; !reflect.DeepEqual(got, []uint32{1, 2, 1, 2, 1}) {
		t.Fatalf("first-use IDs/draw order=%v, want [1 2 1 2 1]", got)
	}
	assets, err := assembler.finish()
	if err != nil {
		t.Fatalf("finish stream assembler: %v", err)
	}
	if got := []uint32{assets[0].ID, assets[1].ID}; !reflect.DeepEqual(got, []uint32{1, 2}) {
		t.Fatalf("final assets sorted by ID=%v, want [1 2]", got)
	}

	streamScene := CommentTimeline{Header: timelineTestHeader(), Draws: streamDraws, Assets: assets}
	if err := ValidateCommentTimeline(streamScene); err != nil {
		t.Fatalf("validate reconstructed per-element scene: %v", err)
	}
	collectScene, err := assembleTimelineCapturedScene(timelineTestHeader(), []timelineCapturedElement{elements[1], elements[0]}, textures)
	if err != nil {
		t.Fatalf("collect scene from reversed elements: %v", err)
	}
	if !reflect.DeepEqual(streamScene, collectScene) {
		t.Fatalf("per-element scene differs from sorted collect scene:\nstream=%+v\ncollect=%+v", streamScene, collectScene)
	}
}

func TestTimelineCaptureStreamSceneAssemblerChecksPixelsOnFingerprintCollision(t *testing.T) {
	red := timelineTestTexture(1, []byte{255, 0, 0, 255})
	blue := timelineTestTexture(2, []byte{0, 0, 255, 255})
	blue.SHA256 = red.SHA256
	element := timelineCapturedElement{
		Meta: timelineCommentMeta{OwnerOrder: 0, Index: 0, Loc: "ue", StartVPos: 0, EndVPos: 100, Width: 10, Height: 10},
		Samples: []timelineCaptureSample{timelineTestSample(20, 10, []spriteJSONCommand{
			timelineTestCommand(1, 10), timelineTestCommand(2, 12),
		})},
	}
	assembler := newTimelineCapturedSceneAssembler(timelineTestHeader())
	draws, assets, err := assembler.addElement(element, map[uint32]timelineCaptureTexture{1: red, 2: blue})
	if err != nil {
		t.Fatalf("add colliding-fingerprint element: %v", err)
	}
	if len(draws) != 2 || len(assets) != 2 || draws[0].AssetID == draws[1].AssetID {
		t.Fatalf("collision draws/assets=%+v/%+v, want separate pixel assets", draws, assets)
	}
	if !bytes.Equal(assets[0].RGBA, red.RGBA) || !bytes.Equal(assets[1].RGBA, blue.RGBA) {
		t.Fatalf("collision asset pixels were merged or reordered: %+v", assets)
	}
}
