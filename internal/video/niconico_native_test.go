package video

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"imagepadserver/internal/nicorender"
)

func TestNicoNativeChild(t *testing.T) {
	roleArg := os.Args[len(os.Args)-1]
	if !strings.HasPrefix(roleArg, "nico-native-child-") && len(os.Args) > 1 {
		roleArg = os.Args[len(os.Args)-2]
	}
	role := roleArg
	if !strings.HasPrefix(role, "nico-native-child-") {
		return
	}
	switch strings.TrimPrefix(role, "nico-native-child-") {
	case "fail":
		os.Exit(17)
	case "encoder":
		_, _ = io.Copy(io.Discard, os.Stdin)
	case "encoder-cwd":
		if len(os.Args) < 2 {
			os.Exit(18)
		}
		_ = os.WriteFile(os.Args[len(os.Args)-1], []byte(mustTestWorkingDirectory()), 0600)
		_, _ = io.Copy(io.Discard, os.Stdin)
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
	for _, name := range []string{"success", "missing-done", "bad-count", "native-fail", "encoder-fail", "produce-fail", "cancel", "start-fail"} {
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
			if time.Since(start) > 2*time.Second {
				t.Fatalf("cleanup stalled: %v", err)
			}
		})
	}
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
