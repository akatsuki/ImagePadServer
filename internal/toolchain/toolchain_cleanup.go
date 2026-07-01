package toolchain

import (
	"os"
	"path/filepath"
	"strings"

	"imagepadserver/internal/about"
)

func CleanupOldToolVersions() {
	root := binDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	current := about.Version
	for _, e := range entries {
		// Only reap strictly older versions; a higher version's bundle is kept
		// (ffmpegPath runs it in place after a downgrade).
		if e.IsDir() && looksLikeVersionDir(e.Name()) && compareAppVersions(e.Name(), current) < 0 {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
	for _, base := range []string{"ffmpeg", "ffprobe", "yt-dlp"} {
		flat := filepath.Join(root, executableName(base))
		versioned := filepath.Join(root, current, executableName(base))
		if fileExists(flat) && fileExists(versioned) {
			_ = os.Remove(flat)
		}
	}
}

// looksLikeVersionDir reports whether name is an app-version directory such as
// "v1.4.2" (so cleanup never touches unrelated entries).
func looksLikeVersionDir(name string) bool {
	return len(name) >= 2 && name[0] == 'v' && name[1] >= '0' && name[1] <= '9'
}

// higherVersionFFmpegPath returns the ffmpeg path inside the highest app-version
// directory that is newer than the running version and actually contains an
// ffmpeg binary, or "" if none. Such a bundle is run in place rather than
// copied down.
func higherVersionFFmpegPath() string {
	return higherVersionToolPath("ffmpeg")
}

// higherVersionToolPath returns the path to the named tool inside the highest
// app-version directory newer than the running version that contains it, or ""
// if none. Such a binary is run in place rather than copied down.
func higherVersionToolPath(base string) string {
	root := binDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	exe := executableName(base)
	bestName := ""
	for _, e := range entries {
		if !e.IsDir() || !looksLikeVersionDir(e.Name()) {
			continue
		}
		if compareAppVersions(e.Name(), about.Version) <= 0 {
			continue
		}
		if !fileExists(filepath.Join(root, e.Name(), exe)) {
			continue
		}
		if bestName == "" || compareAppVersions(e.Name(), bestName) > 0 {
			bestName = e.Name()
		}
	}
	if bestName == "" {
		return ""
	}
	return filepath.Join(root, bestName, exe)
}

// compareAppVersions compares two app version strings like "v1.4.2" or
// "v1.4.2-dev3". Returns -1 if a<b, 0 if equal, 1 if a>b. A plain release ranks
// above a same-numbered pre-release (…-dev) build.
func compareAppVersions(a, b string) int {
	abase, apre := splitAppVersion(a)
	bbase, bpre := splitAppVersion(b)
	for i := 0; i < 3; i++ {
		if abase[i] != bbase[i] {
			if abase[i] < bbase[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "": // release > pre-release
		return 1
	case bpre == "":
		return -1
	case apre < bpre:
		return -1
	default:
		return 1
	}
}

// splitAppVersion parses "v1.4.2-dev3" into ([1,4,2], "dev3"). Missing numeric
// components default to 0; unparseable components are treated as 0.
func splitAppVersion(v string) ([3]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	base := v
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		base = v[:i]
		pre = v[i+1:]
	}
	var nums [3]int
	for i, part := range strings.SplitN(base, ".", 3) {
		if i > 2 {
			break
		}
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				n = 0
				break
			}
			n = n*10 + int(r-'0')
		}
		nums[i] = n
	}
	return nums, pre
}

// ---------------------------------------------------------------------------
// Execution helpers
// ---------------------------------------------------------------------------
