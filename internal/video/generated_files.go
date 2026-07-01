package video

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func GeneratedFiles(outDir, id string) []string {
	var files []string
	playlist := filepath.Join(outDir, playlistName(id))
	if fileExists(playlist) {
		files = append(files, playlist)
	}
	pattern := filepath.Join(outDir, "current*.ts")
	if id != "" {
		pattern = filepath.Join(outDir, "current-"+safeID(id)+"-*.ts")
	}
	matches, _ := filepath.Glob(pattern)
	files = append(files, matches...)
	mp4 := filepath.Join(outDir, MP4File)
	if fileExists(mp4) {
		files = append(files, mp4)
	}
	return files
}

func removeHLSForID(outDir, id string) {
	_ = os.Remove(filepath.Join(outDir, playlistName(id)))
	pattern := filepath.Join(outDir, "current*.ts")
	if id != "" {
		pattern = filepath.Join(outDir, "current-"+safeID(id)+"-*.ts")
	}
	matches, _ := filepath.Glob(pattern)
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func RemoveGenerated(outDir string) {
	stopActive(outDir)
	removeGenerated(outDir)
}

func CancelQueue(outDir string) {
	cancelQueue(outDir)
	stopActive(outDir)
}

// CancelConversion stops and discards any pending or running conversion job for
// the given media id so it can never be resumed. Unlike preemption (which sends
// a running job back to "pending" to be retried later), this is used when the
// published media is being replaced: the old job must not come back and
// regenerate stale HLS output after the new media has taken over.
func CancelConversion(outDir, id string) {
	if id == "" {
		return
	}
	value, ok := queues.Load(outDir)
	if !ok {
		return
	}
	state, ok := value.(*queueState)
	if !ok {
		return
	}
	state.mu.Lock()
	for _, job := range state.items {
		if job == nil || job.MediaID != id {
			continue
		}
		switch job.Status {
		case "pending", "running":
			job.Preempted = false
			job.Status = "canceled"
			job.Message = "差し替えのため中止しました"
			job.FinishedAt = time.Now()
			if job.Cancel != nil {
				job.Cancel()
			}
		}
	}
	state.mu.Unlock()
}

func PlaylistName(id string) string {
	return playlistName(id)
}

func SegmentPattern(id string) string {
	return segmentPattern(id)
}

func FinalizeHLSPlaylist(outDir, id string) error {
	return finalizeHLSPlaylist(outDir, id)
}

func BeginExternalHLS(outDir, id string, preset QualityPreset, cancel context.CancelFunc, done chan struct{}) {
	activeHLS.Store(outDir, &activeJob{
		Preset:  preset,
		Cancel:  cancel,
		Done:    done,
		MediaID: id,
	})
}

func EndExternalHLS(outDir string, done chan struct{}) {
	if current, ok := activeHLS.Load(outDir); ok {
		if job, ok := current.(*activeJob); ok && job != nil && job.Done == done {
			activeHLS.Delete(outDir)
		}
	}
}

func isActiveForID(outDir, id string) bool {
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return false
	}
	if value, ok := active.(bool); ok {
		return value && id == ""
	}
	job, ok := active.(*activeJob)
	if !ok || job == nil {
		return false
	}
	return id == "" || (job.QueueJob != nil && job.QueueJob.MediaID == id) || (job.MediaID != "" && job.MediaID == id)
}

func isPendingForID(outDir, id string) bool {
	if id == "" {
		return false
	}
	value, ok := queues.Load(outDir)
	if !ok {
		return false
	}
	state, ok := value.(*queueState)
	if !ok {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, job := range state.items {
		if job == nil || job.MediaID != id {
			continue
		}
		if job.Status == "pending" || job.Status == "running" {
			return true
		}
	}
	return false
}

func ActiveQuality(outDir string) (QualityPreset, bool) {
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return QualityPreset{}, false
	}
	if preset, ok := active.(QualityPreset); ok {
		return preset, true
	}
	if job, ok := active.(*activeJob); ok && job != nil {
		return job.Preset, true
	}
	return QualityPreset{}, false
}

func stopActive(outDir string) {
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return
	}
	if job, ok := active.(*activeJob); ok && job != nil && job.Cancel != nil {
		job.Cancel()
		if job.Done != nil {
			select {
			case <-job.Done:
			case <-time.After(2 * time.Second):
			}
		}
	}
	activeHLS.Delete(outDir)
}

func removeGenerated(outDir string) {
	_ = os.Remove(filepath.Join(outDir, MP4File))
	_ = os.Remove(filepath.Join(outDir, HLSPlaylist))
	matches, _ := filepath.Glob(filepath.Join(outDir, "current-*.m3u8"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	matches, _ = filepath.Glob(filepath.Join(outDir, "current*.ts"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	matches, _ = filepath.Glob(filepath.Join(outDir, "current*.tmp"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func removeYTDLPFiles(outDir string) {
	matches, _ := filepath.Glob(filepath.Join(outDir, "yt-dlp-source.*"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
	matches, _ = filepath.Glob(filepath.Join(outDir, "yt-dlp-source.*.part"))
	for _, match := range matches {
		_ = os.Remove(match)
	}
}

func finalizeHLSPlaylist(outDir, id string) error {
	path := filepath.Join(outDir, playlistName(id))
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(data)
	text = strings.Replace(text, "#EXT-X-PLAYLIST-TYPE:EVENT", "#EXT-X-PLAYLIST-TYPE:VOD", 1)
	if !strings.Contains(text, "#EXT-X-ENDLIST") {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "#EXT-X-ENDLIST\n"
	}
	return os.WriteFile(path, []byte(text), 0600)
}

func hlsSegmentExists(outDir string) bool {
	matches, _ := filepath.Glob(filepath.Join(outDir, "current*.ts"))
	return len(matches) > 0
}

func hlsSegmentExistsForID(outDir, id string) bool {
	if id == "" {
		return hlsSegmentExists(outDir)
	}
	return hlsSegmentCountForID(outDir, id) > 0
}

func hlsSegmentCount(outDir string) int {
	matches, _ := filepath.Glob(filepath.Join(outDir, "current*.ts"))
	return len(matches)
}

func hlsSegmentCountForID(outDir, id string) int {
	if id == "" {
		return hlsSegmentCount(outDir)
	}
	matches, _ := filepath.Glob(filepath.Join(outDir, "current-"+safeID(id)+"-*.ts"))
	return len(matches)
}

func hlsPlaylistExists(outDir string) bool {
	matches, _ := filepath.Glob(filepath.Join(outDir, "current*.m3u8"))
	return len(matches) > 0
}

func hlsPlaylistExistsForID(outDir, id string) bool {
	if id == "" {
		return hlsPlaylistExists(outDir)
	}
	return fileExists(filepath.Join(outDir, playlistName(id)))
}

func playlistName(id string) string {
	if id == "" {
		return HLSPlaylist
	}
	return "current-" + safeID(id) + ".m3u8"
}

func segmentPattern(id string) string {
	prefix := "current"
	if id != "" {
		prefix = "current-" + safeID(id)
	}
	return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 10) + "-%d.ts"
}

func clipDurationSeconds() int {
	seconds, err := strconv.Atoi(ClipDuration)
	if err != nil || seconds <= 0 {
		return 0
	}
	return seconds
}

func safeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "current"
	}
	return b.String()
}
