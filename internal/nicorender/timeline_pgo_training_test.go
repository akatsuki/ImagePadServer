package nicorender

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/niconico"
)

type pgoTrainingMaterial struct {
	ID             string `json:"id"`
	Coverage       string `json:"coverage"`
	SourceSHA256   string `json:"sourceSha256"`
	SnapshotSHA256 string `json:"snapshotSha256"`
	Seed           int64  `json:"seed"`
}

type pgoTrainingManifest struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Role          string                `json:"role"`
	Materials     []pgoTrainingMaterial `json:"materials"`
}

type pgoWorkloadMaterial struct {
	ID            string `json:"id"`
	Coverage      string `json:"coverage"`
	SourcePath    string `json:"sourcePath"`
	SnapshotPath  string `json:"snapshotPath"`
	Protocol      string `json:"protocol"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	DurationMs    int64  `json:"durationMs"`
	FPSNum        int64  `json:"fpsNum"`
	FPSDen        int64  `json:"fpsDen"`
	Backend       string `json:"backend"`
	ReadbackSlots int    `json:"readbackSlots"`
	AssetLayout   string `json:"assetLayout"`
}

type pgoWorkloadCatalog struct {
	SchemaVersion int                   `json:"schemaVersion"`
	BrowserPath   string                `json:"browserPath"`
	Training      []pgoWorkloadMaterial `json:"training"`
	Evaluation    []pgoWorkloadMaterial `json:"evaluation"`
}

type pgoRuntimeReport struct {
	Protocol             string  `json:"protocol"`
	Renderer             string  `json:"renderer"`
	Version              string  `json:"version"`
	RequestedBackend     string  `json:"requestedBackend"`
	Backend              string  `json:"backend"`
	AdapterName          string  `json:"adapterName"`
	ReadbackSlots        int     `json:"readbackSlots"`
	RequestedAssetLayout string  `json:"requestedAssetLayout"`
	AssetLayout          string  `json:"assetLayout"`
	CompletedFrames      uint32  `json:"completedFrames"`
	Error                *string `json:"error"`
}

type pgoWorkloadRun struct {
	ID                      string           `json:"id"`
	Coverage                string           `json:"coverage"`
	Protocol                string           `json:"protocol"`
	Width                   int              `json:"width"`
	Height                  int              `json:"height"`
	DurationMs              int64            `json:"durationMs"`
	FPSNum                  int64            `json:"fpsNum"`
	FPSDen                  int64            `json:"fpsDen"`
	Backend                 string           `json:"backend"`
	ReadbackSlots           int              `json:"readbackSlots"`
	AssetLayout             string           `json:"assetLayout"`
	SourcePath              string           `json:"sourcePath"`
	SourceSHA256            string           `json:"sourceSha256"`
	SnapshotPath            string           `json:"snapshotPath"`
	SnapshotSHA256          string           `json:"snapshotSha256"`
	ScenePath               string           `json:"scenePath"`
	SceneBytes              int64            `json:"sceneBytes"`
	SceneSHA256             string           `json:"sceneSha256"`
	CaptureFrameCount       int64            `json:"captureFrameCount"`
	CaptureEligibleComments int              `json:"captureEligibleComments"`
	CaptureAssets           int              `json:"captureAssets"`
	CaptureDraws            int              `json:"captureDraws"`
	RuntimeReportPath       string           `json:"runtimeReportPath"`
	RuntimeReport           pgoRuntimeReport `json:"runtimeReport"`
	WallSeconds             float64          `json:"wallSeconds"`
}

type pgoWorkloadRunDocument struct {
	SchemaVersion int              `json:"schemaVersion"`
	HelperPath    string           `json:"helperPath"`
	HelperSHA256  string           `json:"helperSha256"`
	Runs          []pgoWorkloadRun `json:"runs"`
}

var pgoRequiredCoverage = []string{"short", "long", "dense", "NCT1", "NCT2"}

func validatePGOWorkloadCatalog(catalog pgoWorkloadCatalog, training, evaluation pgoTrainingManifest) error {
	if catalog.SchemaVersion != 1 {
		return fmt.Errorf("workload catalog schemaVersion must be 1")
	}
	if !filepath.IsAbs(catalog.BrowserPath) || strings.TrimSpace(catalog.BrowserPath) == "" {
		return fmt.Errorf("workload catalog browserPath must be absolute")
	}
	if training.SchemaVersion != 1 || training.Role != "training" || evaluation.SchemaVersion != 1 || evaluation.Role != "evaluation" {
		return fmt.Errorf("training/evaluation manifest schema or role is invalid")
	}
	if err := validatePGOManifestCoverage(training); err != nil {
		return fmt.Errorf("training manifest: %w", err)
	}
	if err := validatePGOManifestCoverage(evaluation); err != nil {
		return fmt.Errorf("evaluation manifest: %w", err)
	}
	if err := validatePGOWorkloadRole("training", catalog.Training, training); err != nil {
		return err
	}
	if err := validatePGOWorkloadRole("evaluation", catalog.Evaluation, evaluation); err != nil {
		return err
	}
	trainingPairs := make(map[string]struct{}, len(training.Materials))
	for _, material := range training.Materials {
		trainingPairs[strings.ToLower(material.SourceSHA256)+":"+strings.ToLower(material.SnapshotSHA256)] = struct{}{}
	}
	for _, material := range evaluation.Materials {
		pair := strings.ToLower(material.SourceSHA256) + ":" + strings.ToLower(material.SnapshotSHA256)
		if _, exists := trainingPairs[pair]; exists {
			return fmt.Errorf("training/evaluation source-snapshot overlap for material %q", material.ID)
		}
	}
	return nil
}

func validatePGOManifestCoverage(manifest pgoTrainingManifest) error {
	if len(manifest.Materials) == 0 {
		return fmt.Errorf("materials must not be empty")
	}
	seen := make(map[string]bool, len(manifest.Materials))
	ids := make(map[string]bool, len(manifest.Materials))
	for _, material := range manifest.Materials {
		if !isSafePGOWorkloadID(material.ID) || ids[material.ID] {
			return fmt.Errorf("material id %q is invalid or duplicated", material.ID)
		}
		ids[material.ID] = true
		if !isRequiredPGOCoverage(material.Coverage) {
			return fmt.Errorf("material %q has unsupported coverage %q", material.ID, material.Coverage)
		}
		if seen[material.Coverage] {
			return fmt.Errorf("coverage %q is duplicated", material.Coverage)
		}
		seen[material.Coverage] = true
		if !isPGOSHA256(material.SourceSHA256) || !isPGOSHA256(material.SnapshotSHA256) {
			return fmt.Errorf("material %q has invalid source/snapshot SHA-256", material.ID)
		}
		if material.Seed < 0 || material.Seed > int64(^uint32(0)) {
			return fmt.Errorf("material %q seed is outside uint32", material.ID)
		}
	}
	for _, coverage := range pgoRequiredCoverage {
		if !seen[coverage] {
			return fmt.Errorf("required coverage %q is missing", coverage)
		}
	}
	return nil
}

func validatePGOWorkloadRole(role string, inputs []pgoWorkloadMaterial, manifest pgoTrainingManifest) error {
	if len(inputs) != len(manifest.Materials) {
		return fmt.Errorf("%s workload count %d does not match manifest material count %d", role, len(inputs), len(manifest.Materials))
	}
	manifestByID := make(map[string]pgoTrainingMaterial, len(manifest.Materials))
	for _, material := range manifest.Materials {
		manifestByID[material.ID] = material
	}
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if !isSafePGOWorkloadID(input.ID) || seen[input.ID] {
			return fmt.Errorf("%s workload id %q is invalid or duplicated", role, input.ID)
		}
		seen[input.ID] = true
		material, exists := manifestByID[input.ID]
		if !exists {
			return fmt.Errorf("%s workload id %q is absent from its manifest", role, input.ID)
		}
		if material.Coverage != input.Coverage {
			return fmt.Errorf("%s workload %q coverage %q does not match manifest coverage %q", role, input.ID, input.Coverage, material.Coverage)
		}
		if !filepath.IsAbs(input.SourcePath) || !filepath.IsAbs(input.SnapshotPath) {
			return fmt.Errorf("%s workload %q sourcePath and snapshotPath must be absolute", role, input.ID)
		}
		if input.Protocol != "NCT1" && input.Protocol != "NCT2" {
			return fmt.Errorf("%s workload %q protocol must be NCT1 or NCT2", role, input.ID)
		}
		if (input.Coverage == "NCT1" && input.Protocol != "NCT1") || (input.Coverage == "NCT2" && input.Protocol != "NCT2") {
			return fmt.Errorf("%s workload %q protocol does not match its protocol coverage", role, input.ID)
		}
		if input.Width < 16 || input.Width > 3840 || input.Height < 16 || input.Height > 2160 || input.DurationMs <= 0 || input.FPSNum <= 0 || input.FPSDen <= 0 {
			return fmt.Errorf("%s workload %q has invalid render bounds", role, input.ID)
		}
		if input.Backend != "auto" && input.Backend != "dx12" && input.Backend != "vulkan" && input.Backend != "metal" {
			return fmt.Errorf("%s workload %q has unsupported backend %q", role, input.ID, input.Backend)
		}
		if input.ReadbackSlots < 1 || input.ReadbackSlots > 3 {
			return fmt.Errorf("%s workload %q readbackSlots must be 1..3", role, input.ID)
		}
		if input.AssetLayout != "separate" && input.AssetLayout != "atlas" {
			return fmt.Errorf("%s workload %q has unsupported assetLayout %q", role, input.ID, input.AssetLayout)
		}
	}
	return nil
}

func isSafePGOWorkloadID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return value[0] != '.'
}

func isRequiredPGOCoverage(value string) bool {
	for _, coverage := range pgoRequiredCoverage {
		if value == coverage {
			return true
		}
	}
	return false
}

func isPGOSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func pgoFileSHA256(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("input is not a regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyPGOWorkloadInputHashes(input pgoWorkloadMaterial, material pgoTrainingMaterial) error {
	sourceHash, err := pgoFileSHA256(input.SourcePath)
	if err != nil {
		return fmt.Errorf("source %q: %w", input.SourcePath, err)
	}
	if !strings.EqualFold(sourceHash, material.SourceSHA256) {
		return fmt.Errorf("source SHA-256 mismatch for material %q", material.ID)
	}
	snapshotHash, err := pgoFileSHA256(input.SnapshotPath)
	if err != nil {
		return fmt.Errorf("snapshot %q: %w", input.SnapshotPath, err)
	}
	if !strings.EqualFold(snapshotHash, material.SnapshotSHA256) {
		return fmt.Errorf("snapshot SHA-256 mismatch for material %q", material.ID)
	}
	return nil
}

func validatePGOWorkloadPathsOwned(root string, catalog pgoWorkloadCatalog) error {
	for _, role := range []struct {
		name   string
		inputs []pgoWorkloadMaterial
	}{
		{name: "training", inputs: catalog.Training},
		{name: "evaluation", inputs: catalog.Evaluation},
	} {
		for _, input := range role.inputs {
			for _, item := range []struct {
				name string
				path string
			}{
				{name: "sourcePath", path: input.SourcePath},
				{name: "snapshotPath", path: input.SnapshotPath},
			} {
				if !isPathInsidePGORoot(root, item.path) {
					return fmt.Errorf("%s workload %q %s escaped the owned PGO root", role.name, input.ID, item.name)
				}
			}
		}
	}
	return nil
}

func verifyPGOWorkloadCommentIDSeparation(catalog pgoWorkloadCatalog) (int, error) {
	collect := func(role string, inputs []pgoWorkloadMaterial) (map[string]struct{}, error) {
		ids := make(map[string]struct{})
		for _, input := range inputs {
			data, err := os.ReadFile(input.SnapshotPath)
			if err != nil {
				return nil, fmt.Errorf("read %s snapshot for comment-ID audit: %w", role, err)
			}
			var snapshot niconico.Snapshot
			if err := json.Unmarshal(data, &snapshot); err != nil {
				return nil, fmt.Errorf("decode %s snapshot for comment-ID audit: %w", role, err)
			}
			snapshot, err = niconico.NormalizeSnapshot(snapshot)
			if err != nil {
				return nil, fmt.Errorf("normalize %s snapshot for comment-ID audit: %w", role, err)
			}
			for _, thread := range snapshot.Threads {
				for _, comment := range thread.Comments {
					ids[comment.ID] = struct{}{}
				}
			}
		}
		return ids, nil
	}
	trainingIDs, err := collect("training", catalog.Training)
	if err != nil {
		return 0, err
	}
	evaluationIDs, err := collect("evaluation", catalog.Evaluation)
	if err != nil {
		return 0, err
	}
	shared := 0
	for id := range trainingIDs {
		if _, exists := evaluationIDs[id]; exists {
			shared++
		}
	}
	if shared != 0 {
		return shared, fmt.Errorf("training/evaluation snapshots share %d comment IDs", shared)
	}
	return 0, nil
}

func TestNicoTimelinePGOWorkload(t *testing.T) {
	catalogPath := strings.TrimSpace(os.Getenv("NICO_PGO_WORKLOAD_CATALOG"))
	if catalogPath == "" {
		t.Skip("set NICO_PGO_WORKLOAD_CATALOG for the real PGO training workload")
	}
	root := filepath.Clean(os.Getenv("NICO_PGO_RUN_ROOT"))
	helper := filepath.Clean(os.Getenv("NICO_TIMELINE_WORKER_HELPER"))
	trainingPath := filepath.Clean(os.Getenv("NICO_PGO_TRAINING_MANIFEST"))
	evaluationPath := filepath.Clean(os.Getenv("NICO_PGO_EVALUATION_MANIFEST"))
	profilePattern := filepath.Clean(os.Getenv("LLVM_PROFILE_FILE"))
	if !filepath.IsAbs(root) || !filepath.IsAbs(helper) || !filepath.IsAbs(trainingPath) || !filepath.IsAbs(evaluationPath) || !filepath.IsAbs(profilePattern) {
		t.Fatal("PGO training environment contains a missing or non-absolute owned path")
	}
	wantHelper := filepath.Join(root, "instrumented", "nico-compositord.exe")
	if !samePGOPath(helper, wantHelper) || !isPathInsidePGORoot(root, helper) || !isPathInsidePGORoot(root, profilePattern) {
		t.Fatal("instrumented helper or LLVM profile path escaped the owned PGO root")
	}
	if !samePGOPath(trainingPath, filepath.Join(root, "training.json")) || !samePGOPath(evaluationPath, filepath.Join(root, "evaluation.json")) || !samePGOPath(catalogPath, filepath.Join(root, "workload-catalog.json")) {
		t.Fatal("training/evaluation manifests and workload catalog must be copied into the owned PGO root")
	}
	if err := requirePGORegularFile(helper); err != nil {
		t.Fatal(err)
	}
	if err := requirePGORegularFile(catalogPath); err != nil {
		t.Fatal(err)
	}
	if err := requirePGORegularFile(trainingPath); err != nil {
		t.Fatal(err)
	}
	if err := requirePGORegularFile(evaluationPath); err != nil {
		t.Fatal(err)
	}
	training, err := readPGOTrainingManifest(trainingPath, "training")
	if err != nil {
		t.Fatal(err)
	}
	evaluation, err := readPGOTrainingManifest(evaluationPath, "evaluation")
	if err != nil {
		t.Fatal(err)
	}
	catalogBytes, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	var catalog pgoWorkloadCatalog
	if err := json.Unmarshal(catalogBytes, &catalog); err != nil {
		t.Fatalf("decode workload catalog: %v", err)
	}
	if err := validatePGOWorkloadCatalog(catalog, training, evaluation); err != nil {
		t.Fatal(err)
	}
	if err := validatePGOWorkloadPathsOwned(root, catalog); err != nil {
		t.Fatal(err)
	}
	if err := requirePGORegularFile(catalog.BrowserPath); err != nil {
		t.Fatalf("configured Chrome: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(profilePattern)); err != nil || !info.IsDir() {
		t.Fatalf("LLVM profile directory is unavailable: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "workloads"), 0o700); err != nil {
		t.Fatalf("create owned workload directory: %v", err)
	}

	trainingByID := make(map[string]pgoTrainingMaterial, len(training.Materials))
	for _, material := range training.Materials {
		trainingByID[material.ID] = material
	}
	for _, input := range catalog.Training {
		material := trainingByID[input.ID]
		if err := verifyPGOWorkloadInputHashes(input, material); err != nil {
			t.Fatalf("preflight material %q: %v", input.ID, err)
		}
	}
	for _, input := range catalog.Evaluation {
		if err := verifyPGOWorkloadInputHashes(input, pgoTrainingMaterial{
			ID: input.ID, SourceSHA256: findPGOMaterial(evaluation, input.ID).SourceSHA256,
			SnapshotSHA256: findPGOMaterial(evaluation, input.ID).SnapshotSHA256,
		}); err != nil {
			t.Fatalf("preflight held-out material %q: %v", input.ID, err)
		}
	}
	sharedCommentIDs, err := verifyPGOWorkloadCommentIDSeparation(catalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PGO training/evaluation shared_comment_ids=%d", sharedCommentIDs)

	helperHash, err := pgoFileSHA256(helper)
	if err != nil {
		t.Fatal(err)
	}
	runs := make([]pgoWorkloadRun, 0, len(catalog.Training))
	documentPath := filepath.Join(root, "pgo-workload-runs.json")
	for _, input := range catalog.Training {
		material := trainingByID[input.ID]
		started := time.Now()
		captureReport, scenePath := capturePGOWorkload(t, root, catalog.BrowserPath, input, material)
		sceneHash, err := pgoFileSHA256(scenePath)
		if err != nil {
			t.Fatalf("hash captured scene %q: %v", input.ID, err)
		}
		runtimePath := filepath.Join(root, "workloads", input.ID+"-runtime.json")
		stderrPath := filepath.Join(root, "workloads", input.ID+"-helper.stderr.log")
		runtimeReport := runInstrumentedPGOWorkload(t, helper, scenePath, runtimePath, stderrPath, input, captureReport.FrameCount)
		info, err := os.Stat(scenePath)
		if err != nil {
			t.Fatalf("stat captured scene %q: %v", input.ID, err)
		}
		run := pgoWorkloadRun{
			ID: input.ID, Coverage: input.Coverage, Protocol: input.Protocol,
			Width: input.Width, Height: input.Height, DurationMs: input.DurationMs,
			FPSNum: input.FPSNum, FPSDen: input.FPSDen, Backend: input.Backend,
			ReadbackSlots: input.ReadbackSlots, AssetLayout: input.AssetLayout,
			SourcePath: input.SourcePath, SourceSHA256: material.SourceSHA256,
			SnapshotPath: input.SnapshotPath, SnapshotSHA256: material.SnapshotSHA256,
			ScenePath: scenePath, SceneBytes: info.Size(), SceneSHA256: sceneHash,
			CaptureFrameCount: captureReport.FrameCount, CaptureEligibleComments: captureReport.EligibleComments,
			CaptureAssets: captureReport.TimelineAssets, CaptureDraws: captureReport.TimelineDraws,
			RuntimeReportPath: runtimePath, RuntimeReport: runtimeReport,
			WallSeconds: time.Since(started).Seconds(),
		}
		runs = append(runs, run)
		document := pgoWorkloadRunDocument{SchemaVersion: 1, HelperPath: helper, HelperSHA256: helperHash, Runs: runs}
		if err := writePGOTrainingEvidence(documentPath, document); err != nil {
			t.Fatalf("write workload evidence: %v", err)
		}
		t.Logf("PGO material=%s coverage=%s protocol=%s frames=%d comments=%d assets=%d draws=%d backend=%s adapter=%s scene_sha256=%s", input.ID, input.Coverage, input.Protocol, runtimeReport.CompletedFrames, captureReport.EligibleComments, captureReport.TimelineAssets, captureReport.TimelineDraws, runtimeReport.Backend, runtimeReport.AdapterName, sceneHash)
	}
}

func readPGOTrainingManifest(path, role string) (pgoTrainingManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pgoTrainingManifest{}, err
	}
	var manifest pgoTrainingManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return pgoTrainingManifest{}, fmt.Errorf("decode %s manifest: %w", role, err)
	}
	if manifest.Role != role {
		return pgoTrainingManifest{}, fmt.Errorf("manifest role %q does not match %q", manifest.Role, role)
	}
	return manifest, nil
}

func findPGOMaterial(manifest pgoTrainingManifest, id string) pgoTrainingMaterial {
	for _, material := range manifest.Materials {
		if material.ID == id {
			return material
		}
	}
	return pgoTrainingMaterial{}
}

func requirePGORegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("expected a regular non-symlink file: %s", path)
	}
	return nil
}

func isPathInsidePGORoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func samePGOPath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

func capturePGOWorkload(t *testing.T, root, browser string, input pgoWorkloadMaterial, material pgoTrainingMaterial) (TimelineCaptureReport, string) {
	t.Helper()
	snapshotBytes, err := os.ReadFile(input.SnapshotPath)
	if err != nil {
		t.Fatalf("read snapshot for %q: %v", input.ID, err)
	}
	snapshotHash := fmt.Sprintf("%x", sha256.Sum256(snapshotBytes))
	material.ID = input.ID
	material.SnapshotSHA256 = snapshotHash
	if err := verifyPGOWorkloadInputHashes(input, material); err != nil {
		t.Fatalf("recheck material %q before browser capture: %v", input.ID, err)
	}
	var snapshot niconico.Snapshot
	if err := json.Unmarshal(snapshotBytes, &snapshot); err != nil {
		t.Fatalf("decode snapshot for %q: %v", input.ID, err)
	}
	snapshot, err = niconico.NormalizeSnapshot(snapshot)
	if err != nil {
		t.Fatalf("normalize snapshot for %q: %v", input.ID, err)
	}
	options := RenderOptions{
		Width: input.Width, Height: input.Height, DurationMs: input.DurationMs,
		FPSNum: input.FPSNum, FPSDen: input.FPSDen, Transport: "binary", BrowserPath: browser,
	}
	sceneSuffix := ".nct"
	if input.Protocol == "NCT2" {
		sceneSuffix = ".nct2"
	}
	scenePath := filepath.Join(root, "workloads", input.ID+sceneSuffix)
	sceneFile, err := os.OpenFile(scenePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create owned %s scene: %v", input.Protocol, err)
	}
	seed := uint32(material.Seed)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var report TimelineCaptureReport
	if input.Protocol == "NCT1" {
		var scene CommentTimeline
		scene, report, err = captureCommentTimelineWithCaptureLimitsAndStarter(
			ctx, snapshot, options, &seed, timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, nil,
		)
		if err == nil {
			err = WriteCommentTimeline(sceneFile, scene)
		}
	} else {
		_, report, err = captureCommentTimelineWithCaptureLimitsAndStarterAndStream(
			ctx, snapshot, options, &seed, timelineCaptureBatchDefault, timelineCaptureBatchTargetMax, nil, sceneFile,
		)
	}
	closeErr := sceneFile.Close()
	if err != nil {
		t.Fatalf("capture %s material %q: %v", input.Protocol, input.ID, err)
	}
	if closeErr != nil {
		t.Fatalf("close captured %s material %q: %v", input.Protocol, input.ID, closeErr)
	}
	if report.FrameCount <= 0 || report.FrameCount > int64(^uint32(0)) || report.EligibleComments <= 0 || report.TimelineAssets <= 0 || report.TimelineDraws <= 0 {
		t.Fatalf("captured %s material %q did not contain comment work: frames=%d comments=%d assets=%d draws=%d", input.Protocol, input.ID, report.FrameCount, report.EligibleComments, report.TimelineAssets, report.TimelineDraws)
	}
	return report, scenePath
}

func runInstrumentedPGOWorkload(t *testing.T, helper, scenePath, reportPath, stderrPath string, input pgoWorkloadMaterial, wantFrames int64) pgoRuntimeReport {
	t.Helper()
	args := []string{"--stdin", "--backend", input.Backend, "--readback-slots", strconv.Itoa(input.ReadbackSlots), "--asset-layout", input.AssetLayout, "--report", reportPath}
	if input.Protocol == "NCT2" {
		args[0] = "--stdin-stream"
	}
	stdin, err := os.Open(scenePath)
	if err != nil {
		t.Fatalf("open captured %s scene: %v", input.Protocol, err)
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = stdin.Close()
		t.Fatalf("create owned compositor log: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	started := time.Now()
	command := exec.CommandContext(ctx, helper, args...)
	command.Stdin = stdin
	command.Stdout = io.Discard
	command.Stderr = stderr
	command.Env = os.Environ()
	err = command.Run()
	stdinErr := stdin.Close()
	stderrErr := stderr.Close()
	if err != nil {
		t.Fatalf("instrumented %s compositor failed for %q after %.1fs: %v (log %s)", input.Protocol, input.ID, time.Since(started).Seconds(), err, stderrPath)
	}
	if stdinErr != nil || stderrErr != nil {
		t.Fatalf("close compositor streams for %q: stdin=%v stderr=%v", input.ID, stdinErr, stderrErr)
	}
	reportBytes, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read compositor runtime report for %q: %v", input.ID, err)
	}
	var report pgoRuntimeReport
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		t.Fatalf("decode compositor runtime report for %q: %v", input.ID, err)
	}
	if report.Protocol != input.Protocol || report.Renderer != "wgpu" || report.Version == "" || report.RequestedBackend != input.Backend || report.Backend == "" || report.AdapterName == "" {
		t.Fatalf("compositor report for %q does not prove the requested GPU protocol/backend: %+v", input.ID, report)
	}
	if report.ReadbackSlots != input.ReadbackSlots || report.RequestedAssetLayout != input.AssetLayout || report.AssetLayout != input.AssetLayout {
		t.Fatalf("compositor configuration drift for %q: %+v", input.ID, report)
	}
	if report.Error != nil || int64(report.CompletedFrames) != wantFrames {
		t.Fatalf("compositor frame/result mismatch for %q: completed=%d expected=%d error=%v", input.ID, report.CompletedFrames, wantFrames, report.Error)
	}
	return report
}

func writePGOTrainingEvidence(path string, document pgoWorkloadRunDocument) error {
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporaryPath := path + ".tmp"
	if err := os.WriteFile(temporaryPath, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func pgoTestDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func pgoTestManifests(root string) (pgoTrainingManifest, pgoTrainingManifest, pgoWorkloadCatalog) {
	coverage := []string{"short", "long", "dense", "NCT1", "NCT2"}
	training := pgoTrainingManifest{SchemaVersion: 1, Role: "training"}
	evaluation := pgoTrainingManifest{SchemaVersion: 1, Role: "evaluation"}
	catalog := pgoWorkloadCatalog{
		SchemaVersion: 1,
		BrowserPath:   filepath.Join(root, "chrome.exe"),
	}
	for _, class := range coverage {
		trainID := "train-" + class
		evalID := "eval-" + class
		trainSource := filepath.Join(root, "train-source-"+class+".mp4")
		trainSnapshot := filepath.Join(root, "train-snapshot-"+class+".json")
		evalSource := filepath.Join(root, "eval-source-"+class+".mp4")
		evalSnapshot := filepath.Join(root, "eval-snapshot-"+class+".json")
		training.Materials = append(training.Materials, pgoTrainingMaterial{
			ID: trainID, Coverage: class, SourceSHA256: pgoTestDigest("train-source-" + class),
			SnapshotSHA256: pgoTestDigest("train-snapshot-" + class), Seed: 17,
		})
		evaluation.Materials = append(evaluation.Materials, pgoTrainingMaterial{
			ID: evalID, Coverage: class, SourceSHA256: pgoTestDigest("eval-source-" + class),
			SnapshotSHA256: pgoTestDigest("eval-snapshot-" + class), Seed: 29,
		})
		protocol := "NCT1"
		if class == "long" || class == "dense" || class == "NCT2" {
			protocol = "NCT2"
		}
		trainInput := pgoWorkloadMaterial{
			ID: trainID, Coverage: class, SourcePath: trainSource, SnapshotPath: trainSnapshot,
			Protocol: protocol, Width: 1280, Height: 720, DurationMs: 6000,
			FPSNum: 30, FPSDen: 1, Backend: "auto", ReadbackSlots: 3, AssetLayout: "separate",
		}
		evalInput := trainInput
		evalInput.ID = evalID
		evalInput.SourcePath = evalSource
		evalInput.SnapshotPath = evalSnapshot
		catalog.Training = append(catalog.Training, trainInput)
		catalog.Evaluation = append(catalog.Evaluation, evalInput)
	}
	return training, evaluation, catalog
}

func TestValidatePGOWorkloadCatalogMatchesManifestCoverageAndKeepsRolesDisjoint(t *testing.T) {
	training, evaluation, catalog := pgoTestManifests(t.TempDir())
	if err := validatePGOWorkloadCatalog(catalog, training, evaluation); err != nil {
		t.Fatalf("valid PGO workload catalog rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*pgoTrainingManifest, *pgoTrainingManifest, *pgoWorkloadCatalog)
	}{
		{
			name: "missing training workload",
			mutate: func(_, _ *pgoTrainingManifest, catalog *pgoWorkloadCatalog) {
				catalog.Training = catalog.Training[:len(catalog.Training)-1]
			},
		},
		{
			name: "coverage mismatch",
			mutate: func(_, _ *pgoTrainingManifest, catalog *pgoWorkloadCatalog) {
				catalog.Evaluation[0].Coverage = "long"
			},
		},
		{
			name: "unsafe output id",
			mutate: func(_, _ *pgoTrainingManifest, catalog *pgoWorkloadCatalog) {
				catalog.Training[0].ID = "..\\outside"
			},
		},
		{
			name: "training evaluation material overlap",
			mutate: func(training, evaluation *pgoTrainingManifest, _ *pgoWorkloadCatalog) {
				evaluation.Materials[0].SourceSHA256 = training.Materials[0].SourceSHA256
				evaluation.Materials[0].SnapshotSHA256 = training.Materials[0].SnapshotSHA256
			},
		},
		{
			name: "unsupported protocol",
			mutate: func(_, _ *pgoTrainingManifest, catalog *pgoWorkloadCatalog) {
				catalog.Training[0].Protocol = "NCT3"
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			trainCopy, evalCopy, catalogCopy := clonePgoTestInputs(training, evaluation, catalog)
			test.mutate(&trainCopy, &evalCopy, &catalogCopy)
			if err := validatePGOWorkloadCatalog(catalogCopy, trainCopy, evalCopy); err == nil {
				t.Fatal("invalid workload catalog was accepted")
			}
		})
	}
}

func TestVerifyPGOWorkloadInputHashesBeforeCapture(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.mp4")
	snapshotPath := filepath.Join(root, "snapshot.json")
	if err := os.WriteFile(sourcePath, []byte("real source fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, []byte("real snapshot fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := pgoWorkloadMaterial{SourcePath: sourcePath, SnapshotPath: snapshotPath}
	manifest := pgoTrainingMaterial{
		SourceSHA256: pgoTestDigest("real source fixture"), SnapshotSHA256: pgoTestDigest("real snapshot fixture"),
	}
	if err := verifyPGOWorkloadInputHashes(input, manifest); err != nil {
		t.Fatalf("matching source and snapshot hashes rejected: %v", err)
	}
	manifest.SnapshotSHA256 = pgoTestDigest("different snapshot")
	if err := verifyPGOWorkloadInputHashes(input, manifest); err == nil {
		t.Fatal("changed snapshot bytes were accepted")
	}
}

func TestPGOWorkloadInputPathsMustStayInsideOwnedRoot(t *testing.T) {
	root := t.TempDir()
	_, _, catalog := pgoTestManifests(root)
	if err := validatePGOWorkloadPathsOwned(root, catalog); err != nil {
		t.Fatalf("owned workload inputs rejected: %v", err)
	}
	catalog.Evaluation[0].SnapshotPath = filepath.Join(filepath.Dir(root), "outside.json")
	if err := validatePGOWorkloadPathsOwned(root, catalog); err == nil {
		t.Fatal("workload snapshot outside the owned PGO root was accepted")
	}
}

func TestPGOWorkloadCommentIDSeparationUsesSnapshotContents(t *testing.T) {
	root := t.TempDir()
	writeSnapshot := func(name string, ids ...string) string {
		t.Helper()
		comments := make([]niconico.Comment, 0, len(ids))
		for index, id := range ids {
			comments = append(comments, niconico.Comment{ID: id, No: int64(index + 1), VposMs: int64(index * 1000), Body: "fixture"})
		}
		snapshot := niconico.Snapshot{
			SchemaVersion: 1, VideoID: "sm9", CommentStatus: niconico.CommentStatusReady,
			CommentCount: len(comments), Threads: []niconico.Thread{{ID: "main", Fork: "main", Comments: comments}},
		}
		data, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	trainingPath := writeSnapshot("training.json", "train-a", "train-b")
	evaluationPath := writeSnapshot("evaluation.json", "eval-a", "eval-b")
	catalog := pgoWorkloadCatalog{
		Training:   []pgoWorkloadMaterial{{ID: "train-short", SnapshotPath: trainingPath}, {ID: "train-long", SnapshotPath: trainingPath}},
		Evaluation: []pgoWorkloadMaterial{{ID: "eval-short", SnapshotPath: evaluationPath}, {ID: "eval-long", SnapshotPath: evaluationPath}},
	}
	shared, err := verifyPGOWorkloadCommentIDSeparation(catalog)
	if err != nil || shared != 0 {
		t.Fatalf("disjoint comment IDs rejected: shared=%d err=%v", shared, err)
	}
	evaluationPath = writeSnapshot("evaluation-overlap.json", "eval-a", "train-b")
	catalog.Evaluation[0].SnapshotPath = evaluationPath
	catalog.Evaluation[1].SnapshotPath = evaluationPath
	shared, err = verifyPGOWorkloadCommentIDSeparation(catalog)
	if err == nil || shared != 1 {
		t.Fatalf("shared comment ID was not rejected: shared=%d err=%v", shared, err)
	}
}

func clonePgoTestInputs(training, evaluation pgoTrainingManifest, catalog pgoWorkloadCatalog) (pgoTrainingManifest, pgoTrainingManifest, pgoWorkloadCatalog) {
	cloneManifest := func(value pgoTrainingManifest) pgoTrainingManifest {
		value.Materials = append([]pgoTrainingMaterial(nil), value.Materials...)
		return value
	}
	cloneCatalog := func(value pgoWorkloadCatalog) pgoWorkloadCatalog {
		value.Training = append([]pgoWorkloadMaterial(nil), value.Training...)
		value.Evaluation = append([]pgoWorkloadMaterial(nil), value.Evaluation...)
		return value
	}
	return cloneManifest(training), cloneManifest(evaluation), cloneCatalog(catalog)
}
