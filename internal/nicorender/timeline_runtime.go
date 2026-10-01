package nicorender

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	timelineRuntimeProbeTimeout = 10 * time.Second
	timelineRuntimeWaitDelay    = 2 * time.Second
	timelineReportMaxBytes      = 64 << 10
	timelineSelfTestFrameBytes  = 33 * 19 * 4 * 3
	timelineProtocolNCT1        = "NCT1"
	timelineProtocolNCT2        = "NCT2"
)

// TimelineRuntimeOptions select the helper's adapter API, bounded readback
// ring, and comment texture layout. Empty values normalize to auto, three slots,
// and separate textures.
type TimelineRuntimeOptions struct {
	Backend       string
	ReadbackSlots int
	AssetLayout   string
}

type TimelineSelfTestReport struct {
	Schema                int    `json:"schema"`
	Protocol              string `json:"protocol"`
	Renderer              string `json:"renderer"`
	Version               string `json:"version"`
	Backend               string `json:"backend"`
	AdapterName           string `json:"adapterName"`
	AdapterType           string `json:"adapterType"`
	ReadbackSlots         int    `json:"readbackSlots"`
	RequestedAssetLayout  string `json:"requestedAssetLayout"`
	AssetLayout           string `json:"assetLayout"`
	AssetLayoutFallback   string `json:"assetLayoutFallbackReason"`
	AssetPageCount        int    `json:"assetPageCount"`
	AssetSourceBytes      uint64 `json:"assetSourceBytes"`
	AssetAllocatedBytes   uint64 `json:"assetAllocatedBytes"`
	MaxTextureDimension2D uint32 `json:"maxTextureDimension2D"`
	TestFrameCount        uint32 `json:"testFrameCount"`
	TestFrameBytes        uint64 `json:"testFrameBytes"`
	TestFrameSHA256       string `json:"testFrameSHA256"`
}

// TimelineCapabilitiesReport describes an explicitly versioned helper
// protocol capability response. It is separate from the legacy NCT1
// self-test report so older strict consumers never receive extra fields.
type TimelineCapabilitiesReport struct {
	Schema          int      `json:"schema"`
	Protocol        string   `json:"protocol"`
	ProtocolVersion int      `json:"protocolVersion"`
	InputMode       string   `json:"inputMode"`
	OutputFormat    string   `json:"outputFormat"`
	Capabilities    []string `json:"capabilities"`
}

type timelineCommandResult struct {
	stdout         []byte
	stderr         []byte
	err            error
	timedOut       bool
	stdoutExceeded bool
	stderrExceeded bool
}

type timelineCommandRunner func(context.Context, []string) timelineCommandResult

func normalizeTimelineRuntimeOptions(options TimelineRuntimeOptions) (TimelineRuntimeOptions, error) {
	options.Backend = strings.TrimSpace(options.Backend)
	if options.Backend == "" {
		options.Backend = "auto"
	}
	if options.Backend != "auto" && options.Backend != "dx12" && options.Backend != "vulkan" && options.Backend != "metal" {
		return TimelineRuntimeOptions{}, fmt.Errorf("niconico timeline: unsupported WGPU backend %q", options.Backend)
	}
	if options.ReadbackSlots == 0 {
		options.ReadbackSlots = 3
	}
	if options.ReadbackSlots < 1 || options.ReadbackSlots > 3 {
		return TimelineRuntimeOptions{}, fmt.Errorf("niconico timeline: readback slots must be 1, 2, or 3")
	}
	options.AssetLayout = strings.TrimSpace(options.AssetLayout)
	if options.AssetLayout == "" {
		options.AssetLayout = "separate"
	}
	if options.AssetLayout != "separate" && options.AssetLayout != "atlas" {
		return TimelineRuntimeOptions{}, fmt.Errorf("niconico timeline: unsupported WGPU asset layout %q", options.AssetLayout)
	}
	return options, nil
}

// ValidateTimelineRuntimeOptions checks the WGPU backend and bounded
// readback-ring settings without locating or launching the helper.
func ValidateTimelineRuntimeOptions(options TimelineRuntimeOptions) (TimelineRuntimeOptions, error) {
	return normalizeTimelineRuntimeOptions(options)
}

// PrepareTimelineCompositor resolves a job-owned helper, validates its
// versioned protocol capabilities, then checks the NCT1 GPU self-test before
// encoding starts. An empty path uses a manifest-validated payload in
// nico_timeline_embedded builds.
func PrepareTimelineCompositor(ctx context.Context, configured string, options TimelineRuntimeOptions) (string, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeTimelineRuntimeOptions(options)
	if err != nil {
		return "", nil, err
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return "", nil, fmt.Errorf("%w: timeline compositor is unsupported on %s", ErrUnavailable, runtime.GOOS)
	}
	path := strings.TrimSpace(configured)
	embedded := path == ""
	cleanup := func() {}
	if embedded {
		payload, manifest := timelinePayload()
		if len(payload) == 0 || len(manifest) == 0 {
			return "", nil, fmt.Errorf("%w: timeline compositor is not embedded in this build", ErrUnavailable)
		}
		path, cleanup, err = materializeTimelinePayload(payload, manifest, runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", nil, fmt.Errorf("%w: timeline embedded payload: %v", ErrUnavailable, err)
		}
	}
	if !embedded {
		path, err = filepath.Abs(path)
		if err != nil {
			return "", nil, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%w: timeline compositor: %v", ErrUnavailable, err)
	}
	if !info.Mode().IsRegular() {
		cleanup()
		return "", nil, fmt.Errorf("%w: timeline compositor path is not a regular file", ErrUnavailable)
	}
	if !embedded {
		payload, err := os.ReadFile(path)
		if err != nil {
			cleanup()
			return "", nil, fmt.Errorf("%w: timeline compositor read: %v", ErrUnavailable, err)
		}
		if err := validateTimelineExecutable(payload, runtime.GOOS, runtime.GOARCH); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("%w: timeline compositor architecture: %v", ErrUnavailable, err)
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, timelineRuntimeProbeTimeout)
	defer cancel()
	runner := func(runCtx context.Context, args []string) timelineCommandResult {
		return runTimelineRuntimeCommand(runCtx, path, args)
	}
	_, legacyReport, err := probeTimelineHelperProtocol(probeCtx, normalized, runtime.GOOS, runner)
	if err != nil {
		cleanup()
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if probeCtx.Err() != nil {
			return "", nil, fmt.Errorf("%w: timeline capability/self-test probe timed out after %s", ErrUnavailable, timelineRuntimeProbeTimeout)
		}
		return "", nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if legacyReport == nil {
		result := runner(probeCtx, timelineSelfTestArgs(normalized))
		if result.err != nil {
			cleanup()
			if ctx.Err() != nil {
				return "", nil, ctx.Err()
			}
			if probeCtx.Err() != nil || result.timedOut {
				return "", nil, fmt.Errorf("%w: timeline self-test timed out after %s", ErrUnavailable, timelineRuntimeProbeTimeout)
			}
			return "", nil, fmt.Errorf("%w: timeline self-test: %v (%s)", ErrUnavailable, result.err, strings.TrimSpace(string(result.stderr)))
		}
		if result.stdoutExceeded {
			cleanup()
			return "", nil, fmt.Errorf("%w: timeline self-test JSON exceeds 64 KiB", ErrUnavailable)
		}
		if result.stderrExceeded {
			cleanup()
			return "", nil, fmt.Errorf("%w: timeline self-test wrote more than 64 KiB to stderr", ErrUnavailable)
		}
		if _, err := validateTimelineSelfTestReport(result.stdout, normalized, runtime.GOOS); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("%w: timeline self-test report: %v", ErrUnavailable, err)
		}
	}
	return path, cleanup, nil
}

func timelineSelfTestArgs(options TimelineRuntimeOptions) []string {
	args := []string{"--self-test", "--backend", options.Backend, "--readback-slots", fmt.Sprint(options.ReadbackSlots)}
	if options.AssetLayout != "separate" {
		args = append(args, "--asset-layout", options.AssetLayout)
	}
	return args
}

func probeTimelineHelperProtocol(ctx context.Context, options TimelineRuntimeOptions, goos string, run timelineCommandRunner) (string, *TimelineSelfTestReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	normalized, err := normalizeTimelineRuntimeOptions(options)
	if err != nil {
		return "", nil, err
	}
	if run == nil {
		return "", nil, fmt.Errorf("timeline command runner is nil")
	}
	capabilities := run(ctx, []string{"--capabilities", "--protocol-version", "2"})
	if capabilities.err == nil {
		if capabilities.timedOut {
			return "", nil, fmt.Errorf("timeline capability probe timed out")
		}
		if capabilities.stdoutExceeded || capabilities.stderrExceeded {
			return "", nil, fmt.Errorf("timeline capability probe exceeded the 64 KiB output limit")
		}
		if _, err := validateTimelineCapabilitiesReport(capabilities.stdout); err != nil {
			return "", nil, fmt.Errorf("invalid timeline capability response: %w", err)
		}
		return timelineProtocolNCT2, nil, nil
	}
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	if capabilities.timedOut {
		return "", nil, fmt.Errorf("timeline capability probe timed out")
	}
	if !isUnsupportedTimelineCapabilitiesOption(capabilities) {
		return "", nil, fmt.Errorf("timeline capability probe failed: %v (%s)", capabilities.err, strings.TrimSpace(string(capabilities.stderr)))
	}

	legacy := run(ctx, timelineSelfTestArgs(normalized))
	if legacy.err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		if legacy.timedOut {
			return "", nil, fmt.Errorf("legacy NCT1 self-test timed out")
		}
		return "", nil, fmt.Errorf("legacy NCT1 self-test failed: %v (%s)", legacy.err, strings.TrimSpace(string(legacy.stderr)))
	}
	if legacy.stdoutExceeded || legacy.stderrExceeded {
		return "", nil, fmt.Errorf("legacy NCT1 self-test exceeded the 64 KiB output limit")
	}
	report, err := validateTimelineSelfTestReport(legacy.stdout, normalized, goos)
	if err != nil {
		return "", nil, fmt.Errorf("legacy NCT1 self-test report is invalid: %w", err)
	}
	return timelineProtocolNCT1, &report, nil
}

func isUnsupportedTimelineCapabilitiesOption(result timelineCommandResult) bool {
	return result.err != nil && !result.timedOut && !result.stdoutExceeded && !result.stderrExceeded && len(result.stdout) == 0 && strings.TrimSpace(string(result.stderr)) == `NICO_ERROR unknown argument "--capabilities"`
}

func runTimelineRuntimeCommand(ctx context.Context, path string, args []string) timelineCommandResult {
	cmd := exec.CommandContext(ctx, path, args...)
	hideNativeWindow(cmd)
	cmd.WaitDelay = timelineRuntimeWaitDelay
	var stdout, stderr boundedRuntimeOutput
	stdout.limit = timelineReportMaxBytes
	stderr.limit = timelineReportMaxBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return timelineCommandResult{
		stdout:         stdout.Bytes(),
		stderr:         stderr.Bytes(),
		err:            err,
		timedOut:       ctx.Err() != nil,
		stdoutExceeded: stdout.exceeded,
		stderrExceeded: stderr.exceeded,
	}
}

func validateTimelineCapabilitiesReport(payload []byte) (TimelineCapabilitiesReport, error) {
	var report TimelineCapabilitiesReport
	if len(payload) == 0 || len(payload) > timelineReportMaxBytes {
		return report, fmt.Errorf("capability response size %d is outside 1..%d bytes", len(payload), timelineReportMaxBytes)
	}
	if err := ValidateStrictJSONDocument(payload); err != nil {
		return report, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, fmt.Errorf("decode JSON: %w", err)
	}
	if report.Schema != 1 || report.Protocol != timelineProtocolNCT2 || report.ProtocolVersion != 2 || report.InputMode != "--stdin-stream" || report.OutputFormat != "rgba8" {
		return report, fmt.Errorf("unsupported capability identity schema=%d protocol=%q version=%d input=%q output=%q", report.Schema, report.Protocol, report.ProtocolVersion, report.InputMode, report.OutputFormat)
	}
	required := map[string]bool{"incremental-assets": false, "ordered-elements": false, "watermarks": false, "end-and-eof": false}
	for _, capability := range report.Capabilities {
		if _, ok := required[capability]; ok {
			required[capability] = true
		}
	}
	for capability, present := range required {
		if !present {
			return report, fmt.Errorf("capability response is missing required capability %q", capability)
		}
	}
	return report, nil
}

// ValidateStrictJSONDocument validates a single JSON value and rejects duplicate object keys.
func ValidateStrictJSONDocument(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var scanValue func() error
	scanValue = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := scanValue(); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("invalid JSON object ending: %v", err)
			}
		case '[':
			for decoder.More() {
				if err := scanValue(); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("invalid JSON array ending: %v", err)
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
		return nil
	}
	if err := scanValue(); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON has trailing data")
	}
	return nil
}

func validateTimelineSelfTestReport(payload []byte, options TimelineRuntimeOptions, goos string) (TimelineSelfTestReport, error) {
	var report TimelineSelfTestReport
	if len(payload) == 0 || len(payload) > timelineReportMaxBytes {
		return report, fmt.Errorf("report size %d is outside 1..%d bytes", len(payload), timelineReportMaxBytes)
	}
	if err := ValidateStrictJSONDocument(payload); err != nil {
		return report, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return report, fmt.Errorf("report has trailing JSON data")
	}
	if report.Schema != 1 || report.Protocol != "NCT1" || report.Renderer != "wgpu" || strings.TrimSpace(report.Version) == "" {
		return report, fmt.Errorf("unsupported self-test identity schema=%d protocol=%q renderer=%q version=%q", report.Schema, report.Protocol, report.Renderer, report.Version)
	}
	if report.AdapterName == "" || (report.AdapterType != "DiscreteGpu" && report.AdapterType != "IntegratedGpu") {
		return report, fmt.Errorf("self-test did not report a supported hardware adapter: %q (%q)", report.AdapterName, report.AdapterType)
	}
	if report.ReadbackSlots != options.ReadbackSlots {
		return report, fmt.Errorf("self-test used %d readback slots, requested %d", report.ReadbackSlots, options.ReadbackSlots)
	}
	if report.MaxTextureDimension2D < 33 {
		return report, fmt.Errorf("adapter texture limit %d is too small for self-test", report.MaxTextureDimension2D)
	}
	if report.TestFrameCount != 3 || report.TestFrameBytes != timelineSelfTestFrameBytes {
		return report, fmt.Errorf("self-test readback was %d frames / %d bytes; expected 3 / %d", report.TestFrameCount, report.TestFrameBytes, timelineSelfTestFrameBytes)
	}
	digest, err := hex.DecodeString(report.TestFrameSHA256)
	if err != nil || len(digest) != 32 {
		return report, fmt.Errorf("invalid self-test RGBA SHA-256")
	}
	allowed := allowedTimelineBackends(goos)
	if !allowed[report.Backend] {
		return report, fmt.Errorf("backend %q is not allowed on %s", report.Backend, goos)
	}
	if options.Backend != "auto" && report.Backend != options.Backend {
		return report, fmt.Errorf("self-test selected %q, requested %q", report.Backend, options.Backend)
	}
	expectedLayout := strings.TrimSpace(options.AssetLayout)
	if expectedLayout == "" {
		expectedLayout = "separate"
	}
	if err := ValidateTimelineAssetLayoutReport(expectedLayout, report.RequestedAssetLayout, report.AssetLayout, report.AssetLayoutFallback, report.AssetPageCount, report.AssetSourceBytes, report.AssetAllocatedBytes); err != nil {
		return report, fmt.Errorf("self-test asset layout: %w", err)
	}
	return report, nil
}

// ValidateTimelineAssetLayoutReport keeps old separate-layout helpers usable,
// while requiring explicit evidence before an atlas run can be accepted.
func ValidateTimelineAssetLayoutReport(requested, reportedRequested, actual, fallback string, pageCount int, sourceBytes, allocatedBytes uint64) error {
	requested = strings.TrimSpace(requested)
	reportedRequested = strings.TrimSpace(reportedRequested)
	actual = strings.TrimSpace(actual)
	fallback = strings.TrimSpace(fallback)
	if requested == "" {
		requested = "separate"
	}
	if reportedRequested == "" && actual == "" {
		if requested == "separate" {
			return nil // older helper report; its renderer used separate textures
		}
		return fmt.Errorf("legacy helper report cannot verify requested %q layout", requested)
	}
	if reportedRequested != requested {
		return fmt.Errorf("helper requested %q layout, want %q", reportedRequested, requested)
	}
	if actual != "separate" && actual != "atlas" {
		return fmt.Errorf("helper reported invalid actual layout %q", actual)
	}
	if requested == "separate" && actual != "separate" {
		return fmt.Errorf("separate request selected %q layout", actual)
	}
	if requested == "atlas" && actual == "separate" && fallback == "" {
		return fmt.Errorf("atlas request fell back without a reason")
	}
	if actual == "atlas" && fallback != "" {
		return fmt.Errorf("atlas layout reported an unexpected fallback reason")
	}
	if pageCount < 0 {
		return fmt.Errorf("helper reported negative asset page count %d", pageCount)
	}
	if actual == "separate" && allocatedBytes != sourceBytes {
		return fmt.Errorf("separate layout allocated %d bytes for %d source bytes", allocatedBytes, sourceBytes)
	}
	if pageCount == 0 && (sourceBytes != 0 || allocatedBytes != 0) {
		return fmt.Errorf("helper reported asset bytes without any texture pages")
	}
	return nil
}

func allowedTimelineBackends(goos string) map[string]bool {
	switch goos {
	case "windows":
		return map[string]bool{"dx12": true, "vulkan": true}
	case "darwin":
		return map[string]bool{"metal": true}
	case "linux":
		return map[string]bool{"vulkan": true}
	default:
		return map[string]bool{}
	}
}

// TimelineBackendAllowed reports whether a runtime helper backend is part of
// the supported platform allowlist.
func TimelineBackendAllowed(backend, goos string) bool {
	return allowedTimelineBackends(goos)[backend]
}

type boundedRuntimeOutput struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedRuntimeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(remaining, n)])
	}
	if n > remaining {
		b.exceeded = true
	}
	return n, nil
}
