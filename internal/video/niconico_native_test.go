package video

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/nicorender"
)

func TestNicoNativeChild(t *testing.T) {
	role := ""
	for i := len(os.Args) - 1; i >= 0; i-- {
		if strings.HasPrefix(os.Args[i], "nico-native-child-") {
			role = os.Args[i]
			break
		}
	}
	if !strings.HasPrefix(role, "nico-native-child-") {
		return
	}
	switch strings.TrimPrefix(role, "nico-native-child-") {
	case "fail":
		os.Exit(17)
	case "partial-exit":
		_, _ = io.Copy(io.Discard, os.Stdin)
		_, _ = os.Stdout.Write([]byte{1, 2, 3, 4})
		fmt.Fprintln(os.Stderr, "NICO_PROGRESS 1 10")
		os.Exit(17)
	case "truncated-head", "truncated-tail":
		_, _ = io.Copy(io.Discard, os.Stdin)
		size := 1
		if strings.HasSuffix(role, "truncated-tail") {
			size = 39
		}
		_, _ = os.Stdout.Write(make([]byte, size))
		fmt.Fprintln(os.Stderr, "NICO_PROGRESS 1 10")
		fmt.Fprintln(os.Stderr, "NICO_PROGRESS 10 10")
		fmt.Fprintln(os.Stderr, "NICO_DONE 10")
	case "count-encoder":
		got, err := io.Copy(io.Discard, os.Stdin)
		expected, parseErr := strconv.ParseInt(os.Args[len(os.Args)-1], 10, 64)
		if err != nil || parseErr != nil || got != expected {
			os.Exit(21)
		}
	case "encoder":
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "slow-encoder":
		if len(os.Args) < 2 {
			os.Exit(18)
		}
		marker := os.Args[len(os.Args)-1]
		buffer := make([]byte, 4096)
		markedRead := false
		for {
			n, err := os.Stdin.Read(buffer)
			if n > 0 {
				if !markedRead {
					_ = os.WriteFile(marker, []byte("read"), 0600)
					markedRead = true
				}
				time.Sleep(25 * time.Millisecond)
			}
			if err != nil {
				return
			}
		}
	case "burst":
		_, _ = io.Copy(io.Discard, os.Stdin)
		if len(os.Args) < 2 {
			os.Exit(19)
		}
		marker := os.Args[len(os.Args)-1]
		_ = os.WriteFile(marker, []byte("started"), 0600)
		chunk := make([]byte, 64*1024)
		for i := 0; i < 1024; i++ {
			if _, err := os.Stdout.Write(chunk); err != nil {
				os.Exit(20)
			}
		}
		_ = os.WriteFile(marker+".complete", []byte("complete"), 0600)
	case "encoder-cwd":
		if len(os.Args) < 2 {
			os.Exit(18)
		}
		_ = os.WriteFile(os.Args[len(os.Args)-1], []byte(mustTestWorkingDirectory()), 0600)
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "stream-valid", "stream-zero-assets", "stream-missing-end", "stream-duplicate-end", "stream-end-after-done", "stream-asset-after-end", "stream-duplicate-asset", "stream-malformed-end":
		_, _ = io.Copy(io.Discard, os.Stdin)
		_, _ = os.Stdout.Write(make([]byte, 40))
		switch strings.TrimPrefix(role, "nico-native-child-") {
		case "stream-valid":
			fmt.Fprintln(os.Stderr, "NICO_FIRST_ASSET_READY")
			fmt.Fprintln(os.Stderr, "NICO_STREAM_END")
		case "stream-zero-assets":
			fmt.Fprintln(os.Stderr, "NICO_STREAM_END")
		case "stream-duplicate-end":
			fmt.Fprintln(os.Stderr, "NICO_STREAM_END")
			fmt.Fprintln(os.Stderr, "NICO_STREAM_END")
		case "stream-end-after-done":
			fmt.Fprintln(os.Stderr, "NICO_PROGRESS 1 10")
			fmt.Fprintln(os.Stderr, "NICO_PROGRESS 10 10")
			fmt.Fprintln(os.Stderr, "NICO_DONE 10")
			fmt.Fprintln(os.Stderr, "NICO_STREAM_END")
			return
		case "stream-asset-after-end":
			fmt.Fprintln(os.Stderr, "NICO_STREAM_END")
			fmt.Fprintln(os.Stderr, "NICO_FIRST_ASSET_READY")
		case "stream-duplicate-asset":
			fmt.Fprintln(os.Stderr, "NICO_FIRST_ASSET_READY")
			fmt.Fprintln(os.Stderr, "NICO_FIRST_ASSET_READY")
			fmt.Fprintln(os.Stderr, "NICO_STREAM_END")
		case "stream-malformed-end":
			fmt.Fprintln(os.Stderr, "NICO_STREAM_END extra")
		default:
			// The missing-end case deliberately reaches NICO_DONE without its marker.
		}
		fmt.Fprintln(os.Stderr, "NICO_PROGRESS 1 10")
		fmt.Fprintln(os.Stderr, "NICO_PROGRESS 10 10")
		fmt.Fprintln(os.Stderr, "NICO_DONE 10")
	case "blocked":
		for {
			time.Sleep(time.Second)
		}
	default:
		_, _ = io.Copy(io.Discard, os.Stdin)
		_, _ = os.Stdout.Write(make([]byte, 40))
		fmt.Fprintln(os.Stderr, "NICO_PROGRESS 1 10")
		fmt.Fprintln(os.Stderr, "NICO_PROGRESS 10 10")
		if role == "nico-native-child-ok" {
			fmt.Fprintln(os.Stderr, "NICO_DONE 10")
		}
		if role == "nico-native-child-bad-count" {
			fmt.Fprintln(os.Stderr, "NICO_DONE 9")
		}
	}
	os.Exit(0)
}

func TestNicoNativeStreamStatusRequiresValidatedStreamEnd(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := func(role string) []string {
		return []string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-" + role, "--stdin-stream"}
	}
	for _, tc := range []struct {
		role    string
		wantErr bool
	}{
		{role: "stream-valid"},
		{role: "stream-zero-assets"},
		{role: "stream-missing-end", wantErr: true},
		{role: "stream-duplicate-end", wantErr: true},
		{role: "stream-end-after-done", wantErr: true},
		{role: "stream-asset-after-end", wantErr: true},
		{role: "stream-duplicate-asset", wantErr: true},
		{role: "stream-malformed-end", wantErr: true},
	} {
		t.Run(tc.role, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := runNicoNativePipeTimedInDir(
				ctx, exe, args(tc.role), exe,
				[]string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-encoder"},
				10, nil,
				func(context.Context, io.Writer) (nicorender.RenderReport, error) {
					return nicorender.RenderReport{FrameCount: 10}, nil
				}, "",
			)
			if tc.wantErr && err == nil {
				t.Fatal("invalid NCT2 status sequence was accepted")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("valid NCT2 status sequence failed: %v", err)
			}
		})
	}
}

func TestNicoNativeMilestonesShareOneOrigin(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := func(role string) []string {
		return []string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-" + role, "--stdin-stream"}
	}
	result, err := runNicoNativePipeTimedInDir(
		context.Background(), exe, args("stream-valid"), exe,
		[]string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-encoder"},
		10, nil,
		func(_ context.Context, w io.Writer) (nicorender.RenderReport, error) {
			_, err := w.Write([]byte("frozen declarations"))
			return nicorender.RenderReport{FrameCount: 10}, err
		}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	values := []struct {
		name  string
		value *time.Duration
	}{
		{"capture_done", result.milestones.CaptureDone},
		{"first_asset_ready", result.milestones.FirstAssetReady},
		{"stream_end", result.milestones.StreamEnd},
		{"first_frame", result.milestones.FirstFrame},
		{"helper_done", result.milestones.HelperDone},
		{"ffmpeg_done", result.milestones.FFmpegDone},
	}
	for _, value := range values {
		if value.value == nil || *value.value < 0 {
			t.Fatalf("%s offset=%v, want reached non-negative offset from attempt origin", value.name, value.value)
		}
	}
	if *result.milestones.CaptureDone > *result.milestones.FirstAssetReady ||
		*result.milestones.FirstAssetReady > *result.milestones.StreamEnd ||
		*result.milestones.StreamEnd > *result.milestones.FirstFrame ||
		*result.milestones.FirstFrame > *result.milestones.HelperDone {
		t.Fatalf("milestones are not offsets from the shared origin in event order: %+v", result.milestones)
	}
}

func TestNicoNativeCaptureDoneIsRecordedWhenProducerFails(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-ok"}
	result, err := runNicoNativePipeTimedInDir(
		context.Background(), exe, args, exe,
		[]string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-encoder"},
		10, nil,
		func(context.Context, io.Writer) (nicorender.RenderReport, error) {
			return nicorender.RenderReport{FrameCount: 10}, errors.New("capture failed")
		}, "",
	)
	if err == nil || !strings.Contains(err.Error(), "capture failed") {
		t.Fatalf("producer failure err=%v, want capture failure", err)
	}
	if result.milestones.CaptureDone == nil || *result.milestones.CaptureDone < 0 {
		t.Fatalf("capture_done=%v, want reached offset despite producer failure", result.milestones.CaptureDone)
	}
}

func TestNicoNativeStatusParsesFragmentedTimelineMarkers(t *testing.T) {
	var progress []int64
	status := nicoNativeStatus{
		expected:  2,
		streaming: true,
		progress:  func(value, _ int64) { progress = append(progress, value) },
	}
	for _, part := range []string{
		"NICO_FIRST_ASSET_", "READY\nNICO_STREAM_", "END\nNICO_PROGRESS 1 ",
		"2\nNICO_PROGRESS 2 2\nNICO_DONE ", "2\n",
	} {
		if _, err := status.Write([]byte(part)); err != nil {
			t.Fatalf("write status fragment %q: %v", part, err)
		}
	}
	if err := status.complete(); err != nil {
		t.Fatalf("fragmented NCT2 status: %v", err)
	}
	if !status.firstAssetSeen || !status.streamEndSeen || status.firstAssetAt.IsZero() || status.streamEndAt.IsZero() || status.firstFrameAt.IsZero() {
		t.Fatalf("reached marker state incomplete: %+v", status)
	}
	if status.firstAssetAt.After(status.streamEndAt) || status.streamEndAt.After(status.firstFrameAt) {
		t.Fatalf("marker/frame receipt order=%v, %v, %v", status.firstAssetAt, status.streamEndAt, status.firstFrameAt)
	}
	if !reflect.DeepEqual(progress, []int64{1, 2}) {
		t.Fatalf("progress=%v, want [1 2]", progress)
	}
}

func TestNicoNativeNCT1StatusIgnoresNCT2Markers(t *testing.T) {
	status := nicoNativeStatus{expected: 2}
	if _, err := status.Write([]byte("NICO_FIRST_ASSET_READY\nNICO_STREAM_END\nNICO_PROGRESS 1 2\nNICO_PROGRESS 2 2\nNICO_DONE 2\n")); err != nil {
		t.Fatalf("legacy status write: %v", err)
	}
	if err := status.complete(); err != nil {
		t.Fatalf("legacy NCT1 status changed: %v", err)
	}
	if status.firstAssetSeen || status.streamEndSeen || !status.firstFrameAt.IsZero() {
		t.Fatalf("legacy NCT1 parser interpreted NCT2 marker state: %+v", status)
	}
}

func mustTestWorkingDirectory() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func TestNicoNativePipeLifecycle(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := func(role string) []string {
		return []string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-" + role}
	}
	for _, name := range []string{"success", "missing-done", "bad-count", "native-fail", "encoder-fail", "produce-fail", "cancel", "start-fail", "partial-output-exit", "truncated-head", "truncated-tail", "slow-output-cancel"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			na, ea := args("ok"), args("encoder")
			nativeExe := exe
			produce := func(context.Context, io.Writer) (nicorender.RenderReport, error) {
				return nicorender.RenderReport{FrameCount: 10}, nil
			}
			switch name {
			case "missing-done":
				na = args("missing-done")
			case "bad-count":
				na = args("bad-count")
			case "native-fail":
				na = args("fail")
			case "encoder-fail":
				ea = args("fail")
			case "produce-fail":
				produce = func(context.Context, io.Writer) (nicorender.RenderReport, error) {
					return nicorender.RenderReport{}, fmt.Errorf("fixture failed")
				}
			case "cancel":
				na = args("blocked")
				produce = func(ctx context.Context, w io.Writer) (nicorender.RenderReport, error) {
					b := make([]byte, 65536)
					for {
						if _, err := w.Write(b); err != nil {
							return nicorender.RenderReport{}, err
						}
					}
				}
				time.AfterFunc(150*time.Millisecond, cancel)
			case "start-fail":
				nativeExe += ".missing"
			case "partial-output-exit":
				na = args("partial-exit")
			case "truncated-head", "truncated-tail":
				na = args(name)
				ea = append(args("count-encoder"), "40")
			case "slow-output-cancel":
				helperStarted := filepath.Join(t.TempDir(), "helper-started")
				encoderReadStarted := filepath.Join(t.TempDir(), "encoder-read-started")
				na = append(args("burst"), helperStarted)
				ea = append(args("slow-encoder"), encoderReadStarted)
				resultCh := make(chan error, 1)
				go func() {
					_, err := runNicoNativePipe(ctx, exe, na, exe, ea, 10, nil, produce)
					resultCh <- err
				}()
				if !waitForNicoNativeTestFile(helperStarted, 2*time.Second) || !waitForNicoNativeTestFile(encoderReadStarted, 2*time.Second) {
					cancel()
					select {
					case <-resultCh:
					case <-time.After(5 * time.Second):
						t.Fatal("pipeline did not return after setup timeout cancellation")
					}
					t.Fatal("helper and slow encoder did not both start")
				}
				// Give the deliberately slow consumer time to block the helper on
				// its stdout pipe before canceling the owning pipeline.
				time.Sleep(150 * time.Millisecond)
				if waitForNicoNativeTestFile(helperStarted+".complete", 10*time.Millisecond) {
					cancel()
					<-resultCh
					t.Fatal("helper unexpectedly completed while downstream was deliberately slow")
				}
				select {
				case err := <-resultCh:
					cancel()
					t.Fatalf("owner returned before cancellation: %v", err)
				default:
				}
				start := time.Now()
				cancel()
				select {
				case err := <-resultCh:
					if err == nil {
						t.Fatal("slow output pipe cancellation returned success")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("owner did not reap the blocked helper and slow encoder within 5s")
				}
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatalf("test context error=%v, want explicit cancellation", ctx.Err())
				}
				if elapsed := time.Since(start); elapsed > 5*time.Second {
					t.Fatalf("slow-output child cleanup took %s, want <=5s", elapsed)
				}
				return
			}
			var progress []int64
			start := time.Now()
			report, err := runNicoNativePipe(ctx, nativeExe, na, exe, ea, 10, func(n, total int64) { progress = append(progress, n) }, produce)
			if name == "success" {
				if err != nil || report.FrameCount != 10 {
					t.Fatalf("success: %+v %v", report, err)
				}
				if fmt.Sprint(progress) != "[1 10]" {
					t.Fatalf("progress %v", progress)
				}
			} else if err == nil {
				t.Fatal("accepted incomplete pipeline")
			}
			if name == "encoder-fail" {
				var pipeErr *nicoNativePipeError
				if !errors.As(err, &pipeErr) || pipeErr.PrimaryStage != "ffmpeg" {
					t.Fatalf("primary pipeline failure=%v, want typed ffmpeg failure", err)
				}
			}
			if name == "partial-output-exit" && (!strings.Contains(err.Error(), "exit_code=17") || fmt.Sprint(progress) != "[1]") {
				t.Fatalf("partial-output failure=%v progress=%v, want helper exit 17 after progress 1", err, progress)
			}
			if strings.HasPrefix(name, "truncated-") && !strings.Contains(err.Error(), "exit_code=21") {
				t.Fatalf("truncated stream failure=%v, want encoder byte-count exit 21", err)
			}
			limit := 2 * time.Second
			if name == "partial-output-exit" {
				limit = 5 * time.Second
			}
			if time.Since(start) > limit {
				t.Fatalf("cleanup stalled: %v", err)
			}
		})
	}
}

func waitForNicoNativeTestFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, err := os.Stat(path)
	return err == nil
}

func TestNicoNativePipeTimedReportsConcurrentStages(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := func(role string) []string {
		return []string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-" + role}
	}
	result, err := runNicoNativePipeTimed(
		context.Background(),
		exe, args("ok"), exe, args("encoder"), 10,
		func(int64, int64) {},
		func(context.Context, io.Writer) (nicorender.RenderReport, error) {
			return nicorender.RenderReport{FrameCount: 10}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.report.FrameCount != 10 {
		t.Fatalf("report=%+v, want ten frames", result.report)
	}
	want := []string{"native_sprite_stream", "native_compositor", "native_ffmpeg"}
	if len(result.stageTimings) != len(want) {
		t.Fatalf("stage timings=%v, want %v", result.stageTimings, want)
	}
	for i, name := range want {
		if result.stageTimings[i].Name != name || result.stageTimings[i].Elapsed < 0 {
			t.Fatalf("stage[%d]=%+v, want %q with non-negative elapsed", i, result.stageTimings[i], name)
		}
	}
}

func TestNicoNativePipeTimedUsesEncoderWorkingDirectory(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	marker := filepath.Join(workDir, "encoder-cwd.txt")
	args := func(role string, extra ...string) []string {
		return append([]string{"-test.run=^TestNicoNativeChild$", "--", "nico-native-child-" + role}, extra...)
	}
	_, err = runNicoNativePipeTimedInDir(
		context.Background(),
		exe, args("ok"), exe, args("encoder-cwd", marker), 10,
		func(int64, int64) {},
		func(context.Context, io.Writer) (nicorender.RenderReport, error) {
			return nicorender.RenderReport{FrameCount: 10}, nil
		}, workDir,
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Clean(string(data)); got != filepath.Clean(workDir) {
		t.Fatalf("encoder working directory=%q, want %q", got, workDir)
	}
}
