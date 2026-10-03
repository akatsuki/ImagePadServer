package library

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PreparedNicoMedia describes a fully rendered Nico export that is still
// outside the public Store namespace. The worker writes these paths; the
// Store is the only owner allowed to admit them.
type PreparedNicoMedia struct {
	Info             CurrentImage
	SourcePath       string
	ThumbnailPath    string
	SnapshotPath     string
	HLSDir           string
	RunID            string
	SelectCurrent    bool
	ExpectedRevision int64
	// Empty preserves the historical Nico identity. Other generated videos use
	// the same atomic admission path without a second conversion.
	Resolution     string
	SnapshotPrefix string
}

type PreparedVideo = PreparedNicoMedia

func (s *Store) CommitPreparedVideo(prepared PreparedVideo) (CurrentImage, error) {
	return s.CommitPreparedNicoMedia(prepared)
}

// CommitPreparedNicoMedia validates and atomically admits a new Nico media
// identity. Existing current/history artifacts are never replaced because all
// destinations are derived from the new ID; state.json is written before the
// in-memory current pointer changes.
func (s *Store) CommitPreparedNicoMedia(prepared PreparedNicoMedia) (CurrentImage, error) {
	if s == nil || !validHistoryItemID(prepared.Info.ID) || !validHistoryFileName(prepared.Info.FileName) {
		return CurrentImage{}, os.ErrInvalid
	}
	if prepared.Info.Kind == "" {
		prepared.Info.Kind = "video"
	}
	if prepared.Info.PublicName == "" {
		prepared.Info.PublicName = prepared.Info.FileName
	}
	if prepared.RunID == "" || !validHistoryItemID(prepared.RunID) {
		return CurrentImage{}, os.ErrInvalid
	}

	// Check the optimistic publication boundary before doing any staging work.
	// A caller that lost the race gets a deterministic error and cannot create
	// a new artifact tree merely by submitting a stale request.
	if revision := s.PublishedRevision(); revision != prepared.ExpectedRevision {
		return CurrentImage{}, fmt.Errorf("niconico: publication revision conflict: expected %d, got %d", prepared.ExpectedRevision, revision)
	}

	sourceInfo, err := regularPreparedFile(prepared.SourcePath, "source MP4")
	if err != nil {
		return CurrentImage{}, err
	}
	if prepared.SnapshotPath == "" {
		return CurrentImage{}, errors.New("niconico: snapshot is required")
	}
	if _, err := regularPreparedFile(prepared.SnapshotPath, "snapshot"); err != nil {
		return CurrentImage{}, err
	}
	if prepared.ThumbnailPath != "" {
		if _, err := regularPreparedFile(prepared.ThumbnailPath, "thumbnail"); err != nil {
			return CurrentImage{}, err
		}
	}
	hls, err := inspectPreparedHLS(prepared.HLSDir, prepared.Info.ID, prepared.RunID)
	if err != nil {
		return CurrentImage{}, err
	}

	info := prepared.Info
	info.UpdatedAt = now()
	info.Published = true
	info.Converted = true
	resolution := prepared.Resolution
	if resolution == "" {
		resolution = "niconico"
	}
	if !validHistoryItemID(resolution) {
		return CurrentImage{}, os.ErrInvalid
	}
	info.Resolutions = []string{resolution}
	info.SizeBytes = sourceInfo.Size()
	if prepared.ThumbnailPath != "" {
		info.Thumbnail = "thumb-" + info.ID + ".jpg"
	}
	historyName := historyFileName(info)
	snapshotPrefix := prepared.SnapshotPrefix
	if snapshotPrefix == "" {
		snapshotPrefix = "niconico-snapshot"
	}
	if !validHistoryItemID(snapshotPrefix) {
		return CurrentImage{}, os.ErrInvalid
	}
	snapshotName := snapshotPrefix + "-" + info.ID + ".json"
	convertedDir := filepath.Join(s.convertedDir, info.ID)
	if _, err := os.Stat(convertedDir); err == nil {
		return CurrentImage{}, fmt.Errorf("niconico: converted identity already exists: %s", info.ID)
	} else if !os.IsNotExist(err) {
		return CurrentImage{}, err
	}

	pendingDir, err := os.MkdirTemp(s.dir, ".niconico-prepared-")
	if err != nil {
		return CurrentImage{}, err
	}
	installed := make([]string, 0, 8)
	defer func() {
		_ = os.RemoveAll(pendingDir)
		if len(installed) > 0 {
			for _, path := range installed {
				_ = os.RemoveAll(path)
			}
		}
	}()

	pendingSource := filepath.Join(pendingDir, info.FileName)
	pendingHistory := filepath.Join(pendingDir, historyName)
	pendingSnapshot := filepath.Join(pendingDir, snapshotName)
	if err := copyFile(pendingSource, prepared.SourcePath); err != nil {
		return CurrentImage{}, err
	}
	if err := copyFile(pendingHistory, prepared.SourcePath); err != nil {
		return CurrentImage{}, err
	}
	if err := copyFile(pendingSnapshot, prepared.SnapshotPath); err != nil {
		return CurrentImage{}, err
	}
	var pendingThumbnail string
	if prepared.ThumbnailPath != "" {
		pendingThumbnail = filepath.Join(pendingDir, info.Thumbnail)
		if err := copyFile(pendingThumbnail, prepared.ThumbnailPath); err != nil {
			return CurrentImage{}, err
		}
	}
	pendingHLS := filepath.Join(pendingDir, "hls")
	if err := os.MkdirAll(pendingHLS, 0700); err != nil {
		return CurrentImage{}, err
	}
	if err := stagePreparedHLS(pendingHLS, hls); err != nil {
		return CurrentImage{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publishedRevision != prepared.ExpectedRevision {
		return CurrentImage{}, fmt.Errorf("niconico: publication revision conflict: expected %d, got %d", prepared.ExpectedRevision, s.publishedRevision)
	}
	rootTargets := []struct{ stage, target string }{
		{pendingSource, filepath.Join(s.dir, info.FileName)},
		{pendingHistory, filepath.Join(s.dir, historyName)},
		{pendingSnapshot, filepath.Join(s.dir, snapshotName)},
	}
	if pendingThumbnail != "" {
		rootTargets = append(rootTargets, struct{ stage, target string }{pendingThumbnail, filepath.Join(s.dir, info.Thumbnail)})
	}
	if prepared.SelectCurrent {
		rootTargets = append(rootTargets, hlsTargets(pendingHLS, s.dir, info.ID)...)
	}
	for _, target := range rootTargets {
		if _, err := os.Stat(target.target); err == nil {
			return CurrentImage{}, fmt.Errorf("niconico: destination already exists: %s", target.target)
		} else if !os.IsNotExist(err) {
			return CurrentImage{}, err
		}
	}
	if err := os.MkdirAll(convertedDir, 0700); err != nil {
		return CurrentImage{}, err
	}
	if err := copyPreparedTree(convertedDir, pendingHLS); err != nil {
		_ = os.RemoveAll(convertedDir)
		return CurrentImage{}, err
	}
	installed = append(installed, convertedDir)
	for _, target := range rootTargets {
		if err := copyFile(target.target, target.stage); err != nil {
			return CurrentImage{}, err
		}
		installed = append(installed, target.target)
	}
	if prepared.SelectCurrent {
		data, err := json.MarshalIndent(&info, "", "  ")
		if err != nil {
			return CurrentImage{}, err
		}
		data = append(data, '\n')
		if err := writeAtomicFile(filepath.Join(s.dir, "state.json"), data, 0600); err != nil {
			return CurrentImage{}, err
		}
	}

	item := HistoryItem{CurrentImage: info, HistoryFileName: historyName}
	s.history = append([]HistoryItem{item}, s.history...)
	s.pruneHistoryLocked(40)
	if prepared.SelectCurrent {
		current := info
		s.current = &current
	}
	s.publishedRevision++
	installed = installed[:0]
	return info, nil
}

type preparedHLSTree struct {
	Playlist string
	Segments []string
	Names    []string
	MediaID  string
	RunID    string
}

func inspectPreparedHLS(dir, mediaID, runID string) (preparedHLSTree, error) {
	if dir == "" || !validHistoryItemID(mediaID) || !validHistoryItemID(runID) {
		return preparedHLSTree{}, os.ErrInvalid
	}
	playlistPath := filepath.Join(dir, "playlist.m3u8")
	f, err := os.Open(playlistPath)
	if err != nil {
		return preparedHLSTree{}, fmt.Errorf("niconico: prepared HLS playlist: %w", err)
	}
	defer f.Close()
	tree := preparedHLSTree{Playlist: playlistPath, MediaID: mediaID, RunID: runID}
	seen := make(map[string]struct{})
	endList := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "#EXT-X-ENDLIST" {
			endList = true
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == "." || line == ".." || filepath.Base(line) != line || strings.ContainsAny(line, `/\\:`) {
			return preparedHLSTree{}, fmt.Errorf("niconico: playlist segment escapes staging: %q", line)
		}
		if _, ok := seen[line]; ok {
			return preparedHLSTree{}, fmt.Errorf("niconico: duplicate HLS segment: %q", line)
		}
		seen[line] = struct{}{}
		path := filepath.Join(dir, line)
		if _, err := regularPreparedFile(path, "HLS segment"); err != nil {
			return preparedHLSTree{}, err
		}
		index := len(tree.Segments)
		tree.Segments = append(tree.Segments, path)
		tree.Names = append(tree.Names, fmt.Sprintf("current-%s-%s-%05d.ts", mediaID, runID, index))
	}
	if err := scanner.Err(); err != nil {
		return preparedHLSTree{}, err
	}
	if !endList || len(tree.Segments) == 0 {
		return preparedHLSTree{}, errors.New("niconico: prepared HLS playlist is incomplete")
	}
	return tree, nil
}

func stagePreparedHLS(dst string, tree preparedHLSTree) error {
	data, err := os.ReadFile(tree.Playlist)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	segmentIndex := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if segmentIndex >= len(tree.Names) || trimmed != filepath.Base(tree.Segments[segmentIndex]) {
			return fmt.Errorf("niconico: HLS playlist changed during staging")
		}
		lines[index] = tree.Names[segmentIndex]
		segmentIndex++
	}
	if segmentIndex != len(tree.Names) {
		return errors.New("niconico: HLS playlist segment count changed during staging")
	}
	for index, source := range tree.Segments {
		if err := copyFile(filepath.Join(dst, tree.Names[index]), source); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dst, "current-"+tree.MediaID+".m3u8"), []byte(strings.Join(lines, "\n")), 0600)
}

func hlsTargets(pending, root, id string) []struct{ stage, target string } {
	entries, _ := os.ReadDir(pending)
	targets := make([]struct{ stage, target string }, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		targets = append(targets, struct{ stage, target string }{filepath.Join(pending, name), filepath.Join(root, name)})
	}
	return targets
}

func copyPreparedTree(dst, src string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return os.ErrInvalid
		}
		if err := copyFile(filepath.Join(dst, entry.Name()), filepath.Join(src, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func regularPreparedFile(path, label string) (os.FileInfo, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("niconico: %s path is required", label)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("niconico: %s: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("niconico: %s is not a non-empty regular file", label)
	}
	return info, nil
}

var now = func() time.Time { return time.Now() }
