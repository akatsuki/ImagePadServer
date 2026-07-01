package video

import (
	"context"
	"os/exec"

	"imagepadserver/internal/toolchain"
)

var validateToolExecutable = toolchain.ValidateExecutable

func EnsureFFmpeg() (string, error) {
	if ffmpeg, err := ffmpegPath(); err == nil && validateToolExecutable(ffmpeg, "-version") == nil {
		return ffmpeg, nil
	}
	return toolchain.EnsureFFmpeg()
}

func EnsureFFprobe() (string, error) {
	return toolchain.EnsureFFprobe()
}

func EnsureYTDLP() (string, error) {
	return toolchain.EnsureYTDLP()
}

func EnsureLatestYTDLP() (string, bool, error) {
	return toolchain.EnsureLatestYTDLP()
}

func ToolsReady() bool {
	return toolchain.ToolsReady()
}

func ValidateInstalledTools() {
	toolchain.ValidateInstalledTools()
}

func CleanupOldToolVersions() {
	toolchain.CleanupOldToolVersions()
}

func executableName(base string) string {
	return toolchain.ExecutableName(base)
}

func ffmpegPath() (string, error) {
	return toolchain.FFmpegPath()
}

func ffprobePath() (string, error) {
	return toolchain.FFprobePath()
}

func ytdlpPath() (string, error) {
	return toolchain.YTDLPPath()
}

func run(ffmpeg string, args ...string) error {
	return toolchain.Run(ffmpeg, args...)
}

func runInDir(dir, ffmpeg string, args ...string) error {
	return toolchain.RunInDir(dir, ffmpeg, args...)
}

func runInDirContext(ctx context.Context, dir, ffmpeg string, args ...string) error {
	return toolchain.RunInDirContext(ctx, dir, ffmpeg, args...)
}

func fileExists(path string) bool {
	return toolchain.FileExists(path)
}

func trimOutput(output []byte) string {
	return toolchain.TrimOutput(output)
}

func hasFFmpegAssFilter(ffmpeg string) bool {
	return toolchain.HasFFmpegAssFilter(ffmpeg)
}

func TrackStartedFFmpeg(cmd *exec.Cmd) func() {
	return toolchain.TrackStartedFFmpeg(cmd)
}

func CleanupTrackedFFmpeg() (int, error) {
	return toolchain.CleanupTrackedFFmpeg()
}

func CombinedOutputTrackedFFmpeg(cmd *exec.Cmd) ([]byte, error) {
	return toolchain.CombinedOutputTrackedFFmpeg(cmd)
}

func SeparateOutputTrackedFFmpeg(cmd *exec.Cmd) ([]byte, []byte, error) {
	return toolchain.SeparateOutputTrackedFFmpeg(cmd)
}

func KillOwnedProcesses(executableBase, requiredMarker string, preferredPIDs []int) (int, error) {
	return toolchain.KillOwnedProcesses(executableBase, requiredMarker, preferredPIDs)
}

func KillFFmpegOnPort(port int) (int, error) {
	return toolchain.KillFFmpegOnPort(port)
}
