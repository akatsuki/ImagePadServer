package nicorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

func TestCaptureCommentTimelineStreamUsesRenderOptionsRandomSeed(t *testing.T) {
	t.Setenv("NICO_TIMELINE_ASSET_READBACK", "sync")
	options := timelinePoolTestOptions()
	seed := uint32(0x54423331)
	options.TimelineRandomSeed = &seed
	snapshot := timelineCaptureStreamTestSnapshot()
	var stream bytes.Buffer
	baseStarter, _ := timelineCaptureStreamTestStarter(t, &stream, nil, nil)
	var capturedPage string
	starter := func(ctx context.Context, configured, pagePath string) (*browserSession, error) {
		page, err := os.ReadFile(pagePath)
		if err != nil {
			return nil, err
		}
		capturedPage = string(page)
		return baseStarter(ctx, configured, pagePath)
	}
	_, err := captureCommentTimelineStreamWithCaptureLimitsAndStarter(
		context.Background(), snapshot, options, &stream, nil,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, starter,
	)
	if err != nil {
		t.Fatalf("capture seeded timeline stream: %v", err)
	}
	if !strings.Contains(capturedPage, fmt.Sprintf("let state=%d>>>0", seed)) {
		t.Fatal("renderer page does not initialize the requested deterministic random seed")
	}
}

func TestCaptureCommentTimelineStreamWritesDeclarationsBeforeCaptureAndMatchesCollectScene(t *testing.T) {
	t.Setenv("NICO_TIMELINE_ASSET_READBACK", "sync")
	options := timelinePoolTestOptions()
	snapshot := timelineCaptureStreamTestSnapshot()
	seed := uint32(0x4e49434f)
	output := &closeTrackingWriter{}
	var captureObservedDeclarations bool
	starter, closeCount := timelineCaptureStreamTestStarter(t, output, nil, func() {
		captureObservedDeclarations = true
	})
	streamReport, err := captureCommentTimelineStreamWithCaptureLimitsAndStarter(
		context.Background(), snapshot, options, output, &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, starter,
	)
	if err != nil {
		t.Fatalf("stream capture: %v", err)
	}
	if !captureObservedDeclarations {
		t.Fatal("fake browser did not reach element capture")
	}
	if *closeCount != 1 {
		t.Fatalf("browser cleanup count=%d, want 1", *closeCount)
	}
	if output.closeCount != 0 {
		t.Fatalf("caller writer was closed %d times", output.closeCount)
	}

	collectStarter, collectCloseCount := timelineCaptureStreamTestStarter(t, nil, nil, nil)
	collectScene, collectReport, err := captureCommentTimelineWithCaptureLimitsAndStarter(
		context.Background(), snapshot, options, &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, collectStarter,
	)
	if err != nil {
		t.Fatalf("collect capture: %v", err)
	}
	if *collectCloseCount != 1 {
		t.Fatalf("collect browser cleanup count=%d, want 1", *collectCloseCount)
	}
	if streamReport.TimelineDraws != len(collectScene.Draws) || streamReport.TimelineAssets != len(collectScene.Assets) || streamReport.AssetBytes != collectReport.AssetBytes {
		t.Fatalf("stream report scene counts differ from collect: stream=%+v collect=%+v", streamReport, collectReport)
	}

	declarations := []TimelineStreamDeclaration{
		{Ordinal: 0, OwnerOrder: 0, CommentIndex: 0, StartVPos: 0, EndVPos: 97},
		{Ordinal: 1, OwnerOrder: 0, CommentIndex: 1, StartVPos: 100, EndVPos: 100},
	}
	stream := CommentTimelineStream{
		Header: collectScene.Header, Declarations: declarations, Assets: collectScene.Assets,
		Elements: []TimelineStreamElement{
			{Ordinal: 0, Draws: collectScene.Draws},
			{Ordinal: 1}, // Inactive declaration still completes explicitly.
		},
	}
	var want bytes.Buffer
	if err := WriteCommentTimelineStream(&want, stream); err != nil {
		t.Fatalf("write whole-scene stream reference: %v", err)
	}
	if !bytes.Equal(output.Bytes(), want.Bytes()) {
		t.Fatalf("incremental capture stream differs from collected scene reference: got %d, want %d bytes", output.Len(), want.Len())
	}
	completionCount := 0
	for _, record := range nct2TestRecords(t, output.Bytes()) {
		if record.kind == TimelineStreamRecordElementComplete {
			completionCount++
		}
	}
	if completionCount != len(declarations) {
		t.Fatalf("ElementComplete count=%d, want %d declarations including inactive", completionCount, len(declarations))
	}
}

func TestCaptureCommentTimelineStreamRetainsEarlierTextureIDWithoutRetransfer(t *testing.T) {
	t.Setenv("NICO_TIMELINE_ASSET_READBACK", "sync")
	options := timelinePoolTestOptions()
	snapshot := timelineCaptureStreamTestSnapshot()
	snapshot.Threads[0].Comments = append(snapshot.Threads[0].Comments, niconico.Comment{
		ID: "second-active-comment", VposMs: 100, Body: "also visible",
	})
	snapshot.CommentCount = 3
	seed := uint32(0x4e49434f)
	output := &closeTrackingWriter{}
	streamStarter, streamCloseCount := timelineCaptureStreamTestStarter(t, output, nil, nil, timelineCaptureStreamTestReusePreviousTexture)
	_, err := captureCommentTimelineStreamWithCaptureLimitsAndStarter(
		context.Background(), snapshot, options, output, &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, streamStarter,
	)
	if err != nil {
		t.Fatalf("stream capture reusing prior texture ID: %v", err)
	}
	if *streamCloseCount != 1 {
		t.Fatalf("stream browser cleanup count=%d, want 1", *streamCloseCount)
	}

	collectStarter, collectCloseCount := timelineCaptureStreamTestStarter(t, nil, nil, nil, timelineCaptureStreamTestReusePreviousTexture)
	collectScene, _, err := captureCommentTimelineWithCaptureLimitsAndStarter(
		context.Background(), snapshot, options, &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, collectStarter,
	)
	if err != nil {
		t.Fatalf("collect capture reusing prior texture ID: %v", err)
	}
	if *collectCloseCount != 1 {
		t.Fatalf("collect browser cleanup count=%d, want 1", *collectCloseCount)
	}
	if len(collectScene.Draws) != 2 || len(collectScene.Assets) != 1 || collectScene.Draws[0].AssetID != collectScene.Draws[1].AssetID {
		t.Fatalf("collect reuse fixture did not deduplicate the earlier texture: draws=%+v assets=%d", collectScene.Draws, len(collectScene.Assets))
	}

	stream := CommentTimelineStream{
		Header: collectScene.Header,
		Declarations: []TimelineStreamDeclaration{
			{Ordinal: 0, OwnerOrder: 0, CommentIndex: 0, StartVPos: 0, EndVPos: 40},
			{Ordinal: 1, OwnerOrder: 0, CommentIndex: 1, StartVPos: 40, EndVPos: 97},
			{Ordinal: 2, OwnerOrder: 0, CommentIndex: 2, StartVPos: 100, EndVPos: 100},
		},
		Assets: collectScene.Assets,
		Elements: []TimelineStreamElement{
			{Ordinal: 0, Draws: collectScene.Draws[:1]},
			{Ordinal: 1, Draws: collectScene.Draws[1:]},
			{Ordinal: 2},
		},
	}
	var want bytes.Buffer
	if err := WriteCommentTimelineStream(&want, stream); err != nil {
		t.Fatalf("write whole-scene texture reuse reference: %v", err)
	}
	if !bytes.Equal(output.Bytes(), want.Bytes()) {
		t.Fatalf("texture reuse stream differs from collect-scene reference: got %d, want %d bytes", output.Len(), want.Len())
	}
	assetRecords := 0
	completionRecords := 0
	for _, record := range nct2TestRecords(t, output.Bytes()) {
		switch record.kind {
		case TimelineStreamRecordAssetBegin:
			assetRecords++
		case TimelineStreamRecordElementComplete:
			completionRecords++
		}
	}
	if assetRecords != 1 {
		t.Fatalf("asset transfer count=%d, want one first-use transfer", assetRecords)
	}
	if completionRecords != 3 {
		t.Fatalf("ElementComplete count=%d, want two active and one inactive declaration", completionRecords)
	}
}

func TestCaptureCommentTimelineStreamFreezesStageClippedInterval(t *testing.T) {
	t.Setenv("NICO_TIMELINE_ASSET_READBACK", "sync")
	options := timelinePoolTestOptions()
	options.Width, options.Height = 640, 360
	snapshot := timelineCaptureStreamTestSnapshot()
	seed := uint32(0x4e49434f)
	output := &closeTrackingWriter{}
	streamStarter, streamCloseCount := timelineCaptureStreamTestStarter(t, output, nil, nil, timelineCaptureStreamTestScreenEdge)
	if _, err := captureCommentTimelineStreamWithCaptureLimitsAndStarter(
		context.Background(), snapshot, options, output, &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, streamStarter,
	); err != nil {
		t.Fatalf("stream capture stage-clipped comment: %v", err)
	}
	if *streamCloseCount != 1 {
		t.Fatalf("stream browser cleanup count=%d, want 1", *streamCloseCount)
	}

	collectStarter, collectCloseCount := timelineCaptureStreamTestStarter(t, nil, nil, nil, timelineCaptureStreamTestScreenEdge)
	collectScene, _, err := captureCommentTimelineWithCaptureLimitsAndStarter(
		context.Background(), snapshot, options, &seed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, collectStarter,
	)
	if err != nil {
		t.Fatalf("collect capture stage-clipped comment: %v", err)
	}
	if *collectCloseCount != 1 {
		t.Fatalf("collect browser cleanup count=%d, want 1", *collectCloseCount)
	}
	if len(collectScene.Draws) != 1 || collectScene.Draws[0].StartVPos != 11 || collectScene.Draws[0].EndVPos != 50 {
		t.Fatalf("collect Draw interval=%+v, want one Draw clipped to [11,50)", collectScene.Draws)
	}

	stream := CommentTimelineStream{
		Header: collectScene.Header,
		Declarations: []TimelineStreamDeclaration{
			{Ordinal: 0, OwnerOrder: 0, CommentIndex: 0, StartVPos: 11, EndVPos: 50},
			{Ordinal: 1, OwnerOrder: 0, CommentIndex: 1, StartVPos: 100, EndVPos: 100},
		},
		Assets: collectScene.Assets,
		Elements: []TimelineStreamElement{
			{Ordinal: 0, Draws: collectScene.Draws},
			{Ordinal: 1}, // Inactive declaration still completes explicitly.
		},
	}
	var want bytes.Buffer
	if err := WriteCommentTimelineStream(&want, stream); err != nil {
		t.Fatalf("write stage-clipped whole-scene reference: %v", err)
	}
	if !bytes.Equal(output.Bytes(), want.Bytes()) {
		t.Fatalf("stage-clipped stream differs from collect-scene reference: got %d, want %d bytes", output.Len(), want.Len())
	}
}

func TestCaptureCommentTimelineStreamPreservedSnapshotChromeParity(t *testing.T) {
	const (
		optIn       = "NICO_TIMELINE_STREAM_PARITY"
		snapshotRel = "imagepad-nico-perf-20260916/snapshot.json"
		snapshotSHA = "b7be86d86016175c99925f5f1b5651d43279eeffcc54deb1c22c5b2e6e15e946"
		wantNCT1SHA = "9e0e0c88f63a1dffc54256e6248d2b64a0561679bb74a260d575ab33bc592373"
		seedValue   = uint32(0x4e49434f)
	)
	if os.Getenv(optIn) != "1" {
		t.Skipf("set %s=1 to run current-Chrome preserved-snapshot NCT1/NCT2 parity", optIn)
	}
	browserPath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER"))
	if browserPath == "" {
		t.Fatal("NICO_TIMELINE_BROWSER must point to the Chrome executable")
	}
	snapshotPath := filepath.Join(os.TempDir(), filepath.FromSlash(snapshotRel))
	snapshotBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read preserved snapshot: %v", err)
	}
	snapshotDigest := sha256.Sum256(snapshotBytes)
	if got := fmt.Sprintf("%x", snapshotDigest); got != snapshotSHA {
		t.Fatalf("preserved snapshot SHA-256=%s, want %s", got, snapshotSHA)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotBytes, &snapshot); err != nil {
		t.Fatalf("decode preserved snapshot: %v", err)
	}
	snapshot, err = niconico.NormalizeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("normalize preserved snapshot: %v", err)
	}
	options := RenderOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		Transport: "binary", BrowserPath: browserPath,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	seed := seedValue
	scene, collectReport, err := captureCommentTimelineWithCaptureLimitsAndStarter(
		ctx, snapshot, options, &seed, timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, nil,
	)
	if err != nil {
		t.Fatalf("capture preserved snapshot for NCT1 parity: %v", err)
	}
	var nct1 bytes.Buffer
	if err := WriteCommentTimeline(&nct1, scene); err != nil {
		t.Fatalf("serialize NCT1 in memory: %v", err)
	}
	nct1Digest := sha256.Sum256(nct1.Bytes())
	if got := fmt.Sprintf("%x", nct1Digest); got != wantNCT1SHA || nct1.Len() != 1_903_788 {
		t.Fatalf("preserved-snapshot NCT1 bytes=%d SHA-256=%s, want 1903788 bytes SHA-256=%s", nct1.Len(), fmt.Sprintf("%x", nct1Digest), wantNCT1SHA)
	}

	exclusiveEnd, err := timelineStreamFrameClock(uint32(collectReport.FrameCount), uint32(options.FPSNum), uint32(options.FPSDen))
	if err != nil {
		t.Fatalf("compute exclusive frame-clock end E: %v", err)
	}
	groups := make(map[[2]uint32][]TimelineDraw)
	intervals := make(map[[2]uint32]map[[2]int64]int)
	for _, draw := range scene.Draws {
		key := [2]uint32{draw.OwnerOrder, draw.CommentIndex}
		groups[key] = append(groups[key], draw)
		if intervals[key] == nil {
			intervals[key] = make(map[[2]int64]int)
		}
		interval := [2]int64{clipTimelineVPos(draw.StartVPos, exclusiveEnd), clipTimelineVPos(draw.EndVPos, exclusiveEnd)}
		intervals[key][interval]++
	}
	if collectReport.EligibleComments != len(groups) {
		t.Fatalf("NCT2 parity stopped before stream capture: eligible_comments=%d distinct_draw_groups=%d draws=%d; missing/zero-Draw declarations are not inferred", collectReport.EligibleComments, len(groups), len(scene.Draws))
	}
	keys := make([][2]uint32, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
		if len(intervals[key]) != 1 {
			t.Fatalf("NCT2 parity stopped before stream capture: owner=%d comment=%d clipped_intervals=%v", key[0], key[1], intervals[key])
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	whole := CommentTimelineStream{Header: scene.Header, Assets: scene.Assets}
	for ordinal, key := range keys {
		var interval [2]int64
		for value := range intervals[key] {
			interval = value
		}
		whole.Declarations = append(whole.Declarations, TimelineStreamDeclaration{
			Ordinal: uint32(ordinal), OwnerOrder: key[0], CommentIndex: key[1], StartVPos: int32(interval[0]), EndVPos: int32(interval[1]),
		})
		whole.Elements = append(whole.Elements, TimelineStreamElement{Ordinal: uint32(ordinal), Draws: groups[key]})
	}
	var expected bytes.Buffer
	if err := WriteCommentTimelineStream(&expected, whole); err != nil {
		t.Fatalf("write collected-scene NCT2 reference: %v", err)
	}
	var streamed bytes.Buffer
	streamSeed := seedValue
	var rawDescribeComments []json.RawMessage
	var diagnosticStarter freshBrowserStarter = func(browserCtx context.Context, configured, pagePath string) (*browserSession, error) {
		session, err := startBrowser(browserCtx, configured, pagePath)
		if err != nil {
			return nil, err
		}
		var wrapped func(context.Context, string, map[string]any) (json.RawMessage, error)
		wrapped = func(callCtx context.Context, method string, params map[string]any) (json.RawMessage, error) {
			session.pooledCall = nil
			raw, callErr := session.call(callCtx, method, params)
			session.pooledCall = wrapped
			if callErr == nil && method == "Runtime.evaluate" {
				expression, _ := params["expression"].(string)
				if strings.Contains(expression, "window.__nicoTimelineDescribe") {
					var envelope struct {
						Result struct {
							Value json.RawMessage `json:"value"`
						} `json:"result"`
					}
					if json.Unmarshal(raw, &envelope) == nil {
						var description struct {
							Comments []json.RawMessage `json:"comments"`
						}
						if json.Unmarshal(envelope.Result.Value, &description) == nil && len(description.Comments) > 0 {
							rawDescribeComments = append(rawDescribeComments[:0], description.Comments...)
						}
					}
				}
			}
			return raw, callErr
		}
		session.pooledCall = wrapped
		return session, nil
	}
	streamReport, err := captureCommentTimelineStreamWithCaptureLimitsAndStarter(
		ctx, snapshot, options, &streamed, &streamSeed,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, diagnosticStarter,
	)
	if err != nil {
		var intervalDiagnostic string
		for ordinal, key := range keys {
			if strings.HasSuffix(err.Error(), fmt.Sprintf("declaration %d", ordinal)) {
				var interval [2]int64
				for clipped := range intervals[key] {
					interval = clipped
				}
				var raw json.RawMessage
				if ordinal < len(rawDescribeComments) {
					raw = rawDescribeComments[ordinal]
				}
				intervalDiagnostic = fmt.Sprintf("ordinal=%d owner=%d comment=%d collect_clipped=[%d,%d) raw_description=%s", ordinal, key[0], key[1], interval[0], interval[1], raw)
				break
			}
		}
		t.Fatalf("capture preserved snapshot as NCT2: %v; %s", err, intervalDiagnostic)
	}
	if streamReport.EligibleComments != collectReport.EligibleComments || streamReport.TimelineDraws != len(scene.Draws) || streamReport.TimelineAssets != len(scene.Assets) {
		t.Fatalf("NCT2 capture report differs from collect: stream=%+v collect=%+v", streamReport, collectReport)
	}
	if !bytes.Equal(streamed.Bytes(), expected.Bytes()) {
		t.Fatalf("preserved-snapshot NCT2 bytes differ from collect-scene whole-stream reference: got=%d want=%d", streamed.Len(), expected.Len())
	}
	t.Logf("current-Chrome parity: eligible=%d draw_groups=%d draws=%d assets=%d NCT1_bytes=%d NCT1_sha256=%s NCT2_bytes=%d", collectReport.EligibleComments, len(groups), len(scene.Draws), len(scene.Assets), nct1.Len(), fmt.Sprintf("%x", nct1Digest), streamed.Len())
}

func TestCaptureCommentTimelineStreamAbortsBeforeEndOnCaptureOrWriterError(t *testing.T) {
	t.Setenv("NICO_TIMELINE_ASSET_READBACK", "sync")
	for _, tt := range []struct {
		name       string
		captureErr error
		failKind   uint32
	}{
		{name: "capture", captureErr: errors.New("fake browser capture failure")},
		{name: "writer", failKind: TimelineStreamRecordWatermark},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := timelinePoolTestOptions()
			output := &recordFailTimelineWriter{failKind: tt.failKind, err: errors.New("fake stream writer failure")}
			starter, closeCount := timelineCaptureStreamTestStarter(t, output, tt.captureErr, nil)
			_, err := captureCommentTimelineStreamWithCaptureLimitsAndStarter(
				context.Background(), timelineCaptureStreamTestSnapshot(), options, output, ptrUint32(0x4e49434f),
				timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, starter,
			)
			if err == nil {
				t.Fatal("stream capture unexpectedly succeeded")
			}
			if !strings.Contains(err.Error(), "capture failure") && !strings.Contains(err.Error(), "writer failure") {
				t.Fatalf("stream capture error=%v, want injected capture/writer failure", err)
			}
			if tt.name == "capture" && errors.Is(err, context.Canceled) {
				t.Fatalf("capture failure was joined with cleanup-only cancellation: %v", err)
			}
			if output.endAttempted {
				t.Fatal("stream capture attempted End after capture or element write failure")
			}
			if *closeCount != 1 {
				t.Fatalf("browser cleanup count=%d, want 1 after failure", *closeCount)
			}
		})
	}
}

func TestCaptureCommentTimelineStreamWritesEmptySceneWithoutBrowser(t *testing.T) {
	t.Setenv("NICO_TIMELINE_ASSET_READBACK", "sync")
	options := timelinePoolTestOptions()
	output := &closeTrackingWriter{}
	starterCalled := false
	starter := func(context.Context, string, string) (*browserSession, error) {
		starterCalled = true
		return nil, errors.New("browser should not start for an empty snapshot")
	}
	report, err := captureCommentTimelineStreamWithCaptureLimitsAndStarter(
		context.Background(), niconico.Snapshot{VideoID: "sm-empty"}, options, output, nil,
		timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, starter,
	)
	if err != nil {
		t.Fatalf("empty stream capture: %v", err)
	}
	if starterCalled {
		t.Fatal("empty snapshot started a browser")
	}
	if report.FrameCount != 30 || report.TimelineDraws != 0 || report.TimelineAssets != 0 {
		t.Fatalf("empty stream report=%+v", report)
	}
	header := TimelineHeader{
		Width: uint32(options.Width), Height: uint32(options.Height), FrameCount: uint32(report.FrameCount),
		FPSNum: uint32(options.FPSNum), FPSDen: uint32(options.FPSDen), BundleSHA256: sha256.Sum256(bundledRenderer),
	}
	var want bytes.Buffer
	if err := WriteCommentTimelineStream(&want, CommentTimelineStream{Header: header}); err != nil {
		t.Fatalf("write empty reference: %v", err)
	}
	if !bytes.Equal(output.Bytes(), want.Bytes()) {
		t.Fatalf("empty incremental stream differs from whole-scene stream: got %d, want %d bytes", output.Len(), want.Len())
	}
	if output.closeCount != 0 {
		t.Fatalf("caller writer was closed %d times", output.closeCount)
	}
}

func TestCaptureCommentTimelineStreamRejectsNilWriter(t *testing.T) {
	_, err := CaptureCommentTimelineStream(context.Background(), niconico.Snapshot{VideoID: "sm-empty"}, timelinePoolTestOptions(), nil)
	if err == nil || !strings.Contains(err.Error(), "writer") {
		t.Fatalf("nil writer error=%v, want a writer error", err)
	}
}

type closeTrackingWriter struct {
	bytes.Buffer
	closeCount int
}

func (writer *closeTrackingWriter) Close() error {
	writer.closeCount++
	return nil
}

func timelineCaptureStreamTestSnapshot() niconico.Snapshot {
	snapshot := timelinePoolTestSnapshot()
	snapshot.Threads[0].Comments = append(snapshot.Threads[0].Comments, niconico.Comment{
		ID: "inactive-comment", VposMs: 100_000, Body: "later",
	})
	snapshot.CommentCount = 2
	return snapshot
}

type timelineCaptureStreamTestMode uint8

const (
	timelineCaptureStreamTestReusePreviousTexture timelineCaptureStreamTestMode = iota + 1
	timelineCaptureStreamTestScreenEdge
)

func timelineCaptureStreamTestStarter(t *testing.T, output io.Writer, captureErr error, beforeCapture func(), mode ...timelineCaptureStreamTestMode) (freshBrowserStarter, *int) {
	t.Helper()
	closeCount := new(int)
	command := timelineTestCommand(1, 20)
	command.Rect[1] = 20
	texture := spriteJSONTexture{ID: 1, Width: 1, Height: 1, Encoding: "rgba", Data: "/wAA/w=="}
	description := timelineDescribeResult{
		OK: true,
		Comments: []timelineCommentMeta{
			{OwnerOrder: 0, Index: 0, Loc: "ue", StartVPos: 0, EndVPos: 97, ProbeVPos: 48, ProbeX: 20, ProbeY: 20, Width: 10, Height: 10},
			{OwnerOrder: 0, Index: 1, Loc: "ue", StartVPos: 10_000, EndVPos: 11_000, ProbeX: 20, ProbeY: 20, Width: 10, Height: 10},
		},
		ElementDrawCalls: 2,
	}
	batch := timelineCaptureBatchResult{Consumed: 1, Items: []timelineCaptureElementResult{{
		Index:            0,
		Samples:          []timelineCaptureSample{{VPos: 48, X: 20, Y: 20, Commands: []spriteJSONCommand{command}, Textures: []spriteJSONTexture{texture}}},
		ElementDrawCalls: 1, SpriteMetrics: &browserSpriteCaptureMetrics{Mode: "sync", TextureCreations: 1},
	}}}
	if len(mode) > 0 && mode[0] == timelineCaptureStreamTestReusePreviousTexture {
		description.Comments = []timelineCommentMeta{
			{OwnerOrder: 0, Index: 0, Loc: "ue", StartVPos: 0, EndVPos: 40, ProbeVPos: 19, ProbeX: 20, ProbeY: 20, Width: 10, Height: 10},
			{OwnerOrder: 0, Index: 1, Loc: "ue", StartVPos: 40, EndVPos: 97, ProbeVPos: 68, ProbeX: 20, ProbeY: 20, Width: 10, Height: 10},
			{OwnerOrder: 0, Index: 2, Loc: "ue", StartVPos: 10_000, EndVPos: 11_000, ProbeX: 20, ProbeY: 20, Width: 10, Height: 10},
		}
		description.ElementDrawCalls = 2
		firstElement := batch.Items[0]
		firstElement.Samples[0].VPos = 19
		batch = timelineCaptureBatchResult{Consumed: 2, Items: []timelineCaptureElementResult{
			firstElement,
			{
				Index:            1,
				Samples:          []timelineCaptureSample{{VPos: 68, X: 20, Y: 20, Commands: []spriteJSONCommand{command}}},
				ElementDrawCalls: 1, SpriteMetrics: &browserSpriteCaptureMetrics{Mode: "sync", TextureCreations: 1},
			},
		}}
	}
	if len(mode) > 0 && mode[0] == timelineCaptureStreamTestScreenEdge {
		speed := -4.0
		description.Comments = []timelineCommentMeta{
			{OwnerOrder: 0, Index: 0, Loc: "naka", StartVPos: 0, EndVPos: 50, ProbeVPos: 24, ProbeX: 584.5, ProbeY: 20, Width: 100, Height: 20, SpeedX: &speed},
			{OwnerOrder: 0, Index: 1, Loc: "ue", StartVPos: 10_000, EndVPos: 11_000, ProbeX: 20, ProbeY: 20, Width: 10, Height: 10},
		}
		batch = timelineCaptureBatchResult{Consumed: 1, Items: []timelineCaptureElementResult{{
			Index:            0,
			Samples:          []timelineCaptureSample{{VPos: 24, X: 584.5, Y: 20, Commands: []spriteJSONCommand{timelineTestCommand(1, 636.5)}, Textures: []spriteJSONTexture{texture}}},
			ElementDrawCalls: 1, SpriteMetrics: &browserSpriteCaptureMetrics{Mode: "sync", TextureCreations: 1},
		}}}
	}
	call := func(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if method != "Runtime.evaluate" {
			return nil, fmt.Errorf("unexpected CDP method %q", method)
		}
		expression, _ := params["expression"].(string)
		switch {
		case strings.Contains(expression, "ready:Boolean(window.__niconiReady)"):
			return timelineRuntimeValue(map[string]any{"ready": true, "error": ""}), nil
		case strings.Contains(expression, "window.__niconi.comments ? window.__niconi.comments.length"):
			return timelineRuntimeValue(len(description.Comments)), nil
		case strings.Contains(expression, "window.__nicoTimelineInstallError"):
			return timelineRuntimeValue(""), nil
		case strings.Contains(expression, "window.__nicoTimelineDescribe"):
			return timelineRuntimeValue(description), nil
		case strings.Contains(expression, "window.__nicoTimelineCaptureBatch") || strings.Contains(expression, "window.__nicoTimelineCaptureElement"):
			if beforeCapture != nil {
				beforeCapture()
			}
			if output != nil {
				buffer, ok := output.(interface{ Bytes() []byte })
				if !ok || !containsNCT2Record(buffer.Bytes(), TimelineStreamRecordDeclarationsComplete) {
					return nil, errors.New("NCT2 declarations were not complete before browser capture")
				}
			}
			if captureErr != nil {
				return nil, captureErr
			}
			return timelineRuntimeValue(batch), nil
		default:
			return timelineRuntimeValue(nil), nil
		}
	}
	return func(_ context.Context, _, _ string) (*browserSession, error) {
		return &browserSession{pooledCall: call, releasePool: func() error { *closeCount = *closeCount + 1; return nil }}, nil
	}, closeCount
}

func containsNCT2Record(stream []byte, kind uint32) bool {
	for offset := 0; offset+TimelineStreamEnvelopeBytes <= len(stream); {
		currentKind := uint32(stream[offset]) | uint32(stream[offset+1])<<8 | uint32(stream[offset+2])<<16 | uint32(stream[offset+3])<<24
		payloadLength := int(stream[offset+4]) | int(stream[offset+5])<<8 | int(stream[offset+6])<<16 | int(stream[offset+7])<<24
		if currentKind == kind {
			return true
		}
		next := offset + TimelineStreamEnvelopeBytes + payloadLength
		if payloadLength < 0 || next > len(stream) || next <= offset {
			return false
		}
		offset = next
	}
	return false
}
