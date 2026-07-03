package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"imagepadserver/internal/video"

	"golang.org/x/net/html"
)

var errPageHLSNotFound = errors.New("page does not expose an HLS video source")

func downloadPageHLSMedia(ctx context.Context, pageURL, outDir string) (video.DownloadedMedia, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			_, err := validatePublicURL(req.URL.String())
			return err
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return video.DownloadedMedia{}, err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return video.DownloadedMedia{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return video.DownloadedMedia{}, fmt.Errorf("page HLS probe failed: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return video.DownloadedMedia{}, err
	}
	hlsURL, ok := extractPageHLSURL(resp.Request.URL.String(), string(body))
	if !ok {
		return video.DownloadedMedia{}, errPageHLSNotFound
	}
	if _, err := validatePublicURL(hlsURL); err != nil {
		return video.DownloadedMedia{}, err
	}
	return video.DownloadMediaURL(hlsURL, outDir)
}

func extractPageHLSURL(pageURL, htmlText string) (string, bool) {
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", false
	}
	doc, err := html.Parse(strings.NewReader(htmlText))
	if err != nil {
		return "", false
	}
	var found string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil || found != "" {
			return
		}
		if n.Type == html.ElementNode && (n.Data == "video" || n.Data == "source") {
			src := attrValue(n, "src")
			typ := strings.ToLower(attrValue(n, "type"))
			if src != "" && (isHLSURL(src) || strings.Contains(typ, "mpegurl")) {
				found = resolvePageURL(base, src)
				return
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	if found == "" {
		return "", false
	}
	return found, true
}

func attrValue(n *html.Node, name string) string {
	for _, attr := range n.Attr {
		if strings.EqualFold(attr.Key, name) {
			return strings.TrimSpace(attr.Val)
		}
	}
	return ""
}

func isHLSURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return strings.Contains(strings.ToLower(rawURL), ".m3u8")
	}
	return strings.Contains(strings.ToLower(u.Path), ".m3u8")
}

func resolvePageURL(base *url.URL, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return base.ResolveReference(u).String()
}
