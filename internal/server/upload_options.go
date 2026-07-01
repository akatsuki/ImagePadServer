package server

import (
	"fmt"
	"strconv"
	"strings"

	"imagepadserver/internal/imageproc"
)

func optionsFromValues(value func(string) string) imageproc.Options {
	opts := imageproc.DefaultOptions()
	if v := value("format"); v != "" {
		opts.Format = v
	}
	if v := value("quality"); v != "" {
		preset := strings.ToLower(strings.TrimSpace(v))
		if q, ok := qualityPresetToJPEG[preset]; ok {
			opts.JPEGQuality = q
		} else if q, err := strconv.Atoi(v); err == nil {
			opts.JPEGQuality = q
		}
		if q, ok := qualityPresetToWebP[preset]; ok {
			opts.WebPQuality = q
		} else if q, err := strconv.Atoi(v); err == nil {
			opts.WebPQuality = q
		}
		if _, ok := qualityPresetToPNGRange[preset]; ok || preset == "lossless" {
			opts.PNGQuality = preset
		}
	}
	if v := value("maxDimension"); v != "" {
		if maxDim, err := strconv.Atoi(v); err == nil {
			opts.MaxDimension = maxDim
		}
	}
	if v := value("maxMB"); v != "" {
		if maxMB, err := strconv.Atoi(v); err == nil && maxMB > 0 {
			if maxMB > 120 {
				maxMB = 120
			}
			opts.MaxBytes = int64(maxMB) << 20
		}
	}
	return opts
}

var qualityPresetToJPEG = map[string]int{
	"highest": 95,
	"high":    85,
	"medium":  75,
	"low":     60,
	"lowest":  45,
}

var qualityPresetToWebP = map[string]int{
	"highest": 90,
	"high":    80,
	"medium":  70,
	"low":     55,
	"lowest":  40,
}

var qualityPresetToPNGRange = map[string][2]int{
	"highest": {90, 100},
	"high":    {75, 90},
	"medium":  {60, 75},
	"low":     {45, 60},
	"lowest":  {30, 45},
}

func uploadMemoryLimit() int64 {
	return maxMultipartMemory
}

func videoURLDownloadError(err error) string {
	if err == nil {
		return "動画URLの取得に失敗しました"
	}
	return fmt.Sprintf(
		"動画URLの取得に失敗しました: %v。yt-dlp で取得できないURLの場合は動画ファイルを直接アップロードするか、ビデオプレーヤーモードをオフにして画像URLとして指定してください。",
		err,
	)
}
