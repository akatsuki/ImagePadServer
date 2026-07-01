package toolchain

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/about"
)

func extractFFmpegZip(zipPath, ffmpegTarget string) error {
	if err := extractNamedBinaryFromZip(zipPath, ffmpegTarget, executableName("ffmpeg")); err != nil {
		return err
	}
	ffprobeTarget := filepath.Join(filepath.Dir(ffmpegTarget), executableName("ffprobe"))
	if err := extractNamedBinaryFromZip(zipPath, ffprobeTarget, executableName("ffprobe")); err != nil {
		// Partial installation — clean up ffmpeg so we don't leave a broken state.
		os.Remove(ffmpegTarget)
		return fmt.Errorf("ffprobe not found after FFmpeg installation: %w", err)
	}
	return nil
}

func extractNamedBinaryFromZip(zipPath, target, binaryName string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		if !strings.EqualFold(filepath.Base(name), binaryName) {
			continue
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		defer src.Close()

		tempTarget := target + ".tmp"
		dst, err := os.OpenFile(tempTarget, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(dst, src)
		closeErr := dst.Close()
		if copyErr != nil {
			_ = os.Remove(tempTarget)
			return copyErr
		}
		if closeErr != nil {
			_ = os.Remove(tempTarget)
			return closeErr
		}
		return replaceFile(target, tempTarget)
	}
	return fmt.Errorf("%s was not found in the downloaded archive", binaryName)
}

// replaceFile moves tempTarget onto target, tolerating a target that is locked
// because it is currently executing or being scanned by antivirus (a common
// Windows failure that surfaced as "rename ... Access is denied"). It first
// tries a direct rename, then moves the existing file aside (Windows allows
// renaming a running executable even when it cannot be overwritten), and
// finally retries a few times for transient locks.
func replaceFile(target, tempTarget string) error {
	if err := os.Rename(tempTarget, target); err == nil {
		return nil
	}
	aside := fmt.Sprintf("%s.old-%d", target, time.Now().UnixNano())
	if err := os.Rename(target, aside); err == nil {
		if err := os.Rename(tempTarget, target); err == nil {
			_ = os.Remove(aside) // best effort; may still be locked
			return nil
		}
		_ = os.Rename(aside, target) // restore on failure
	}
	var lastErr error
	for i := 0; i < 5; i++ {
		time.Sleep(time.Duration(150*(i+1)) * time.Millisecond)
		_ = os.Remove(target)
		if err := os.Rename(tempTarget, target); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	_ = os.Remove(tempTarget)
	if lastErr == nil {
		lastErr = fmt.Errorf("could not replace %s", target)
	}
	return lastErr
}

// migrateFFmpegToolsInto copies a working ffmpeg + ffprobe pair from an older
// version directory (or the legacy flat bin/ layout) into dstDir, so an app
// update does not have to re-download the archive. Higher versions are never
// migrated down (they are run in place by ffmpegPath). It only migrates when
// the candidate ffmpeg reports the pinned version and ffprobe is present and
// valid; otherwise it returns false and the caller downloads the pinned build.
func migrateFFmpegToolsInto(dstDir string) bool {
	ffName := executableName("ffmpeg")
	fpName := executableName("ffprobe")
	root := binDir()

	// Legacy flat layout first, then strictly older version directories.
	candidates := []string{root}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && looksLikeVersionDir(e.Name()) && compareAppVersions(e.Name(), about.Version) < 0 {
				candidates = append(candidates, filepath.Join(root, e.Name()))
			}
		}
	}

	for _, dir := range candidates {
		if filepath.Clean(dir) == filepath.Clean(dstDir) {
			continue
		}
		ff := filepath.Join(dir, ffName)
		fp := filepath.Join(dir, fpName)
		if !fileExists(ff) || !fileExists(fp) {
			continue
		}
		if !ffmpegReportsVersion(ff, ffmpegPinnedVersion) {
			continue
		}
		if validateToolExecutable(fp, "-version") != nil {
			continue
		}
		// If a copy cannot complete (e.g. a locked file), drop any partial
		// result and move on; exhausting all candidates returns false so the
		// caller falls back to a fresh download.
		ffDst := filepath.Join(dstDir, ffName)
		fpDst := filepath.Join(dstDir, fpName)
		if err := copyFileTo(ffDst, ff); err != nil {
			_ = os.Remove(ffDst)
			continue
		}
		if err := copyFileTo(fpDst, fp); err != nil {
			_ = os.Remove(ffDst)
			_ = os.Remove(fpDst)
			continue
		}
		return true
	}
	return false
}

// migrateYTDLPInto copies an existing yt-dlp from an older version dir (or the
// legacy flat layout) into dstDir to avoid a re-download. When wantSHA is set
// the candidate must match it (so the update-to-latest path is not satisfied by
// a stale build); otherwise any binary that passes --version is accepted.
// Higher versions are never copied down — they are run in place by ytdlpPath.
func migrateYTDLPInto(dstDir, wantSHA string) bool {
	exe := executableName("yt-dlp")
	root := binDir()

	candidates := []string{root}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && looksLikeVersionDir(e.Name()) && compareAppVersions(e.Name(), about.Version) < 0 {
				candidates = append(candidates, filepath.Join(root, e.Name()))
			}
		}
	}

	for _, dir := range candidates {
		if filepath.Clean(dir) == filepath.Clean(dstDir) {
			continue
		}
		src := filepath.Join(dir, exe)
		if !fileExists(src) {
			continue
		}
		if wantSHA != "" {
			if verifySHA256(src, wantSHA) != nil {
				continue
			}
		} else if validateToolExecutable(src, "--version") != nil {
			continue
		}
		dst := filepath.Join(dstDir, exe)
		if err := copyFileTo(dst, src); err != nil {
			_ = os.Remove(dst)
			continue
		}
		return true
	}
	return false
}

// ffmpegReportsVersion runs "<path> -version" and reports whether the output
// identifies the wanted FFmpeg version. It is a var so tests can stub it.
var ffmpegReportsVersion = func(path, want string) bool {
	cmd := exec.Command(path, "-version")
	hideWindow(cmd)
	output, err := CombinedOutputTrackedFFmpeg(cmd)
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "ffmpeg version "+want)
}

// copyFileTo copies src to dst via a temp file and atomic rename, preserving an
// executable mode. It is a var so tests can stub it (e.g. to simulate a locked
// destination). When it fails during migration the caller falls back to a fresh
// download.
var copyFileTo = func(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".copy.tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return replaceFile(dst, tmp)
}

// CleanupOldToolVersions removes per-version tool directories that do not match
// the running app version, plus any leftover legacy flat ffmpeg/ffprobe once a
// versioned copy exists. It is best-effort: a directory still locked by another
// running instance is left for a later run.
