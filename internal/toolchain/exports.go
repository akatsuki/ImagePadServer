package toolchain

import "context"

func ExecutableName(base string) string {
	return executableName(base)
}

func FFmpegPath() (string, error) {
	return ffmpegPath()
}

func FFprobePath() (string, error) {
	return ffprobePath()
}

func YTDLPPath() (string, error) {
	return ytdlpPath()
}

func Run(ffmpeg string, args ...string) error {
	return run(ffmpeg, args...)
}

func RunInDir(dir, ffmpeg string, args ...string) error {
	return runInDir(dir, ffmpeg, args...)
}

func RunInDirContext(ctx context.Context, dir, ffmpeg string, args ...string) error {
	return runInDirContext(ctx, dir, ffmpeg, args...)
}

func FileExists(path string) bool {
	return fileExists(path)
}

func TrimOutput(output []byte) string {
	return trimOutput(output)
}

func ValidateExecutable(path string, args ...string) error {
	return validateExecutable(path, args...)
}

func HasFFmpegAssFilter(ffmpeg string) bool {
	return hasFFmpegAssFilter(ffmpeg)
}
