package toolchain

import "sync"

// ToolInstall is an immutable snapshot of the current tool acquisition.
type ToolInstall struct {
	Active  bool   `json:"active"`
	Tool    string `json:"tool"`    // "ffmpeg" | "ffprobe" | "yt-dlp"
	Phase   string `json:"phase"`   // "download" | "extract" | "validate" | ""
	Percent int    `json:"percent"` // 0-100; download is byte-driven, others 0
	Attempt int    `json:"attempt"` // 1-based attempt across sources/retries
	Failed  bool   `json:"failed"`
	Message string `json:"message"`
}

type installTracker struct {
	mu     sync.Mutex
	status ToolInstall
}

var installProgress = &installTracker{}

// ToolInstallStatus returns a copy of the current tracker state.
func ToolInstallStatus() ToolInstall {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	return installProgress.status
}

func resetInstallProgress() {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status = ToolInstall{}
}

// ClearToolInstallStatus clears stale progress after tools are known to be
// available, including a previous failed install banner.
func ClearToolInstallStatus() {
	resetInstallProgress()
}

func installBegin(tool string) {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status = ToolInstall{Active: true, Tool: tool, Attempt: 1}
}

func installPhase(phase string) {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status.Phase = phase
	if phase != "download" {
		installProgress.status.Percent = 0
	}
}

func installPercent(p int) {
	if p < 0 {
		p = 0
	} else if p > 100 {
		p = 100
	}
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status.Percent = p
}

func installAttempt(n int) {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status.Attempt = n
}

func installDone() {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status = ToolInstall{Active: false, Failed: false, Percent: 100}
}

func installFail(msg string) {
	installProgress.mu.Lock()
	defer installProgress.mu.Unlock()
	installProgress.status.Active = false
	installProgress.status.Failed = true
	installProgress.status.Message = msg
}

// progressWriter counts bytes and reports an integer percent via onProgress.
type progressWriter struct {
	total      int64
	written    int64
	lastPct    int
	onProgress func(int)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.written += int64(n)
	if w.onProgress != nil && w.total > 0 {
		pct := int(w.written * 100 / w.total)
		if pct > 100 {
			pct = 100
		}
		if pct != w.lastPct {
			w.lastPct = pct
			w.onProgress(pct)
		}
	}
	return n, nil
}
