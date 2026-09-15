package obsrtmp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/airplaycontract"
)

const backendOutputFixture = `{"streams":[{"codec_name":"h264","width":640,"height":360}],"frames":[{"media_type":"video","key_frame":1,"pict_type":"I","width":640,"height":360,"best_effort_timestamp_time":"4.000000"},{"media_type":"video","key_frame":0,"pict_type":"P","width":640,"height":360,"best_effort_timestamp_time":"4.033333"}]}`

func TestDirectBackendOutputRequiresTimedKeyframeAfterUntimedStartup(t *testing.T) {
	// Observed on the real TCP gate: the join-time decoded I-frame has no
	// timestamp, followed by timed P-frames. None of that prefix may prove a
	// timeline. Require a later timed I-frame AND a progressing successor.
	untimedI := `{"media_type":"video","key_frame":1,"pict_type":"I","width":640,"height":360}`
	preP := `{"media_type":"video","key_frame":0,"pict_type":"P","width":640,"height":360,"best_effort_timestamp_time":"0.1"}`
	timedI := `{"media_type":"video","key_frame":1,"pict_type":"I","width":640,"height":360,"best_effort_timestamp_time":"1.1"}`
	timedP := `{"media_type":"video","key_frame":0,"pict_type":"P","width":640,"height":360,"best_effort_timestamp_time":"1.133333"}`
	for _, tc := range []struct {
		name   string
		frames []string
		valid  bool
	}{
		{"complete-later-timed-sequence", []string{untimedI, preP, timedI, timedP}, true},
		{"untimed-anchor-only", []string{untimedI, preP}, false},
		{"no-successor-after-anchor", []string{untimedI, preP, timedI}, false},
		{"timestamp-missing-after-anchor", []string{timedI, untimedI, timedP}, false},
		{"invalid-prefix-timestamp", []string{untimedI, strings.Replace(preP, "0.1", "NaN", 1), timedI, timedP}, false},
		{"B-frame-prefix", []string{untimedI, strings.Replace(preP, `"P"`, `"B"`, 1), timedI, timedP}, false},
		{"backwards-after-anchor", []string{untimedI, timedI, preP}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := `{"streams":[{"codec_name":"h264","width":640,"height":360}],"frames":[` + strings.Join(tc.frames, ",") + `]}`
			proof, err := decodeDirectBackendOutputJSON([]byte(payload), 640, 360)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t proof=%+v err=%v", tc.valid, proof, err)
			}
			if tc.valid && (proof.Frames != 2 || proof.KeyFrames != 1 || proof.FirstPTS != 1.1 || proof.LastPTS != 1.133333) {
				t.Fatal("startup prefix was used as timestamp evidence")
			}
		})
	}
}

// Reject metadata-only readiness, wrong dimensions, undecoded output and
// non-progressing frames; a native event or successful DESCRIBE is not enough.
func TestDirectBackendOutputRequiresDecodedVideo(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		valid         bool
	}{
		{"decoded", backendOutputFixture, true},
		{"metadata only", `{"streams":[{"codec_name":"h264","width":640,"height":360}]}`, false},
		{"no streams", `{"frames":[]}`, false},
		{"wrong codec", strings.ReplaceAll(backendOutputFixture, "h264", "hevc"), false},
		{"wrong size", strings.ReplaceAll(backendOutputFixture, "640", "1280"), false},
		{"dimension change", strings.Replace(backendOutputFixture, `"width":640`, `"width":1280`, 1), false},
		{"decoded frame wrong size", strings.Replace(backendOutputFixture, `"key_frame":0,"pict_type":"P","width":640`, `"key_frame":0,"pict_type":"P","width":1280`, 1), false},
		{"no decoded keyframe", strings.ReplaceAll(backendOutputFixture, `"key_frame":1`, `"key_frame":0`), false},
		{"B frame", strings.ReplaceAll(backendOutputFixture, `"pict_type":"P"`, `"pict_type":"B"`), false},
		{"no progress", strings.ReplaceAll(backendOutputFixture, "4.033333", "4.000000"), false},
		{"regression", strings.ReplaceAll(backendOutputFixture, "4.033333", "3.000000"), false},
		{"invalid timestamp", strings.ReplaceAll(backendOutputFixture, "4.033333", "NaN"), false},
		{"partial JSON", backendOutputFixture[:len(backendOutputFixture)-2], false},
		{"trailing JSON", backendOutputFixture + `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proof, err := decodeDirectBackendOutputJSON([]byte(tc.payload), 640, 360)
			if (err == nil) != tc.valid {
				t.Fatalf("proof=%+v err=%v", proof, err)
			}
			if tc.valid && (proof.Frames != 2 || proof.KeyFrames != 1 || proof.Width != 640 || proof.Height != 360 || proof.LastPTS <= proof.FirstPTS) {
				t.Fatalf("invalid decoded proof: %+v", proof)
			}
		})
	}
}

func TestDirectBackendOutputHelperProcess(t *testing.T) {
	mode := os.Getenv("IMAGEPAD_TEST_BACKEND_OUTPUT")
	if mode == "" {
		return
	}
	switch mode {
	case "good":
		fmt.Print(backendOutputFixture)
	case "stderr":
		fmt.Print(backendOutputFixture)
		fmt.Fprint(os.Stderr, "decoder error")
	case "overflow":
		fmt.Print(strings.Repeat("x", directRecordingProbeOutputLimit+1))
	case "hang":
		time.Sleep(time.Minute)
	case "exit":
		fmt.Fprint(os.Stderr, "method DESCRIBE failed: 404 Not Found")
		os.Exit(22)
	}
	os.Exit(0)
}

func TestDirectBackendOutputProbeProcessBoundary(t *testing.T) {
	for _, mode := range []string{"good", "stderr", "overflow", "hang", "exit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			var child *exec.Cmd
			command := func(ctx context.Context, name string, args ...string) *exec.Cmd {
				joined := strings.Join(args, " ")
				if name != "owned-ffprobe" || !strings.Contains(joined, "-rtsp_transport tcp") || !strings.Contains(joined, "-show_frames") || !strings.Contains(joined, "-select_streams v:0") || !strings.Contains(joined, "-read_intervals %+5") || args[len(args)-1] != "rtsp://127.0.0.1:49000/private" {
					t.Fatalf("wrong command %s %v", name, args)
				}
				child = exec.CommandContext(ctx, os.Args[0], "-test.run=TestDirectBackendOutputHelperProcess$")
				child.Env = append(os.Environ(), "IMAGEPAD_TEST_BACKEND_OUTPUT="+mode)
				return child
			}
			proof, err := runDirectBackendFFprobe(ctx, "owned-ffprobe", "rtsp://127.0.0.1:49000/private", 640, 360, command)
			if mode == "good" {
				if err != nil || proof.Frames != 2 {
					t.Fatalf("%+v %v", proof, err)
				}
			} else if err == nil {
				t.Fatal("failed probe accepted")
			}
			if child == nil || child.ProcessState == nil {
				t.Fatal("probe process was not reaped")
			}
			if mode == "hang" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout lost: %v", err)
			}
			if mode == "exit" && !strings.Contains(err.Error(), "method DESCRIBE failed: 404 Not Found") {
				t.Fatalf("probe failure discarded its diagnostic: %v", err)
			}
		})
	}
}

func TestDirectBackendOutputProbeRejectsNonPrivateTarget(t *testing.T) {
	for _, endpoint := range []string{"rtsp://192.168.0.2:49000/path", "rtsp://127.0.0.1/path", "file:///tmp/a.mp4", "rtsp://u:p@127.0.0.1:49000/path", "rtsp://127.0.0.1:49000/", "rtsp://127.0.0.1:49000/path?x=1"} {
		_, err := runDirectBackendFFprobe(t.Context(), "owned-ffprobe", endpoint, 640, 360, func(context.Context, string, ...string) *exec.Cmd {
			t.Fatal("invalid target started a process")
			return nil
		})
		if err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
}

func backendOutputIdentityFixture(t *testing.T) (airplaycontract.DeliveryGeneration, directBackendRoute) {
	t.Helper()
	descriptor := airplaycontract.DeliveryGeneration{
		SessionID: "private-output", SessionEpoch: 3, Generation: 2, RequestID: "quality-change",
		Output:                 airplaycontract.DeliveryOutput{Width: 640, Height: 360},
		ExpectedRecordingWidth: 640, ExpectedRecordingHeight: 360,
		ArtifactPaths: airplaycontract.NewPublisherArtifacts(t.TempDir(), "private-output", 2),
	}
	runtime := newMediaMTXRuntime("unused", mediaMTXSessionConfig{
		Path: mediaMTXPathName(descriptor.SessionID), Ports: mediaMTXPorts{RTSP: 49001, BackendRTSP: 49000},
	})
	route := directBackendRoute{sessionID: descriptor.SessionID, sessionEpoch: 3, generation: 2,
		requestID: descriptor.RequestID, privateRTSPPort: 49000, runtime: runtime}
	return descriptor, route
}

func TestDirectBackendOutputGenerationIdentity(t *testing.T) {
	for _, mode := range []string{"valid", "old generation", "foreign session", "epoch", "request", "public port", "foreign path", "artifact"} {
		t.Run(mode, func(t *testing.T) {
			descriptor, route := backendOutputIdentityFixture(t)
			switch mode {
			case "old generation":
				route.generation = 1
			case "foreign session":
				route.sessionID = "other"
			case "epoch":
				route.sessionEpoch++
			case "request":
				route.requestID = "other"
			case "public port":
				route.privateRTSPPort = 49001
			case "foreign path":
				route.runtime.cfg.Path = "other"
			case "artifact":
				descriptor.ArtifactPaths.Generation = 1
			}
			calls := 0
			command := func(ctx context.Context, name string, args ...string) *exec.Cmd {
				calls++
				if args[len(args)-1] != "rtsp://127.0.0.1:49000/obs_privateoutput" {
					t.Fatalf("wrong endpoint: %v", args)
				}
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestDirectBackendOutputHelperProcess$")
				cmd.Env = append(os.Environ(), "IMAGEPAD_TEST_BACKEND_OUTPUT=good")
				return cmd
			}
			proof, err := probeDirectBackendGeneration(t.Context(), "owned-ffprobe", descriptor, route, command)
			if mode == "valid" {
				if err != nil || calls != 1 || !proof.matches(descriptor, route) {
					t.Fatalf("proof=%+v err=%v", proof, err)
				}
				old := route
				old.generation = 1
				if proof.matches(descriptor, old) {
					t.Fatal("proof matches old runtime generation")
				}
				return
			}
			if err == nil || calls != 0 {
				t.Fatalf("invalid identity reached probe: calls=%d err=%v", calls, err)
			}
		})
	}
}
