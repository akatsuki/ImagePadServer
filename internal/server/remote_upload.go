package server

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/imageproc"
)

func downloadRemoteImage(rawURL string, maxBytes int64) (io.ReadCloser, string, error) {
	parsed, err := validatePublicURL(rawURL)
	if err != nil {
		return nil, "", err
	}
	if maxBytes <= 0 || maxBytes > 120<<20 {
		maxBytes = 120 << 20
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			_, err := validatePublicURL(req.URL.String())
			return err
		},
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
	}

	req, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ImagePadServer/1.0")
	req.Header.Set("Accept", "image/webp,image/svg+xml,image/png,image/jpeg,image/gif,image/bmp,image/tiff,image/x-sony-arw,image/x-sony-srf,image/x-sony-sr2,image/x-canon-crw,image/x-canon-cr2,image/x-canon-cr3,image/x-panasonic-rw2,image/x-olympus-orf,image/x-fuji-raf,image/x-nikon-nef,image/x-nikon-nrw,image/x-sigma-x3f,image/x-adobe-dng,image/*;q=0.8,application/octet-stream;q=0.6,*/*;q=0.2")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download failed: %s", resp.Status)
	}
	if resp.ContentLength > maxBytes {
		return nil, "", fmt.Errorf("remote image exceeds size limit of %d bytes", maxBytes)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !remoteContentTypeAllowed(ct) {
		return nil, "", fmt.Errorf("remote content is not an image: %s", ct)
	}

	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("remote image exceeds size limit of %d bytes", maxBytes)
	}
	name := remoteFileName(resp.Request.URL, resp.Header.Get("Content-Type"))
	return io.NopCloser(bytes.NewReader(data)), name, nil
}

func validatePublicURL(rawURL string) (*url.URL, error) {
	parsed, err := validateRemoteHTTPURL(rawURL)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func validateHTTPURL(rawURL string) error {
	_, err := validateRemoteHTTPURL(rawURL)
	return err
}

func validateRemoteHTTPURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https URLs are allowed")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("invalid URL host")
	}
	if isBlockedHost(host) {
		return nil, fmt.Errorf("local or private network URLs are not allowed")
	}
	return parsed, nil
}

func isBlockedHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return true
	}
	for _, ip := range ips {
		if isBlockedIP(ip) {
			return true
		}
	}
	return false
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 10:
			return true
		case v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31:
			return true
		case v4[0] == 192 && v4[1] == 168:
			return true
		case v4[0] == 169 && v4[1] == 254:
			return true
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
			return true
		}
		return false
	}
	return ip.IsPrivate()
}

func remoteContentTypeAllowed(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	return strings.HasPrefix(mediaType, "image/") || mediaType == "application/octet-stream"
}

func remoteFileName(u *url.URL, contentType string) string {
	name := filepath.Base(u.Path)
	if name == "." || name == "/" || name == "" {
		name = "remote-image"
	}
	if filepath.Ext(name) != "" {
		return name
	}
	if rawExt := rawExtensionFromQuery(u.RawQuery); rawExt != "" {
		return name + rawExt
	}
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(mediaType) {
	case "image/jpeg":
		return name + ".jpg"
	case "image/png":
		return name + ".png"
	case "image/gif":
		return name + ".gif"
	case "image/webp":
		return name + ".webp"
	case "image/bmp":
		return name + ".bmp"
	case "image/tiff":
		return name + ".tiff"
	case "image/svg+xml":
		return name + ".svg"
	case "image/x-sony-arw":
		return name + ".arw"
	case "image/x-sony-srf":
		return name + ".srf"
	case "image/x-sony-sr2":
		return name + ".sr2"
	case "image/x-canon-crw":
		return name + ".crw"
	case "image/x-canon-cr2":
		return name + ".cr2"
	case "image/x-canon-cr3":
		return name + ".cr3"
	case "image/x-panasonic-rw2":
		return name + ".rw2"
	case "image/x-olympus-orf":
		return name + ".orf"
	case "image/x-fuji-raf":
		return name + ".raf"
	case "image/x-nikon-nef":
		return name + ".nef"
	case "image/x-nikon-nrw":
		return name + ".nrw"
	case "image/x-sigma-x3f":
		return name + ".x3f"
	case "image/x-adobe-dng":
		return name + ".dng"
	default:
		return name
	}
}

func rawExtensionFromQuery(rawQuery string) string {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return ""
	}
	for _, key := range []string{"filename", "file", "name"} {
		for _, value := range values[key] {
			ext := strings.ToLower(filepath.Ext(value))
			if ext != "" && imageproc.IsCameraRAWName("remote"+ext) {
				return ext
			}
		}
	}
	return ""
}
