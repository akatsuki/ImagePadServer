package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/skip2/go-qrcode"

	"imagepadserver/internal/clipboard"
	"imagepadserver/internal/obsrtmp"
)

func (s *Server) withClipboardResult(state map[string]interface{}) map[string]interface{} {
	copiedURL := urlForClipboard(state)
	clipboardCopied := false
	if copiedURL != "" {
		if err := clipboard.CopyText(copiedURL); err == nil {
			clipboardCopied = true
		}
	}
	state["copiedURL"] = copiedURL
	state["clipboardCopied"] = clipboardCopied
	return state
}

func (s *Server) handleCopyURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Target string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid copy request", http.StatusBadRequest)
		return
	}

	state := s.state(r)
	copiedURL := urlForCopyTarget(state, req.Target)
	if copiedURL == "" {
		http.Error(w, "no URL available to copy", http.StatusBadRequest)
		return
	}

	clipboardCopied := clipboard.CopyText(copiedURL) == nil
	writeJSON(w, map[string]interface{}{
		"copiedURL":         copiedURL,
		"pcClipboardCopied": clipboardCopied,
	})
}

func (s *Server) handlePhoneQR(w http.ResponseWriter, r *http.Request) {
	png, err := qrcode.Encode(s.adminURL(s.lanURL), qrcode.Medium, 512)
	if err != nil {
		http.Error(w, "failed to generate QR", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) adminURL(baseURL string) string {
	if s.adminToken == "" {
		return baseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	query := parsed.Query()
	query.Set("token", s.adminToken)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Server) adminPath(path string) string {
	if s.adminToken == "" {
		return path
	}
	if strings.Contains(path, "?") {
		return path + "&token=" + url.QueryEscape(s.adminToken)
	}
	return path + "?token=" + url.QueryEscape(s.adminToken)
}

func urlForClipboard(state map[string]interface{}) string {
	if shareURL, _ := state["shareURL"].(string); strings.HasPrefix(shareURL, "http") {
		return shareURL
	}
	shareURL, _ := primaryShareURL(state)
	if strings.HasPrefix(shareURL, "http") {
		return shareURL
	}
	return urlForCopyTarget(state, "imageURL")
}

func primaryShareURL(state map[string]interface{}) (string, string) {
	if videoPlayer, ok := state["videoPlayer"].(map[string]interface{}); ok {
		if enabled, _ := videoPlayer["enabled"].(bool); enabled {
			if hlsURL, ok := state["hlsURL"].(string); ok && strings.HasPrefix(hlsURL, "http") {
				return hlsURL, "HLS URL"
			}
			if videoURL, ok := state["videoURL"].(string); ok && strings.HasPrefix(videoURL, "http") {
				return videoURL, "MP4 URL"
			}
		}
	}
	if imageURL, ok := state["imageURL"].(string); ok && strings.HasPrefix(imageURL, "http") {
		return imageURL, "ImagePad URL"
	}
	if publicURL, ok := state["publicImageURL"].(string); ok && strings.HasPrefix(publicURL, "http") {
		return publicURL, "ImagePad URL"
	}
	if localURL, ok := state["localImageURL"].(string); ok && strings.HasPrefix(localURL, "http") {
		return localURL, "Local URL"
	}
	return "", "URL"
}

func urlForCopyTarget(state map[string]interface{}, target string) string {
	switch target {
	case "shareURL":
		if shareURL, ok := state["shareURL"].(string); ok {
			return shareURL
		}
		shareURL, _ := primaryShareURL(state)
		return shareURL
	case "phoneURL", "phoneURLMobile":
		if phoneURL, ok := state["phoneURL"].(string); ok {
			return phoneURL
		}
	case "localImageURL":
		if localURL, ok := state["localImageURL"].(string); ok {
			return localURL
		}
	case "publicImageURL":
		if publicURL, ok := state["publicImageURL"].(string); ok {
			return publicURL
		}
	case "videoURL":
		if videoURL, ok := state["videoURL"].(string); ok {
			return videoURL
		}
	case "hlsURL":
		if hlsURL, ok := state["hlsURL"].(string); ok {
			return hlsURL
		}
	case "publicVideoURL":
		if publicURL, ok := state["publicVideoURL"].(string); ok {
			return publicURL
		}
	case "publicHLSURL":
		if publicURL, ok := state["publicHLSURL"].(string); ok {
			return publicURL
		}
	case "obsServerAddress":
		if obs, ok := state["obs"].(obsrtmp.Status); ok {
			return obs.ServerAddress
		}
	case "obsStreamKey":
		if obs, ok := state["obs"].(obsrtmp.Status); ok {
			return obs.StreamKey
		}
	default:
		if imageURL, ok := state["imageURL"].(string); ok && strings.HasPrefix(imageURL, "http") {
			return imageURL
		}
		if publicURL, ok := state["publicImageURL"].(string); ok && publicURL != "" {
			return publicURL
		}
		if localURL, ok := state["localImageURL"].(string); ok {
			return localURL
		}
	}
	return ""
}
