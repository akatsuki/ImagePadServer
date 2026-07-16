package video

import (
	"fmt"
	"strings"
)

// ValidateGPUVideoColorMetadata checks the textual ffprobe stream fields that
// the GPU encoder contract requires. Keeping this as a small pure validator
// lets runtime smoke tests and offline fixture checks share the same gate.
func ValidateGPUVideoColorMetadata(probeText string) error {
	required := []string{
		"pix_fmt=yuv420p",
		"color_range=tv",
		"color_space=bt709",
		"color_transfer=bt709",
		"color_primaries=bt709",
	}
	for _, field := range required {
		if !strings.Contains(probeText, field) {
			return fmt.Errorf("GPU video color contract missing %s", field)
		}
	}
	return nil
}
