package server

import (
	"os"
	"strings"
	"testing"
)

// TestInactiveVideoPlaceholderGeneratesMP4 は動画プレースホルダが有効な MP4 を
// 生成することを検証する。IMAGEPAD_FFMPEG 未設定の環境ではスキップする。
func TestInactiveVideoPlaceholderGeneratesMP4(t *testing.T) {
	if strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG")) == "" {
		t.Skip("IMAGEPAD_FFMPEG not set")
	}
	mp4 := inactiveVideoBytes()
	if len(mp4) == 0 {
		t.Fatal("inactiveVideoBytes returned empty; ffmpeg should be available")
	}
	// MP4 シグネチャ: オフセット4 に "ftyp"
	if len(mp4) < 8 || string(mp4[4:8]) != "ftyp" {
		n := len(mp4)
		if n > 16 {
			n = 16
		}
		t.Fatalf("not a valid MP4 (missing ftyp): first %d bytes = % x", n, mp4[:n])
	}
}
