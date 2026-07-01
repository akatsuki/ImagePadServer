package toolchain

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func run(ffmpeg string, args ...string) error {
	cmd := exec.Command(ffmpeg, args...)
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

func runInDir(dir, ffmpeg string, args ...string) error {
	cmd := exec.Command(ffmpeg, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

func runInDirContext(ctx context.Context, dir, ffmpeg string, args ...string) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, trimOutput(output))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func trimOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 700 {
		return text[len(text)-700:]
	}
	if text == "" {
		return "no output"
	}
	return text
}
