package nicorender

import (
	"math"
	"sort"
	"testing"
)

type timelinePositionKey struct {
	ownerOrder uint32
	index      uint32
}

type timelineDrawOrderMismatch struct {
	Frame    int64       `json:"frame"`
	VPos     int64       `json:"vpos"`
	Browser  [][2]uint32 `json:"browser"`
	Timeline [][2]uint32 `json:"timeline"`
}

type timelineDrawOrderComparison struct {
	SchemaVersion  int                        `json:"schemaVersion"`
	Frames         int                        `json:"frames"`
	MismatchFrames int                        `json:"mismatchFrames"`
	BrowserDraws   uint64                     `json:"browserDraws"`
	TimelineDraws  uint64                     `json:"timelineDraws"`
	FirstMismatch  *timelineDrawOrderMismatch `json:"firstMismatch,omitempty"`
}

type timelinePositionSample struct {
	Frame         int64   `json:"frame"`
	VPos          int64   `json:"vpos"`
	OwnerOrder    uint32  `json:"ownerOrder"`
	CommentIndex  uint32  `json:"commentIndex"`
	BrowserRectX  float64 `json:"browserRectX"`
	BrowserRectY  float64 `json:"browserRectY"`
	TimelineRectX float64 `json:"timelineRectX"`
	TimelineRectY float64 `json:"timelineRectY"`
	AbsErrorX     float64 `json:"absErrorX"`
	AbsErrorY     float64 `json:"absErrorY"`
}

type timelinePositionComparison struct {
	SchemaVersion          int                      `json:"schemaVersion"`
	Samples                int                      `json:"samples"`
	MissingBrowserPosition int                      `json:"missingBrowserPosition"`
	MaxAbsErrorX           float64                  `json:"maxAbsErrorX"`
	MaxAbsErrorY           float64                  `json:"maxAbsErrorY"`
	OverToleranceX         int                      `json:"overToleranceX"`
	OverToleranceY         int                      `json:"overToleranceY"`
	TolerancePixels        float64                  `json:"tolerancePixels"`
	WorstX                 *timelinePositionSample  `json:"worstX,omitempty"`
	WorstY                 *timelinePositionSample  `json:"worstY,omitempty"`
	LargestOutliers        []timelinePositionSample `json:"largestOutliers,omitempty"`
}

func compareTimelineReferencePositions(scene CommentTimeline, reference TimelineReference, tolerance float64) timelinePositionComparison {
	result := timelinePositionComparison{SchemaVersion: 1, TolerancePixels: tolerance}
	activeDraw := make(map[timelinePositionKey]TimelineDraw)
	for _, draw := range scene.Draws {
		key := timelinePositionKey{ownerOrder: draw.OwnerOrder, index: draw.CommentIndex}
		if _, exists := activeDraw[key]; !exists {
			activeDraw[key] = draw
		}
	}
	for _, frame := range reference.FrameRefs {
		for _, browserDraw := range frame.DrawOrder {
			if browserDraw.PositionX == nil || browserDraw.PositionY == nil || browserDraw.ImagePadding == nil {
				result.MissingBrowserPosition++
				continue
			}
			key := timelinePositionKey{index: uint32(browserDraw.CommentIndex)}
			if browserDraw.Owner {
				key.ownerOrder = 1
			}
			draw, exists := activeDraw[key]
			if !exists || int64(draw.StartVPos) > frame.VPos || frame.VPos >= int64(draw.EndVPos) {
				result.MissingBrowserPosition++
				continue
			}
			delta := frame.VPos - int64(draw.AnchorVPos)
			timelineX := float64(float32(draw.Rect[0] + float32(delta)*float32(draw.SpeedX)))
			timelineY := float64(draw.Rect[1])
			browserX := *browserDraw.PositionX - *browserDraw.ImagePadding
			browserY := *browserDraw.PositionY - *browserDraw.ImagePadding
			browserPixelX, browserPixelY := timelineProjectPosition(draw, browserX, browserY, reference.Width, reference.Height)
			timelinePixelX, timelinePixelY := timelineProjectPosition(draw, timelineX, timelineY, reference.Width, reference.Height)
			errorX := math.Abs(browserPixelX - timelinePixelX)
			errorY := math.Abs(browserPixelY - timelinePixelY)
			sample := timelinePositionSample{
				Frame: frame.Frame, VPos: frame.VPos,
				OwnerOrder: key.ownerOrder, CommentIndex: key.index,
				BrowserRectX: browserPixelX, BrowserRectY: browserPixelY,
				TimelineRectX: timelinePixelX, TimelineRectY: timelinePixelY,
				AbsErrorX: errorX, AbsErrorY: errorY,
			}
			result.Samples++
			if errorX > result.MaxAbsErrorX || result.WorstX == nil {
				copy := sample
				result.WorstX = &copy
				result.MaxAbsErrorX = errorX
			}
			if errorY > result.MaxAbsErrorY || result.WorstY == nil {
				copy := sample
				result.WorstY = &copy
				result.MaxAbsErrorY = errorY
			}
			if errorX > tolerance {
				result.OverToleranceX++
			}
			if errorY > tolerance {
				result.OverToleranceY++
			}
			result.LargestOutliers = append(result.LargestOutliers, sample)
		}
	}
	sort.Slice(result.LargestOutliers, func(i, j int) bool {
		return math.Max(result.LargestOutliers[i].AbsErrorX, result.LargestOutliers[i].AbsErrorY) > math.Max(result.LargestOutliers[j].AbsErrorX, result.LargestOutliers[j].AbsErrorY)
	})
	if len(result.LargestOutliers) > 20 {
		result.LargestOutliers = result.LargestOutliers[:20]
	}
	return result
}

func timelineProjectPosition(draw TimelineDraw, x, y float64, width, height int) (float64, float64) {
	p := draw.Projection
	clipX := float32(p[0]*float32(x) + p[4]*float32(y) + p[12])
	clipY := float32(p[1]*float32(x) + p[5]*float32(y) + p[13])
	clipW := float32(p[3]*float32(x) + p[7]*float32(y) + p[15])
	ndcX := float64(clipX / clipW)
	ndcY := float64(clipY / clipW)
	return (ndcX + 1) * float64(width) / 2, (1 - ndcY) * float64(height) / 2
}

func compareTimelineReferenceDrawOrder(scene CommentTimeline, reference TimelineReference) timelineDrawOrderComparison {
	result := timelineDrawOrderComparison{SchemaVersion: 1, Frames: len(reference.FrameRefs)}
	for _, frame := range reference.FrameRefs {
		browser := make([][2]uint32, 0, len(frame.DrawOrder))
		for _, draw := range frame.DrawOrder {
			ownerOrder := uint32(0)
			if draw.Owner {
				ownerOrder = 1
			}
			browser = append(browser, [2]uint32{ownerOrder, uint32(draw.CommentIndex)})
		}
		timeline := make([][2]uint32, 0)
		seen := make(map[timelinePositionKey]struct{})
		for _, draw := range scene.Draws {
			if int64(draw.StartVPos) > frame.VPos || frame.VPos >= int64(draw.EndVPos) {
				continue
			}
			key := timelinePositionKey{ownerOrder: draw.OwnerOrder, index: draw.CommentIndex}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			timeline = append(timeline, [2]uint32{key.ownerOrder, key.index})
		}
		result.BrowserDraws += uint64(len(browser))
		result.TimelineDraws += uint64(len(timeline))
		if !equalTimelineDrawOrder(browser, timeline) {
			result.MismatchFrames++
			if result.FirstMismatch == nil {
				result.FirstMismatch = &timelineDrawOrderMismatch{Frame: frame.Frame, VPos: frame.VPos, Browser: browser, Timeline: timeline}
			}
		}
	}
	return result
}

func equalTimelineDrawOrder(left, right [][2]uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestCompareTimelineReferencePositionsChecksProjectedTextureRect(t *testing.T) {
	browserX, browserY, imagePadding := 13.0, 44.0, 4.0
	reference := TimelineReference{FrameRefs: []timelineReferenceFrame{{
		Frame: 4, VPos: 4,
		DrawOrder: []timelineReferenceDraw{{CommentIndex: 2, PositionX: &browserX, PositionY: &browserY, ImagePadding: &imagePadding}},
	}}}
	draw := TimelineDraw{
		StartVPos: 0, EndVPos: 10, AnchorVPos: 0, CommentIndex: 2,
		Rect: [4]float32{8, 40, 20, 10}, Projection: [16]float32{2.0 / 100, 0, 0, 0, 0, -2.0 / 50, 0, 0, 0, 0, 1, 0, -1, 1, 0, 1},
		AnchorX: 10, SpeedX: 0.25,
	}
	scene := CommentTimeline{Draws: []TimelineDraw{draw}}

	result := compareTimelineReferencePositions(scene, reference, 1.0/256.0)
	if result.Samples != 1 || result.MissingBrowserPosition != 0 || result.MaxAbsErrorX != 0 || result.MaxAbsErrorY != 0 || result.OverToleranceX != 0 || result.OverToleranceY != 0 {
		t.Fatalf("position comparison=%+v", result)
	}
}

func TestCompareTimelineReferencePositionsReportsMissingAndOutOfRange(t *testing.T) {
	browserX, browserY, imagePadding := 100.0, 100.0, 4.0
	reference := TimelineReference{FrameRefs: []timelineReferenceFrame{
		{Frame: 10, VPos: 10, DrawOrder: []timelineReferenceDraw{{CommentIndex: 2, PositionX: &browserX, PositionY: &browserY, ImagePadding: &imagePadding}}},
		{Frame: 11, VPos: 11, DrawOrder: []timelineReferenceDraw{{CommentIndex: 2}}},
	}}
	scene := CommentTimeline{Draws: []TimelineDraw{{
		StartVPos: 0, EndVPos: 10, CommentIndex: 2, AnchorX: 0,
	}}}

	result := compareTimelineReferencePositions(scene, reference, 1.0/256.0)
	if result.Samples != 0 || result.MissingBrowserPosition != 2 {
		t.Fatalf("position comparison=%+v", result)
	}
}

func TestCompareTimelineReferenceDrawOrderUsesExactIntervals(t *testing.T) {
	reference := TimelineReference{FrameRefs: []timelineReferenceFrame{
		{Frame: 0, VPos: 0, DrawOrder: []timelineReferenceDraw{{CommentIndex: 3}, {CommentIndex: 4, Owner: true}}},
		{Frame: 1, VPos: 1, DrawOrder: []timelineReferenceDraw{{CommentIndex: 4, Owner: true}}},
	}}
	scene := CommentTimeline{Draws: []TimelineDraw{
		{StartVPos: 0, EndVPos: 1, OwnerOrder: 0, CommentIndex: 3},
		{StartVPos: 0, EndVPos: 2, OwnerOrder: 1, CommentIndex: 4},
	}}

	result := compareTimelineReferenceDrawOrder(scene, reference)
	if result.Frames != 2 || result.MismatchFrames != 0 || result.BrowserDraws != 3 || result.TimelineDraws != 3 {
		t.Fatalf("draw order comparison=%+v", result)
	}
}

func TestCompareTimelineReferenceDrawOrderReportsReorder(t *testing.T) {
	reference := TimelineReference{FrameRefs: []timelineReferenceFrame{{
		Frame: 8, VPos: 26,
		DrawOrder: []timelineReferenceDraw{{CommentIndex: 4}, {CommentIndex: 3}},
	}}}
	scene := CommentTimeline{Draws: []TimelineDraw{
		{StartVPos: 0, EndVPos: 100, OwnerOrder: 0, CommentIndex: 3},
		{StartVPos: 0, EndVPos: 100, OwnerOrder: 0, CommentIndex: 4},
	}}

	result := compareTimelineReferenceDrawOrder(scene, reference)
	if result.MismatchFrames != 1 || result.FirstMismatch == nil || result.FirstMismatch.Frame != 8 {
		t.Fatalf("draw order comparison=%+v", result)
	}
}
