package xpostimage

import (
	"fmt"
	"net/url"
	"strings"
)

type photoInfo struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type layoutKind string

const (
	layoutTextOnly   layoutKind = "text-only"
	layoutStacked    layoutKind = "stacked"
	layoutSideBySide layoutKind = "side-by-side"
)

type galleryArrangement uint8

const (
	galleryDefault galleryArrangement = iota
	galleryTwoColumns
	galleryTwoRows
	galleryLeftOneRightTwo
	galleryLeftTwoRightOne
	galleryTopOneBottomTwo
	galleryTopTwoBottomOne
)

type canvasLayout struct {
	kind    layoutKind
	width   int
	height  int
	gallery galleryArrangement
}

func parseTweetID(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid X post URL: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("X post URL must use https")
	}
	host := strings.ToLower(u.Hostname())
	if host != "x.com" && host != "www.x.com" && host != "twitter.com" && host != "www.twitter.com" {
		return "", fmt.Errorf("unsupported X post host %q", host)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if strings.EqualFold(parts[i], "status") {
			id := parts[i+1]
			if id == "" || len(id) > 40 {
				break
			}
			for _, r := range id {
				if r < '0' || r > '9' {
					return "", fmt.Errorf("post id must be numeric")
				}
			}
			return id, nil
		}
	}
	return "", fmt.Errorf("URL does not contain a numeric /status/{id} path")
}

func chooseLayout(photo *photoInfo) canvasLayout {
	if photo == nil || photo.Width <= 0 || photo.Height <= 0 {
		return canvasLayout{kind: layoutTextOnly, width: 2048, height: 1536}
	}
	if photo.Width >= photo.Height {
		return canvasLayout{kind: layoutStacked, width: 1536, height: 2048}
	}
	return canvasLayout{kind: layoutSideBySide, width: 2048, height: 1536}
}
