package obsrtmp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// lhlsSink is the private HTTP PUT target that FFmpeg's DASH/LHLS muxer writes
// to. FFmpeg suppresses #EXT-X-PREFETCH for plain file output, so the muxer
// must stream fMP4 segments and playlists over HTTP. The sink binds to loopback
// only, authenticates each request with a random per-session token, restricts
// methods and filenames to the exact set the muxer produces, and never shares
// the public route or the admin authentication model.
type lhlsSink struct {
	dir      string
	token    string
	maxBytes int64

	listener net.Listener
	server   *http.Server

	mu     sync.RWMutex
	closed bool
}

const (
	// lhlsSinkMaxBytes caps any single file the muxer may PUT. fMP4 segments
	// for a few seconds of 1080p stay far below this; it exists to bound
	// memory and disk for a misbehaving or hostile producer.
	lhlsSinkMaxBytes = 64 << 20

	lhlsManifestPrefetchTag = "#EXT-X-PREFETCH"
)

var (
	lhlsManifestRe = regexp.MustCompile(`^(?:master|media_\d+)\.m3u8$`)
	lhlsMPDRe      = regexp.MustCompile(`^[A-Za-z0-9_-]+\.mpd$`)
	lhlsInitRe     = regexp.MustCompile(`^init-stream\d+\.m4s$`)
	lhlsChunkRe    = regexp.MustCompile(`^chunk-stream\d+-\d+\.m4s$`)
)

// newLHLSSink creates the per-session directory under parentDir and a random
// path token. It does not start listening; call start to bind a loopback port.
func newLHLSSink(parentDir, id string, maxBytes int64) (*lhlsSink, error) {
	if maxBytes <= 0 {
		maxBytes = lhlsSinkMaxBytes
	}
	dir := lhlsSessionDir(parentDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create LHLS session directory: %w", err)
	}
	token, err := lhlsToken()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &lhlsSink{dir: dir, token: token, maxBytes: maxBytes}, nil
}

func lhlsSessionDir(parentDir, id string) string {
	return filepath.Join(parentDir, "obs-lhls-"+id)
}

func lhlsToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate LHLS token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// start binds a loopback listener and serves the sink in the background.
func (s *lhlsSink) start() error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind LHLS sink: %w", err)
	}
	s.mu.Lock()
	s.listener = listener
	s.server = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	srv := s.server
	s.mu.Unlock()
	go func() { _ = srv.Serve(listener) }()
	return nil
}

// baseURL is the FFmpeg output base, including the secret token path. The .mpd
// target name is appended by the caller.
func (s *lhlsSink) baseURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listener == nil {
		return ""
	}
	return "http://" + s.listener.Addr().String() + "/" + s.token
}

func (s *lhlsSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRemote(r.RemoteAddr) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token, name, ok := splitSinkPath(r.URL.Path)
	if !ok || token != s.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !isAllowedSinkName(name) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.handleWrite(w, r, name)
	case http.MethodDelete:
		s.handleDelete(w, name)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *lhlsSink) handleWrite(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.RLock()
	closed := s.closed
	s.mu.RUnlock()
	if closed {
		http.Error(w, "gone", http.StatusGone)
		return
	}

	limited := http.MaxBytesReader(w, r.Body, s.maxBytes)
	final := filepath.Join(s.dir, name)

	// Manifests (.m3u8/.mpd) are published atomically so readers never observe
	// a half-written playlist. Segments and init files are written in place so
	// the current prefetch target is readable while FFmpeg streams it.
	if isManifestName(name) {
		tmp, err := os.CreateTemp(s.dir, name+".tmp-*")
		if err != nil {
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		tmpPath := tmp.Name()
		if _, err := io.Copy(tmp, limited); err != nil {
			tmp.Close()
			_ = os.Remove(tmpPath)
			writeCopyError(w, err)
			return
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpPath)
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmpPath, final); err != nil {
			_ = os.Remove(tmpPath)
			http.Error(w, "server error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		return
	}

	file, err := os.OpenFile(final, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(file, limited); err != nil {
		file.Close()
		_ = os.Remove(final)
		writeCopyError(w, err)
		return
	}
	if err := file.Close(); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *lhlsSink) handleDelete(w http.ResponseWriter, name string) {
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeCopyError(w http.ResponseWriter, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "bad request", http.StatusBadRequest)
}

// ready reports whether the LHLS output has reached a state safe to expose:
// a media playlist advertising #EXT-X-PREFETCH, a non-empty init segment, and
// at least one non-empty media segment.
func (s *lhlsSink) ready() bool {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return false
	}
	var prefetch, init, segment bool
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		info, err := entry.Info()
		if err != nil {
			continue
		}
		switch {
		case lhlsManifestRe.MatchString(name) && !prefetch:
			if data, err := os.ReadFile(filepath.Join(s.dir, name)); err == nil &&
				strings.Contains(string(data), lhlsManifestPrefetchTag) {
				prefetch = true
			}
		case lhlsInitRe.MatchString(name) && info.Size() > 0:
			init = true
		case lhlsChunkRe.MatchString(name) && info.Size() > 0:
			segment = true
		}
	}
	return prefetch && init && segment
}

// publicReadable reports whether a generated filename may be served on the
// public /stream surface. The DASH .mpd and any temporary file are private.
func (s *lhlsSink) publicReadable(name string) bool {
	if name != path.Base(name) || strings.Contains(name, "..") {
		return false
	}
	return lhlsManifestRe.MatchString(name) || lhlsInitRe.MatchString(name) || lhlsChunkRe.MatchString(name)
}

func (s *lhlsSink) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	server := s.server
	listener := s.listener
	dir := s.dir
	s.mu.Unlock()

	if server != nil {
		_ = server.Close()
	} else if listener != nil {
		_ = listener.Close()
	}
	return os.RemoveAll(dir)
}

func splitSinkPath(p string) (token, name string, ok bool) {
	trimmed := strings.TrimPrefix(p, "/")
	if trimmed == p && p != "" {
		// Path did not start with "/"; treat as malformed.
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	token, name = parts[0], parts[1]
	if token == "" || name == "" {
		return "", "", false
	}
	return token, name, true
}

func isAllowedSinkName(name string) bool {
	if name != path.Base(name) || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return false
	}
	return lhlsManifestRe.MatchString(name) ||
		lhlsMPDRe.MatchString(name) ||
		lhlsInitRe.MatchString(name) ||
		lhlsChunkRe.MatchString(name)
}

func isManifestName(name string) bool {
	return strings.HasSuffix(name, ".m3u8") || strings.HasSuffix(name, ".mpd")
}

func isLoopbackRemote(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}
