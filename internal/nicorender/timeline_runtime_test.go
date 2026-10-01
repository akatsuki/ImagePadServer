package nicorender

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTimelineRuntimeNormalizesOptions(t *testing.T) {
	cases := []struct {
		name string
		in   TimelineRuntimeOptions
		want TimelineRuntimeOptions
	}{
		{name: "defaults", in: TimelineRuntimeOptions{}, want: TimelineRuntimeOptions{Backend: "auto", ReadbackSlots: 3, AssetLayout: "separate"}},
		{name: "explicit", in: TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 2, AssetLayout: "atlas"}, want: TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 2, AssetLayout: "atlas"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeTimelineRuntimeOptions(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("normalizeTimelineRuntimeOptions(%+v) = %+v, %v; want %+v", tc.in, got, err, tc.want)
			}
		})
	}
	for _, in := range []TimelineRuntimeOptions{{Backend: "gl"}, {ReadbackSlots: 4}, {Backend: "VULKAN"}, {AssetLayout: "quilt"}} {
		if _, err := normalizeTimelineRuntimeOptions(in); err == nil {
			t.Fatalf("accepted invalid runtime options %+v", in)
		}
	}
}

func TestTimelineRuntimeSelfTestReportRequiresHardwareAndMatchingRequest(t *testing.T) {
	valid := `{"schema":1,"protocol":"NCT1","renderer":"wgpu","version":"0.1.0","backend":"vulkan","adapterName":"GPU","adapterType":"DiscreteGpu","readbackSlots":2,"maxTextureDimension2D":16384,"testFrameCount":3,"testFrameBytes":7524,"testFrameSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	report, err := validateTimelineSelfTestReport([]byte(valid), TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 2}, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if report.AdapterName != "GPU" || report.Backend != "vulkan" || report.TestFrameCount != 3 {
		t.Fatalf("unexpected parsed report: %+v", report)
	}
	atlasReportJSON := strings.Replace(valid,
		`"backend":"vulkan"`,
		`"backend":"vulkan","requestedAssetLayout":"atlas","assetLayout":"atlas","assetPageCount":1,"assetSourceBytes":100,"assetAllocatedBytes":100`,
		1,
	)
	atlasReport, err := validateTimelineSelfTestReport([]byte(atlasReportJSON), TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 2, AssetLayout: "atlas"}, "linux")
	if err != nil {
		t.Fatalf("validate atlas self-test report: %v", err)
	}
	if atlasReport.AssetLayout != "atlas" || atlasReport.AssetPageCount != 1 {
		t.Fatalf("unexpected atlas self-test report: %+v", atlasReport)
	}
	for _, tc := range []struct {
		name    string
		payload string
		options TimelineRuntimeOptions
		goos    string
	}{
		{name: "protocol", payload: strings.Replace(valid, `"NCT1"`, `"NPS3"`, 1), options: TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 2}, goos: "linux"},
		{name: "software", payload: strings.Replace(valid, `"DiscreteGpu"`, `"Cpu"`, 1), options: TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 2}, goos: "linux"},
		{name: "backend mismatch", payload: valid, options: TimelineRuntimeOptions{Backend: "dx12", ReadbackSlots: 2}, goos: "windows"},
		{name: "slot mismatch", payload: valid, options: TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 3}, goos: "linux"},
		{name: "auto backend outside allowlist", payload: strings.Replace(valid, `"vulkan"`, `"gl"`, 1), options: TimelineRuntimeOptions{Backend: "auto", ReadbackSlots: 2}, goos: "linux"},
		{name: "zero texture limit", payload: strings.Replace(valid, `"maxTextureDimension2D":16384`, `"maxTextureDimension2D":0`, 1), options: TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 2}, goos: "linux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateTimelineSelfTestReport([]byte(tc.payload), tc.options, tc.goos); err == nil {
				t.Fatal("accepted invalid self-test report")
			}
		})
	}
	if _, err := validateTimelineSelfTestReport(make([]byte, 64*1024+1), TimelineRuntimeOptions{Backend: "auto", ReadbackSlots: 3}, "windows"); err == nil {
		t.Fatal("accepted oversized report")
	}
}

func TestTimelineCapabilitiesReportIsStrict(t *testing.T) {
	valid := `{"schema":1,"protocol":"NCT2","protocolVersion":2,"inputMode":"--stdin-stream","outputFormat":"rgba8","capabilities":["incremental-assets","ordered-elements","watermarks","end-and-eof"]}`
	report, err := validateTimelineCapabilitiesReport([]byte(valid))
	if err != nil {
		t.Fatalf("valid NCT2 capabilities rejected: %v", err)
	}
	if report.Schema != 1 || report.Protocol != "NCT2" || report.ProtocolVersion != 2 || report.InputMode != "--stdin-stream" || report.OutputFormat != "rgba8" {
		t.Fatalf("unexpected capability report: %+v", report)
	}
	for _, tc := range []struct {
		name string
		json string
	}{
		{"missing schema", `{"protocol":"NCT2","protocolVersion":2,"inputMode":"--stdin-stream","outputFormat":"rgba8","capabilities":["incremental-assets","ordered-elements","watermarks","end-and-eof"]}`},
		{"missing protocol", strings.Replace(valid, `,"protocol":"NCT2"`, ``, 1)},
		{"missing protocol version", strings.Replace(valid, `,"protocolVersion":2`, ``, 1)},
		{"missing input mode", strings.Replace(valid, `,"inputMode":"--stdin-stream"`, ``, 1)},
		{"missing output format", strings.Replace(valid, `,"outputFormat":"rgba8"`, ``, 1)},
		{"missing capabilities field", strings.Replace(valid, `,"capabilities":["incremental-assets","ordered-elements","watermarks","end-and-eof"]`, ``, 1)},
		{"schema mismatch", strings.Replace(valid, `"schema":1`, `"schema":2`, 1)},
		{"protocol mismatch", strings.Replace(valid, `"NCT2"`, `"NCT1"`, 1)},
		{"version mismatch", strings.Replace(valid, `"protocolVersion":2`, `"protocolVersion":1`, 1)},
		{"input mismatch", strings.Replace(valid, `"--stdin-stream"`, `"--stdin"`, 1)},
		{"output mismatch", strings.Replace(valid, `"rgba8"`, `"rgb8"`, 1)},
		{"missing capability", strings.Replace(valid, `,"end-and-eof"`, ``, 1)},
		{"unknown field", strings.Replace(valid, `,"capabilities"`, `,"future":true,"capabilities"`, 1)},
		{"duplicate field", strings.Replace(valid, `,"protocolVersion":2`, `,"schema":1,"protocolVersion":2`, 1)},
		{"duplicate capabilities field", strings.Replace(valid, `,"capabilities"`, `,"protocol":"NCT2","capabilities"`, 1)},
		{"trailing object", valid + ` {}`},
		{"trailing token", valid + ` x`},
		{"empty", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateTimelineCapabilitiesReport([]byte(tc.json)); err == nil {
				t.Fatal("accepted invalid capabilities JSON")
			}
		})
	}
}

func TestTimelineRuntimeProtocolProbeUsesNCT2OrStrictLegacyNCT1(t *testing.T) {
	capabilityJSON := `{"schema":1,"protocol":"NCT2","protocolVersion":2,"inputMode":"--stdin-stream","outputFormat":"rgba8","capabilities":["incremental-assets","ordered-elements","watermarks","end-and-eof"]}`
	legacyReport := []byte(`{"schema":1,"protocol":"NCT1","renderer":"wgpu","version":"0.1.0","backend":"vulkan","adapterName":"GPU","adapterType":"DiscreteGpu","readbackSlots":2,"maxTextureDimension2D":16384,"testFrameCount":3,"testFrameBytes":7524,"testFrameSHA256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	options := TimelineRuntimeOptions{Backend: "vulkan", ReadbackSlots: 2}
	validLegacyDiagnostic := []byte(`NICO_ERROR unknown argument "--capabilities"`)

	t.Run("new helper", func(t *testing.T) {
		calls := 0
		protocol, legacy, err := probeTimelineHelperProtocol(context.Background(), options, "linux", func(_ context.Context, args []string) timelineCommandResult {
			calls++
			if calls != 1 || !reflect.DeepEqual(args, []string{"--capabilities", "--protocol-version", "2"}) {
				t.Fatalf("unexpected capability command %d: %q", calls, args)
			}
			return timelineCommandResult{stdout: []byte(capabilityJSON)}
		})
		if err != nil || protocol != "NCT2" || legacy != nil || calls != 1 {
			t.Fatalf("probe = protocol %q, legacy %v, calls %d, err %v", protocol, legacy, calls, err)
		}
	})

	t.Run("old helper with strictly valid NCT1 self-test", func(t *testing.T) {
		calls := 0
		protocol, legacy, err := probeTimelineHelperProtocol(context.Background(), options, "linux", func(_ context.Context, args []string) timelineCommandResult {
			calls++
			if calls == 1 {
				return timelineCommandResult{stderr: validLegacyDiagnostic, err: errors.New("exit status 2")}
			}
			if !reflect.DeepEqual(args, []string{"--self-test", "--backend", "vulkan", "--readback-slots", "2"}) {
				t.Fatalf("unexpected legacy self-test command: %q", args)
			}
			return timelineCommandResult{stdout: legacyReport}
		})
		if err != nil || protocol != "NCT1" || legacy == nil || calls != 2 {
			t.Fatalf("probe = protocol %q, legacy %v, calls %d, err %v", protocol, legacy, calls, err)
		}
	})

	for _, tc := range []struct {
		name      string
		first     timelineCommandResult
		second    timelineCommandResult
		wantCalls int
	}{
		{"arbitrary execution error", timelineCommandResult{err: errors.New("exit status 1"), stderr: []byte("NICO_ERROR gpu unavailable")}, timelineCommandResult{stdout: legacyReport}, 1},
		{"timeout", timelineCommandResult{err: context.DeadlineExceeded, timedOut: true, stderr: validLegacyDiagnostic}, timelineCommandResult{stdout: legacyReport}, 1},
		{"zero exit with unknown-option text", timelineCommandResult{stderr: validLegacyDiagnostic}, timelineCommandResult{stdout: legacyReport}, 1},
		{"unknown option with stdout", timelineCommandResult{stdout: []byte("noise"), stderr: validLegacyDiagnostic, err: errors.New("exit status 2")}, timelineCommandResult{stdout: legacyReport}, 1},
		{"unknown option with oversized output", timelineCommandResult{stderr: validLegacyDiagnostic, stderrExceeded: true, err: errors.New("exit status 2")}, timelineCommandResult{stdout: legacyReport}, 1},
		{"malformed legacy self-test", timelineCommandResult{stderr: validLegacyDiagnostic, err: errors.New("exit status 2")}, timelineCommandResult{stdout: []byte(`{} {}`)}, 2},
		{"trailing legacy self-test JSON", timelineCommandResult{stderr: validLegacyDiagnostic, err: errors.New("exit status 2")}, timelineCommandResult{stdout: append(append([]byte(nil), legacyReport...), []byte(` {}`)...)}, 2},
		{"duplicate legacy self-test field", timelineCommandResult{stderr: validLegacyDiagnostic, err: errors.New("exit status 2")}, timelineCommandResult{stdout: []byte(strings.Replace(string(legacyReport), `,"protocol"`, `,"schema":1,"protocol"`, 1))}, 2},
		{"unknown field in legacy self-test", timelineCommandResult{stderr: validLegacyDiagnostic, err: errors.New("exit status 2")}, timelineCommandResult{stdout: append(append([]byte(nil), legacyReport[:len(legacyReport)-1]...), []byte(`,"future":true}`)...)}, 2},
		{"legacy self-test failed", timelineCommandResult{stderr: validLegacyDiagnostic, err: errors.New("exit status 2")}, timelineCommandResult{err: errors.New("exit status 1")}, 2},
		{"legacy self-test timeout", timelineCommandResult{stderr: validLegacyDiagnostic, err: errors.New("exit status 2")}, timelineCommandResult{err: context.DeadlineExceeded, timedOut: true}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			protocol, legacy, err := probeTimelineHelperProtocol(context.Background(), options, "linux", func(context.Context, []string) timelineCommandResult {
				calls++
				if calls == 1 {
					return tc.first
				}
				return tc.second
			})
			if err == nil || protocol != "" || legacy != nil || calls != tc.wantCalls {
				t.Fatalf("probe = protocol %q, legacy %v, calls %d, err %v", protocol, legacy, calls, err)
			}
		})
	}

	t.Run("invalid successful capability response does not fall back", func(t *testing.T) {
		calls := 0
		invalid := strings.Replace(capabilityJSON, `,"end-and-eof"`, ``, 1)
		protocol, legacy, err := probeTimelineHelperProtocol(context.Background(), options, "linux", func(context.Context, []string) timelineCommandResult {
			calls++
			if calls == 1 {
				return timelineCommandResult{stdout: []byte(invalid)}
			}
			return timelineCommandResult{stdout: legacyReport}
		})
		if err == nil || protocol != "" || legacy != nil || calls != 1 {
			t.Fatalf("invalid capability probe fell back: protocol %q, legacy %v, calls %d, err %v", protocol, legacy, calls, err)
		}
	})
}

func TestTimelineRuntimeMissingEmbeddedHelperIsUnavailable(t *testing.T) {
	_, cleanup, err := PrepareTimelineCompositor(context.Background(), filepath.Join(t.TempDir(), "missing-helper"), TimelineRuntimeOptions{})
	if cleanup != nil {
		cleanup()
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable without an embedded or configured helper, got %v", err)
	}
}

func TestTimelineRuntimeExplicitWrongArchitectureIsUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-compositor")
	if err := os.WriteFile(path, []byte("not an executable"), 0600); err != nil {
		t.Fatal(err)
	}
	_, cleanup, err := PrepareTimelineCompositor(context.Background(), path, TimelineRuntimeOptions{})
	if cleanup != nil {
		cleanup()
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable for a non-executable explicit helper, got %v", err)
	}
}

func TestTimelineRuntimeCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cleanup, err := PrepareTimelineCompositor(ctx, "helper", TimelineRuntimeOptions{})
	if cleanup != nil {
		cleanup()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context canceled, got %v", err)
	}
}

func TestTimelineRuntimeRealHelper(t *testing.T) {
	if os.Getenv("NICO_TIMELINE_INTEGRATION") != "1" {
		t.Skip("set NICO_TIMELINE_INTEGRATION=1 to run the real GPU helper self-test")
	}
	helper := strings.TrimSpace(os.Getenv("NICO_TIMELINE_HELPER"))
	if helper == "" {
		t.Fatal("NICO_TIMELINE_INTEGRATION=1 requires NICO_TIMELINE_HELPER")
	}
	backend := strings.TrimSpace(os.Getenv("NICO_TIMELINE_GPU_BACKEND"))
	if backend == "" {
		backend = "auto"
	}
	slots := 3
	if value := strings.TrimSpace(os.Getenv("NICO_TIMELINE_READBACK_SLOTS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("invalid NICO_TIMELINE_READBACK_SLOTS: %v", err)
		}
		slots = parsed
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	path, cleanup, err := PrepareTimelineCompositor(ctx, helper, TimelineRuntimeOptions{Backend: backend, ReadbackSlots: slots})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("helper path is not absolute: %q", path)
	}
}
