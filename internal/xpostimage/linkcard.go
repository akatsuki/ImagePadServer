package xpostimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"image"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

const maxCardHTMLBytes = 2 << 20
const maxLinkCardCandidates = 3

var (
	metaTagPattern  = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	metaAttrPattern = regexp.MustCompile(`(?is)([a-zA-Z_:][a-zA-Z0-9_:.-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	titleTagPattern = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
)

type postLink struct {
	URL        string `json:"url"`
	ShortURL   string `json:"shortUrl"`
	DisplayURL string `json:"displayUrl"`
}

type linkCardData struct {
	URL         string `json:"url"`
	SiteName    string `json:"siteName"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ImageURL    string `json:"imageUrl"`
	SourceURL   string `json:"-"`
}

func fetchLinkCard(ctx context.Context, links []postLink) (*linkCardData, image.Image, string, error) {
	if len(links) > maxLinkCardCandidates {
		links = links[:maxLinkCardCandidates]
	}
	var lastErr error
	for _, link := range links {
		rawURL := strings.TrimSpace(link.URL)
		if rawURL == "" {
			continue
		}
		body, contentType, finalURL, err := fetchRemoteResource(ctx, rawURL, maxImageBytes)
		if err != nil {
			lastErr = err
			continue
		}
		candidateImage, _, decodeErr := image.Decode(bytes.NewReader(body))
		if strings.HasPrefix(strings.ToLower(contentType), "image/") || looksLikeImageURL(finalURL) || decodeErr == nil {
			if decodeErr != nil {
				lastErr = decodeErr
				continue
			}
			return nil, candidateImage, rawURL, nil
		}
		if int64(len(body)) > maxCardHTMLBytes {
			continue
		}

		card, err := parseLinkCardMetadata(finalURL, string(body))
		if err != nil {
			lastErr = err
			continue
		}
		card.URL = finalURL
		card.SourceURL = rawURL
		if !hasLinkCardPreview(card) {
			continue
		}
		if card.ImageURL == "" {
			return card, nil, rawURL, nil
		}
		img, err := downloadImage(ctx, card.ImageURL)
		if err != nil {
			card.ImageURL = ""
			return card, nil, rawURL, fmt.Errorf("download link-card image: %w", err)
		}
		return card, img, rawURL, nil
	}
	return nil, nil, "", lastErr
}

func hasLinkCardPreview(card *linkCardData) bool {
	return card != nil && (strings.TrimSpace(card.Title) != "" || strings.TrimSpace(card.Description) != "" || strings.TrimSpace(card.ImageURL) != "")
}

func parseLinkCardMetadata(pageURL, document string) (*linkCardData, error) {
	base, err := url.Parse(pageURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" {
		return nil, errors.New("link-card page URL must be HTTPS")
	}
	values := make(map[string]string)
	for _, tag := range metaTagPattern.FindAllString(document, -1) {
		attrs := make(map[string]string)
		for _, match := range metaAttrPattern.FindAllStringSubmatch(tag, -1) {
			value := match[2]
			if value == "" {
				value = match[3]
			}
			if value == "" {
				value = match[4]
			}
			attrs[strings.ToLower(match[1])] = html.UnescapeString(strings.TrimSpace(value))
		}
		key := strings.ToLower(attrs["property"])
		if key == "" {
			key = strings.ToLower(attrs["name"])
		}
		if value := strings.TrimSpace(attrs["content"]); key != "" && value != "" && values[key] == "" {
			values[key] = value
		}
	}
	first := func(keys ...string) string {
		for _, key := range keys {
			if value := strings.TrimSpace(values[key]); value != "" {
				return value
			}
		}
		return ""
	}
	title := first("og:title", "twitter:title")
	if title == "" {
		if match := titleTagPattern.FindStringSubmatch(document); len(match) == 2 {
			title = strings.TrimSpace(html.UnescapeString(stripHTMLTags(match[1])))
		}
	}
	description := first("og:description", "twitter:description", "description")
	imageURL := first("og:image:secure_url", "og:image", "twitter:image", "twitter:image:src")
	if imageURL != "" {
		imageRef, err := url.Parse(imageURL)
		if err != nil {
			imageURL = ""
		} else {
			resolved := base.ResolveReference(imageRef)
			if resolved.Scheme != "https" || resolved.Hostname() == "" {
				imageURL = ""
			} else {
				imageURL = resolved.String()
			}
		}
	}
	siteName := first("og:site_name")
	if siteName == "" {
		siteName = base.Hostname()
	}
	return &linkCardData{
		SiteName:    siteName,
		Title:       title,
		Description: description,
		ImageURL:    imageURL,
	}, nil
}

func fetchRemoteResource(ctx context.Context, rawURL string, maxBytes int64) ([]byte, string, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, "", "", errors.New("remote URL is invalid")
	}
	if err := validatePublicHTTPSURL(ctx, u); err != nil {
		return nil, "", "", err
	}
	client := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext: safePublicDialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return validatePublicHTTPSURL(req.Context(), req.URL)
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "x-post-image-prototype/0.1")
	req.Header.Set("Accept", "text/html,image/avif,image/webp,image/*;q=0.9,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("remote server returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if int64(len(body)) > maxBytes {
		return nil, "", "", fmt.Errorf("remote content exceeds %d MiB", maxBytes>>20)
	}
	return body, resp.Header.Get("Content-Type"), resp.Request.URL.String(), nil
}

func validatePublicHTTPSURL(ctx context.Context, u *url.URL) error {
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("remote URL must use HTTPS and a public host")
	}
	host := u.Hostname()
	addresses := []netip.Addr{}
	if address, err := netip.ParseAddr(host); err == nil {
		addresses = append(addresses, address.Unmap())
	} else {
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return fmt.Errorf("resolve remote host %q: %w", host, err)
		}
		addresses = resolved
	}
	if len(addresses) == 0 {
		return fmt.Errorf("remote host %q has no IP addresses", host)
	}
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
			return fmt.Errorf("remote host %q resolves to a non-public IP address", host)
		}
	}
	return nil
}

func safePublicDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses := []netip.Addr{}
	if ip, err := netip.ParseAddr(host); err == nil {
		addresses = append(addresses, ip.Unmap())
	} else {
		addresses, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	dialer := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range addresses {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("remote host %q has no public IP addresses", host)
}

func decodeRemoteImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

func looksLikeImageURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".avif":
		return true
	default:
		return false
	}
}

func stripHTMLTags(raw string) string {
	var out strings.Builder
	inTag := false
	for _, r := range raw {
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		default:
			if !inTag {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}
