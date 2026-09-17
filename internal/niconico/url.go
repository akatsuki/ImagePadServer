package niconico

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// VideoURL is the server-normalized identity of a NicoNico video URL.
type VideoURL struct {
	ID           string
	CanonicalURL string
}

var (
	watchIDPattern  = regexp.MustCompile(`^(?:(?:[a-z]{2})?\d+)$`)
	shortsIDPattern = regexp.MustCompile(`^ss\d+$`)
)

// ParseVideoURL accepts only NicoNico video URLs and removes presentation-only
// query parameters from the canonical identity.
func ParseVideoURL(raw string) (VideoURL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return VideoURL{}, fmt.Errorf("niconico: empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return VideoURL{}, fmt.Errorf("niconico: invalid URL")
	}
	if !strings.EqualFold(u.Scheme, "https") && !strings.EqualFold(u.Scheme, "http") {
		return VideoURL{}, fmt.Errorf("niconico: unsupported scheme")
	}
	if u.User != nil || u.Port() != "" {
		return VideoURL{}, fmt.Errorf("niconico: userinfo and custom ports are not allowed")
	}
	host := strings.ToLower(u.Hostname())
	path := strings.Trim(u.Path, "/")
	parts := strings.Split(path, "/")
	var id, canonicalPath string
	switch host {
	case "nico.ms":
		if len(parts) != 1 {
			return VideoURL{}, fmt.Errorf("niconico: unsupported short URL path")
		}
		id = parts[0]
		canonicalPath = "/watch/" + id
	case "nicovideo.jp", "www.nicovideo.jp", "sp.nicovideo.jp", "embed.nicovideo.jp":
		if len(parts) != 2 || (parts[0] != "watch" && parts[0] != "shorts") {
			return VideoURL{}, fmt.Errorf("niconico: unsupported video path")
		}
		id = parts[1]
		if parts[0] == "shorts" {
			if !shortsIDPattern.MatchString(id) {
				return VideoURL{}, fmt.Errorf("niconico: invalid shorts ID")
			}
			canonicalPath = "/shorts/" + id
		} else {
			canonicalPath = "/watch/" + id
		}
	default:
		return VideoURL{}, fmt.Errorf("niconico: unsupported host")
	}
	if !watchIDPattern.MatchString(id) && !shortsIDPattern.MatchString(id) {
		return VideoURL{}, fmt.Errorf("niconico: invalid video ID")
	}
	return VideoURL{ID: id, CanonicalURL: "https://www.nicovideo.jp" + canonicalPath}, nil
}
