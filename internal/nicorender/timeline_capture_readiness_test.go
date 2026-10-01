package nicorender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

const (
	timelineReadinessOptIn       = "NICO_TIMELINE_READINESS_CAPTURE"
	timelineReadinessOutputPath  = "NICO_TIMELINE_READINESS_OUTPUT"
	timelineReadinessSnapshot    = "imagepad-nico-perf-20260916/snapshot.json"
	timelineReadinessSnapshotSHA = "b7be86d86016175c99925f5f1b5651d43279eeffcc54deb1c22c5b2e6e15e946"
	timelineReadinessSeed        = uint32(0x4e49434f)
)

type timelineReadinessDeclaration struct {
	OwnerOrder   uint32 `json:"owner_order"`
	CommentIndex uint32 `json:"comment_index"`
	StartVPos    int64  `json:"start_vpos"`
	EndVPos      int64  `json:"end_vpos"`
}

type timelineReadinessFixture struct {
	ProvenanceHashes        map[string]string              `json:"provenance_hashes"`
	Render                  timelineReadinessRender        `json:"render"`
	Declarations            []timelineReadinessDeclaration `json:"declarations"`
	ReadyByPrefix           []int64                        `json:"ready_by_prefix"`
	AvailableFramesByPrefix []uint32                       `json:"available_frames_by_prefix"`
}

type timelineReadinessRender struct {
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	DurationMs int64  `json:"duration_ms"`
	FPSNum     int64  `json:"fps_num"`
	FPSDen     int64  `json:"fps_den"`
	FrameCount int64  `json:"frame_count"`
	Seed       uint32 `json:"seed"`
}

type timelineReadinessDrawGroup struct {
	OwnerOrder   uint32
	CommentIndex uint32
	Intervals    map[[2]int64]int
}

// TestCaptureTimelineReadinessFixture records compact frame-availability data
// from the preserved snapshot using the current browser capture implementation.
func TestCaptureTimelineReadinessFixture(t *testing.T) {
	if os.Getenv(timelineReadinessOptIn) != "1" {
		t.Skipf("set %s=1 to run the preserved-snapshot browser diagnostic", timelineReadinessOptIn)
	}

	outputPath := strings.TrimSpace(os.Getenv(timelineReadinessOutputPath))
	if outputPath == "" || !filepath.IsAbs(outputPath) {
		t.Fatalf("%s must be an explicit absolute output path", timelineReadinessOutputPath)
	}
	browserPath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER"))
	if browserPath == "" {
		t.Fatal("NICO_TIMELINE_BROWSER must point to the Chrome executable")
	}

	snapshotPath := filepath.Join(os.TempDir(), filepath.FromSlash(timelineReadinessSnapshot))
	snapshotBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read preserved snapshot %s: %v", snapshotPath, err)
	}
	snapshotDigest := sha256.Sum256(snapshotBytes)
	snapshotSHA := hex.EncodeToString(snapshotDigest[:])
	if snapshotSHA != timelineReadinessSnapshotSHA {
		t.Fatalf("preserved snapshot SHA-256=%s, want %s", snapshotSHA, timelineReadinessSnapshotSHA)
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	scene, report, err := captureCommentTimelineSeededForTest(ctx, snapshot, options, timelineReadinessSeed)
	if err != nil {
		t.Fatalf("capture preserved snapshot for readiness diagnostic: %v", err)
	}

	exclusiveEnd, err := timelineStreamFrameClock(uint32(report.FrameCount), uint32(options.FPSNum), uint32(options.FPSDen))
	if err != nil {
		t.Fatalf("compute exclusive frame-clock end E: %v", err)
	}
	groups := make(map[[2]uint32]*timelineReadinessDrawGroup)
	for _, draw := range scene.Draws {
		key := [2]uint32{draw.OwnerOrder, draw.CommentIndex}
		group := groups[key]
		if group == nil {
			group = &timelineReadinessDrawGroup{
				OwnerOrder: draw.OwnerOrder, CommentIndex: draw.CommentIndex,
				Intervals: make(map[[2]int64]int),
			}
			groups[key] = group
		}
		interval := [2]int64{
			clipTimelineVPos(draw.StartVPos, exclusiveEnd),
			clipTimelineVPos(draw.EndVPos, exclusiveEnd),
		}
		group.Intervals[interval]++
	}
	keys := make([][2]uint32, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	var inconsistent []string
	for _, key := range keys {
		group := groups[key]
		if len(group.Intervals) != 1 {
			inconsistent = append(inconsistent, fmt.Sprintf("owner=%d comment=%d intervals=%s", key[0], key[1], formatTimelineReadinessIntervals(group.Intervals)))
		}
	}
	if report.EligibleComments != len(groups) || len(inconsistent) > 0 {
		t.Fatalf("readiness diagnostic stopped before derivation: eligible_comments=%d distinct_draw_groups=%d draws=%d clipped_interval_groups=%s inconsistent_groups=%v; no zero-Draw declarations were inferred",
			report.EligibleComments, len(groups), len(scene.Draws), formatTimelineReadinessIntervalSummary(groups), inconsistent)
	}

	declarations := make([]timelineReadinessDeclaration, 0, len(keys))
	streamDeclarations := make([]TimelineStreamDeclaration, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		var interval [2]int64
		for clipped := range group.Intervals {
			interval = clipped
		}
		declarations = append(declarations, timelineReadinessDeclaration{
			OwnerOrder: key[0], CommentIndex: key[1], StartVPos: interval[0], EndVPos: interval[1],
		})
		streamDeclarations = append(streamDeclarations, TimelineStreamDeclaration{
			OwnerOrder: key[0], CommentIndex: key[1], StartVPos: int32(interval[0]), EndVPos: int32(interval[1]),
		})
	}
	readyByPrefix := make([]int64, len(streamDeclarations)+1)
	availableFramesByPrefix := make([]uint32, len(streamDeclarations)+1)
	for prefix := range readyByPrefix {
		ready := readyTimelineStream(streamDeclarations, uint32(prefix), exclusiveEnd)
		readyByPrefix[prefix] = ready
		for frame := uint32(0); frame < uint32(report.FrameCount); frame++ {
			frameVPos, err := timelineStreamFrameClock(frame, uint32(options.FPSNum), uint32(options.FPSDen))
			if err != nil {
				t.Fatalf("compute FrameClock(%d): %v", frame, err)
			}
			if frameVPos < ready {
				availableFramesByPrefix[prefix]++
			}
		}
	}

	captureHash, err := timelineReadinessFileSHA256("timeline_capture.go")
	if err != nil {
		t.Fatal(err)
	}
	runtimeHash, err := timelineReadinessFileSHA256(filepath.Join("assets", "timeline.js"))
	if err != nil {
		t.Fatal(err)
	}
	streamHash, err := timelineReadinessFileSHA256("timeline_stream_format.go")
	if err != nil {
		t.Fatal(err)
	}
	fixture := timelineReadinessFixture{
		ProvenanceHashes: map[string]string{
			"snapshot_sha256":                  snapshotSHA,
			"timeline_capture_go_sha256":       captureHash,
			"timeline_js_sha256":               runtimeHash,
			"timeline_stream_format_go_sha256": streamHash,
			"renderer_bundle_sha256":           report.BundleSHA256,
		},
		Render: timelineReadinessRender{
			Width: options.Width, Height: options.Height, DurationMs: options.DurationMs,
			FPSNum: options.FPSNum, FPSDen: options.FPSDen, FrameCount: report.FrameCount,
			Seed: timelineReadinessSeed,
		},
		Declarations: declarations, ReadyByPrefix: readyByPrefix,
		AvailableFramesByPrefix: availableFramesByPrefix,
	}
	fixtureBytes, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatalf("marshal readiness fixture: %v", err)
	}
	fixtureBytes = append(fixtureBytes, '\n')
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		t.Fatalf("create readiness fixture directory: %v", err)
	}
	if err := writeExclusiveSynced(outputPath, fixtureBytes); err != nil {
		t.Fatalf("write readiness fixture exclusively: %v", err)
	}
	t.Logf("readiness captured: eligible_comments=%d distinct_draw_groups=%d draws=%d declarations=%d prefixes=%d output=%s",
		report.EligibleComments, len(groups), len(scene.Draws), len(declarations), len(readyByPrefix), outputPath)
	t.Logf("ready/frame summary: prefix0=%d/%d prefix%d=%d/%d; fixture_bytes=%d",
		readyByPrefix[0], availableFramesByPrefix[0], len(declarations), readyByPrefix[len(declarations)],
		availableFramesByPrefix[len(declarations)], len(fixtureBytes))
}

func formatTimelineReadinessIntervals(intervals map[[2]int64]int) string {
	keys := make([][2]int64, 0, len(intervals))
	for interval := range intervals {
		keys = append(keys, interval)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	parts := make([]string, 0, len(keys))
	for _, interval := range keys {
		parts = append(parts, fmt.Sprintf("[%d,%d)x%d", interval[0], interval[1], intervals[interval]))
	}
	return strings.Join(parts, ",")
}

func formatTimelineReadinessIntervalSummary(groups map[[2]uint32]*timelineReadinessDrawGroup) string {
	counts := make(map[[2]int64]int)
	for _, group := range groups {
		for interval, drawCount := range group.Intervals {
			counts[interval] += drawCount
		}
	}
	return formatTimelineReadinessIntervals(counts)
}

func timelineReadinessFileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("hash readiness provenance %s: %w", path, err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
