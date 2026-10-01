package nicorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"imagepadserver/internal/niconico"
)

//go:embed assets/timeline.js
var timelineCaptureScript string

//go:embed assets/timeline_pbo.js
var timelinePBOCaptureScript string

const (
	timelineCaptureTimeout        = 60 * time.Second
	timelineCaptureMessageMax     = 48 << 20
	timelineCapturePerElementMax  = 32 << 20
	timelineCapturePerElementIDs  = 512
	timelineCaptureBatchMaxItems  = 32
	timelineCaptureBatchDefault   = 4
	timelineCaptureBatchTargetMax = 16 << 20
	timelineCaptureCommandsPerRun = 4096
	timelineCaptureDrawsPerItem   = 1024
	maxTimelineElements           = 100_000
)

var ErrTimelineUnsupported = errors.New("niconico timeline unsupported")

// TimelineCaptureReport describes the browser-side comment extraction pass.
// DrawCanvasCalls counts calls made after the browser page has initialized;
// ElementDrawCalls counts direct calls to the pinned comment draw primitive.
type TimelineCaptureReport struct {
	RenderReport
	EligibleComments         int
	UnsupportedComments      int
	DrawCanvasCalls          int
	ElementDrawCalls         int
	TimelineDraws            int
	TimelineAssets           int
	CaptureBatchCalls        int
	AssetBytes               int64
	BrowserAssetPayloadBytes int64
	SpriteCaptureMetrics     *TimelineCaptureMetrics
	BundleSHA256             string
}

// TimelineCaptureMetrics records the bounded browser extraction path without
// implying that missing metrics are zero-cost measurements.
type TimelineCaptureMetrics struct {
	RequestedMode         string  `json:"requested_mode"`
	Mode                  string  `json:"mode"`
	TextureCreations      int     `json:"texture_creations"`
	DrawWallMs            float64 `json:"draw_wall_ms"`
	ReadbackCalls         int     `json:"readback_calls"`
	ReadbackBytes         int64   `json:"readback_bytes"`
	ReadbackWallMs        float64 `json:"readback_wall_ms"`
	PBOTextures           int     `json:"pbo_textures"`
	PBODrainCalls         int     `json:"pbo_drain_calls"`
	PBOEnqueueWallMs      float64 `json:"pbo_enqueue_wall_ms"`
	PBODrainWallMs        float64 `json:"pbo_drain_wall_ms"`
	PBOFenceWaitWallMs    float64 `json:"pbo_fence_wait_wall_ms"`
	PBOCopyWallMs         float64 `json:"pbo_copy_wall_ms"`
	PBOBytes              int64   `json:"pbo_bytes"`
	PBOFallbacks          int     `json:"pbo_fallbacks"`
	PBOFallbackReason     string  `json:"pbo_fallback_reason,omitempty"`
	PBOPeakBytes          int64   `json:"pbo_peak_bytes"`
	PendingPixelPeakBytes int64   `json:"pending_pixel_peak_bytes"`
	PackWallMs            float64 `json:"pack_wall_ms"`
	DeflateWallMs         float64 `json:"deflate_wall_ms"`
	SerializeWallMs       float64 `json:"serialize_wall_ms"`
	BrowserCallCount      int     `json:"browser_call_count"`
	BrowserCallWallMs     float64 `json:"browser_call_wall_ms"`
	CaptureWallSeconds    float64 `json:"capture_wall_seconds"`
	Valid                 bool    `json:"valid"`
}

type browserSpriteCaptureMetrics struct {
	Mode                  string  `json:"mode"`
	TextureCreations      int     `json:"textureCreations"`
	DrawWallMs            float64 `json:"drawWallMs"`
	ReadbackCalls         int     `json:"readbackCalls"`
	ReadbackBytes         int64   `json:"readbackBytes"`
	ReadbackWallMs        float64 `json:"readbackWallMs"`
	PBOTextures           int     `json:"pboTextures"`
	PBODrainCalls         int     `json:"pboDrainCalls"`
	PBOEnqueueWallMs      float64 `json:"pboEnqueueWallMs"`
	PBODrainWallMs        float64 `json:"pboDrainWallMs"`
	PBOFenceWaitWallMs    float64 `json:"pboFenceWaitWallMs"`
	PBOCopyWallMs         float64 `json:"pboCopyWallMs"`
	PBOBytes              int64   `json:"pboBytes"`
	PBOFallbacks          int     `json:"pboFallbacks"`
	PBOFallbackReason     string  `json:"pboFallbackReason"`
	PBOPeakBytes          int64   `json:"pboPeakBytes"`
	PendingPixelPeakBytes int64   `json:"pendingPixelPeakBytes"`
	PackWallMs            float64 `json:"packWallMs"`
	DeflateWallMs         float64 `json:"deflateWallMs"`
	SerializeWallMs       float64 `json:"serializeWallMs"`
}

type timelineDescribeResult struct {
	OK                  bool                  `json:"ok"`
	Reason              string                `json:"reason"`
	UnsupportedComments int                   `json:"unsupportedComments"`
	Comments            []timelineCommentMeta `json:"comments"`
	DrawCanvasCalls     int                   `json:"drawCanvasCalls"`
	ElementDrawCalls    int                   `json:"elementDrawCalls"`
}

type timelineCommentMeta struct {
	Index       uint32   `json:"index"`
	OwnerOrder  uint32   `json:"ownerOrder"`
	Loc         string   `json:"loc"`
	StartVPos   int32    `json:"startVPos"`
	EndVPos     int32    `json:"endVPos"`
	ProbeVPos   int32    `json:"probeVPos"`
	ProbeX      float64  `json:"probeX"`
	ProbeY      float64  `json:"probeY"`
	X           float64  `json:"x"`
	Y           float64  `json:"y"`
	Width       float64  `json:"width"`
	Height      float64  `json:"height"`
	SpeedX      *float64 `json:"speedX"`
	Unsupported string   `json:"unsupported"`
}

type timelineCaptureElementResult struct {
	Index            uint32                       `json:"index"`
	Samples          []timelineCaptureSample      `json:"samples"`
	DrawCanvasCalls  int                          `json:"drawCanvasCalls"`
	ElementDrawCalls int                          `json:"elementDrawCalls"`
	SpriteMetrics    *browserSpriteCaptureMetrics `json:"spriteMetrics"`
}

type timelineCaptureBatchItem struct {
	Index     uint32  `json:"index"`
	ProbeVPos []int32 `json:"probeVposes"`
}

type timelineCapturePlan struct {
	Meta    timelineCommentMeta
	Probes  []int32
	Ordinal uint32
}

type timelineCaptureBatchResult struct {
	Consumed int                            `json:"consumed"`
	Items    []timelineCaptureElementResult `json:"items"`
}

type timelineCaptureSample struct {
	VPos     int32               `json:"vpos"`
	X        float64             `json:"x"`
	Y        float64             `json:"y"`
	Commands []spriteJSONCommand `json:"commands"`
	Textures []spriteJSONTexture `json:"textures"`
}

type timelineCapturedElement struct {
	Meta    timelineCommentMeta
	Samples []timelineCaptureSample
}

type timelineCaptureTexture struct {
	Width  uint32
	Height uint32
	RGBA   []byte
	SHA256 [32]byte
}

// CaptureCommentTimeline extracts stable comment assets and motion events from
// the pinned niconicomments WebGL renderer. It never advances drawCanvas; each
// candidate comment is drawn once at an anchor vpos. Motion speed is read
// from the pinned renderer's affine naka-position formula.
func CaptureCommentTimeline(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions) (CommentTimeline, TimelineCaptureReport, error) {
	return captureCommentTimeline(ctx, snapshot, options, options.TimelineRandomSeed)
}

// captureCommentTimelineSeededForTest supplies the same deterministic layout
// random stream to the reference and candidate browser sessions. It is only
// used by browser parity tests; production keeps Math.random unchanged.
func captureCommentTimelineSeededForTest(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, seed uint32) (CommentTimeline, TimelineCaptureReport, error) {
	return captureCommentTimeline(ctx, snapshot, options, &seed)
}

func captureCommentTimeline(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, randomSeed *uint32) (CommentTimeline, TimelineCaptureReport, error) {
	return captureCommentTimelineWithBatchLimit(ctx, snapshot, options, randomSeed, timelineCaptureBatchDefault)
}

func captureCommentTimelineWithBatchLimit(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, randomSeed *uint32, batchLimit int) (CommentTimeline, TimelineCaptureReport, error) {
	return captureCommentTimelineWithCaptureLimits(ctx, snapshot, options, randomSeed, batchLimit, timelineCaptureBatchTargetMax)
}

func captureCommentTimelineWithCaptureLimits(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, randomSeed *uint32, batchLimit, batchTargetBytes int) (CommentTimeline, TimelineCaptureReport, error) {
	return captureCommentTimelineWithCaptureLimitsAndStarter(ctx, snapshot, options, randomSeed, batchLimit, batchTargetBytes, nil)
}

func captureCommentTimelineWithCaptureLimitsAndStarter(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, randomSeed *uint32, batchLimit, batchTargetBytes int, freshStarter freshBrowserStarter) (scene CommentTimeline, report TimelineCaptureReport, retErr error) {
	return captureCommentTimelineWithCaptureLimitsAndStarterAndStream(ctx, snapshot, options, randomSeed, batchLimit, batchTargetBytes, freshStarter, nil)
}

func captureCommentTimelineWithCaptureLimitsAndStarterAndStream(ctx context.Context, snapshot niconico.Snapshot, options RenderOptions, randomSeed *uint32, batchLimit, batchTargetBytes int, freshStarter freshBrowserStarter, streamOut io.Writer) (scene CommentTimeline, report TimelineCaptureReport, retErr error) {
	if randomSeed == nil {
		randomSeed = options.TimelineRandomSeed
	}
	captureStarted := time.Now()
	var finishStream func() error
	defer func() {
		if retErr == nil && finishStream != nil {
			retErr = finishStream()
		}
	}()
	report = TimelineCaptureReport{RenderReport: RenderReport{
		Width: options.Width, Height: options.Height, FPSNum: options.FPSNum, FPSDen: options.FPSDen,
		RendererLabel: options.RendererLabel,
	}}
	requestedMode, modeErr := timelineAssetReadbackMode()
	metrics := &TimelineCaptureMetrics{RequestedMode: requestedMode, Mode: "sync"}
	report.SpriteCaptureMetrics = metrics
	defer func() { metrics.CaptureWallSeconds = time.Since(captureStarted).Seconds() }()
	if modeErr != nil {
		return CommentTimeline{}, report, modeErr
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var streamBudget *timelineStreamBudget
	if provider, ok := streamOut.(timelineStreamBudgetProvider); ok {
		streamBudget = provider.timelineStreamBudget()
	}
	var liveReservations []*timelineStreamReservation
	var activeResponseReservation *timelineStreamReservation
	var validation *timelineCaptureValidationState
	var streamAssembler *timelineCapturedSceneAssembler
	var captured []timelineCapturedElement
	var finalAssets []TimelineAsset
	defer func() {
		activeResponseReservation.release()
		if validation != nil {
			for id := range validation.texturePixels {
				delete(validation.texturePixels, id)
			}
		}
		if streamAssembler != nil {
			for id := range streamAssembler.assetIDs {
				delete(streamAssembler.assetIDs, id)
			}
			for id := range streamAssembler.textureToAsset {
				delete(streamAssembler.textureToAsset, id)
			}
			for fingerprint := range streamAssembler.assetByFingerprint {
				delete(streamAssembler.assetByFingerprint, fingerprint)
			}
		}
		for i := range finalAssets {
			finalAssets[i].RGBA = nil
		}
		for i := range captured {
			captured[i].Samples = nil
		}
		for _, reservation := range liveReservations {
			reservation.release()
		}
	}()
	reserveStreamBytes := func(amount int64, purpose string) (*timelineStreamReservation, error) {
		if streamBudget == nil {
			return nil, nil
		}
		reservation, err := streamBudget.reserve(ctx, amount)
		if err != nil {
			return nil, fmt.Errorf("niconico timeline: reserve %s (%d bytes): %w", purpose, amount, err)
		}
		return reservation, nil
	}
	if err := ctx.Err(); err != nil {
		return CommentTimeline{}, report, err
	}
	if err := validateTimelineSnapshot(snapshot); err != nil {
		return CommentTimeline{}, report, err
	}
	if err := options.validate(); err != nil {
		return CommentTimeline{}, report, err
	}
	compression := options.SpriteCompression
	if compression == "" {
		compression = "deflate"
	}
	if compression != "none" && compression != "deflate" {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: unsupported sprite compression %q", compression)
	}
	if batchLimit < 1 || batchLimit > timelineCaptureBatchMaxItems {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: capture batch limit %d is outside 1..%d", batchLimit, timelineCaptureBatchMaxItems)
	}
	if batchTargetBytes < 1 || batchTargetBytes > timelineCaptureMessageMax {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: capture response target %d is outside 1..%d", batchTargetBytes, timelineCaptureMessageMax)
	}
	if options.Width > MaxTimelineWidth || options.Height > MaxTimelineHeight {
		return CommentTimeline{}, report, timelineUnsupported("output size exceeds NCT1 bounds")
	}
	clock, err := niconico.NewFrameClock(options.FPSNum, options.FPSDen)
	if err != nil {
		return CommentTimeline{}, report, err
	}
	frameCount := clock.FrameCountForDurationMs(options.DurationMs)
	if frameCount <= 0 || frameCount > MaxTimelineFrames {
		return CommentTimeline{}, report, timelineUnsupported(fmt.Sprintf("output frame count %d is outside NCT1 bounds", frameCount))
	}
	if uint64(frameCount) > uint64(1<<32-1) || uint64(options.FPSNum) > uint64(1<<32-1) || uint64(options.FPSDen) > uint64(1<<32-1) {
		return CommentTimeline{}, report, timelineUnsupported("output clock exceeds NCT1 bounds")
	}
	report.FrameCount = frameCount

	bundleDigest := sha256.Sum256(bundledRenderer)
	report.BundleSHA256 = hex.EncodeToString(bundleDigest[:])
	if options.BundlePath != "" {
		custom, err := os.ReadFile(options.BundlePath)
		if err != nil {
			return CommentTimeline{}, report, fmt.Errorf("niconico timeline: read configured bundle: %w", err)
		}
		if !bytes.Equal(custom, bundledRenderer) {
			return CommentTimeline{}, report, timelineUnsupported("configured bundle does not match the pinned renderer bytes")
		}
	}

	header := TimelineHeader{
		Width: uint32(options.Width), Height: uint32(options.Height), FrameCount: uint32(frameCount),
		FPSNum: uint32(options.FPSNum), FPSDen: uint32(options.FPSDen), BundleSHA256: bundleDigest,
	}
	var streamWriter *IncrementalCommentTimelineStreamWriter
	finishNCT2 := func() error {
		if queue, ok := streamOut.(*timelineStreamOutputQueue); ok {
			if err := queue.flush(queue.ctx); err != nil {
				return err
			}
			if err := streamWriter.Finish(); err != nil {
				return err
			}
			return queue.flush(queue.ctx)
		}
		return streamWriter.Finish()
	}
	empty := CommentTimeline{Header: header}
	threads := snapshot.RendererThreads()
	commentCount := 0
	for _, thread := range threads {
		commentCount += len(thread.Comments)
	}
	if commentCount == 0 {
		metrics.Valid = true
		if err := ValidateCommentTimeline(empty); err != nil {
			return CommentTimeline{}, report, err
		}
		if streamOut != nil {
			streamWriter, err = NewIncrementalCommentTimelineStreamWriter(streamOut, header, nil)
			if err != nil {
				return CommentTimeline{}, report, err
			}
			finishStream = finishNCT2
		}
		return empty, report, nil
	}
	if commentCount > maxTimelineElements {
		return CommentTimeline{}, report, timelineUnsupported("comment count exceeds the browser capture bound")
	}

	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > timelineCaptureTimeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timelineCaptureTimeout)
		defer cancel()
	}

	bundlePath, cleanupBundle, err := materializeBundle("")
	if err != nil {
		return CommentTimeline{}, report, err
	}
	defer cleanupBundle()
	pagePath, err := writeRendererPageWithRandomSeed(options.Width, options.Height, bundlePath, threads, nil, randomSeed)
	if err != nil {
		return CommentTimeline{}, report, err
	}
	defer os.Remove(pagePath)

	session, err := openCaptureBrowser(ctx, options, pagePath, freshStarter)
	if err != nil {
		return CommentTimeline{}, report, err
	}
	defer func() {
		if closeErr := session.close(); closeErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("niconico timeline: release browser lease: %w", closeErr))
		}
	}()
	if err := session.waitReady(ctx, false); err != nil {
		return CommentTimeline{}, report, err
	}
	if requestedMode == "pbo" {
		if err := spriteEvaluate(ctx, session, timelinePBOCaptureScript, nil); err != nil {
			return CommentTimeline{}, report, fmt.Errorf("niconico timeline: install PBO readback manager: %w", err)
		}
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cleanupCancel()
			_ = spriteEvaluate(cleanupCtx, session, "window.__nicoTimelinePBO && window.__nicoTimelinePBO.dispose()", nil)
		}()
	}
	var browserCommentCount int
	if err := spriteEvaluate(ctx, session, "window.__niconi && window.__niconi.comments ? window.__niconi.comments.length : -1", &browserCommentCount); err != nil {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: read browser comment count: %w", err)
	}
	if browserCommentCount != commentCount {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: browser parsed %d comments from %d selected snapshot comments", browserCommentCount, commentCount)
	}
	if err := spriteEvaluate(ctx, session, spriteCaptureScript, nil); err != nil {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: install sprite capture: %w", err)
	}
	if err := spriteEvaluate(ctx, session, timelineCaptureScript, nil); err != nil {
		if strings.Contains(err.Error(), "NCT_CAPTURE_UNSUPPORTED:") {
			return CommentTimeline{}, report, timelineUnsupported(err.Error())
		}
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: install capture bridge: %w", err)
	}
	if err := spriteEvaluate(ctx, session, fmt.Sprintf("window.__nicoSpritesDeflate = %t", compression == "deflate"), nil); err != nil {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: configure sprite compression: %w", err)
	}
	var installError string
	if err := spriteEvaluate(ctx, session, "window.__nicoTimelineInstallError || ''", &installError); err != nil {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: inspect capture bridge: %w", err)
	}
	if installError != "" {
		report.UnsupportedComments = 1
		return CommentTimeline{}, report, timelineUnsupported(installError)
	}

	lastVPos, err := TimelineVPos(clock, frameCount-1)
	if err != nil {
		return CommentTimeline{}, report, err
	}
	frameEnd := int64(lastVPos) + 1
	if frameEnd > int64(1<<31-1) {
		return CommentTimeline{}, report, timelineUnsupported("exclusive output vpos end exceeds int32")
	}
	describeExpression := fmt.Sprintf("window.__nicoTimelineDescribe(0,%d)", frameEnd)
	var description timelineDescribeResult
	describeReservation, err := reserveStreamBytes(timelineCaptureMessageMax, "bounded describe response")
	if err != nil {
		return CommentTimeline{}, report, err
	}
	if describeReservation != nil {
		liveReservations = append(liveReservations, describeReservation)
	}
	if err := timelineEvaluate(ctx, session, describeExpression, timelineCaptureMessageMax, &description); err != nil {
		if strings.Contains(err.Error(), "NCT_CAPTURE_UNSUPPORTED:") {
			report.UnsupportedComments = 1
			return CommentTimeline{}, report, timelineUnsupported(err.Error())
		}
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: describe browser comments: %w", err)
	}
	report.DrawCanvasCalls = description.DrawCanvasCalls
	report.ElementDrawCalls = description.ElementDrawCalls
	if !description.OK {
		report.UnsupportedComments = description.UnsupportedComments
		return CommentTimeline{}, report, timelineUnsupported(description.Reason)
	}
	if report.DrawCanvasCalls != 0 {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: drawCanvas was called during page initialization")
	}

	comments := append([]timelineCommentMeta(nil), description.Comments...)
	sort.SliceStable(comments, func(i, j int) bool {
		if comments[i].OwnerOrder != comments[j].OwnerOrder {
			return comments[i].OwnerOrder < comments[j].OwnerOrder
		}
		return comments[i].Index < comments[j].Index
	})
	report.EligibleComments = len(comments)
	if report.EligibleComments > maxTimelineElements {
		return CommentTimeline{}, report, timelineUnsupported("eligible comment count exceeds the capture bound")
	}

	clockFirst, err := TimelineVPos(clock, 0)
	if err != nil {
		return CommentTimeline{}, report, err
	}
	activeStart := int32(clockFirst)
	activeEnd := int32(frameEnd)
	streamEnd := frameEnd
	if streamOut != nil {
		streamEnd, err = timelineStreamFrameClock(uint32(frameCount), uint32(options.FPSNum), uint32(options.FPSDen))
		if err != nil {
			return CommentTimeline{}, report, err
		}
		if streamEnd < 0 || streamEnd > int64(1<<31-1) {
			return CommentTimeline{}, report, timelineUnsupported("exclusive stream vpos end exceeds int32")
		}
	}
	capturePlans := make([]timelineCapturePlan, 0, len(comments))
	var declarations []TimelineStreamDeclaration
	if streamOut != nil {
		declarations = make([]TimelineStreamDeclaration, 0, len(comments))
	}
	for _, meta := range comments {
		if meta.Unsupported != "" {
			report.UnsupportedComments++
			return CommentTimeline{}, report, timelineUnsupported(meta.Unsupported)
		}
		if meta.StartVPos >= meta.EndVPos {
			return CommentTimeline{}, report, fmt.Errorf("niconico timeline: invalid comment interval at index %d", meta.Index)
		}
		clipped, active, err := clipTimelineCaptureInterval(meta, activeStart, activeEnd)
		if err != nil {
			return CommentTimeline{}, report, err
		}
		var probes []int32
		var declarationStart, declarationEnd int32
		if active {
			probes = timelineProbeVPos(clipped.StartVPos, clipped.EndVPos)
			if streamOut != nil {
				declarationStart = int32(clipTimelineVPos(meta.StartVPos, streamEnd))
				declarationEnd = int32(clipTimelineVPos(meta.EndVPos, streamEnd))
				if len(probes) != 1 || meta.ProbeVPos != probes[0] {
					return CommentTimeline{}, report, fmt.Errorf("niconico timeline: comment %d describe probe vpos=%d, want %v", meta.Index, meta.ProbeVPos, probes)
				}
				speed := 0.0
				if meta.SpeedX != nil {
					speed = *meta.SpeedX
				}
				visibleStart, visibleEnd, visible, err := clipTimelineStageVisibility(
					meta,
					timelineCaptureSample{VPos: meta.ProbeVPos, X: meta.ProbeX, Y: meta.ProbeY},
					speed, uint32(options.Width), uint32(options.Height),
				)
				if err != nil {
					return CommentTimeline{}, report, timelineUnsupported(fmt.Sprintf("comment %d predicted stage visibility: %v", meta.Index, err))
				}
				if visible {
					declarationStart = int32(clipTimelineVPos(visibleStart, streamEnd))
					declarationEnd = int32(clipTimelineVPos(visibleEnd, streamEnd))
				} else {
					declarationEnd = declarationStart
				}
			}
		} else if streamOut != nil {
			declarationStart = int32(clipTimelineVPos(meta.StartVPos, streamEnd))
			declarationEnd = int32(clipTimelineVPos(meta.EndVPos, streamEnd))
		}
		var ordinal uint32
		if streamOut != nil {
			ordinal = uint32(len(declarations))
			declarations = append(declarations, TimelineStreamDeclaration{
				Ordinal: ordinal, OwnerOrder: meta.OwnerOrder, CommentIndex: meta.Index,
				StartVPos: declarationStart, EndVPos: declarationEnd,
			})
		}
		if active {
			capturePlans = append(capturePlans, timelineCapturePlan{
				Meta: meta, Probes: probes, Ordinal: ordinal,
			})
		}
	}
	if streamOut != nil {
		streamWriter, err = NewIncrementalCommentTimelineStreamWriter(streamOut, header, declarations)
		if err != nil {
			return CommentTimeline{}, report, err
		}
		streamAssembler = newTimelineCapturedSceneAssembler(header)
		if queue, ok := streamOut.(*timelineStreamOutputQueue); ok {
			if err := queue.flush(queue.ctx); err != nil {
				return CommentTimeline{}, report, err
			}
		}
		finishStream = finishNCT2
	}
	if len(capturePlans) == 0 {
		metrics.Valid = true
	}
	if streamWriter == nil {
		captured = make([]timelineCapturedElement, 0, len(capturePlans))
	}
	validation = newTimelineCaptureValidationState(&report, metrics)
	var nextStreamOrdinal uint32
	for next := 0; next < len(capturePlans); {
		batchEnd := min(next+batchLimit, len(capturePlans))
		batchRequests := make([]timelineCaptureBatchItem, 0, batchEnd-next)
		for _, plan := range capturePlans[next:batchEnd] {
			batchRequests = append(batchRequests, timelineCaptureBatchItem{Index: plan.Meta.Index, ProbeVPos: plan.Probes})
		}
		requestJSON, err := json.Marshal(batchRequests)
		if err != nil {
			return CommentTimeline{}, report, fmt.Errorf("niconico timeline: encode capture batch: %w", err)
		}
		var batch timelineCaptureBatchResult
		var captureErr error
		responseReservation, err := reserveStreamBytes(timelineCaptureMessageMax, "bounded CDP capture response")
		if err != nil {
			return CommentTimeline{}, report, err
		}
		activeResponseReservation = responseReservation
		if batchLimit == 1 {
			var element timelineCaptureElementResult
			probeJSON, marshalErr := json.Marshal(batchRequests[0].ProbeVPos)
			if marshalErr != nil {
				return CommentTimeline{}, report, fmt.Errorf("niconico timeline: encode capture probes: %w", marshalErr)
			}
			expression := fmt.Sprintf("window.__nicoTimelineCaptureElement(%d,%s,%d,%d)", batchRequests[0].Index, probeJSON, MaxTimelineAssets, MaxTimelineTotalAssetBytes)
			callStarted := time.Now()
			captureErr = timelineEvaluate(ctx, session, expression, timelineCaptureMessageMax, &element)
			metrics.BrowserCallCount++
			metrics.BrowserCallWallMs += float64(time.Since(callStarted)) / float64(time.Millisecond)
			if captureErr == nil {
				batch = timelineCaptureBatchResult{Consumed: 1, Items: []timelineCaptureElementResult{element}}
			}
		} else {
			expression := fmt.Sprintf("window.__nicoTimelineCaptureBatch(%s,%d,%d,%d)", requestJSON, MaxTimelineAssets, MaxTimelineTotalAssetBytes, batchTargetBytes)
			callStarted := time.Now()
			captureErr = timelineEvaluate(ctx, session, expression, timelineCaptureMessageMax, &batch)
			metrics.BrowserCallCount++
			metrics.BrowserCallWallMs += float64(time.Since(callStarted)) / float64(time.Millisecond)
		}
		if captureErr != nil {
			if strings.Contains(captureErr.Error(), "NCT_CAPTURE_UNSUPPORTED:") {
				report.UnsupportedComments++
				return CommentTimeline{}, report, timelineUnsupported(captureErr.Error())
			}
			return CommentTimeline{}, report, fmt.Errorf("niconico timeline: capture comment batch beginning at %d: %w", capturePlans[next].Meta.Index, captureErr)
		}
		report.CaptureBatchCalls++
		if batch.Consumed <= 0 || batch.Consumed > len(batchRequests) || len(batch.Items) != batch.Consumed {
			return CommentTimeline{}, report, fmt.Errorf("niconico timeline: browser returned invalid capture batch size consumed=%d items=%d requested=%d", batch.Consumed, len(batch.Items), len(batchRequests))
		}
		for itemIndex, element := range batch.Items {
			plan := capturePlans[next+itemIndex]
			textureIDs := make([]uint32, 0)
			if streamWriter != nil {
				for _, sample := range element.Samples {
					for _, texture := range sample.Textures {
						textureIDs = append(textureIDs, texture.ID)
					}
				}
			}
			pixelReservation, err := reserveTimelineElementPixels(ctx, streamBudget, &element)
			if err != nil {
				return CommentTimeline{}, report, err
			}
			if pixelReservation != nil {
				liveReservations = append(liveReservations, pixelReservation)
			}
			capturedElement, err := validation.validateElement(plan.Meta, plan.Probes, &element)
			if err != nil {
				return CommentTimeline{}, report, err
			}
			if streamWriter != nil {
				if len(capturedElement.Samples) != 1 || capturedElement.Samples[0].X != plan.Meta.ProbeX || capturedElement.Samples[0].Y != plan.Meta.ProbeY {
					return CommentTimeline{}, report, fmt.Errorf("niconico timeline: comment %d captured probe geometry (%v,%v) differs from frozen geometry (%v,%v)",
						plan.Meta.Index, capturedElement.Samples[0].X, capturedElement.Samples[0].Y, plan.Meta.ProbeX, plan.Meta.ProbeY)
				}
			}
			if streamWriter == nil {
				captured = append(captured, capturedElement)
				continue
			}
			for nextStreamOrdinal < plan.Ordinal {
				if err := streamWriter.WriteElement(TimelineStreamElement{Ordinal: nextStreamOrdinal}, nil); err != nil {
					return CommentTimeline{}, report, err
				}
				nextStreamOrdinal++
			}
			draws, assets, err := streamAssembler.addElement(capturedElement, validation.texturePixels)
			if err != nil {
				return CommentTimeline{}, report, err
			}
			if err := streamWriter.WriteElement(TimelineStreamElement{Ordinal: plan.Ordinal, Draws: draws}, assets); err != nil {
				return CommentTimeline{}, report, err
			}
			nextStreamOrdinal = plan.Ordinal + 1
			for _, textureID := range textureIDs {
				assetID, referenced := streamAssembler.textureToAsset[textureID]
				if !referenced {
					continue
				}
				asset, ok := streamAssembler.assetIDs[assetID]
				if !ok {
					return CommentTimeline{}, report, fmt.Errorf("niconico timeline: missing assembled asset %d for texture %d", assetID, textureID)
				}
				// Preserve legacy cross-element texture-ID references while aliasing
				// duplicate RGBA to the assembler's canonical asset pixels.
				validation.texturePixels[textureID] = timelineCaptureTexture{
					Width: asset.Width, Height: asset.Height, RGBA: asset.RGBA, SHA256: asset.SHA256,
				}
			}
		}
		next += batch.Consumed
		if responseReservation != nil {
			responseReservation.release()
		}
		activeResponseReservation = nil
	}
	if streamWriter != nil {
		for nextStreamOrdinal < uint32(len(declarations)) {
			if err := streamWriter.WriteElement(TimelineStreamElement{Ordinal: nextStreamOrdinal}, nil); err != nil {
				return CommentTimeline{}, report, err
			}
			nextStreamOrdinal++
		}
	}
	if report.DrawCanvasCalls != 0 {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: ordinary drawCanvas was used")
	}
	if report.ElementDrawCalls > report.EligibleComments {
		return CommentTimeline{}, report, fmt.Errorf("niconico timeline: total element draw bound exceeded")
	}
	if !metrics.Valid {
		return CommentTimeline{}, report, errors.New("niconico timeline: browser omitted sprite capture metrics")
	}
	if metrics.RequestedMode == "pbo" && metrics.PBOTextures == 0 {
		metrics.Mode = "sync"
	}

	if streamWriter != nil {
		assets, err := streamAssembler.finish()
		if err != nil {
			if errors.Is(err, errTimelineLimit) {
				return CommentTimeline{}, report, timelineUnsupported(err.Error())
			}
			return CommentTimeline{}, report, err
		}
		report.TimelineDraws = streamAssembler.drawCount
		report.TimelineAssets = len(assets)
		finalAssets = assets
		for _, asset := range assets {
			report.AssetBytes += int64(len(asset.RGBA))
		}
		if err := ValidateCommentTimeline(CommentTimeline{Header: header, Assets: assets}); err != nil {
			if errors.Is(err, errTimelineLimit) {
				return CommentTimeline{}, report, timelineUnsupported(err.Error())
			}
			return CommentTimeline{}, report, err
		}
		assets = nil
		scene = empty
		return scene, report, nil
	}
	scene, err = assembleTimelineCapturedScene(header, captured, validation.texturePixels)
	if err != nil {
		if errors.Is(err, errTimelineLimit) {
			return CommentTimeline{}, report, timelineUnsupported(err.Error())
		}
		return CommentTimeline{}, report, err
	}
	report.TimelineDraws = len(scene.Draws)
	report.TimelineAssets = len(scene.Assets)
	for _, asset := range scene.Assets {
		report.AssetBytes += int64(len(asset.RGBA))
	}
	if err := ValidateCommentTimeline(scene); err != nil {
		if errors.Is(err, errTimelineLimit) {
			return CommentTimeline{}, report, timelineUnsupported(err.Error())
		}
		return CommentTimeline{}, report, err
	}
	return scene, report, nil
}

func validateTimelineSnapshot(snapshot niconico.Snapshot) error {
	count := 0
	for _, thread := range snapshot.Threads {
		count += len(thread.Comments)
	}
	switch snapshot.CommentStatus {
	case niconico.CommentStatusEmpty:
		if count != 0 || snapshot.CommentCount != 0 {
			return errors.New("niconico timeline: empty snapshot contains comments")
		}
	case niconico.CommentStatusReady:
		if count == 0 || snapshot.CommentCount != count {
			return errors.New("niconico timeline: ready snapshot comment count is inconsistent")
		}
	case "":
		// Pre-schema snapshots contain the videoId/threads payload consumed by
		// the legacy renderer, but omit the later commentStatus/commentCount
		// summary. Keep those inputs eligible for timeline capture without
		// weakening validation of current-schema snapshots.
		if snapshot.SchemaVersion != 0 || snapshot.CommentCount != 0 {
			return fmt.Errorf("niconico timeline: invalid snapshot comment status %q", snapshot.CommentStatus)
		}
	default:
		return fmt.Errorf("niconico timeline: invalid snapshot comment status %q", snapshot.CommentStatus)
	}
	if snapshot.VideoID == "" {
		return errors.New("niconico timeline: snapshot video ID is required")
	}
	return nil
}

func timelineAssetReadbackMode() (string, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("NICO_TIMELINE_ASSET_READBACK")))
	if mode == "" {
		mode = "sync"
	}
	if mode != "sync" && mode != "pbo" {
		return "", fmt.Errorf("niconico timeline: unsupported NICO_TIMELINE_ASSET_READBACK %q (want sync or pbo)", mode)
	}
	return mode, nil
}

func timelineUnsupported(reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "unknown capture feature"
	}
	return fmt.Errorf("%w: %s", ErrTimelineUnsupported, strings.TrimSpace(reason))
}

func timelineProbeVPos(start, end int32) []int32 {
	length := int64(end) - int64(start)
	anchor := int32(int64(start) + (length-1)/2)
	return []int32{anchor}
}

func clipTimelineCaptureInterval(meta timelineCommentMeta, start, end int32) (timelineCommentMeta, bool, error) {
	if meta.StartVPos >= meta.EndVPos || start >= end {
		return timelineCommentMeta{}, false, fmt.Errorf("niconico timeline: invalid interval [%d,%d) clipped to [%d,%d)", meta.StartVPos, meta.EndVPos, start, end)
	}
	meta.StartVPos = max(meta.StartVPos, start)
	meta.EndVPos = min(meta.EndVPos, end)
	return meta, meta.StartVPos < meta.EndVPos, nil
}

// clipTimelineStageVisibility mirrors the pinned renderer's isOutsideStage
// check. It clips the comment's half-open vpos interval to the positions where
// its logical bounds intersect the output canvas. The sprite may extend past
// those bounds because CanvasRenderer adds transparent padding around text.
func clipTimelineStageVisibility(meta timelineCommentMeta, anchor timelineCaptureSample, speed float64, canvasWidth, canvasHeight uint32) (int32, int32, bool, error) {
	if meta.StartVPos >= meta.EndVPos || canvasWidth == 0 || canvasHeight == 0 {
		return 0, 0, false, fmt.Errorf("invalid interval or canvas dimensions")
	}
	if !finite64(anchor.X) || !finite64(anchor.Y) || !finite64(speed) ||
		!finite64(meta.Width) || !finite64(meta.Height) || meta.Width <= 0 || meta.Height <= 0 {
		return 0, 0, false, fmt.Errorf("non-finite or non-positive comment geometry")
	}
	if anchor.Y >= float64(canvasHeight) || anchor.Y+meta.Height <= 0 {
		return 0, 0, false, nil
	}

	xAt := func(vpos int32) float64 {
		return anchor.X + (float64(vpos)-float64(anchor.VPos))*speed
	}
	if speed == 0 {
		x := anchor.X
		if x >= float64(canvasWidth) || x+meta.Width <= 0 {
			return 0, 0, false, nil
		}
		return meta.StartVPos, meta.EndVPos, true, nil
	}

	firstMatching := func(matches func(float64) bool) int32 {
		low, high := int64(meta.StartVPos), int64(meta.EndVPos)
		for low < high {
			mid := low + (high-low)/2
			if matches(xAt(int32(mid))) {
				high = mid
			} else {
				low = mid + 1
			}
		}
		return int32(low)
	}

	var visibleStart, visibleEnd int32
	if speed < 0 {
		// Scrolling left enters when the comment's origin moves inside the right
		// edge, then leaves when its trailing edge passes the left edge.
		visibleStart = firstMatching(func(x float64) bool { return x < float64(canvasWidth) })
		visibleEnd = firstMatching(func(x float64) bool { return x+meta.Width <= 0 })
	} else {
		visibleStart = firstMatching(func(x float64) bool { return x+meta.Width > 0 })
		visibleEnd = firstMatching(func(x float64) bool { return x >= float64(canvasWidth) })
	}
	if visibleStart >= visibleEnd {
		return 0, 0, false, nil
	}
	return visibleStart, visibleEnd, true, nil
}

func timelineEvaluate(ctx context.Context, session *browserSession, expression string, maxBytes int, out any) error {
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
		message := string(envelope.ExceptionDetails)
		if strings.Contains(message, "NCT_CAPTURE_UNSUPPORTED:") {
			return fmt.Errorf("NCT_CAPTURE_UNSUPPORTED:%s", message)
		}
		if strings.Contains(message, "NCT_CAPTURE_INVALID:") {
			return fmt.Errorf("niconico timeline: malformed browser capture: %s", message)
		}
		return fmt.Errorf("niconico timeline: browser evaluate: %s", message)
	}
	if len(envelope.Result.ExceptionDetails) > 0 && string(envelope.Result.ExceptionDetails) != "null" {
		message := string(envelope.Result.ExceptionDetails)
		if strings.Contains(message, "NCT_CAPTURE_UNSUPPORTED:") {
			return fmt.Errorf("NCT_CAPTURE_UNSUPPORTED:%s", message)
		}
		if strings.Contains(message, "NCT_CAPTURE_INVALID:") {
			return fmt.Errorf("niconico timeline: malformed browser capture: %s", message)
		}
		return fmt.Errorf("niconico timeline: browser evaluate: %s", message)
	}
	if out == nil {
		return nil
	}
	if len(envelope.Result.Value) == 0 || string(envelope.Result.Value) == "null" {
		return errors.New("niconico timeline: browser returned no value")
	}
	if maxBytes <= 0 || len(envelope.Result.Value) > maxBytes {
		return timelineUnsupported(fmt.Sprintf("browser capture response is %d bytes; limit is %d", len(envelope.Result.Value), maxBytes))
	}
	if err := json.Unmarshal(envelope.Result.Value, out); err != nil {
		return err
	}
	return nil
}

func preflightTimelineSpriteTextures(textures []spriteJSONTexture) (int64, error) {
	if len(textures) > timelineCapturePerElementIDs {
		return 0, errors.New("too many textures in one element batch")
	}
	var total int64
	for _, texture := range textures {
		if texture.Width == 0 || texture.Height == 0 || texture.Width > MaxTimelineAssetDimension || texture.Height > MaxTimelineAssetDimension {
			return 0, fmt.Errorf("texture dimensions %dx%d exceed bound", texture.Width, texture.Height)
		}
		bytes := uint64(texture.Width) * uint64(texture.Height) * 4
		if bytes > MaxTimelineAssetBytes {
			return 0, errors.New("texture RGBA bytes exceed per-asset bound")
		}
		if bytes > uint64(math.MaxInt64) || total > timelineCapturePerElementMax-int64(bytes) {
			return 0, errors.New("texture batch bytes exceed browser capture bound")
		}
		total += int64(bytes)
		encodedDataLimit := int64(bytes)*2 + 4096
		if int64(len(texture.Data)) > encodedDataLimit || int64(len(texture.Palette)) > 1368 {
			return 0, errors.New("texture encoding exceeds expected input bound")
		}
	}
	return total, nil
}

func reserveTimelineElementPixels(ctx context.Context, budget *timelineStreamBudget, element *timelineCaptureElementResult) (*timelineStreamReservation, error) {
	if element == nil {
		return nil, errors.New("niconico timeline: nil element for pixel reservation")
	}
	var total int64
	for sampleIndex := range element.Samples {
		bytes, err := preflightTimelineSpriteTextures(element.Samples[sampleIndex].Textures)
		if err != nil {
			return nil, timelineUnsupported(err.Error())
		}
		if bytes > MaxTimelineTotalAssetBytes-total {
			return nil, timelineUnsupported("one browser capture element exceeds the bounded pixel reservation")
		}
		total += bytes
	}
	if total == 0 {
		return nil, nil
	}
	if budget == nil {
		return nil, nil
	}
	reservation, err := budget.reserve(ctx, total)
	if err != nil {
		return nil, fmt.Errorf("niconico timeline: reserve preflighted element RGBA (%d bytes): %w", total, err)
	}
	return reservation, nil
}

func assembleTimelineCapturedScene(header TimelineHeader, elements []timelineCapturedElement, textures map[uint32]timelineCaptureTexture) (CommentTimeline, error) {
	sortedElements := append([]timelineCapturedElement(nil), elements...)
	sort.SliceStable(sortedElements, func(i, j int) bool {
		if sortedElements[i].Meta.OwnerOrder != sortedElements[j].Meta.OwnerOrder {
			return sortedElements[i].Meta.OwnerOrder < sortedElements[j].Meta.OwnerOrder
		}
		return sortedElements[i].Meta.Index < sortedElements[j].Meta.Index
	})
	scene := CommentTimeline{Header: header}
	assembler := newTimelineCapturedSceneAssembler(header)
	for _, element := range sortedElements {
		draws, _, err := assembler.addElement(element, textures)
		if err != nil {
			return CommentTimeline{}, err
		}
		scene.Draws = append(scene.Draws, draws...)
	}
	assets, err := assembler.finish()
	if err != nil {
		return CommentTimeline{}, err
	}
	scene.Assets = assets
	if err := ValidateCommentTimeline(scene); err != nil {
		return CommentTimeline{}, err
	}
	return scene, nil
}
func commandTextureIDs(commands []spriteJSONCommand) []uint32 {
	ids := make([]uint32, 0, len(commands))
	for _, command := range commands {
		if command.ID != 0 {
			ids = append(ids, command.ID)
		}
	}
	return ids
}

func sameTimelineImage(a, b timelineCaptureTexture) bool {
	return a.Width == b.Width && a.Height == b.Height && a.SHA256 == b.SHA256 && bytes.Equal(a.RGBA, b.RGBA)
}

func sameTimelinePrimitiveExceptX(a, b spriteJSONCommand) bool {
	for i := 1; i < len(a.Rect); i++ {
		if math.Float32bits(a.Rect[i]) != math.Float32bits(b.Rect[i]) {
			return false
		}
	}
	if a.Color != b.Color || a.Proj != b.Proj {
		return false
	}
	return true
}

func nearTimeline64(a, b, tolerance float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) || math.IsInf(a, 0) || math.IsInf(b, 0) {
		return false
	}
	return math.Abs(a-b) <= tolerance*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}
