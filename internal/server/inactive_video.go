package server

import (
	"bytes"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"imagepadserver/internal/video"
)

var (
	inactiveVideoOnce sync.Once
	inactiveVideoMP4  []byte
)

// inactiveVideoBytes は "ERROR INACTIVE ADDRESS" を焼き込んだ動画プレースホルダ
// （MP4・静止画ループ）を遅延生成して返す。ffmpeg が無い等で生成できない場合は
// nil を返し、呼び出し側は画像プレースホルダへフォールバックする。
//
// 生成は非ダウンロード解決（IMAGEPAD_FFMPEG → PATH）で行い、プラグホルダ取得の
// ために 100MB 級の ffmpeg ダウンロードを誘発しない。
func inactiveVideoBytes() []byte {
	inactiveVideoOnce.Do(func() {
		ffmpeg := strings.TrimSpace(os.Getenv("IMAGEPAD_FFMPEG"))
		if ffmpeg == "" {
			if p, err := exec.LookPath("ffmpeg"); err == nil {
				ffmpeg = p
			}
		}
		if ffmpeg == "" {
			return
		}

		var pngBuf bytes.Buffer
		if err := png.Encode(&pngBuf, inactiveImage()); err != nil {
			return
		}
		dir, err := os.MkdirTemp("", "imagepad-inactive-video")
		if err != nil {
			return
		}
		defer os.RemoveAll(dir)

		pngPath := filepath.Join(dir, "inactive.png")
		mp4Path := filepath.Join(dir, "inactive.mp4")
		if err := os.WriteFile(pngPath, pngBuf.Bytes(), 0600); err != nil {
			return
		}

		cmd := exec.Command(ffmpeg,
			"-y", "-loop", "1", "-i", pngPath,
			"-t", "3", "-r", "30",
			"-c:v", "libx264", "-pix_fmt", "yuv420p",
			"-an", mp4Path,
		)
		if _, err := video.CombinedOutputTrackedFFmpeg(cmd); err != nil {
			return
		}
		data, err := os.ReadFile(mp4Path)
		if err != nil {
			return
		}
		inactiveVideoMP4 = data
	})
	return inactiveVideoMP4
}
