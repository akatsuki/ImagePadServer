package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/settings"
)

func (s *Server) recordImageRequest(r *http.Request) {
	line := fmt.Sprintf("%s\t%s\t%s\t%s\n",
		time.Now().Format(time.RFC3339),
		r.RemoteAddr,
		r.URL.RequestURI(),
		strings.ReplaceAll(r.UserAgent(), "\t", " "),
	)
	go appendAccessLog(line)
}

func appendAccessLog(line string) {
	logPath := filepath.Join(settings.Dir(), "image-access.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return
	}
	file, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line)
}
