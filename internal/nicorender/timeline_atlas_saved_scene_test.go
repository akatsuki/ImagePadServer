package nicorender

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	timelineT0SceneSHA256       = "dbbd2a75b9cdafe9d89081ee0d7448e27e607efbb91a67e59680af2459fd631a"
	timelineT0BaselineSHA256    = "18c30a8cc0da989284709078fbc42dadec96ccbaf1e6c44e050f6259a20ef80e"
	timelineT0ExpectedSceneName = "sync-scene.json"
)

type timelineSavedSceneRun struct {
	Name           string `json:"name"`
	HelperPath     string `json:"helper_path"`
	HelperSHA256   string `json:"helper_sha256"`
	LayoutRequest  string `json:"layout_requested"`
	LayoutActual   string `json:"layout_actual"`
	FallbackReason string `json:"layout_fallback_reason,omitempty"`
	Backend        string `json:"backend"`
	Adapter        string `json:"adapter"`
	ReadbackSlots  int    `json:"readback_slots"`
	PageCount      int    `json:"asset_page_count"`
	SourceBytes    uint64 `json:"asset_source_bytes"`
	AllocatedBytes uint64 `json:"asset_allocated_bytes"`
	Frames         uint32 `json:"completed_frames"`
	RGBABytes      int64  `json:"rgba_bytes"`
	RGBASHA256     string `json:"rgba_sha256"`
	ReportPath     string `json:"report_path"`
	StderrPath     string `json:"stderr_path"`
}

// TestTimelineAtlasSavedT0Parity opts into a full-resolution, saved-scene
// comparison against the pre-atlas helper. RGBA is hashed while streaming so
// the multi-gigabyte frame output is never retained as an artifact.
func TestTimelineAtlasSavedT0Parity(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_ATLAS_PARITY") != "1" {
		t.Skip("opt-in; set NICO_TIMELINE_ATLAS_PARITY=1 with the pinned T0 inputs")
	}
	scenePath := requiredTimelineEnv(t, "NICO_TIMELINE_ATLAS_SCENE")
	baselinePath := requiredTimelineEnv(t, "NICO_TIMELINE_ATLAS_BASELINE_HELPER")
	candidatePath := requiredTimelineEnv(t, "NICO_TIMELINE_ATLAS_CANDIDATE_HELPER")
	artifactDir := requiredTimelineEnv(t, "NICO_TIMELINE_ATLAS_ARTIFACTS")
	for path, label := range map[string]string{scenePath: "saved scene", baselinePath: "T0 baseline helper"} {
		assertOutsideTimelineRepo(t, path, label)
	}
	assertOutsideTimelineRepo(t, artifactDir, "artifact directory")
	if info, err := os.Stat(candidatePath); err != nil || info.IsDir() {
		t.Fatalf("candidate helper is unavailable: %s (%v)", candidatePath, err)
	}
	if filepath.Base(scenePath) != timelineT0ExpectedSceneName {
		t.Fatalf("saved scene name=%q, want %q", filepath.Base(scenePath), timelineT0ExpectedSceneName)
	}
	sceneBytes, sceneErr := os.ReadFile(scenePath)
	if sceneErr != nil {
		t.Fatalf("read saved T0 scene: %v", sceneErr)
	}
	if got := sha256Hex(sceneBytes); got != timelineT0SceneSHA256 {
		t.Fatalf("saved scene SHA-256=%s, want pinned T0 %s", got, timelineT0SceneSHA256)
	}
	baselineHash, err := sha256File(baselinePath)
	if err != nil {
		t.Fatalf("hash T0 baseline helper: %v", err)
	}
	if baselineHash != timelineT0BaselineSHA256 {
		t.Fatalf("T0 baseline helper SHA-256=%s, want pinned T0 %s", baselineHash, timelineT0BaselineSHA256)
	}
	candidateHash, err := sha256File(candidatePath)
	if err != nil {
		t.Fatalf("hash candidate helper: %v", err)
	}
	var scene CommentTimeline
	if err := json.Unmarshal(sceneBytes, &scene); err != nil {
		t.Fatalf("decode saved T0 scene JSON: %v", err)
	}
	if err := ValidateCommentTimeline(scene); err != nil {
		t.Fatalf("validate saved T0 scene: %v", err)
	}
	var wire bytes.Buffer
	if err := WriteCommentTimeline(&wire, scene); err != nil {
		t.Fatalf("encode saved scene as NCT1: %v", err)
	}
	backend := strings.TrimSpace(os.Getenv("NICO_TIMELINE_GPU_BACKEND"))
	if backend == "" {
		backend = "dx12"
	}
	slots := 3
	if raw := strings.TrimSpace(os.Getenv("NICO_TIMELINE_READBACK_SLOTS")); raw != "" {
		slots, err = strconv.Atoi(raw)
		if err != nil || slots < 1 || slots > 3 {
			t.Fatalf("NICO_TIMELINE_READBACK_SLOTS=%q must be 1, 2, or 3", raw)
		}
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("create parity artifact directory: %v", err)
	}
	if !directoryIsEmpty(t, artifactDir) {
		t.Fatalf("parity artifact directory must be empty: %s", artifactDir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	runs := make([]timelineSavedSceneRun, 0, 3)
	for _, variant := range []struct {
		name, helper, layout string
	}{
		{name: "t0-separate", helper: baselinePath, layout: "separate"},
		{name: "candidate-separate", helper: candidatePath, layout: "separate"},
		{name: "candidate-atlas", helper: candidatePath, layout: "atlas"},
	} {
		expectedRGBABytes := int64(scene.Header.Width) * int64(scene.Header.Height) * 4 * int64(scene.Header.FrameCount)
		run, runErr := runTimelineSavedSceneHelper(ctx, artifactDir, variant.name, variant.helper, variant.layout, backend, slots, wire.Bytes(), uint32(scene.Header.FrameCount), expectedRGBABytes)
		if runErr != nil {
			t.Fatalf("run %s: %v", variant.name, runErr)
		}
		if run.HelperSHA256 == baselineHash && variant.name != "t0-separate" {
			t.Fatalf("candidate %s unexpectedly uses the T0 helper binary", variant.name)
		}
		runs = append(runs, run)
	}
	if runs[0].RGBABytes != runs[1].RGBABytes || runs[0].RGBASHA256 != runs[1].RGBASHA256 {
		t.Fatalf("candidate separate output differs from pinned T0: T0 bytes/hash=%d/%s candidate=%d/%s", runs[0].RGBABytes, runs[0].RGBASHA256, runs[1].RGBABytes, runs[1].RGBASHA256)
	}
	if runs[1].RGBABytes != runs[2].RGBABytes || runs[1].RGBASHA256 != runs[2].RGBASHA256 {
		t.Fatalf("atlas output differs from candidate separate: separate bytes/hash=%d/%s atlas=%d/%s", runs[1].RGBABytes, runs[1].RGBASHA256, runs[2].RGBABytes, runs[2].RGBASHA256)
	}
	for _, run := range runs[1:] {
		if run.Backend != runs[0].Backend || run.Adapter != runs[0].Adapter || run.ReadbackSlots != runs[0].ReadbackSlots {
			t.Fatalf("parity runs used different GPU conditions: baseline=%+v candidate=%+v", runs[0], run)
		}
	}
	manifest := struct {
		SchemaVersion  int                     `json:"schema_version"`
		CreatedAt      time.Time               `json:"created_at"`
		ScenePath      string                  `json:"scene_path"`
		SceneSHA256    string                  `json:"scene_sha256"`
		SceneWireBytes int                     `json:"scene_wire_bytes"`
		BaselineHelper string                  `json:"baseline_helper_path"`
		BaselineSHA256 string                  `json:"baseline_helper_sha256"`
		Candidate      string                  `json:"candidate_helper_path"`
		CandidateSHA   string                  `json:"candidate_helper_sha256"`
		Backend        string                  `json:"backend"`
		Runs           []timelineSavedSceneRun `json:"runs"`
		ExactParity    bool                    `json:"exact_rgba_parity"`
		RetainedRGBA   []string                `json:"retained_rgba_files"`
	}{
		SchemaVersion: 1, CreatedAt: time.Now().UTC(), ScenePath: scenePath, SceneSHA256: timelineT0SceneSHA256,
		SceneWireBytes: wire.Len(), BaselineHelper: baselinePath, BaselineSHA256: baselineHash,
		Candidate: candidatePath, CandidateSHA: candidateHash, Backend: backend, Runs: runs, ExactParity: true,
		RetainedRGBA: []string{},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode parity manifest: %v", err)
	}
	manifestPath := filepath.Join(artifactDir, "atlas-saved-scene-parity.json")
	if err := writeTimelineArtifactExclusive(manifestPath, append(manifestBytes, '\n')); err != nil {
		t.Fatalf("write parity manifest: %v", err)
	}
	t.Logf("exact old/separate/atlas parity passed: frames=%d rgba_bytes=%d sha256=%s backend=%s adapter=%q manifest=%s", scene.Header.FrameCount, runs[0].RGBABytes, runs[0].RGBASHA256, backend, runs[0].Adapter, manifestPath)
}

func runTimelineSavedSceneHelper(ctx context.Context, artifactDir, name, helper, layout, backend string, slots int, wire []byte, frameCount uint32, expectedRGBABytes int64) (timelineSavedSceneRun, error) {
	result := timelineSavedSceneRun{Name: name, HelperPath: helper, LayoutRequest: layout, ReadbackSlots: slots}
	helperHash, err := sha256File(helper)
	if err != nil {
		return result, fmt.Errorf("hash helper: %w", err)
	}
	result.HelperSHA256 = helperHash
	result.ReportPath = filepath.Join(artifactDir, name+"-runtime.json")
	result.StderrPath = filepath.Join(artifactDir, name+"-stderr.log")
	for _, path := range []string{result.ReportPath, result.StderrPath} {
		if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("artifact path already exists or cannot be inspected: %s (%v)", path, err)
		}
	}
	stderr, err := os.OpenFile(result.StderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return result, fmt.Errorf("create stderr log: %w", err)
	}
	args := []string{"--stdin", "--backend", backend, "--readback-slots", strconv.Itoa(slots), "--report", result.ReportPath}
	if helperHash != timelineT0BaselineSHA256 || layout != "separate" {
		args = append(args, "--asset-layout", layout)
	}
	cmd := exec.CommandContext(ctx, helper, args...)
	cmd.Stdin = bytes.NewReader(wire)
	cmd.Stderr = stderr
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stderr.Close()
		return result, fmt.Errorf("open RGBA stream: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stderr.Close()
		return result, fmt.Errorf("start helper: %w", err)
	}
	streamHash := sha256.New()
	result.RGBABytes, err = io.Copy(streamHash, stdout)
	if waitErr := cmd.Wait(); err == nil {
		err = waitErr
	}
	if closeErr := stderr.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return result, fmt.Errorf("helper exited: %w", err)
	}
	result.RGBASHA256 = hex.EncodeToString(streamHash.Sum(nil))
	if result.RGBABytes != expectedRGBABytes {
		return result, fmt.Errorf("helper emitted %d RGBA bytes, want %d", result.RGBABytes, expectedRGBABytes)
	}
	reportBytes, err := os.ReadFile(result.ReportPath)
	if err != nil {
		return result, fmt.Errorf("read runtime report: %w", err)
	}
	var report struct {
		Schema               int     `json:"schema"`
		Protocol             string  `json:"protocol"`
		Renderer             string  `json:"renderer"`
		Version              string  `json:"version"`
		Backend              string  `json:"backend"`
		AdapterName          string  `json:"adapterName"`
		AdapterType          string  `json:"adapterType"`
		ReadbackSlots        int     `json:"readbackSlots"`
		RequestedAssetLayout string  `json:"requestedAssetLayout"`
		AssetLayout          string  `json:"assetLayout"`
		AssetLayoutFallback  string  `json:"assetLayoutFallbackReason"`
		AssetPageCount       int     `json:"assetPageCount"`
		AssetSourceBytes     uint64  `json:"assetSourceBytes"`
		AssetAllocatedBytes  uint64  `json:"assetAllocatedBytes"`
		CompletedFrames      uint32  `json:"completedFrames"`
		Error                *string `json:"error"`
	}
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		return result, fmt.Errorf("decode runtime report: %w", err)
	}
	if report.Schema != 1 || report.Protocol != "NCT1" || report.Renderer != "wgpu" || report.Version == "" || report.Backend == "" || report.AdapterName == "" || report.AdapterType == "" || report.ReadbackSlots != slots || report.CompletedFrames != frameCount || report.Error != nil {
		return result, fmt.Errorf("runtime report is incomplete or unsuccessful: %+v", report)
	}
	if err := ValidateTimelineAssetLayoutReport(layout, report.RequestedAssetLayout, report.AssetLayout, report.AssetLayoutFallback, report.AssetPageCount, report.AssetSourceBytes, report.AssetAllocatedBytes); err != nil {
		return result, fmt.Errorf("validate layout report: %w", err)
	}
	if report.AssetLayout == "" {
		report.AssetLayout = "separate"
	}
	result.LayoutRequest, result.LayoutActual, result.FallbackReason = layout, report.AssetLayout, report.AssetLayoutFallback
	result.Backend, result.Adapter = report.Backend, report.AdapterName
	result.PageCount, result.SourceBytes, result.AllocatedBytes = report.AssetPageCount, report.AssetSourceBytes, report.AssetAllocatedBytes
	return result, nil
}

func directoryIsEmpty(t *testing.T, path string) bool {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read artifact directory: %v", err)
	}
	return len(entries) == 0
}
