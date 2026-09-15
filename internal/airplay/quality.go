package airplay

import (
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

func NormalizeAirPlayQualityMode(mode string) string {
	return settings.NormalizeAirPlayQualityMode(mode)
}

func ResolveAirPlayQuality(mode string, downloadMbps, uploadMbps int) video.QualityPreset {
	return video.ResolveQualityForUpload(NormalizeAirPlayQualityMode(mode), downloadMbps, uploadMbps)
}
