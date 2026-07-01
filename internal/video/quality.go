package video

import (
	"strconv"
	"strings"
)

func ResolveQuality(mode string, networkMbps int) QualityPreset {
	return ResolveQualityForUpload(mode, networkMbps, 0)
}

func ResolveQualityForUpload(mode string, downloadMbps, uploadMbps int) QualityPreset {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	decisionMbps := uploadMbps
	if decisionMbps <= 0 {
		decisionMbps = downloadMbps
	}
	effective := mode
	if mode == "auto" {
		switch {
		case decisionMbps >= 12:
			effective = "1080"
		case decisionMbps >= 5:
			effective = "720"
		default:
			effective = "360"
		}
	}
	preset := QualityPreset{
		Mode:        mode,
		Effective:   effective,
		Height:      720,
		NetworkMbps: downloadMbps,
		UploadMbps:  uploadMbps,
	}
	switch effective {
	// CRF is +2 vs the older presets: the encoder efficiency upgrades (NVENC p6
	// + AQ + B-frames, AMF quality + VBAQ, libx264 medium) buy back the quality,
	// so the higher CRF lands at a similar look for a smaller file. CRF is not
	// used by the OBS low-latency path, so live streaming quality is unchanged.
	case "1080":
		preset.Height = 1080
		preset.VideoBitrate = "4500k"
		preset.MaxRate = "5200k"
		preset.BufferSize = "9000k"
		preset.AudioBitrate = "160k"
		preset.CRF = 26
	case "360":
		preset.Height = 360
		preset.VideoBitrate = "900k"
		preset.MaxRate = "1100k"
		preset.BufferSize = "1800k"
		preset.AudioBitrate = "96k"
		preset.CRF = 32
	default:
		preset.Effective = "720"
		preset.Height = 720
		preset.VideoBitrate = "2500k"
		preset.MaxRate = "3000k"
		preset.BufferSize = "5000k"
		preset.AudioBitrate = "128k"
		preset.CRF = 29
	}
	return preset
}

// ResolveQualityForMusic returns a preset tuned for the music visualizer path
// (SoundCloud / uploaded audio). The output is mostly a static background plus
// a small animated waveform, so it compresses far better than camera/game
// footage. CRF is raised and the bitrate ceiling is lowered to keep songs
// small, but we avoid pushing it so hard that the waveform area becomes
// blocky.
func ResolveQualityForMusic(mode string, downloadMbps, uploadMbps int) QualityPreset {
	preset := ResolveQualityForUpload(mode, downloadMbps, uploadMbps)
	preset.CRF = clampInt(preset.CRF+2, 18, 40)
	// Cap peaks at ~30% of the uploaded-video ceiling. Buffer is 40% so short
	// waveform spikes do not stutter the rate controller. Spatial AQ is disabled
	// in the NVENC static-content path so the moving waveform is not starved of
	// bits in favor of the flat background.
	if preset.MaxRate != "" {
		preset.MaxRate = scaleBitrate(preset.MaxRate, 0.30)
	}
	if preset.BufferSize != "" {
		preset.BufferSize = scaleBitrate(preset.BufferSize, 0.40)
	}
	return preset
}

// scaleBitrate multiplies a bitrate string like "3000k" by factor, preserving
// the unit suffix. Empty or unparseable values are returned unchanged.
func scaleBitrate(s string, factor float64) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	unit := ""
	num := s
	if last := s[len(s)-1]; last < '0' || last > '9' {
		unit = s[len(s)-1:]
		num = s[:len(s)-1]
	}
	v, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil {
		return s
	}
	return strconv.Itoa(int(float64(v)*factor)) + unit
}

func BitrateOnlyPreset(requested, active QualityPreset) QualityPreset {
	if active.Height <= 0 || requested.Height <= 0 {
		return requested
	}
	requested.Height = active.Height
	requested.Effective = active.Effective
	requested.BitrateOnly = true
	return requested
}
