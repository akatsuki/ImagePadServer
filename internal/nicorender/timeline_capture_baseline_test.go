package nicorender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

type timelineCaptureBaselineManifest struct {
	SchemaVersion  int                     `json:"schema_version"`
	Seed           uint32                  `json:"seed"`
	SnapshotSHA256 string                  `json:"snapshot_sha256"`
	BundleSHA256   string                  `json:"bundle_sha256"`
	SceneSHA256    string                  `json:"scene_sha256"`
	SceneBytes     int                     `json:"scene_bytes"`
	Width          int                     `json:"width"`
	Height         int                     `json:"height"`
	DurationMs     int64                   `json:"duration_ms"`
	FPSNum         int64                   `json:"fps_num"`
	FPSDen         int64                   `json:"fps_den"`
	Metrics        *TimelineCaptureMetrics `json:"sprite_capture_metrics,omitempty"`
}

// TestCaptureTimelineBaselineScene saves one seeded, lossless scene outside
// the checkout so later readback modes can be compared against exact bytes.
func TestCaptureTimelineBaselineScene(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_BASELINE_SCENE") != "1" {
		t.Skip("set NICO_TIMELINE_BASELINE_SCENE=1 to capture an external seeded NCT1 baseline")
	}
	snapshotPath := strings.TrimSpace(os.Getenv("IMAGEPAD_NICO_PERF_SNAPSHOT"))
	outputPath := strings.TrimSpace(os.Getenv("NICO_TIMELINE_BASELINE_SCENE_OUTPUT"))
	if snapshotPath == "" || outputPath == "" {
		t.Fatal("IMAGEPAD_NICO_PERF_SNAPSHOT and NICO_TIMELINE_BASELINE_SCENE_OUTPUT are required")
	}
	snapshotBytes, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotBytes, &snapshot); err != nil {
		t.Fatalf("decode timeline baseline snapshot: %v", err)
	}
	snapshot, err = niconico.NormalizeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("normalize timeline baseline snapshot: %v", err)
	}
	options := RenderOptions{
		Width: 1920, Height: 1080, DurationMs: 6000, FPSNum: 30, FPSDen: 1,
		Transport: "binary", BrowserPath: strings.TrimSpace(os.Getenv("NICO_TIMELINE_BROWSER")),
	}
	seed := uint32(0x4e49434f)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	scene, report, err := captureCommentTimelineWithBatchLimit(ctx, snapshot, options, &seed, timelineCaptureBatchDefault)
	if err != nil {
		t.Fatalf("capture seeded timeline baseline: %v", err)
	}
	data, err := json.Marshal(scene)
	if err != nil {
		t.Fatalf("serialize seeded timeline baseline: %v", err)
	}
	sceneDigest := sha256.Sum256(data)
	snapshotDigest := sha256.Sum256(snapshotBytes)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		t.Fatalf("create baseline output directory: %v", err)
	}
	if err := writeExclusiveSynced(outputPath, data); err != nil {
		t.Fatalf("write seeded timeline baseline: %v", err)
	}
	manifest := timelineCaptureBaselineManifest{
		SchemaVersion: 1, Seed: 0x4e49434f,
		SnapshotSHA256: hex.EncodeToString(snapshotDigest[:]), BundleSHA256: report.BundleSHA256,
		SceneSHA256: hex.EncodeToString(sceneDigest[:]), SceneBytes: len(data),
		Width: options.Width, Height: options.Height, DurationMs: options.DurationMs,
		FPSNum: options.FPSNum, FPSDen: options.FPSDen, Metrics: report.SpriteCaptureMetrics,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("serialize seeded timeline manifest: %v", err)
	}
	if err := writeExclusiveSynced(outputPath+".manifest.json", append(manifestBytes, '\n')); err != nil {
		t.Fatalf("write seeded timeline manifest: %v", err)
	}
	t.Logf("seeded timeline baseline scene_sha256=%s bytes=%d output=%s", manifest.SceneSHA256, manifest.SceneBytes, outputPath)
}

func writeExclusiveSynced(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
