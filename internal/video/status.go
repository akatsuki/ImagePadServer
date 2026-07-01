package video

import (
	"fmt"
	"path/filepath"
	"strconv"
)

func CurrentStatus(outDir string) Result {
	return CurrentStatusForID(outDir, "")
}

func CurrentStatusForID(outDir, id string) Result {
	mp4 := fileExists(filepath.Join(outDir, MP4File))
	hls := hlsPlaylistExistsForID(outDir, id) && hlsSegmentExistsForID(outDir, id)
	active := isActiveForID(outDir, id)
	pending := isPendingForID(outDir, id)
	result := Result{
		OK:     mp4 || hls,
		MP4:    mp4,
		HLS:    hls,
		Active: active || pending,
	}
	if active && hls {
		applyProgress(outDir, &result)
		result.Message = "HLS conversion is streaming."
		return result
	}
	if active {
		applyProgress(outDir, &result)
		result.Message = "HLS conversion is starting."
		return result
	}
	if pending {
		result.Message = "HLS conversion is waiting."
		return result
	}
	if result.OK {
		result.Message = "VRChat video outputs are available."
		return result
	}
	if _, err := ffmpegPath(); err != nil {
		result.Message = "FFmpeg not found. Turn on video player support to download it, set IMAGEPAD_FFMPEG, or add ffmpeg to PATH."
		return result
	}
	result.Message = "VRChat video outputs have not been generated yet."
	return result
}

func applyProgress(outDir string, result *Result) {
	result.ProgressText = "変換中"
	active, ok := activeHLS.Load(outDir)
	if !ok {
		return
	}
	job, ok := active.(*activeJob)
	if !ok || job.TotalSeconds <= 0 {
		count := hlsSegmentCount(outDir)
		if job != nil && job.QueueJob != nil {
			count = hlsSegmentCountForID(outDir, job.QueueJob.MediaID)
		}
		if count > 0 {
			result.ProgressText = strconv.Itoa(count) + " segments generated"
		}
		return
	}
	id := ""
	if job.QueueJob != nil {
		id = job.QueueJob.MediaID
	}
	percent, seconds, ok := hlsProgress(outDir, id, job.TotalSeconds)
	if !ok {
		return
	}
	result.ProgressPercent = percent
	result.ProgressText = fmt.Sprintf("%d%% (%d / %d sec)", percent, seconds, job.TotalSeconds)
}
