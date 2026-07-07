package playlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store persists named playlists as a single JSON file (same storage layer as
// favorites: one file inside the library directory). Rendered track media is
// copied into a per-playlist folder on save so saved playlists survive queue
// edits and library cleanups.
type Store struct {
	mu       sync.Mutex
	path     string
	mediaDir string
}

type storedPlaylist struct {
	Name   string  `json:"name"`
	Tracks []Track `json:"tracks"`
}

type storeFile struct {
	Playlists []storedPlaylist `json:"playlists"`
}

func NewStore(path string) *Store {
	return &Store{path: path, mediaDir: filepath.Join(filepath.Dir(path), "playlist-media")}
}

// playlistMediaDirName derives a filesystem-safe folder name for a playlist.
func playlistMediaDirName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r > 127:
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "playlist"
	}
	return b.String()
}

func (s *Store) load() (storeFile, error) {
	var f storeFile
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return storeFile{}, fmt.Errorf("parse %s: %w", filepath.Base(s.path), err)
	}
	return f, nil
}

func (s *Store) write(f storeFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Save stores tracks under name, replacing an existing playlist of the same
// name. Track order is preserved, and each ready track's rendered media file
// is copied into the playlist's own media folder so the saved playlist keeps
// playing even after the track is removed from the queue.
func (s *Store) Save(name string, tracks []Track) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("プレイリスト名を入力してください")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	saved := append([]Track(nil), tracks...)
	dir := filepath.Join(s.mediaDir, playlistMediaDirName(name))
	// Rebuild the media folder from scratch so overwriting a playlist does
	// not leak files from the previous version.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for i := range saved {
		if saved[i].Status != TrackReady || saved[i].MediaPath == "" {
			continue
		}
		dst := filepath.Join(dir, filepath.Base(saved[i].MediaPath))
		if err := copyMediaFile(dst, saved[i].MediaPath); err != nil {
			// 元ファイルが消えていても保存全体は止めない。
			saved[i].Status = TrackFailed
			saved[i].Error = "メディアファイルを保存できませんでした（再追加が必要）"
			saved[i].MediaPath = ""
			continue
		}
		saved[i].MediaPath = dst
	}
	stored := storedPlaylist{Name: name, Tracks: saved}
	replaced := false
	for i := range f.Playlists {
		if f.Playlists[i].Name == name {
			f.Playlists[i] = stored
			replaced = true
			break
		}
	}
	if !replaced {
		f.Playlists = append(f.Playlists, stored)
	}
	return s.write(f)
}

// Load returns the tracks of the named playlist. Tracks whose rendered media
// file no longer exists are downgraded to TrackFailed so the UI can surface
// that they need to be re-added.
func (s *Store) Load(name string) ([]Track, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	for _, p := range f.Playlists {
		if p.Name != name {
			continue
		}
		tracks := append([]Track(nil), p.Tracks...)
		for i := range tracks {
			if tracks[i].Status != TrackReady {
				continue
			}
			if _, statErr := os.Stat(tracks[i].MediaPath); statErr != nil {
				tracks[i].Status = TrackFailed
				tracks[i].Error = "メディアファイルが見つかりません（再追加が必要）"
			}
		}
		return tracks, nil
	}
	return nil, fmt.Errorf("プレイリスト %q が見つかりません", name)
}

// List returns saved playlist names in stored order.
func (s *Store) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(f.Playlists))
	for _, p := range f.Playlists {
		names = append(names, p.Name)
	}
	return names, nil
}

func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	for i := range f.Playlists {
		if f.Playlists[i].Name == name {
			f.Playlists = append(f.Playlists[:i], f.Playlists[i+1:]...)
			if err := s.write(f); err != nil {
				return err
			}
			_ = os.RemoveAll(filepath.Join(s.mediaDir, playlistMediaDirName(name)))
			return nil
		}
	}
	return fmt.Errorf("プレイリスト %q が見つかりません", name)
}

func copyMediaFile(dst, src string) error {
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
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
