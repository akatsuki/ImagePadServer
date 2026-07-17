package video

import (
	"crypto/sha256"
	"fmt"
)

// buildMusicPostYUVFilter defines the canonical post-YUV graph ordering used
// by music rendering diagnostics: showwaves, overlay, then ASS text.
func buildMusicPostYUVFilter(waveW, waveH, waveX, waveY int, waveColor, audioFilter, assPath, fontDir string) string {
	return fmt.Sprintf("[1:a]%s[wsrc];[wsrc]showwaves=s=%dx%d:rate=30:mode=line:colors=%s[wave];[0:v][wave]overlay=%d:%d[vid];[vid]ass=filename='%s':fontsdir='%s'[out]", audioFilter, waveW, waveH, waveColor, waveX, waveY, escapeFilterPath(assPath), escapeFilterPath(fontDir))
}

func musicPostYUVFilterSHA256(graph string) string {
	sum := sha256.Sum256([]byte(graph))
	return fmt.Sprintf("%x", sum[:])
}
