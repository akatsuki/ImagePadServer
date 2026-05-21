package library

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type CurrentImage struct {
	ID           string    `json:"id"`
	FileName     string    `json:"fileName"`
	PublicName   string    `json:"publicName"`
	ContentType  string    `json:"contentType"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	SizeBytes    int64     `json:"sizeBytes"`
	OriginalName string    `json:"originalName"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Store struct {
	dir     string
	mu      sync.RWMutex
	current *CurrentImage
}

func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	_ = s.load()
	return s, nil
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

func (s *Store) SetCurrent(srcPath string, info CurrentImage) error {
	info.ID = randomID()
	info.UpdatedAt = time.Now()
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
	s.mu.Unlock()
	return s.save()
}

func (s *Store) save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := json.MarshalIndent(s.current, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filepath.Join(s.dir, "state.json"), data, 0644)
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
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().Format("20060102150405")
	}
	return hex.EncodeToString(b[:])
}
