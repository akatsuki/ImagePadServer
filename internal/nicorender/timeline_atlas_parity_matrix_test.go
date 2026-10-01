package nicorender

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	timelineAtlasParityMatrixOptIn = "NICO_TIMELINE_ATLAS_PARITY_MATRIX"
	timelineAtlasParityMaxReport   = 2 << 20
)

type timelineAtlasParityProfile struct {
	name           string
	width, height  uint32
	fpsNum, fpsDen uint32
	frames         uint32
	duration       string
}

type timelineAtlasParityRun struct {
	LayoutRequested string                       `json:"layout_requested"`
	LayoutActual    string                       `json:"layout_actual"`
	FallbackReason  string                       `json:"fallback_reason,omitempty"`
	Backend         string                       `json:"backend"`
	Adapter         string                       `json:"adapter"`
	ReadbackSlots   int                          `json:"readback_slots"`
	PageCount       int                          `json:"asset_page_count"`
	SourceBytes     uint64                       `json:"asset_source_bytes"`
	AllocatedBytes  uint64                       `json:"asset_allocated_bytes"`
	RGBABytes       int64                        `json:"rgba_bytes"`
	RGBASHA256      string                       `json:"rgba_sha256"`
	Telemetry       timelineAtlasParityTelemetry `json:"asset_telemetry"`
}

type timelineAtlasParityPage struct {
	PageIndex       uint64 `json:"pageIndex"`
	Width           uint64 `json:"width"`
	Height          uint64 `json:"height"`
	UsedTexels      uint64 `json:"usedTexels"`
	AllocatedTexels uint64 `json:"allocatedTexels"`
}

type timelineAtlasParityResidentMemory struct {
	DecodedScenePixelBytes              *uint64  `json:"decodedScenePixelBytes"`
	LogicalTextureAllocationBytes       *uint64  `json:"logicalTextureAllocationBytes"`
	RendererOwnedDrawBufferPayloadBytes *uint64  `json:"rendererOwnedDrawBufferPayloadBytes"`
	RenderBundleInternalBytes           *uint64  `json:"renderBundleInternalBytes"`
	ReadbackRingBufferBytes             *uint64  `json:"readbackRingBufferBytes"`
	WGPUTransientStagingBytes           *uint64  `json:"wgpuTransientStagingBytes"`
	PhysicalVRAMBytes                   *uint64  `json:"physicalVramBytes"`
	UnknownReasons                      []string `json:"unknownReasons"`
}

type timelineAtlasParityTelemetry struct {
	TelemetryScope            string                            `json:"telemetryScope"`
	EffectiveLayout           string                            `json:"effectiveLayout"`
	Pages                     []timelineAtlasParityPage         `json:"pages"`
	TextureUploadCallCount    *uint64                           `json:"textureUploadCallCount"`
	TextureUploadBytes        *uint64                           `json:"textureUploadBytes"`
	DrawOrderPageBindRunCount *uint64                           `json:"drawOrderPageBindRunCount"`
	BundleBuildCPUWallTimeNs  *uint64                           `json:"bundleBuildCpuWallTimeNs"`
	ResidentMemory            timelineAtlasParityResidentMemory `json:"residentMemory"`
	UnavailableReasons        []string                          `json:"unavailableReasons"`
}

func timelineAtlasParityProfiles() []timelineAtlasParityProfile {
	return []timelineAtlasParityProfile{
		{name: "720p30-12s", width: 1280, height: 720, fpsNum: 30, fpsDen: 1, frames: 360, duration: "12"},
		{name: "720p30-6s", width: 1280, height: 720, fpsNum: 30, fpsDen: 1, frames: 180, duration: "6"},
		{name: "720p60-6s", width: 1280, height: 720, fpsNum: 60, fpsDen: 1, frames: 360, duration: "6"},
		{name: "1080p30-6s", width: 1920, height: 1080, fpsNum: 30, fpsDen: 1, frames: 180, duration: "6"},
		{name: "1080p60-6s", width: 1920, height: 1080, fpsNum: 60, fpsDen: 1, frames: 360, duration: "6"},
		{name: "1080p5994-6.006s", width: 1920, height: 1080, fpsNum: 60000, fpsDen: 1001, frames: 360, duration: "6.006"},
		{name: "1440x1080-4x3-6s", width: 1440, height: 1080, fpsNum: 30, fpsDen: 1, frames: 180, duration: "6"},
		{name: "1080x1080-square-6s", width: 1080, height: 1080, fpsNum: 30, fpsDen: 1, frames: 180, duration: "6"},
	}
}

func (profile timelineAtlasParityProfile) expectedRGBABytes() int64 {
	return int64(profile.width) * int64(profile.height) * 4 * int64(profile.frames)
}

func applyTimelineAtlasParityProfile(scene CommentTimeline, profile timelineAtlasParityProfile) CommentTimeline {
	scene.Header.Width = profile.width
	scene.Header.Height = profile.height
	scene.Header.FrameCount = profile.frames
	scene.Header.FPSNum = profile.fpsNum
	scene.Header.FPSDen = profile.fpsDen
	return scene
}

type timelineAtlasParityProfileResult struct {
	Name            string                 `json:"name"`
	Width           uint32                 `json:"width"`
	Height          uint32                 `json:"height"`
	FPSNum          uint32                 `json:"fps_num"`
	FPSDen          uint32                 `json:"fps_den"`
	Frames          uint32                 `json:"frames"`
	DurationSeconds string                 `json:"duration_seconds_exact"`
	ExpectedRGBA    int64                  `json:"expected_rgba_bytes"`
	Separate        timelineAtlasParityRun `json:"separate"`
	Atlas           timelineAtlasParityRun `json:"atlas"`
	ExactByteParity bool                   `json:"exact_byte_parity"`
}

// TestTimelineAtlasParity compares the current helper's Separate and Atlas
// output for the pinned T0 scene. It is deliberately distinct from
// TestTimelineAtlasSavedT0Parity, which also requires the unavailable old
// baseline helper. This matrix does not establish old-baseline to 720p30/360f
// parity; that remains a separate A2-C3 recovery/evidence requirement.
func TestTimelineAtlasParity(t *testing.T) {
	if os.Getenv(timelineAtlasParityMatrixOptIn) != "1" {
		t.Skip("opt-in; set " + timelineAtlasParityMatrixOptIn + "=1 with pinned T0 scene, candidate helper, and owned TEMP artifacts")
	}
	scenePath := requiredTimelineEnv(t, "NICO_TIMELINE_ATLAS_MATRIX_SCENE")
	candidatePath := requiredTimelineEnv(t, "NICO_TIMELINE_ATLAS_MATRIX_CANDIDATE_HELPER")
	artifactDir := requiredTimelineEnv(t, "NICO_TIMELINE_ATLAS_MATRIX_ARTIFACTS")
	assertOutsideTimelineRepo(t, scenePath, "pinned T0 scene")
	assertOutsideTimelineRepo(t, artifactDir, "artifact directory")
	if filepath.Base(scenePath) != timelineT0ExpectedSceneName {
		t.Fatalf("saved scene name=%q, want %q", filepath.Base(scenePath), timelineT0ExpectedSceneName)
	}
	sceneBytes, err := os.ReadFile(scenePath)
	if err != nil {
		t.Fatalf("read pinned T0 scene: %v", err)
	}
	if got := sha256Hex(sceneBytes); got != timelineT0SceneSHA256 {
		t.Fatalf("saved scene SHA-256=%s, want pinned T0 %s", got, timelineT0SceneSHA256)
	}
	candidateHash, err := sha256File(candidatePath)
	if err != nil {
		t.Fatalf("hash candidate helper: %v", err)
	}
	if candidateHash == timelineT0BaselineSHA256 {
		t.Fatal("candidate helper is the pinned old T0 baseline; matrix requires the current candidate helper")
	}
	var source CommentTimeline
	if err := json.Unmarshal(sceneBytes, &source); err != nil {
		t.Fatalf("decode pinned T0 scene: %v", err)
	}
	if err := ValidateCommentTimeline(source); err != nil {
		t.Fatalf("validate pinned T0 scene: %v", err)
	}
	backend := strings.TrimSpace(os.Getenv("NICO_TIMELINE_GPU_BACKEND"))
	if backend == "" {
		backend = "vulkan"
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	results := make([]timelineAtlasParityProfileResult, 0, len(timelineAtlasParityProfiles()))
	for _, profile := range timelineAtlasParityProfiles() {
		profileScene := applyTimelineAtlasParityProfile(source, profile)
		if err := ValidateCommentTimeline(profileScene); err != nil {
			t.Fatalf("validate %s scene profile: %v", profile.name, err)
		}
		var wire bytes.Buffer
		if err := WriteCommentTimeline(&wire, profileScene); err != nil {
			t.Fatalf("encode %s scene profile as NCT1: %v", profile.name, err)
		}
		var expectedSourceBytes uint64
		for _, asset := range profileScene.Assets {
			assetBytes := uint64(len(asset.RGBA))
			if expectedSourceBytes > ^uint64(0)-assetBytes {
				t.Fatalf("%s source asset byte count overflow", profile.name)
			}
			expectedSourceBytes += assetBytes
		}
		profileResult := timelineAtlasParityProfileResult{
			Name: profile.name, Width: profile.width, Height: profile.height,
			FPSNum: profile.fpsNum, FPSDen: profile.fpsDen, Frames: profile.frames,
			DurationSeconds: profile.duration, ExpectedRGBA: profile.expectedRGBABytes(),
		}
		for _, mode := range []struct {
			name, layout string
			out          *timelineAtlasParityRun
		}{
			{name: "separate", layout: "separate", out: &profileResult.Separate},
			{name: "atlas", layout: "atlas", out: &profileResult.Atlas},
		} {
			runName := profile.name + "-" + mode.name
			run, runErr := runTimelineSavedSceneHelper(ctx, artifactDir, runName, candidatePath, mode.layout, backend, slots, wire.Bytes(), profile.frames, profile.expectedRGBABytes())
			var telemetry timelineAtlasParityTelemetry
			if runErr == nil {
				info, statErr := os.Stat(run.ReportPath)
				if statErr != nil {
					runErr = fmt.Errorf("stat runtime report %s: %w", run.ReportPath, statErr)
				} else if info.Size() < 1 || info.Size() > timelineAtlasParityMaxReport {
					runErr = fmt.Errorf("runtime report size %d is outside 1..%d bytes", info.Size(), timelineAtlasParityMaxReport)
				} else if reportBytes, readErr := os.ReadFile(run.ReportPath); readErr != nil {
					runErr = fmt.Errorf("read runtime report %s: %w", run.ReportPath, readErr)
				} else {
					telemetry, runErr = validateTimelineAtlasParityTelemetry(reportBytes, mode.layout, run.PageCount, uint64(len(profileScene.Assets)), expectedSourceBytes, run.AllocatedBytes)
				}
			}
			for _, path := range []string{run.ReportPath, run.StderrPath} {
				if path == "" {
					continue
				}
				if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) && runErr == nil {
					runErr = fmt.Errorf("remove transient %s artifact %s: %w", runName, path, removeErr)
				}
			}
			if runErr != nil {
				t.Fatalf("run %s: %v", runName, runErr)
			}
			if run.HelperSHA256 != candidateHash {
				t.Fatalf("%s helper SHA-256=%s changed from pinned candidate %s", runName, run.HelperSHA256, candidateHash)
			}
			if run.LayoutRequest != mode.layout || run.LayoutActual != mode.layout || run.FallbackReason != "" {
				t.Fatalf("%s layout was not honored without fallback: requested=%q actual=%q fallback=%q", runName, run.LayoutRequest, run.LayoutActual, run.FallbackReason)
			}
			if run.RGBABytes != profile.expectedRGBABytes() {
				t.Fatalf("%s RGBA bytes=%d, want %d", runName, run.RGBABytes, profile.expectedRGBABytes())
			}
			*mode.out = timelineAtlasParityRun{
				LayoutRequested: run.LayoutRequest, LayoutActual: run.LayoutActual,
				FallbackReason: run.FallbackReason, Backend: run.Backend, Adapter: run.Adapter,
				ReadbackSlots: run.ReadbackSlots, PageCount: run.PageCount,
				SourceBytes: run.SourceBytes, AllocatedBytes: run.AllocatedBytes,
				RGBABytes: run.RGBABytes, RGBASHA256: run.RGBASHA256,
				Telemetry: telemetry,
			}
		}
		if profileResult.Separate.Backend != profileResult.Atlas.Backend || profileResult.Separate.Adapter != profileResult.Atlas.Adapter || profileResult.Separate.ReadbackSlots != profileResult.Atlas.ReadbackSlots {
			t.Fatalf("%s Separate/Atlas GPU conditions differ: %+v vs %+v", profile.name, profileResult.Separate, profileResult.Atlas)
		}
		if profileResult.Separate.RGBABytes != profileResult.Atlas.RGBABytes || profileResult.Separate.RGBASHA256 != profileResult.Atlas.RGBASHA256 {
			t.Fatalf("%s Atlas output differs from Separate: bytes/hash=%d/%s vs %d/%s", profile.name, profileResult.Separate.RGBABytes, profileResult.Separate.RGBASHA256, profileResult.Atlas.RGBABytes, profileResult.Atlas.RGBASHA256)
		}
		profileResult.ExactByteParity = true
		results = append(results, profileResult)
		t.Logf("%s exact Separate/Atlas parity: frames=%d rgba_bytes=%d sha256=%s backend=%s adapter=%q duration=%ss", profile.name, profile.frames, profileResult.ExpectedRGBA, profileResult.Separate.RGBASHA256, backend, profileResult.Separate.Adapter, profile.duration)
	}
	manifest := struct {
		SchemaVersion       int                                `json:"schema_version"`
		CreatedAt           time.Time                          `json:"created_at"`
		ScenePath           string                             `json:"scene_path"`
		SceneSHA256         string                             `json:"scene_sha256"`
		CandidateHelperPath string                             `json:"candidate_helper_path"`
		CandidateHelperSHA  string                             `json:"candidate_helper_sha256"`
		Backend             string                             `json:"backend"`
		ReadbackSlots       int                                `json:"readback_slots"`
		OldBaselineLeg      string                             `json:"old_baseline_720p30_360f_leg"`
		Profiles            []timelineAtlasParityProfileResult `json:"profiles"`
		RetainedRawRGBA     []string                           `json:"retained_raw_rgba"`
	}{
		SchemaVersion: 1, CreatedAt: time.Now().UTC(), ScenePath: scenePath,
		SceneSHA256: timelineT0SceneSHA256, CandidateHelperPath: candidatePath,
		CandidateHelperSHA: candidateHash, Backend: backend, ReadbackSlots: slots,
		OldBaselineLeg: "not established by this current-helper Separate/Atlas matrix; exact old helper is required",
		Profiles:       results, RetainedRawRGBA: []string{},
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode parity matrix manifest: %v", err)
	}
	manifestPath := filepath.Join(artifactDir, "atlas-parity-matrix.json")
	if err := writeTimelineArtifactExclusive(manifestPath, append(manifestBytes, '\n')); err != nil {
		t.Fatalf("write parity matrix manifest: %v", err)
	}
	t.Logf("all %d Separate/Atlas profile comparisons passed; old-baseline 720p30/360f leg remains separate; manifest=%s", len(results), manifestPath)
}

func TestTimelineAtlasParityProfiles(t *testing.T) {
	want := []struct {
		profile timelineAtlasParityProfile
		rgba    int64
	}{
		{timelineAtlasParityProfile{name: "720p30-12s", width: 1280, height: 720, fpsNum: 30, fpsDen: 1, frames: 360, duration: "12"}, 1_327_104_000},
		{timelineAtlasParityProfile{name: "720p30-6s", width: 1280, height: 720, fpsNum: 30, fpsDen: 1, frames: 180, duration: "6"}, 663_552_000},
		{timelineAtlasParityProfile{name: "720p60-6s", width: 1280, height: 720, fpsNum: 60, fpsDen: 1, frames: 360, duration: "6"}, 1_327_104_000},
		{timelineAtlasParityProfile{name: "1080p30-6s", width: 1920, height: 1080, fpsNum: 30, fpsDen: 1, frames: 180, duration: "6"}, 1_492_992_000},
		{timelineAtlasParityProfile{name: "1080p60-6s", width: 1920, height: 1080, fpsNum: 60, fpsDen: 1, frames: 360, duration: "6"}, 2_985_984_000},
		{timelineAtlasParityProfile{name: "1080p5994-6.006s", width: 1920, height: 1080, fpsNum: 60000, fpsDen: 1001, frames: 360, duration: "6.006"}, 2_985_984_000},
		{timelineAtlasParityProfile{name: "1440x1080-4x3-6s", width: 1440, height: 1080, fpsNum: 30, fpsDen: 1, frames: 180, duration: "6"}, 1_119_744_000},
		{timelineAtlasParityProfile{name: "1080x1080-square-6s", width: 1080, height: 1080, fpsNum: 30, fpsDen: 1, frames: 180, duration: "6"}, 839_808_000},
	}
	got := timelineAtlasParityProfiles()
	if len(got) != len(want) {
		t.Fatalf("parity profile count=%d, want %d", len(got), len(want))
	}
	for i, expected := range want {
		if !reflect.DeepEqual(got[i], expected.profile) {
			t.Errorf("profile[%d]=%+v, want %+v", i, got[i], expected.profile)
		}
		if bytes := got[i].expectedRGBABytes(); bytes != expected.rgba {
			t.Errorf("%s expected RGBA bytes=%d, want %d", got[i].name, bytes, expected.rgba)
		}
	}
}

func TestTimelineAtlasParityProfilePreservesSceneContent(t *testing.T) {
	scene := CommentTimeline{
		Header: TimelineHeader{Width: 1920, Height: 1080, FrameCount: 180, FPSNum: 30, FPSDen: 1, BundleSHA256: [32]byte{1, 2, 3}},
		Assets: []TimelineAsset{{ID: 7, Width: 1, Height: 1, RGBA: []byte{1, 2, 3, 4}, SHA256: [32]byte{5}}},
		Draws:  []TimelineDraw{{AssetID: 7, StartVPos: -20, EndVPos: 100, OwnerOrder: 9, Rect: [4]float32{1, 2, 3, 4}}},
	}
	beforeAssets := append([]TimelineAsset(nil), scene.Assets...)
	beforeDraws := append([]TimelineDraw(nil), scene.Draws...)
	beforeBundleHash := scene.Header.BundleSHA256
	profile := timelineAtlasParityProfiles()[5]

	got := applyTimelineAtlasParityProfile(scene, profile)
	if got.Header.Width != profile.width || got.Header.Height != profile.height || got.Header.FrameCount != profile.frames || got.Header.FPSNum != profile.fpsNum || got.Header.FPSDen != profile.fpsDen {
		t.Fatalf("profile header=%+v, want profile %+v", got.Header, profile)
	}
	if got.Header.BundleSHA256 != beforeBundleHash {
		t.Fatal("profile transform changed the scene bundle hash")
	}
	if !reflect.DeepEqual(got.Assets, beforeAssets) || !reflect.DeepEqual(got.Draws, beforeDraws) {
		t.Fatal("profile transform changed assets or draw timing/order/projection data")
	}
	if scene.Header.Width != 1920 || scene.Header.Height != 1080 || scene.Header.FrameCount != 180 || scene.Header.FPSNum != 30 || scene.Header.FPSDen != 1 {
		t.Fatalf("profile transform mutated the source scene header: %+v", scene.Header)
	}
}

func validateTimelineAtlasParityTelemetry(reportBytes []byte, requestedLayout string, expectedPageCount int, expectedAssetCount uint64, expectedSourceBytes, expectedAllocatedBytes uint64) (timelineAtlasParityTelemetry, error) {
	var telemetry timelineAtlasParityTelemetry
	if len(reportBytes) == 0 || len(reportBytes) > timelineAtlasParityMaxReport {
		return telemetry, fmt.Errorf("runtime report size %d is outside 1..%d bytes", len(reportBytes), timelineAtlasParityMaxReport)
	}
	if err := ValidateStrictJSONDocument(reportBytes); err != nil {
		return telemetry, fmt.Errorf("invalid report JSON: %w", err)
	}
	var report struct {
		RequestedAssetLayout string          `json:"requestedAssetLayout"`
		AssetLayout          string          `json:"assetLayout"`
		AssetPageCount       int             `json:"assetPageCount"`
		AssetSourceBytes     uint64          `json:"assetSourceBytes"`
		AssetAllocatedBytes  uint64          `json:"assetAllocatedBytes"`
		AssetTelemetry       json.RawMessage `json:"assetTelemetry"`
	}
	if err := json.Unmarshal(reportBytes, &report); err != nil {
		return telemetry, fmt.Errorf("decode runtime report: %w", err)
	}
	if report.RequestedAssetLayout != requestedLayout || report.AssetLayout != requestedLayout {
		return telemetry, fmt.Errorf("report layout requested/actual=%q/%q, want %q/%q", report.RequestedAssetLayout, report.AssetLayout, requestedLayout, requestedLayout)
	}
	if report.AssetPageCount != expectedPageCount || report.AssetSourceBytes != expectedSourceBytes || report.AssetAllocatedBytes != expectedAllocatedBytes {
		return telemetry, fmt.Errorf("legacy report asset count/bytes=%d/%d/%d, want %d/%d/%d", report.AssetPageCount, report.AssetSourceBytes, report.AssetAllocatedBytes, expectedPageCount, expectedSourceBytes, expectedAllocatedBytes)
	}
	if len(report.AssetTelemetry) == 0 || string(report.AssetTelemetry) == "null" {
		return telemetry, fmt.Errorf("runtime report is missing assetTelemetry object")
	}
	if err := requireTimelineAtlasJSONFields(report.AssetTelemetry,
		"telemetryScope", "effectiveLayout", "pages", "textureUploadCallCount", "textureUploadBytes",
		"drawOrderPageBindRunCount", "bundleBuildCpuWallTimeNs", "residentMemory", "unavailableReasons"); err != nil {
		return telemetry, fmt.Errorf("assetTelemetry fields: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(report.AssetTelemetry))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&telemetry); err != nil {
		return telemetry, fmt.Errorf("decode assetTelemetry: %w", err)
	}
	if telemetry.TelemetryScope != "nct1-static-assets" || telemetry.EffectiveLayout != requestedLayout {
		return telemetry, fmt.Errorf("assetTelemetry scope/layout=%q/%q, want nct1-static-assets/%q", telemetry.TelemetryScope, telemetry.EffectiveLayout, requestedLayout)
	}
	if telemetry.Pages == nil {
		return telemetry, fmt.Errorf("NCT1 assetTelemetry pages must be an array")
	}
	var rawTelemetry map[string]json.RawMessage
	if err := json.Unmarshal(report.AssetTelemetry, &rawTelemetry); err != nil {
		return telemetry, fmt.Errorf("inspect assetTelemetry: %w", err)
	}
	if err := requireTimelineAtlasJSONFields(rawTelemetry["residentMemory"],
		"decodedScenePixelBytes", "logicalTextureAllocationBytes", "rendererOwnedDrawBufferPayloadBytes",
		"renderBundleInternalBytes", "readbackRingBufferBytes", "wgpuTransientStagingBytes",
		"physicalVramBytes", "unknownReasons"); err != nil {
		return telemetry, fmt.Errorf("residentMemory fields: %w", err)
	}
	if string(rawTelemetry["pages"]) == "null" {
		return telemetry, fmt.Errorf("NCT1 assetTelemetry pages must not be null")
	}
	if len(telemetry.Pages) != expectedPageCount {
		return telemetry, fmt.Errorf("assetTelemetry pages=%d, top-level assetPageCount=%d", len(telemetry.Pages), expectedPageCount)
	}
	var rawPages []json.RawMessage
	if err := json.Unmarshal(rawTelemetry["pages"], &rawPages); err != nil {
		return telemetry, fmt.Errorf("decode assetTelemetry pages: %w", err)
	}
	for i, rawPage := range rawPages {
		if err := requireTimelineAtlasJSONFields(rawPage, "pageIndex", "width", "height", "usedTexels", "allocatedTexels"); err != nil {
			return telemetry, fmt.Errorf("assetTelemetry page %d fields: %w", i, err)
		}
	}
	if telemetry.TextureUploadCallCount == nil || *telemetry.TextureUploadCallCount != expectedAssetCount {
		return telemetry, fmt.Errorf("textureUploadCallCount=%v, want asset count %d", timelineAtlasUint64PointerString(telemetry.TextureUploadCallCount), expectedAssetCount)
	}
	if telemetry.TextureUploadBytes == nil || *telemetry.TextureUploadBytes != expectedSourceBytes {
		return telemetry, fmt.Errorf("textureUploadBytes=%v, want source RGBA bytes %d", timelineAtlasUint64PointerString(telemetry.TextureUploadBytes), expectedSourceBytes)
	}
	if telemetry.DrawOrderPageBindRunCount == nil || telemetry.BundleBuildCPUWallTimeNs == nil {
		return telemetry, fmt.Errorf("NCT1 ordered page-run count and host CPU bundle time must be present")
	}
	if telemetry.ResidentMemory.DecodedScenePixelBytes == nil || *telemetry.ResidentMemory.DecodedScenePixelBytes != expectedSourceBytes {
		return telemetry, fmt.Errorf("decodedScenePixelBytes=%v, want %d", timelineAtlasUint64PointerString(telemetry.ResidentMemory.DecodedScenePixelBytes), expectedSourceBytes)
	}
	if telemetry.ResidentMemory.LogicalTextureAllocationBytes == nil || *telemetry.ResidentMemory.LogicalTextureAllocationBytes != expectedAllocatedBytes {
		return telemetry, fmt.Errorf("logicalTextureAllocationBytes=%v, want allocated bytes %d", timelineAtlasUint64PointerString(telemetry.ResidentMemory.LogicalTextureAllocationBytes), expectedAllocatedBytes)
	}
	if telemetry.ResidentMemory.RendererOwnedDrawBufferPayloadBytes == nil || telemetry.ResidentMemory.ReadbackRingBufferBytes == nil {
		return telemetry, fmt.Errorf("known renderer draw-buffer and readback-ring metrics must be present")
	}
	for name, value := range map[string]*uint64{
		"renderBundleInternalBytes": telemetry.ResidentMemory.RenderBundleInternalBytes,
		"wgpuTransientStagingBytes": telemetry.ResidentMemory.WGPUTransientStagingBytes,
		"physicalVramBytes":         telemetry.ResidentMemory.PhysicalVRAMBytes,
	} {
		if value != nil {
			return telemetry, fmt.Errorf("opaque WGPU metric %s must be null", name)
		}
	}
	if len(telemetry.ResidentMemory.UnknownReasons) < 3 {
		return telemetry, fmt.Errorf("residentMemory.unknownReasons has %d entries, want reasons for all three opaque metrics", len(telemetry.ResidentMemory.UnknownReasons))
	}
	for _, requiredReason := range []string{"render-bundle", "staging", "physical vram"} {
		if !timelineAtlasReasonContains(telemetry.ResidentMemory.UnknownReasons, requiredReason) {
			return telemetry, fmt.Errorf("residentMemory.unknownReasons is missing reason for %q", requiredReason)
		}
	}
	if telemetry.UnavailableReasons == nil || len(telemetry.UnavailableReasons) != 0 {
		return telemetry, fmt.Errorf("NCT1 unavailableReasons=%v, want empty array", telemetry.UnavailableReasons)
	}
	if expectedSourceBytes%4 != 0 {
		return telemetry, fmt.Errorf("assetSourceBytes=%d is not an RGBA byte count", expectedSourceBytes)
	}
	if expectedAllocatedBytes%4 != 0 {
		return telemetry, fmt.Errorf("assetAllocatedBytes=%d is not an RGBA byte count", expectedAllocatedBytes)
	}
	var pageBytes uint64
	var usedTexels uint64
	for i, page := range telemetry.Pages {
		if page.PageIndex != uint64(i) || page.Width == 0 || page.Height == 0 || page.UsedTexels > page.AllocatedTexels {
			return telemetry, fmt.Errorf("invalid page occupancy at index %d: %+v", i, page)
		}
		if page.Height > ^uint64(0)/page.Width {
			return telemetry, fmt.Errorf("page dimensions overflow at index %d: %dx%d", i, page.Width, page.Height)
		}
		if page.AllocatedTexels != page.Width*page.Height {
			return telemetry, fmt.Errorf("page %d allocatedTexels=%d, dimensions=%dx%d imply %d", i, page.AllocatedTexels, page.Width, page.Height, page.Width*page.Height)
		}
		if usedTexels > ^uint64(0)-page.UsedTexels {
			return telemetry, fmt.Errorf("page used texel count overflow")
		}
		usedTexels += page.UsedTexels
		if page.AllocatedTexels > ^uint64(0)/4 || pageBytes > ^uint64(0)-page.AllocatedTexels*4 {
			return telemetry, fmt.Errorf("page allocation byte count overflow")
		}
		pageBytes += page.AllocatedTexels * 4
	}
	if usedTexels != expectedSourceBytes/4 {
		return telemetry, fmt.Errorf("pages use %d texels, decoded source has %d texels", usedTexels, expectedSourceBytes/4)
	}
	if pageBytes != expectedAllocatedBytes {
		return telemetry, fmt.Errorf("pages allocate %d RGBA bytes, want %d", pageBytes, expectedAllocatedBytes)
	}
	return telemetry, nil
}

func requireTimelineAtlasJSONFields(object json.RawMessage, names ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(object, &fields); err != nil {
		return fmt.Errorf("expected JSON object: %w", err)
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing field %q", name)
		}
	}
	return nil
}

func timelineAtlasUint64PointerString(value *uint64) string {
	if value == nil {
		return "null"
	}
	return strconv.FormatUint(*value, 10)
}

func timelineAtlasReasonContains(reasons []string, fragment string) bool {
	fragment = strings.ToLower(fragment)
	for _, reason := range reasons {
		if strings.Contains(strings.ToLower(reason), fragment) {
			return true
		}
	}
	return false
}

func TestTimelineAtlasParityTelemetryValidation(t *testing.T) {
	valid := []byte(`{"schema":1,"protocol":"NCT1","requestedAssetLayout":"atlas","assetLayout":"atlas","assetPageCount":1,"assetSourceBytes":16,"assetAllocatedBytes":16,"assetTelemetry":{"telemetryScope":"nct1-static-assets","effectiveLayout":"atlas","pages":[{"pageIndex":0,"width":2,"height":2,"usedTexels":4,"allocatedTexels":4}],"textureUploadCallCount":1,"textureUploadBytes":16,"drawOrderPageBindRunCount":1,"bundleBuildCpuWallTimeNs":0,"residentMemory":{"decodedScenePixelBytes":16,"logicalTextureAllocationBytes":16,"rendererOwnedDrawBufferPayloadBytes":64,"renderBundleInternalBytes":null,"readbackRingBufferBytes":4096,"wgpuTransientStagingBytes":null,"physicalVramBytes":null,"unknownReasons":["WGPU render-bundle internal storage is not exposed by the API","WGPU internal upload staging allocation is not exposed by the API","Physical VRAM residency is driver-managed and not exposed by WGPU"]},"unavailableReasons":[]}}`)
	telemetry, err := validateTimelineAtlasParityTelemetry(valid, "atlas", 1, 1, 16, 16)
	if err != nil {
		t.Fatalf("valid NCT1 telemetry rejected: %v", err)
	}
	if telemetry.TelemetryScope != "nct1-static-assets" || telemetry.EffectiveLayout != "atlas" || len(telemetry.Pages) != 1 || telemetry.TextureUploadBytes == nil || *telemetry.TextureUploadBytes != 16 {
		t.Fatalf("unexpected decoded telemetry: %+v", telemetry)
	}

	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"wrong scope", []byte(strings.Replace(string(valid), `nct1-static-assets`, `nct2-streamed-assets`, 1))},
		{"layout mismatch", []byte(strings.Replace(string(valid), `"effectiveLayout":"atlas"`, `"effectiveLayout":"separate"`, 1))},
		{"page count mismatch", []byte(strings.Replace(string(valid), `"assetPageCount":1`, `"assetPageCount":2`, 1))},
		{"upload call mismatch", []byte(strings.Replace(string(valid), `"textureUploadCallCount":1`, `"textureUploadCallCount":2`, 1))},
		{"upload byte mismatch", []byte(strings.Replace(string(valid), `"textureUploadBytes":16`, `"textureUploadBytes":12`, 1))},
		{"decoded byte mismatch", []byte(strings.Replace(string(valid), `"decodedScenePixelBytes":16`, `"decodedScenePixelBytes":12`, 1))},
		{"page allocation mismatch", []byte(strings.Replace(string(valid), `"allocatedTexels":4`, `"allocatedTexels":3`, 1))},
		{"page dimensions mismatch", []byte(strings.Replace(string(valid), `"width":2`, `"width":3`, 1))},
		{"used texel sum mismatch", []byte(strings.Replace(string(valid), `"usedTexels":4`, `"usedTexels":3`, 1))},
		{"known metric null", []byte(strings.Replace(string(valid), `"readbackRingBufferBytes":4096`, `"readbackRingBufferBytes":null`, 1))},
		{"opaque metric present", []byte(strings.Replace(string(valid), `"physicalVramBytes":null`, `"physicalVramBytes":16`, 1))},
		{"missing opaque reason", []byte(strings.Replace(string(valid), `,"WGPU internal upload staging allocation is not exposed by the API"`, ``, 1))},
		{"unknown telemetry field", []byte(strings.Replace(string(valid), `"unavailableReasons":[]`, `"futureMetric":0,"unavailableReasons":[]`, 1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateTimelineAtlasParityTelemetry(tc.payload, "atlas", 1, 1, 16, 16); err == nil {
				t.Fatal("invalid telemetry was accepted")
			}
		})
	}
}
