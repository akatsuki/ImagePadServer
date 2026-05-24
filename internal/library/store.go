package library

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type CurrentImage struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	FileName     string    `json:"fileName"`
	PublicName   string    `json:"publicName"`
	ContentType  string    `json:"contentType"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	SizeBytes    int64     `json:"sizeBytes"`
	OriginalName string    `json:"originalName"`
	Thumbnail    string    `json:"thumbnail,omitempty"`
	Converted    bool      `json:"converted,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type HistoryItem struct {
	CurrentImage
	HistoryFileName string `json:"historyFileName"`
	Favorite        bool   `json:"favorite"`
	Persistent      bool   `json:"persistent"`
}

type Store struct {
	dir          string
	favoriteDir  string
	convertedDir string
	mu           sync.RWMutex
	current      *CurrentImage
	history      []HistoryItem
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
		return nil, err
	}
	store := &Store{
		dir:          dir,
		favoriteDir:  filepath.Join(filepath.Dir(dir), "favorites"),
		convertedDir: filepath.Join(filepath.Dir(dir), "converted"),
	}
	_ = store.loadFavorites()
	return store, nil
}

// Reset clears in-memory state and reinitializes the media workspace directory.
func (s *Store) Reset() error {
	s.mu.Lock()
	s.current = nil
	s.mu.Unlock()
	return ResetDir(s.dir)
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
	s.current = &info
	_ = s.addHistoryLocked(info, dstPath)
	s.mu.Unlock()
	return s.save()
}

func (s *Store) SetCurrentInfo(info CurrentImage) error {
	info.ID = randomID()
	info.UpdatedAt = time.Now()
	if info.Kind == "" {
		info.Kind = "image"
	}
	if info.PublicName == "" {
		info.PublicName = info.FileName
	}

	s.mu.Lock()
	s.current = &info
	_ = s.addHistoryLocked(info, filepath.Join(s.dir, info.FileName))
	s.mu.Unlock()
	return s.save()
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
				_ = copyDir(filepath.Join(s.favoriteDir, "converted", s.history[i].ID), filepath.Join(s.convertedDir, s.history[i].ID))
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

func (s *Store) MarkConverted(id string, files []string) error {
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

	dstDir := filepath.Join(s.convertedDir, id)
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return err
	}
	for _, src := range files {
		if src == "" {
			continue
		}
		if err := copyFile(filepath.Join(dstDir, filepath.Base(src)), src); err != nil {
			return err
		}
	}
	s.history[index].Converted = true
	if s.history[index].Favorite {
		_ = copyDir(filepath.Join(s.favoriteDir, "converted", id), dstDir)
		if err := s.saveFavoritesLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Clear() error {
	s.mu.Lock()
	s.current = nil
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
	return ioutil.WriteFile(filepath.Join(s.dir, "state.json"), data, 0600)
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

func (s *Store) load() error {
	data, err := ioutil.ReadFile(filepath.Join(s.dir, "state.json"))
	if err != nil {
		return err
	}
	var current CurrentImage
	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(s.dir, current.FileName)); err != nil {
		return err
	}
	s.current = &current
	return nil
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
		if os.IsNotExist(err) {
			return nil
		}
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
