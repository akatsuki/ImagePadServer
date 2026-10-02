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
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

const maxImageBytes = 24 << 20

type postData struct {
	TweetID                string          `json:"tweetId"`
	Text                   string          `json:"text"`
	CreatedAt              string          `json:"createdAt"`
	UserName               string          `json:"userName"`
	Handle                 string          `json:"handle"`
	AvatarURL              string          `json:"avatarUrl"`
	Photo                  *photoInfo      `json:"photo"`
	Photos                 []photoInfo     `json:"photos"`
	MediaImages            []image.Image   `json:"-"`
	MediaLinkWithoutPoster bool            `json:"mediaLinkWithoutPoster"`
	Links                  []postLink      `json:"links"`
	Quoted                 *quotedPostData `json:"quoted"`
	Card                   *linkCardData   `json:"-"`
	CardImageIsMain        bool            `json:"-"`
	MediaSourceURL         string          `json:"-"`
}

type quotedPostData struct {
	Relation               string        `json:"relation"`
	TweetID                string        `json:"tweetId"`
	Text                   string        `json:"text"`
	CreatedAt              string        `json:"createdAt"`
	UserName               string        `json:"userName"`
	Handle                 string        `json:"handle"`
	AvatarURL              string        `json:"avatarUrl"`
	Photo                  *photoInfo    `json:"photo"`
	Photos                 []photoInfo   `json:"photos"`
	MediaImages            []image.Image `json:"-"`
	MediaLinkWithoutPoster bool          `json:"mediaLinkWithoutPoster"`
	Links                  []postLink    `json:"links"`
}

type Result struct {
	TweetID string
	PNG     []byte
}

// IsPostURL reports whether raw is an HTTPS X or Twitter status URL with a
// numeric post ID.
func IsPostURL(raw string) bool {
	_, err := parseTweetID(raw)
	return err == nil
}

// Render fetches a public post through react-tweet and renders the ImagePad
// canvas as a PNG. fontPath should point to the application's bundled Noto
// Sans JP font.
func Render(parent context.Context, rawURL, themeMode, fontPath string) (Result, error) {
	id, err := parseTweetID(rawURL)
	if err != nil {
		return Result{}, err
	}
	if _, err := paletteForTheme(themeMode); err != nil {
		return Result{}, err
	}
	nodePath, err := findNodeExecutable()
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	post, err := fetchPost(ctx, nodePath, id)
	if err != nil {
		return Result{}, err
	}

	var photo image.Image
	var failures []error
	mainPhotoInfos := photoInfoList(post.Photo, post.Photos)
	quotedPhotoInfos := []photoInfo(nil)
	if post.Quoted != nil {
		quotedPhotoInfos = photoInfoList(post.Quoted.Photo, post.Quoted.Photos)
	}
	if len(mainPhotoInfos) > 0 {
		post.MediaImages, failures = downloadPhotoSet(ctx, mainPhotoInfos, downloadImage)
		if failures[0] != nil {
			return Result{}, fmt.Errorf("download first attached image or video poster: %w", failures[0])
		}
		photo = post.MediaImages[0]
		post.Photo = &photoInfo{URL: mainPhotoInfos[0].URL, Width: photo.Bounds().Dx(), Height: photo.Bounds().Dy()}
		warnPhotoFailures("attached media", failures[1:], 2)

		if post.Quoted != nil && len(quotedPhotoInfos) > 0 {
			post.Quoted.MediaImages, failures = downloadPhotoSet(ctx, quotedPhotoInfos, downloadImage)
			warnPhotoFailures("quoted post media", failures, 1)
		}
	} else if post.Quoted != nil && len(quotedPhotoInfos) > 0 {
		post.Quoted.MediaImages, failures = downloadPhotoSet(ctx, quotedPhotoInfos, downloadImage)
		if failures[0] != nil {
			return Result{}, fmt.Errorf("download first quoted post image or video poster: %w", failures[0])
		}
		post.MediaImages = post.Quoted.MediaImages
		photo = post.MediaImages[0]
		post.Photo = &photoInfo{URL: quotedPhotoInfos[0].URL, Width: photo.Bounds().Dx(), Height: photo.Bounds().Dy()}
		post.Quoted.MediaImages = nil
		warnPhotoFailures("quoted post media", failures[1:], 2)
	}
	var cardImage image.Image
	var cardSourceURL string
	cardLinks := linkCardCandidates(post)
	if len(cardLinks) > 0 {
		post.Card, cardImage, cardSourceURL, err = fetchLinkCard(ctx, cardLinks)
		if err != nil {
			// A linked card is optional. Keep rendering the post when its preview is
			// unavailable, matching the standalone prototype's behavior.
		}
		if post.Card != nil {
			post.Card.SourceURL = cardSourceURL
		}
	}
	rewriteShortLinks(&post)
	if photo == nil && cardImage != nil {
		photo = cardImage
		post.MediaImages = []image.Image{cardImage}
		post.Photo = &photoInfo{Width: photo.Bounds().Dx(), Height: photo.Bounds().Dy()}
		post.CardImageIsMain = true
		post.MediaSourceURL = cardSourceURL
	}
	if photo == nil && (post.MediaLinkWithoutPoster || post.Quoted != nil && post.Quoted.MediaLinkWithoutPoster) {
		return Result{}, errors.New("react-tweet found attached media but did not provide an image or video poster; refusing to treat it as a text-only post")
	}
	var avatar image.Image
	if post.AvatarURL != "" {
		avatar, err = downloadImage(ctx, post.AvatarURL)
		if err != nil {
			avatar = nil
		}
	}
	var quotedAvatar image.Image
	if post.Quoted != nil && post.Quoted.AvatarURL != "" {
		if post.Quoted.AvatarURL == post.AvatarURL {
			quotedAvatar = avatar
		} else {
			quotedAvatar, err = downloadImage(ctx, post.Quoted.AvatarURL)
			if err != nil {
				quotedAvatar = nil
			}
		}
	}

	img, err := renderPost(post, photo, avatar, cardImage, quotedAvatar, themeMode, fontPath)
	if err != nil {
		return Result{}, err
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		return Result{}, fmt.Errorf("encode X post image: %w", err)
	}
	return Result{TweetID: id, PNG: output.Bytes()}, nil
}

func photoInfoList(primary *photoInfo, photos []photoInfo) []photoInfo {
	if len(photos) == 0 && primary != nil {
		photos = []photoInfo{*primary}
	}
	if len(photos) > 4 {
		photos = photos[:4]
	}
	return append([]photoInfo(nil), photos...)
}

func downloadPhotoSet(ctx context.Context, photos []photoInfo, load func(context.Context, string) (image.Image, error)) ([]image.Image, []error) {
	if len(photos) > 4 {
		photos = photos[:4]
	}
	images := make([]image.Image, len(photos))
	failures := make([]error, len(photos))
	for index, photo := range photos {
		if strings.TrimSpace(photo.URL) == "" {
			failures[index] = errors.New("photo URL is empty")
			continue
		}
		images[index], failures[index] = load(ctx, photo.URL)
		if images[index] != nil && images[index].Bounds().Empty() && failures[index] == nil {
			failures[index] = errors.New("downloaded image is empty")
			images[index] = nil
		}
		if images[index] == nil && failures[index] == nil {
			failures[index] = errors.New("image loader returned no image")
		}
	}
	return images, failures
}

func warnPhotoFailures(label string, failures []error, firstNumber int) {
	for index, err := range failures {
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not load %s image %d: %v\n", label, index+firstNumber, err)
		}
	}
}

func linkCardCandidates(post postData) []postLink {
	filtered := make([]postLink, 0, len(post.Links))
	for _, link := range post.Links {
		parsed, err := url.Parse(link.URL)
		if err == nil && isXStatusMediaURL(parsed) {
			continue
		}
		filtered = append(filtered, link)
	}
	return filtered
}

func isXStatusMediaURL(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "x.com" && host != "www.x.com" && host != "twitter.com" && host != "www.twitter.com" {
		return false
	}
	return strings.Contains(parsed.Path, "/status/") &&
		(strings.Contains(parsed.Path, "/photo/") || strings.Contains(parsed.Path, "/video/"))
}

func rewriteShortLinks(post *postData) {
	for index, link := range post.Links {
		shortURL := strings.TrimSpace(link.ShortURL)
		displayURL := strings.TrimSpace(link.DisplayURL)
		if shortURL == "" || displayURL == "" || shortURL == displayURL {
			if index == 0 && post.Card != nil {
				if cardURL, err := url.Parse(post.Card.URL); err == nil && cardURL.Hostname() != "" {
					displayURL = cardURL.Hostname()
				}
			}
		}
		if shortURL != "" && displayURL != "" && shortURL != displayURL {
			post.Text = strings.ReplaceAll(post.Text, shortURL, displayURL)
		}
	}
}

func findNodeExecutable() (string, error) {
	node := os.Getenv("IMAGEPAD_NODE")
	if node == "" {
		node = "node"
	}
	path, err := exec.LookPath(node)
	if err != nil {
		return "", errors.New("Node.js 20 or later is required for X post conversion; install Node.js and make node available on PATH")
	}
	return path, nil
}

func fetchPost(ctx context.Context, nodePath, id string) (postData, error) {
	cmd := exec.CommandContext(ctx, nodePath, "--input-type=module", "-", id)
	cmd.Stdin = bytes.NewReader(bundledFetcherScript)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return postData{}, fmt.Errorf("react-tweet fetch failed: %s", message)
	}
	var post postData
	if err := json.Unmarshal(stdout, &post); err != nil {
		return postData{}, fmt.Errorf("decode react-tweet response: %w", err)
	}
	if post.TweetID == "" || post.UserName == "" {
		return postData{}, errors.New("react-tweet returned incomplete post data")
	}
	if post.Photo != nil && (post.Photo.URL == "" || post.Photo.Width <= 0 || post.Photo.Height <= 0) {
		return postData{}, errors.New("react-tweet returned invalid first-photo metadata")
	}
	return post, nil
}

func downloadImage(ctx context.Context, rawURL string) (image.Image, error) {
	data, _, _, err := fetchRemoteResource(ctx, rawURL, maxImageBytes)
	if err != nil {
		return nil, err
	}
	img, err := decodeRemoteImage(data)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return img, nil
}

func savePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create PNG: %w", err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return fmt.Errorf("write PNG: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close PNG: %w", err)
	}
	return nil
}
