package library

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type CurrentImage struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	SourceKind   string    `json:"sourceKind,omitempty"`
	FileName     string    `json:"fileName"`
	PublicName   string    `json:"publicName"`
	ContentType  string    `json:"contentType"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	SizeBytes    int64     `json:"sizeBytes"`
	Duration     float64   `json:"durationSeconds,omitempty"`
	OriginalName string    `json:"originalName"`
	Thumbnail    string    `json:"thumbnail,omitempty"`
	Converted    bool      `json:"converted,omitempty"`
	Resolutions  []string  `json:"resolutions,omitempty"`
	Published    bool      `json:"published,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
	Title        string    `json:"title,omitempty"`
	Artist       string    `json:"artist,omitempty"`
	Album        string    `json:"album,omitempty"`
}

type HistoryItem struct {
	CurrentImage
	HistoryFileName string `json:"historyFileName"`
	Favorite        bool   `json:"favorite"`
	Persistent      bool   `json:"persistent"`
}

type Store struct {
	dir               string
	favoriteDir       string
	convertedDir      string
	mu                sync.RWMutex
	current           *CurrentImage
	history           []HistoryItem
	publishedRevision int64
}

// ResetDir removes and recreates the media workspace directory.
// ImagePadServer intentionally starts with an empty workspace on each launch.
func ResetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0700)
}

func NewStore(dir string) (*Store, error) {
	if err := ResetDir(dir); err != nil {
		if mkdirErr := os.MkdirAll(dir, 0700); mkdirErr != nil {
			return nil, err
		}
	}
	store := &Store{
		dir:          dir,
		favoriteDir:  filepath.Join(filepath.Dir(dir), "favorites"),
		convertedDir: filepath.Join(filepath.Dir(dir), "converted"),
	}
	// Non-favorite converted output belongs to the ephemeral workspace.
	if err := ResetDir(store.convertedDir); err != nil {
		return nil, err
	}
	_ = store.loadFavorites()
	return store, nil
}

// Reset clears in-memory state and reinitializes the media workspace directory.
func (s *Store) Reset() error {
	s.mu.Lock()
	s.current = nil
	s.history = nil
	s.publishedRevision++
	s.mu.Unlock()
	if err := ResetDir(s.dir); err != nil {
		return err
	}
	return ResetDir(s.convertedDir)
}

func (s *Store) Dir() string {
	return s.dir
}

func (s *Store) Current() *CurrentImage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return nil
	}
	copy := *s.current
	return &copy
}

func (s *Store) CurrentPath() (string, *CurrentImage, bool) {
	img := s.Current()
	if img == nil {
		return "", nil, false
	}
	return filepath.Join(s.dir, img.FileName), img, true
}

func (s *Store) History() []HistoryItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]HistoryItem, len(s.history))
	copy(items, s.history)
	return items
}

func (s *Store) HistoryPath(id string) (string, HistoryItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.history {
		if item.ID == id {
			return s.historyPath(item), item, true
		}
	}
	return "", HistoryItem{}, false
}

func (s *Store) ConvertedPath(id string) (string, HistoryItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.history {
		if item.ID != id || !item.Converted {
			continue
		}
		base := s.convertedDir
		if item.Persistent {
			base = filepath.Join(s.favoriteDir, "converted")
		}
		path := filepath.Join(base, item.ID)
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			return path, item, true
		}
	}
	return "", HistoryItem{}, false
}

func (s *Store) HistoryThumbnailPath(id string) (string, HistoryItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.history {
		if item.ID != id || item.Thumbnail == "" {
			continue
		}
		path := filepath.Join(s.dir, item.Thumbnail)
		if item.Persistent {
			path = filepath.Join(s.favoriteDir, item.Thumbnail)
		}
		return path, item, true
	}
	return "", HistoryItem{}, false
}

func (s *Store) SetCurrent(srcPath string, info CurrentImage) error {
	info.ID = randomID()
	info.UpdatedAt = time.Now()
	info.Published = true
	if info.Kind == "" {
		info.Kind = "image"
	}
	info.FileName = "current" + filepath.Ext(info.PublicName)
	if info.PublicName == "" {
		info.PublicName = info.FileName
	}

	dstPath := filepath.Join(s.dir, info.FileName)
	if err := copyFile(dstPath, srcPath); err != nil {
		return err
	}

	if stat, err := os.Stat(dstPath); err == nil {
		info.SizeBytes = stat.Size()
	}

	s.mu.Lock()
	if err := s.addHistoryLocked(info, dstPath); err != nil {
		s.mu.Unlock()
		return err
	}
	s.current = &info
	s.publishedRevision++
	s.mu.Unlock()
	return s.save()
}

func (s *Store) SetCurrentInfo(info CurrentImage) error {
	info.ID = randomID()
	return s.setCurrentInfo(info)
}

func (s *Store) SetCurrentInfoWithID(info CurrentImage) error {
	if err := s.SetCurrentInfoWithIDInMemory(info); err != nil {
		return err
	}
	return s.save()
}

// SetCurrentInfoWithIDInMemory updates current/history without performing disk
// I/O. Callers that need a short external ownership commit can save later.
func (s *Store) SetCurrentInfoWithIDInMemory(info CurrentImage) error {
	if info.ID == "" {
		info.ID = randomID()
	}
	return s.setCurrentInfoInMemory(info)
}

// Save persists the current store snapshot.
func (s *Store) Save() error {
	return s.save()
}

func (s *Store) setCurrentInfo(info CurrentImage) error {
	if err := s.setCurrentInfoInMemory(info); err != nil {
		return err
	}
	return s.save()
}

func (s *Store) setCurrentInfoInMemory(info CurrentImage) error {
	info.UpdatedAt = time.Now()
	info.Published = true
	if info.Kind == "" {
		info.Kind = "image"
	}
	if info.PublicName == "" {
		info.PublicName = info.FileName
	}

	s.mu.Lock()
	srcPath := filepath.Join(s.dir, info.FileName)
	if _, err := os.Stat(srcPath); err == nil {
		if err := s.addHistoryLocked(info, srcPath); err != nil {
			s.mu.Unlock()
			return err
		}
	} else if !os.IsNotExist(err) {
		s.mu.Unlock()
		return err
	}
	s.current = &info
	s.publishedRevision++
	s.mu.Unlock()
	return nil
}

func (s *Store) AddHistory(srcPath string, info CurrentImage) (*CurrentImage, error) {
	info.ID = randomID()
	info.UpdatedAt = time.Now()
	if info.Kind == "" {
		info.Kind = "image"
	}
	if info.PublicName == "" {
		info.PublicName = filepath.Base(srcPath)
	}
	if info.FileName == "" {
		info.FileName = filepath.Base(srcPath)
	}
	if stat, err := os.Stat(srcPath); err == nil {
		info.SizeBytes = stat.Size()
	}

	s.mu.Lock()
	if err := s.addHistoryLocked(info, srcPath); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.mu.Unlock()
	copy := info
	return &copy, nil
}

func (s *Store) SetCurrentFromHistory(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var item *HistoryItem
	for i := range s.history {
		if s.history[i].ID == id {
			item = &s.history[i]
			break
		}
	}
	if item == nil {
		return os.ErrNotExist
	}

	srcPath := s.historyPath(*item)
	info := item.CurrentImage
	info.UpdatedAt = time.Now()
	// Selecting a history item changes the preview target, not visibility.
	info.Published = item.Published
	if info.Kind == "" {
		info.Kind = "image"
	}
	ext := filepath.Ext(info.PublicName)
	if ext == "" {
		ext = filepath.Ext(item.HistoryFileName)
	}
	if info.Kind == "video" {
		info.FileName = "current-history-video" + ext
	} else {
		info.FileName = "current" + ext
	}
	if info.PublicName == "" {
		info.PublicName = info.FileName
	}
	dstPath := filepath.Join(s.dir, info.FileName)
	if err := copyFile(dstPath, srcPath); err != nil {
		return err
	}
	if stat, err := os.Stat(dstPath); err == nil {
		info.SizeBytes = stat.Size()
	}
	if item.Converted {
		srcConverted := filepath.Join(s.convertedDir, item.ID)
		if item.Persistent {
			srcConverted = filepath.Join(s.favoriteDir, "converted", item.ID)
		}
		if err := copyDir(s.dir, srcConverted); err != nil {
			return err
		}
	}
	s.current = &info
	return s.saveCurrentLocked()
}

func (s *Store) SetFavorite(id string, favorite bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.history {
		if s.history[i].ID != id {
			continue
		}
		if favorite {
			if err := os.MkdirAll(s.favoriteDir, 0700); err != nil {
				return err
			}
			dstName := s.history[i].HistoryFileName
			if dstName == "" {
				dstName = historyFileName(s.history[i].CurrentImage)
			}
			if err := copyFile(filepath.Join(s.favoriteDir, dstName), s.historyPath(s.history[i])); err != nil {
				return err
			}
			if s.history[i].Thumbnail != "" {
				srcThumb := filepath.Join(s.dir, s.history[i].Thumbnail)
				if s.history[i].Persistent {
					srcThumb = filepath.Join(s.favoriteDir, s.history[i].Thumbnail)
				}
				if _, err := os.Stat(srcThumb); err == nil {
					if err := copyFile(filepath.Join(s.favoriteDir, s.history[i].Thumbnail), srcThumb); err != nil {
						return err
					}
				}
			}
			if s.history[i].Converted {
				if err := copyDir(filepath.Join(s.favoriteDir, "converted", s.history[i].ID), filepath.Join(s.convertedDir, s.history[i].ID)); err != nil {
					return err
				}
			}
			s.history[i].HistoryFileName = dstName
			s.history[i].Favorite = true
			s.history[i].Persistent = true
		} else {
			_ = os.Remove(filepath.Join(s.favoriteDir, s.history[i].HistoryFileName))
			if s.history[i].Thumbnail != "" {
				_ = os.Remove(filepath.Join(s.favoriteDir, s.history[i].Thumbnail))
			}
			_ = os.RemoveAll(filepath.Join(s.favoriteDir, "converted", s.history[i].ID))
			s.history[i].Favorite = false
			s.history[i].Persistent = false
			if _, err := os.Stat(filepath.Join(s.dir, s.history[i].HistoryFileName)); err != nil {
				s.history = append(s.history[:i], s.history[i+1:]...)
			}
		}
		return s.saveFavoritesLocked()
	}
	return os.ErrNotExist
}

func (s *Store) SetPublished(id string, published bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.history {
		if s.history[i].ID != id {
			continue
		}
		s.history[i].Published = published
		currentMatches := s.current != nil && s.current.ID == id
		if currentMatches {
			s.current.Published = published
		}
		s.publishedRevision++
		if s.history[i].Favorite {
			if err := s.saveFavoritesLocked(); err != nil {
				return err
			}
		}
		if currentMatches {
			return s.saveCurrentLocked()
		}
		return nil
	}
	return os.ErrNotExist
}

// PublishedRevision は公開状態（published 集合）の単調増加リビジョンを返す。
// マルチクライアントの同時上書き検知（409）に使う。
func (s *Store) PublishedRevision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publishedRevision
}

func (s *Store) MarkConverted(id string, files []string, resolutions ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index := -1
	for i := range s.history {
		if s.history[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return os.ErrNotExist
	}

	if len(files) == 0 {
		return os.ErrNotExist
	}
	for _, src := range files {
		if src == "" {
			return os.ErrNotExist
		}
		stat, err := os.Stat(src)
		if err != nil {
			return err
		}
		if stat.IsDir() {
			return os.ErrInvalid
		}
	}
	// Build the conversion in a staging directory. The metadata flag is a
	// commit marker, so readers must never observe a half-populated HLS tree.
	dstDir := filepath.Join(s.convertedDir, id)
	tmpDir := filepath.Join(s.convertedDir, "."+id+".tmp")
	if err := os.RemoveAll(tmpDir); err != nil {
		return err
	}
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	for _, src := range files {
		if err := copyFile(filepath.Join(tmpDir, filepath.Base(src)), src); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dstDir); err != nil {
		return err
	}
	if err := os.Rename(tmpDir, dstDir); err != nil {
		return err
	}
	s.history[index].Converted = true
	if len(resolutions) > 0 {
		s.history[index].Resolutions = append([]string(nil), resolutions...)
	}
	if s.current != nil && s.current.ID == id {
		s.current.Converted = true
		s.current.Resolutions = append([]string(nil), s.history[index].Resolutions...)
	}
	if s.history[index].Favorite {
		if err := copyDir(filepath.Join(s.favoriteDir, "converted", id), dstDir); err != nil {
			return err
		}
		if err := s.saveFavoritesLocked(); err != nil {
			return err
		}
	}
	if s.current != nil && s.current.ID == id {
		return s.saveCurrentLocked()
	}
	return nil
}

// UpdateCurrentSize sets the current media's SizeBytes and persists state.json.
func (s *Store) UpdateCurrentSize(size int64) error {
	return s.updateCurrentSizeForID("", size)
}

// UpdateCurrentSizeForID updates SizeBytes only when the requested media is
// still the current item. Conversion jobs can finish after the user selects a
// different history item, so an unconditional update would corrupt the new
// current item's metadata.
func (s *Store) UpdateCurrentSizeForID(id string, size int64) error {
	return s.updateCurrentSizeForID(id, size)
}

func (s *Store) updateCurrentSizeForID(id string, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return os.ErrNotExist
	}
	if id != "" && s.current.ID != id {
		return os.ErrNotExist
	}
	s.current.SizeBytes = size
	return s.saveCurrentLocked()
}

// UpdateHistorySize updates the SizeBytes of the in-memory history item with
// the given id. If the item is a favorite, favorites.json is also persisted so
// the size survives restarts.
func (s *Store) UpdateHistorySize(id string, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.history {
		if s.history[i].ID == id {
			s.history[i].SizeBytes = size
			if s.history[i].Favorite {
				return s.saveFavoritesLocked()
			}
			return nil
		}
	}
	return os.ErrNotExist
}

// UpdateMediaMetadata sets the duration and pixel dimensions for the media
// with the given id, mirroring onto the current item when it matches. Favorites
// are re-persisted so the values survive restarts.
func (s *Store) UpdateMediaMetadata(id string, durationSeconds float64, width, height int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	favorite := false
	for i := range s.history {
		if s.history[i].ID != id {
			continue
		}
		found = true
		if durationSeconds > 0 {
			s.history[i].Duration = durationSeconds
		}
		if width > 0 {
			s.history[i].Width = width
		}
		if height > 0 {
			s.history[i].Height = height
		}
		favorite = s.history[i].Favorite
	}
	if s.current != nil && s.current.ID == id {
		found = true
		if durationSeconds > 0 {
			s.current.Duration = durationSeconds
		}
		if width > 0 {
			s.current.Width = width
		}
		if height > 0 {
			s.current.Height = height
		}
	}
	if !found {
		return os.ErrNotExist
	}
	if favorite {
		return s.saveFavoritesLocked()
	}
	if s.current != nil && s.current.ID == id {
		return s.saveCurrentLocked()
	}
	return nil
}

func (s *Store) Clear() error {
	s.mu.Lock()
	s.current = nil
	s.publishedRevision++
	s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(s.dir, 0700)
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), "history-") {
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, entry.Name()))
	}
	return nil
}

func (s *Store) save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.saveCurrentLocked()
}

func (s *Store) saveCurrentLocked() error {
	data, err := json.MarshalIndent(s.current, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, byte(10))
	return writeAtomicFile(filepath.Join(s.dir, "state.json"), data, 0600)
}

func writeAtomicFile(path string, data []byte, perm os.FileMode) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (s *Store) addHistoryLocked(info CurrentImage, srcPath string) error {
	item := HistoryItem{CurrentImage: info, HistoryFileName: historyFileName(info)}
	dstPath := filepath.Join(s.dir, item.HistoryFileName)
	if filepath.Clean(srcPath) != filepath.Clean(dstPath) {
		if err := copyFile(dstPath, srcPath); err != nil {
			return err
		}
	}
	if item.Thumbnail != "" {
		thumbSrc := filepath.Join(s.dir, item.Thumbnail)
		thumbName := thumbnailFileName(info)
		thumbDst := filepath.Join(s.dir, thumbName)
		if filepath.Clean(thumbSrc) != filepath.Clean(thumbDst) {
			if err := copyFile(thumbDst, thumbSrc); err != nil {
				return err
			}
		}
		item.Thumbnail = thumbName
	}

	for i := range s.history {
		if s.history[i].ID == item.ID {
			s.history[i] = item
			return nil
		}
	}
	s.history = append([]HistoryItem{item}, s.history...)
	s.pruneHistoryLocked(40)
	return nil
}

func (s *Store) pruneHistoryLocked(limit int) {
	if limit <= 0 {
		return
	}
	kept := s.history[:0]
	normalCount := 0
	for _, item := range s.history {
		if item.Favorite {
			kept = append(kept, item)
			continue
		}
		normalCount++
		if normalCount <= limit {
			kept = append(kept, item)
			continue
		}
		_ = os.Remove(filepath.Join(s.dir, item.HistoryFileName))
		if item.Thumbnail != "" {
			_ = os.Remove(filepath.Join(s.dir, item.Thumbnail))
		}
		_ = os.RemoveAll(filepath.Join(s.convertedDir, item.ID))
	}
	s.history = kept
}

func (s *Store) historyPath(item HistoryItem) string {
	if item.Persistent {
		return filepath.Join(s.favoriteDir, item.HistoryFileName)
	}
	return filepath.Join(s.dir, item.HistoryFileName)
}

func (s *Store) loadFavorites() error {
	data, err := os.ReadFile(filepath.Join(s.favoriteDir, "favorites.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var items []HistoryItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	for _, item := range items {
		if item.HistoryFileName == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.favoriteDir, item.HistoryFileName)); err != nil {
			continue
		}
		item.Favorite = true
		item.Persistent = true
		s.history = append(s.history, item)
	}
	return nil
}

func (s *Store) saveFavoritesLocked() error {
	if err := os.MkdirAll(s.favoriteDir, 0700); err != nil {
		return err
	}
	var favorites []HistoryItem
	for _, item := range s.history {
		if item.Favorite {
			item.Persistent = true
			favorites = append(favorites, item)
		}
	}
	data, err := json.MarshalIndent(favorites, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmpPath := filepath.Join(s.favoriteDir, "favorites.json.tmp")
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, filepath.Join(s.favoriteDir, "favorites.json"))
}

func copyFile(dst, src string) error {
	if filepath.Clean(dst) == filepath.Clean(src) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func copyDir(dst, src string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(dst, entry.Name()), filepath.Join(src, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().Format("20060102150405")
	}
	return hex.EncodeToString(b[:])
}

func historyFileName(info CurrentImage) string {
	ext := filepath.Ext(info.PublicName)
	if ext == "" {
		ext = filepath.Ext(info.FileName)
	}
	if ext == "" {
		ext = ".bin"
	}
	return "history-" + info.ID + strings.ToLower(ext)
}

func thumbnailFileName(info CurrentImage) string {
	return "thumb-" + info.ID + ".jpg"
}
