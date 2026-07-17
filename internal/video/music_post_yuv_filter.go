package video

import "fmt"

// buildMusicPostYUVFilter defines the canonical post-YUV graph ordering used
// by music rendering diagnostics: showwaves, overlay, then ASS text.
func buildMusicPostYUVFilter(waveW, waveH, waveX, waveY int, waveColor, assPath, fontDir string) string {
	return fmt.Sprintf("[1:a]showwaves=s=%dx%d:rate=30:mode=line:colors=%s[wave];[0:v][wave]overlay=%d:%d[vid];[vid]ass=filename='%s':fontsdir='%s'[out]", waveW, waveH, waveColor, waveX, waveY, escapeFilterPath(assPath), escapeFilterPath(fontDir))
}
