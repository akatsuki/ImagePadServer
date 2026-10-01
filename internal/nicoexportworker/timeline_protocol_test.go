package nicoexportworker

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTimelineRequestFieldsRoundTripWithoutChangingLegacyDefaults(t *testing.T) {
	legacyData, err := json.Marshal(validTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := ReadRequest(bytes.NewReader(append(legacyData, '\n')))
	if err != nil {
		t.Fatal(err)
	}
	if legacy.TimelineEnabled || legacy.TimelineCompositor != "" || legacy.TimelineReadbackSlots != 0 || legacy.TimelineGPUBackend != "" {
		t.Fatalf("legacy request acquired timeline defaults: %+v", legacy)
	}

	request := validTestRequest()
	request.Backend = "timeline"
	request.TimelineEnabled = true
	request.TimelineCompositor = `C:\helpers\nico-compositord.exe`
	request.TimelineReadbackSlots = 2
	request.TimelineGPUBackend = "vulkan"
	randomSeed := uint32(0x54423331)
	request.TimelineRandomSeed = &randomSeed
	if err := request.Validate(); err != nil {
		t.Fatalf("timeline request validation: %v", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"timeline_enabled":true`, `"timeline_compositor":`, `"timeline_readback_slots":2`, `"timeline_gpu_backend":"vulkan"`, `"timeline_random_seed":1413624625`} {
		if !strings.Contains(string(data), field) {
			t.Fatalf("request JSON lacks %s: %s", field, data)
		}
	}
	got, err := ReadRequest(bytes.NewReader(append(data, '\n')))
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != "timeline" || !got.TimelineEnabled || got.TimelineCompositor != request.TimelineCompositor || got.TimelineReadbackSlots != 2 || got.TimelineGPUBackend != "vulkan" || got.TimelineRandomSeed == nil || *got.TimelineRandomSeed != randomSeed {
		t.Fatalf("round-tripped request = %+v", got)
	}
}

func TestTimelineRequestRejectsInvalidGPUOptionsBeforeWorkerLaunch(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Request)
	}{
		{name: "backend", edit: func(r *Request) { r.TimelineGPUBackend = "opengl" }},
		{name: "readback slots", edit: func(r *Request) { r.TimelineReadbackSlots = 4 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := validTestRequest()
			tc.edit(&request)
			if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "timeline") {
				t.Fatalf("invalid timeline options accepted: %v", err)
			}
		})
	}
}

func TestTimelineResultMetadataRoundTripsInBoundedEvent(t *testing.T) {
	event := Event{
		Version: ProtocolVersion, Type: "result", RunID: "run-1", MediaID: "media-1", OK: true,
		Renderer: "niconico-timeline-wgpu/0.1.0", Backend: "timeline-wgpu", GPUBackend: "vulkan", GPUAdapter: "AMD Radeon",
		HelperSHA256: strings.Repeat("a", 64), BundleSHA256: strings.Repeat("b", 64), ReadbackSlots: 2,
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)+1 > maxEventBytes {
		t.Fatalf("timeline event is %d bytes", len(data)+1)
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Renderer != event.Renderer || got.Backend != event.Backend || got.GPUBackend != event.GPUBackend || got.GPUAdapter != event.GPUAdapter || got.HelperSHA256 != event.HelperSHA256 || got.BundleSHA256 != event.BundleSHA256 || got.ReadbackSlots != 2 {
		t.Fatalf("result metadata round trip = %+v", got)
	}
}
