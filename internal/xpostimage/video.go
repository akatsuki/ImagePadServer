package xpostimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"imagepadserver/internal/video"
	"imagepadserver/internal/xpostmodel"
)

const maxVideoBytes = video.MaxMediaSourceBytes

type VideoAssets struct {
	Post  xpostmodel.Post
	Pages []xpostmodel.Page
	Media []xpostmodel.Media
}

type mediaFetcher func(context.Context, string, int64) ([]byte, string, string, error)

type videoFetchFlags struct {
	VideoExpected bool `json:"videoExpected"`
	Quoted        *struct {
		VideoExpected bool `json:"videoExpected"`
	} `json:"quoted"`
}

// FetchVideoPost returns public post metadata without downloading attached media.
func FetchVideoPost(parent context.Context, rawURL string) (xpostmodel.Post, error) {
	id, err := parseTweetID(rawURL)
	if err != nil {
		return xpostmodel.Post{}, err
	}
	nodePath, err := findNodeExecutable()
	if err != nil {
		return xpostmodel.Post{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodePath, "--input-type=module", "-", id)
	cmd.Stdin = bytes.NewReader(bundledFetcherScript)
	if override := strings.TrimSpace(os.Getenv("IMAGEPAD_XPOST_FETCHER")); override != "" {
		cmd = exec.CommandContext(ctx, nodePath, override, id)
		cmd.Dir = filepath.Dir(override)
	}
	hideVideoFetchWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return xpostmodel.Post{}, fmt.Errorf("react-tweet fetch failed: %s", message)
	}
	var post xpostmodel.Post
	if err := json.Unmarshal(stdout, &post); err != nil {
		return xpostmodel.Post{}, fmt.Errorf("decode react-tweet video response: %w", err)
	}
	if post.ID == "" || post.Name == "" {
		return xpostmodel.Post{}, errors.New("react-tweet returned incomplete post data")
	}
	if post.Truncated {
		return xpostmodel.Post{}, errors.New("react-tweet returned truncated post text")
	}
	if post.Quoted != nil && post.Quoted.Truncated {
		return xpostmodel.Post{}, errors.New("react-tweet returned truncated quoted post text")
	}
	var flags videoFetchFlags
	if err := json.Unmarshal(stdout, &flags); err != nil {
		return xpostmodel.Post{}, fmt.Errorf("decode react-tweet video flags: %w", err)
	}
	if flags.VideoExpected && !containsVideo(post.Media) {
		return xpostmodel.Post{}, errors.New("X post contains video media but no usable MP4 variant was returned")
	}
	if post.Quoted != nil && flags.Quoted != nil && flags.Quoted.VideoExpected && !containsVideo(post.Quoted.Media) {
		// The quote is only a fallback when the original post has no attached media.
		if len(post.Media) == 0 {
			return xpostmodel.Post{}, errors.New("quoted X post contains video media but no usable MP4 variant was returned")
		}
	}
	return post, nil
}

// PrepareVideo fetches post metadata, downloads the selected real media assets,
// and writes static post and quote cards into outDir.
func PrepareVideo(ctx context.Context, rawURL, theme, fontPath, outDir, credit string) (VideoAssets, error) {
	post, err := FetchVideoPost(ctx, rawURL)
	if err != nil {
		return VideoAssets{}, err
	}
	return PrepareVideoPost(ctx, post, theme, fontPath, outDir, credit)
}

// PrepareVideoPost prepares previously fetched metadata so callers can validate
// narration and finalize credit before this function writes any cards.
func PrepareVideoPost(ctx context.Context, post xpostmodel.Post, theme, fontPath, outDir, credit string) (VideoAssets, error) {
	if strings.TrimSpace(outDir) == "" {
		return VideoAssets{}, errors.New("X video output directory is required")
	}
	selected := selectPostMedia(post)
	media, err := downloadMediaFilesWithVideo(ctx, selected, filepath.Join(outDir, "media"), fetchRemoteResource, downloadVideoFile)
	if err != nil {
		return VideoAssets{}, err
	}
	avatars := loadVideoAvatars(ctx, post, downloadImage)
	if err := ctx.Err(); err != nil {
		return VideoAssets{}, err
	}
	pages, err := renderVideoCards(post, theme, fontPath, outDir, credit, avatars)
	if err != nil {
		return VideoAssets{}, err
	}
	return VideoAssets{Post: post, Pages: pages, Media: media}, nil
}

func selectPostMedia(post xpostmodel.Post) []xpostmodel.Media {
	selected := post.Media
	if len(selected) == 0 && post.Quoted != nil {
		selected = post.Quoted.Media
	}
	if len(selected) > 4 {
		selected = selected[:4]
	}
	return append([]xpostmodel.Media(nil), selected...)
}

func containsVideo(media []xpostmodel.Media) bool {
	for _, asset := range media {
		if asset.Kind == "video" || asset.Kind == "animated_gif" {
			return strings.TrimSpace(asset.URL) != ""
		}
	}
	return false
}

func downloadMediaFiles(ctx context.Context, media []xpostmodel.Media, outDir string, fetch mediaFetcher) ([]xpostmodel.Media, error) {
	return downloadMediaFilesWithVideo(ctx, media, outDir, fetch, nil)
}

func downloadMediaFilesWithVideo(ctx context.Context, media []xpostmodel.Media, outDir string, fetch mediaFetcher, streamVideo func(context.Context, string, string, int64) error) ([]xpostmodel.Media, error) {
	if len(media) > 4 {
		media = media[:4]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create X media directory: %w", err)
	}
	result := make([]xpostmodel.Media, 0, len(media))
	for index, asset := range media {
		asset.URL = strings.TrimSpace(asset.URL)
		if asset.URL == "" {
			return nil, fmt.Errorf("download X media %d: URL is empty", index+1)
		}
		limit := int64(maxImageBytes)
		extension := ".img"
		if asset.Kind == "video" || asset.Kind == "animated_gif" {
			limit = maxVideoBytes
			extension = ".mp4"
		} else if asset.Kind != "image" {
			return nil, fmt.Errorf("download X media %d: unsupported media kind %q", index+1, asset.Kind)
		}
		path := filepath.Join(outDir, fmt.Sprintf("media-%02d%s", index+1, extension))
		if extension == ".mp4" && streamVideo != nil {
			if err := streamVideo(ctx, asset.URL, path, limit); err != nil {
				return nil, fmt.Errorf("download X media %d: %w", index+1, err)
			}
			asset.Kind, asset.Path = "video", path
			result = append(result, asset)
			continue
		}
		data, _, _, err := fetch(ctx, asset.URL, limit)
		if err != nil {
			return nil, fmt.Errorf("download X media %d: %w", index+1, err)
		}
		if int64(len(data)) > limit {
			return nil, fmt.Errorf("download X media %d exceeds %d MiB", index+1, limit>>20)
		}
		if extension == ".mp4" && !isMP4Data(data) {
			return nil, fmt.Errorf("download X media %d is not an MP4 video", index+1)
		}
		if extension == ".img" {
			decoded, _, err := image.Decode(bytes.NewReader(data))
			if err != nil || decoded.Bounds().Empty() {
				return nil, fmt.Errorf("download X media %d is not a valid image", index+1)
			}
			asset.Width, asset.Height = decoded.Bounds().Dx(), decoded.Bounds().Dy()
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("save X media %d: %w", index+1, err)
		}
		asset.Path = path
		result = append(result, asset)
	}
	return result, nil
}

func isMP4Data(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	limit := min(len(data)-4, 64)
	for offset := 0; offset <= limit; offset++ {
		if string(data[offset:offset+4]) == "ftyp" {
			return true
		}
	}
	return false
}

// RenderVideoCards renders local post and quote cards without fetching assets.
// PrepareVideoPost supplies optional profile images; attached media stays separate.
func RenderVideoCards(post xpostmodel.Post, theme, fontPath, outDir, credit string) ([]xpostmodel.Page, error) {
	return renderVideoCards(post, theme, fontPath, outDir, credit, videoAvatars{})
}

func renderVideoCards(post xpostmodel.Post, theme, fontPath, outDir, credit string, avatars videoAvatars) ([]xpostmodel.Page, error) {
	if strings.TrimSpace(outDir) == "" {
		return nil, errors.New("X card output directory is required")
	}
	palette, err := paletteForTheme(theme)
	if err != nil {
		return nil, err
	}
	fonts, err := loadFontSource(fontPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create X card directory: %w", err)
	}
	return renderFixedVideoCards(post, palette, fonts, outDir, credit, avatars)
}

type videoTextChunk struct {
	text     string
	entities []xpostmodel.Entity
}

func splitVideoText(text string, entities []xpostmodel.Entity, maxUnits int) []videoTextChunk {
	if text == "" {
		return []videoTextChunk{{}}
	}
	units := utf16.Encode([]rune(text))
	if maxUnits < 1 {
		maxUnits = 520
	}
	chunks := make([]videoTextChunk, 0, (len(units)+maxUnits-1)/maxUnits)
	start := 0
	for start < len(units) {
		end := min(start+maxUnits, len(units))
		lineBreaks := 0
		for index := start; index < end; index++ {
			if units[index] == '\n' {
				lineBreaks++
				if lineBreaks == 16 {
					end = index + 1
					break
				}
			}
		}
		if end < len(units) {
			for _, entity := range entities {
				if entity.Start < end && entity.End > end {
					if entity.Start > start {
						end = entity.Start
					} else {
						end = entity.End
					}
				}
			}
		}
		end = min(end, len(units))
		if end > start && end < len(units) && units[end-1] >= 0xD800 && units[end-1] <= 0xDBFF && units[end] >= 0xDC00 && units[end] <= 0xDFFF {
			end--
		}
		if end <= start {
			end = min(start+maxUnits, len(units))
			if end < len(units) && units[end-1] >= 0xD800 && units[end-1] <= 0xDBFF && units[end] >= 0xDC00 && units[end] <= 0xDFFF {
				end--
			}
		}
		chunkText := string(utf16.Decode(units[start:end]))
		chunkEntities := make([]xpostmodel.Entity, 0, len(entities))
		for _, entity := range entities {
			if entity.Start >= start && entity.End <= end {
				adjusted := entity
				adjusted.Start -= start
				adjusted.End -= start
				chunkEntities = append(chunkEntities, adjusted)
			}
		}
		chunks = append(chunks, videoTextChunk{text: chunkText, entities: chunkEntities})
		start = end
	}
	return chunks
}
