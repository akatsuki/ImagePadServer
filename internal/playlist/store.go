package playlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store persists named playlists as a single JSON file (same storage layer as
// favorites: one file inside the library directory).
type Store struct {
	mu   sync.Mutex
	path string
}

type storedPlaylist struct {
	Name   string  `json:"name"`
	Tracks []Track `json:"tracks"`
}

type storeFile struct {
	Playlists []storedPlaylist `json:"playlists"`
}

func NewStore(path string) *Store {
	return &Store{path: path}
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
// name. Track order is preserved.
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
	stored := storedPlaylist{Name: name, Tracks: append([]Track(nil), tracks...)}
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
			return s.write(f)
		}
	}
	return fmt.Errorf("プレイリスト %q が見つかりません", name)
}
