package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"imagepadserver/internal/about"
)

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/akatsuki/ImagePadServer/releases/latest", nil)
	if err != nil {
		http.Error(w, "failed to create update request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", about.AppName+"/"+about.Version)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeJSON(w, map[string]interface{}{
			"ok":      false,
			"current": about.Version,
			"message": "更新確認に失敗しました",
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, map[string]interface{}{
			"ok":      false,
			"current": about.Version,
			"message": fmt.Sprintf("更新確認に失敗しました: HTTP %d", resp.StatusCode),
		})
		return
	}
	var latest struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&latest); err != nil {
		writeJSON(w, map[string]interface{}{
			"ok":      false,
			"current": about.Version,
			"message": "更新情報を読めませんでした",
		})
		return
	}
	newer := versionGreater(latest.TagName, about.Version)
	message := "最新版です"
	if newer {
		message = "新しいバージョンがあります"
	}
	writeJSON(w, map[string]interface{}{
		"ok":      true,
		"current": about.Version,
		"latest":  latest.TagName,
		"name":    latest.Name,
		"url":     latest.HTMLURL,
		"newer":   newer,
		"message": message,
	})
}
