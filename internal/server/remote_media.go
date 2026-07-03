package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"imagepadserver/internal/video"
)

// downloadedRemoteMedia holds the result of a successful media download.
type downloadedRemoteMedia struct {
	Path  string
	Name  string
	Class video.MediaClass
	Probe video.MediaProbe
}

var directMediaDownloader = downloadRemoteMedia

var (
	directMediaRangeChunkSize int64 = 8 * 1024 * 1024
	directMediaRangeWorkers         = 4
)

var directMediaProgress struct {
	mu sync.RWMutex
	cb func(written, total int64)
}

func (s *Server) downloadDirectMedia(ctx context.Context, rawURL string) (downloadedRemoteMedia, error) {
	ffprobe, err := findFFprobe()
	if err != nil {
		return downloadedRemoteMedia{}, err
	}
	directMediaProgress.mu.Lock()
	prev := directMediaProgress.cb
	directMediaProgress.cb = s.setDownloadByteProgress
	directMediaProgress.mu.Unlock()
	defer func() {
		directMediaProgress.mu.Lock()
		directMediaProgress.cb = prev
		directMediaProgress.mu.Unlock()
	}()
	return directMediaDownloader(ctx, rawURL, s.store.Dir(), func(ctx context.Context, path string) (video.MediaProbe, error) {
		return video.ProbeMedia(ctx, ffprobe, path)
	})
}

// downloadRemoteMedia downloads media from rawURL, validates it against SSRF
// rules and size limits, writes it to outDir, probes the result via the
// provided probe function, and returns metadata.
func downloadRemoteMedia(
	ctx context.Context,
	rawURL, outDir string,
	probe func(context.Context, string) (video.MediaProbe, error),
) (downloadedRemoteMedia, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			_, err := validatePublicURL(req.URL.String())
			return err
		},
	}

	rangeResult, err := probeRemoteMediaRange(ctx, client, rawURL)
	if err != nil {
		return downloadedRemoteMedia{}, err
	}
	if rangeResult.supported && rangeResult.total > directMediaRangeChunkSize {
		return downloadRemoteMediaWithRanges(ctx, client, rangeResult, outDir, probe)
	}
	return downloadRemoteMediaSingle(ctx, client, rawURL, outDir, probe)
}

type remoteMediaRangeProbe struct {
	supported bool
	url       string
	header    http.Header
	total     int64
}

func probeRemoteMediaRange(ctx context.Context, client *http.Client, rawURL string) (remoteMediaRangeProbe, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return remoteMediaRangeProbe{}, err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return remoteMediaRangeProbe{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return remoteMediaRangeProbe{url: resp.Request.URL.String()}, nil
	}
	total, ok := parseContentRangeTotal(resp.Header.Get("Content-Range"))
	if !ok || total <= 0 {
		return remoteMediaRangeProbe{url: resp.Request.URL.String()}, nil
	}
	return remoteMediaRangeProbe{
		supported: true,
		url:       resp.Request.URL.String(),
		header:    resp.Header.Clone(),
		total:     total,
	}, nil
}

func downloadRemoteMediaSingle(
	ctx context.Context,
	client *http.Client,
	rawURL, outDir string,
	probe func(context.Context, string) (video.MediaProbe, error),
) (downloadedRemoteMedia, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return downloadedRemoteMedia{}, err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return downloadedRemoteMedia{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return downloadedRemoteMedia{}, fmt.Errorf("download failed: %s", resp.Status)
	}

	// Reject oversized Content-Length before reading the body.
	if resp.ContentLength > 0 {
		if err := video.ValidateMediaContentLength(resp.ContentLength); err != nil {
			return downloadedRemoteMedia{}, err
		}
	}

	fileName := mediaFileName(resp.Request.URL, resp.Header)
	outPath := filepath.Join(outDir, filepath.Base(fileName))

	outFile, err := os.Create(outPath)
	if err != nil {
		return downloadedRemoteMedia{}, fmt.Errorf("create output file: %w", err)
	}
	defer outFile.Close()

	body := io.Reader(resp.Body)
	if resp.ContentLength > 0 {
		body = &directMediaProgressReader{reader: resp.Body, total: resp.ContentLength}
	} else {
		body = &directMediaProgressReader{reader: resp.Body}
	}
	written, err := video.CopyMediaWithLimit(outFile, body)
	if err != nil {
		_ = os.Remove(outPath)
		return downloadedRemoteMedia{}, err
	}
	_ = written

	_ = outFile.Close()

	probeResult, err := probe(ctx, outPath)
	if err != nil {
		_ = os.Remove(outPath)
		return downloadedRemoteMedia{}, fmt.Errorf("probe downloaded media: %w", err)
	}

	class := video.ClassifyMediaProbe(probeResult)

	return downloadedRemoteMedia{
		Path:  outPath,
		Name:  fileName,
		Class: class,
		Probe: probeResult,
	}, nil
}

func downloadRemoteMediaWithRanges(
	ctx context.Context,
	client *http.Client,
	probeResult remoteMediaRangeProbe,
	outDir string,
	probe func(context.Context, string) (video.MediaProbe, error),
) (downloadedRemoteMedia, error) {
	if err := video.ValidateMediaContentLength(probeResult.total); err != nil {
		return downloadedRemoteMedia{}, err
	}
	fileName := mediaFileName(mustParseURL(probeResult.url), probeResult.header)
	outPath := filepath.Join(outDir, filepath.Base(fileName))

	outFile, err := os.Create(outPath)
	if err != nil {
		return downloadedRemoteMedia{}, fmt.Errorf("create output file: %w", err)
	}
	defer outFile.Close()
	if err := outFile.Truncate(probeResult.total); err != nil {
		_ = os.Remove(outPath)
		return downloadedRemoteMedia{}, fmt.Errorf("preallocate output file: %w", err)
	}
	if err := downloadRemoteMediaRanges(ctx, client, probeResult.url, outFile, probeResult.total); err != nil {
		_ = os.Remove(outPath)
		return downloadedRemoteMedia{}, err
	}
	_ = outFile.Close()

	mediaProbe, err := probe(ctx, outPath)
	if err != nil {
		_ = os.Remove(outPath)
		return downloadedRemoteMedia{}, fmt.Errorf("probe downloaded media: %w", err)
	}
	class := video.ClassifyMediaProbe(mediaProbe)
	return downloadedRemoteMedia{
		Path:  outPath,
		Name:  fileName,
		Class: class,
		Probe: mediaProbe,
	}, nil
}

type mediaRangeChunk struct {
	start int64
	end   int64
}

var errNoRangeWorkers = errors.New("all range download workers stopped")

func downloadRemoteMediaRanges(ctx context.Context, client *http.Client, rawURL string, outFile *os.File, total int64) error {
	chunks := buildMediaRangeChunks(total, directMediaRangeChunkSize)
	if len(chunks) == 0 {
		return nil
	}
	workers := directMediaRangeWorkers
	if workers < 1 {
		workers = 1
	}
	if workers > len(chunks) {
		workers = len(chunks)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	queue := make(chan mediaRangeChunk, len(chunks))
	for _, chunk := range chunks {
		queue <- chunk
	}

	var mu sync.Mutex
	active := workers
	completed := 0
	var written int64
	var firstErr error
	done := make(chan struct{})
	var closeDone sync.Once
	finish := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
		closeDone.Do(func() { close(done) })
	}
	workerDone := func() {
		mu.Lock()
		defer mu.Unlock()
		active--
		if active == 0 && completed < len(chunks) {
			finish(errNoRangeWorkers)
		}
	}
	markComplete := func(n int64) {
		mu.Lock()
		defer mu.Unlock()
		completed++
		written += n
		reportDirectMediaProgress(written, total)
		if completed == len(chunks) {
			finish(nil)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer workerDone()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					mu.Lock()
					finish(ctx.Err())
					mu.Unlock()
					return
				case chunk := <-queue:
					n, err := downloadRemoteMediaRangeChunk(ctx, client, rawURL, outFile, chunk)
					if err != nil {
						if isRecoverableRangeError(err) {
							select {
							case queue <- chunk:
							default:
							}
							return
						}
						mu.Lock()
						finish(err)
						mu.Unlock()
						cancel()
						return
					}
					markComplete(n)
				}
			}
		}()
	}
	<-done
	cancel()
	wg.Wait()
	mu.Lock()
	err := firstErr
	mu.Unlock()
	return err
}

func buildMediaRangeChunks(total, chunkSize int64) []mediaRangeChunk {
	if total <= 0 {
		return nil
	}
	if chunkSize <= 0 {
		chunkSize = 8 * 1024 * 1024
	}
	var chunks []mediaRangeChunk
	for start := int64(0); start < total; start += chunkSize {
		end := start + chunkSize - 1
		if end >= total {
			end = total - 1
		}
		chunks = append(chunks, mediaRangeChunk{start: start, end: end})
	}
	return chunks
}

type rangeDownloadError struct {
	status int
	err    error
}

func (e rangeDownloadError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("range download failed: HTTP %d", e.status)
}

func downloadRemoteMediaRangeChunk(ctx context.Context, client *http.Client, rawURL string, outFile *os.File, chunk mediaRangeChunk) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", chunk.start, chunk.end))
	resp, err := client.Do(req)
	if err != nil {
		return 0, rangeDownloadError{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, rangeDownloadError{status: resp.StatusCode}
	}
	want := chunk.end - chunk.start + 1
	data, err := io.ReadAll(io.LimitReader(resp.Body, want+1))
	if err != nil {
		return 0, rangeDownloadError{err: err}
	}
	if int64(len(data)) != want {
		return 0, rangeDownloadError{err: fmt.Errorf("range length = %d, want %d", len(data), want)}
	}
	n, err := outFile.WriteAt(data, chunk.start)
	if err != nil {
		return 0, err
	}
	if int64(n) != want {
		return 0, fmt.Errorf("range write length = %d, want %d", n, want)
	}
	return want, nil
}

func isRecoverableRangeError(err error) bool {
	var rangeErr rangeDownloadError
	if !errors.As(err, &rangeErr) {
		return false
	}
	if rangeErr.err != nil {
		return true
	}
	return rangeErr.status == http.StatusForbidden ||
		rangeErr.status == http.StatusTooManyRequests ||
		rangeErr.status >= 500
}

func parseContentRangeTotal(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	slash := strings.LastIndex(value, "/")
	if slash < 0 || slash == len(value)-1 {
		return 0, false
	}
	total, err := strconv.ParseInt(value[slash+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return total, true
}

func mustParseURL(rawURL string) *url.URL {
	u, err := url.Parse(rawURL)
	if err != nil {
		return &url.URL{Path: "remote-media"}
	}
	return u
}

type directMediaProgressReader struct {
	reader       io.Reader
	total        int64
	written      int64
	lastPercent  int
	lastReported time.Time
}

func (r *directMediaProgressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.written += int64(n)
		r.maybeReport()
	}
	return n, err
}

func (r *directMediaProgressReader) maybeReport() {
	percent := 0
	if r.total > 0 {
		percent = int(r.written * 100 / r.total)
	}
	now := time.Now()
	if percent == r.lastPercent && now.Sub(r.lastReported) < 500*time.Millisecond {
		return
	}
	r.lastPercent = percent
	r.lastReported = now
	directMediaProgress.mu.RLock()
	cb := directMediaProgress.cb
	directMediaProgress.mu.RUnlock()
	if cb != nil {
		cb(r.written, r.total)
	}
}

func reportDirectMediaProgress(written, total int64) {
	directMediaProgress.mu.RLock()
	cb := directMediaProgress.cb
	directMediaProgress.mu.RUnlock()
	if cb != nil {
		cb(written, total)
	}
}

// mediaFileName extracts a display name for the downloaded media from
// Content-Disposition if present, otherwise from the final URL path.
func mediaFileName(u *url.URL, header http.Header) string {
	if cd := header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if name := params["filename"]; name != "" {
				return filepath.Base(name)
			}
		}
	}
	name := filepath.Base(u.Path)
	if name == "." || name == "/" || name == "" {
		name = "remote-media"
	}
	return name
}
