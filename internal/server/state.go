package server

import (
	"net/http"
	"net/url"

	"imagepadserver/internal/about"
	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/toolchain"
	"imagepadserver/internal/upnp"
	"imagepadserver/internal/video"
)

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.state(r))
}

func (s *Server) state(r *http.Request) map[string]interface{} {
	s.mu.RLock()
	upnpResult := s.upnp
	tunnelURLBase := s.tunnelURLBase
	tunnelStatus := s.tunnelStatus
	s.mu.RUnlock()
	appSettings := s.appSettings()

	localImageURL := ""
	imageURLBase := s.imageURLBase
	if tunnelURLBase != "" {
		imageURLBase = tunnelURLBase
	}
	imageURL := ""
	videoURL := ""
	hlsURL := ""
	previewImageURL := ""
	publicImageURL := ""
	publicVideoURL := ""
	publicHLSURL := ""
	if current := s.store.Current(); current != nil {
		videoPlayer := s.videoPlayerStateForIDFromSettings(current.ID, appSettings)
		if current.Kind != "video" {
			imagePath := imageURLPath(current)
			localImageURL = s.previewURLBase + imagePath + "?v=" + current.ID
			if tunnelURLBase != "" {
				imageURL = imageURLBase + imagePath + "?v=" + current.ID
			}
			previewImageURL = s.previewURLBase + imagePath + "?v=" + current.ID
			if tunnelURLBase != "" {
				publicImageURL = tunnelURLBase + imagePath + "?v=" + current.ID
			}
		}
		videoEnabled, _ := videoPlayer["enabled"].(bool)
		videoStatus, _ := videoPlayer["status"].(video.Result)
		if videoEnabled && tunnelURLBase != "" && current.Kind == "video" && videoStatus.MP4 {
			videoURL = imageURLBase + "video/current.mp4?v=" + current.ID
			publicVideoURL = tunnelURLBase + "video/current.mp4?v=" + current.ID
		}
		if videoEnabled && (videoStatus.HLS || videoStatus.Active) {
			streamPath := hlsURLPath(current.ID)
			hlsURL = imageURLBase + streamPath
			if tunnelURLBase != "" {
				publicHLSURL = tunnelURLBase + streamPath
			}
		}
	} else {
		videoPlayer := s.videoPlayerEmptyStateFromSettings(appSettings)
		shareURL, shareURLLabel := primaryShareURL(map[string]interface{}{
			"imageURL":      imageURL,
			"videoURL":      videoURL,
			"hlsURL":        hlsURL,
			"localImageURL": localImageURL,
			"videoPlayer":   videoPlayer,
		})
		return s.stateWithMedia(r, appSettings, upnpResult, tunnelStatus, videoPlayer, imageURL, videoURL, hlsURL, shareURL, shareURLLabel, publicImageURL, publicVideoURL, publicHLSURL, localImageURL, previewImageURL)
	}
	if imageURL == "" {
		imageURL = ""
	}
	videoPlayer := s.videoPlayerStateForIDFromSettings("", appSettings)
	if current := s.store.Current(); current != nil {
		videoPlayer = s.videoPlayerStateForIDFromSettings(current.ID, appSettings)
	}
	shareURL, shareURLLabel := primaryShareURL(map[string]interface{}{
		"imageURL":      imageURL,
		"videoURL":      videoURL,
		"hlsURL":        hlsURL,
		"localImageURL": localImageURL,
		"videoPlayer":   videoPlayer,
	})

	return map[string]interface{}{
		"appName":         about.AppName,
		"version":         about.Version,
		"author":          about.Author,
		"license":         about.License,
		"copyright":       about.Copyright,
		"openSource":      about.OpenSourceNotices,
		"phoneURL":        s.adminURL(s.lanURL),
		"imageURL":        imageURL,
		"videoURL":        videoURL,
		"hlsURL":          hlsURL,
		"shareURL":        shareURL,
		"shareURLLabel":   shareURLLabel,
		"publicImageURL":  publicImageURL,
		"publicVideoURL":  publicVideoURL,
		"publicHLSURL":    publicHLSURL,
		"localImageURL":   localImageURL,
		"previewImageURL": previewImageURL,
		"qrURL":           s.adminPath("/qr/phone.png"),
		"upnp":            upnpResult,
		"tunnel":          tunnelStatus,
		"video":           videoPlayer["status"],
		"videoPlayer":     videoPlayer,
		"videoQuality":    s.videoQualityStateFromSettings(appSettings),
		"obs":             s.obsState(),
		"pairing":         s.pairingState(),
		"videoQueue":      s.videoQueueState(),
		"ingest":          s.ingestState(),
		"toolInstall":     toolchain.ToolInstallStatus(),
		"current":         s.store.Current(),
		"history":         s.historyState(),
		"remoteAddr":      r.RemoteAddr,
	}
}

func (s *Server) stateWithMedia(r *http.Request, appSettings settings.Settings, upnpResult upnp.Result, tunnelStatus map[string]interface{}, videoPlayer map[string]interface{}, imageURL, videoURL, hlsURL, shareURL, shareURLLabel, publicImageURL, publicVideoURL, publicHLSURL, localImageURL, previewImageURL string) map[string]interface{} {
	return map[string]interface{}{
		"appName":         about.AppName,
		"version":         about.Version,
		"author":          about.Author,
		"license":         about.License,
		"copyright":       about.Copyright,
		"openSource":      about.OpenSourceNotices,
		"phoneURL":        s.adminURL(s.lanURL),
		"imageURL":        imageURL,
		"videoURL":        videoURL,
		"hlsURL":          hlsURL,
		"shareURL":        shareURL,
		"shareURLLabel":   shareURLLabel,
		"publicImageURL":  publicImageURL,
		"publicVideoURL":  publicVideoURL,
		"publicHLSURL":    publicHLSURL,
		"localImageURL":   localImageURL,
		"previewImageURL": previewImageURL,
		"qrURL":           s.adminPath("/qr/phone.png"),
		"upnp":            upnpResult,
		"tunnel":          tunnelStatus,
		"video":           videoPlayer["status"],
		"videoPlayer":     videoPlayer,
		"videoQuality":    s.videoQualityStateFromSettings(appSettings),
		"obs":             s.obsState(),
		"pairing":         s.pairingState(),
		"videoQueue":      s.videoQueueState(),
		"ingest":          s.ingestState(),
		"toolInstall":     toolchain.ToolInstallStatus(),
		"current":         s.store.Current(),
		"history":         s.historyState(),
		"remoteAddr":      r.RemoteAddr,
	}
}

func (s *Server) historyState() []map[string]interface{} {
	items := s.store.History()
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		title := item.OriginalName
		if title == "" {
			title = item.PublicName
		}
		if title == "" {
			title = item.ID
		}
		thumbnailURL := s.adminPath("/history/" + url.PathEscape(item.ID))
		if item.Thumbnail != "" {
			thumbnailURL = s.adminPath("/history/" + url.PathEscape(item.ID) + "/thumbnail")
		}
		result = append(result, map[string]interface{}{
			"id":           item.ID,
			"kind":         item.Kind,
			"title":        title,
			"width":        item.Width,
			"height":       item.Height,
			"sizeBytes":    item.SizeBytes,
			"updatedAt":    item.UpdatedAt,
			"favorite":     item.Favorite,
			"persistent":   item.Persistent,
			"thumbnailURL": thumbnailURL,
			"hasThumbnail": item.Thumbnail != "",
		})
	}
	return result
}

func (s *Server) videoQueueState() []map[string]interface{} {
	queueItems := video.QueueStatus(s.store.Dir())
	historyItems := s.store.History()
	thumbnails := map[string]string{}
	for _, item := range historyItems {
		if item.Thumbnail != "" {
			thumbnails[item.ID] = s.adminPath("/history/" + url.PathEscape(item.ID) + "/thumbnail")
		}
	}
	result := make([]map[string]interface{}, 0, len(queueItems))
	for _, item := range queueItems {
		result = append(result, map[string]interface{}{
			"id":              item.ID,
			"mediaID":         item.MediaID,
			"title":           item.Title,
			"kind":            item.Kind,
			"status":          item.Status,
			"message":         item.Message,
			"progressPercent": item.ProgressPercent,
			"progressText":    item.ProgressText,
			"quality":         item.Quality,
			"createdAt":       item.CreatedAt,
			"startedAt":       item.StartedAt,
			"finishedAt":      item.FinishedAt,
			"thumbnailURL":    thumbnails[item.MediaID],
		})
	}
	return result
}

func (s *Server) videoPlayerEnabled() bool {
	appSettings := s.appSettings()
	return appSettings.VideoPlayerEnabled
}

func (s *Server) musicModeEnabled() bool {
	appSettings := s.appSettings()
	return appSettings.VideoPlayerEnabled && appSettings.MusicModeEnabled
}

func (s *Server) videoPlayerState() map[string]interface{} {
	return s.videoPlayerStateForID("")
}

func (s *Server) videoPlayerStateForID(id string) map[string]interface{} {
	return s.videoPlayerStateForIDFromSettings(id, loadSettingsOrDefault())
}

func (s *Server) videoPlayerStateForIDFromSettings(id string, appSettings settings.Settings) map[string]interface{} {
	enabled := appSettings.VideoPlayerEnabled
	status := video.CurrentStatusForID(s.store.Dir(), id)
	if !enabled {
		status = video.Result{Message: "VRChat video player support is disabled."}
	}
	state := map[string]interface{}{
		"enabled":          enabled,
		"musicModeEnabled": enabled && appSettings.MusicModeEnabled,
		"status":           status,
		"quality":          s.videoQualityPresetFromSettings(appSettings),
	}
	if encoder, ok := video.CurrentVideoEncoder(); ok {
		state["encoder"] = encoder
	}
	return state
}

func (s *Server) videoPlayerEmptyState() map[string]interface{} {
	return s.videoPlayerEmptyStateFromSettings(loadSettingsOrDefault())
}

func (s *Server) videoPlayerEmptyStateFromSettings(appSettings settings.Settings) map[string]interface{} {
	enabled := appSettings.VideoPlayerEnabled
	status := video.Result{Message: "VRChat video outputs have not been generated yet."}
	if !enabled {
		status = video.Result{Message: "VRChat video player support is disabled."}
	}
	return map[string]interface{}{
		"enabled":          enabled,
		"musicModeEnabled": enabled && appSettings.MusicModeEnabled,
		"status":           status,
		"quality":          s.videoQualityPresetFromSettings(appSettings),
	}
}

func (s *Server) musicQualityPreset() video.QualityPreset {
	appSettings := s.appSettings()
	preset := video.ResolveQualityForMusic(appSettings.VideoQualityMode, appSettings.NetworkMbps, appSettings.NetworkUploadMbps)
	if active, ok := video.ActiveQuality(s.store.Dir()); ok {
		return video.BitrateOnlyPreset(preset, active)
	}
	return preset
}

func (s *Server) videoQualityPreset() video.QualityPreset {
	return s.videoQualityPresetFromSettings(s.appSettings())
}

func (s *Server) videoQualityPresetFromSettings(appSettings settings.Settings) video.QualityPreset {
	preset := video.ResolveQualityForUpload(appSettings.VideoQualityMode, appSettings.NetworkMbps, appSettings.NetworkUploadMbps)
	if active, ok := video.ActiveQuality(s.store.Dir()); ok {
		return video.BitrateOnlyPreset(preset, active)
	}
	return preset
}

func (s *Server) videoQualityState() map[string]interface{} {
	return s.videoQualityStateFromSettings(loadSettingsOrDefault())
}

func (s *Server) videoQualityStateFromSettings(appSettings settings.Settings) map[string]interface{} {
	preset := s.videoQualityPresetFromSettings(appSettings)
	return map[string]interface{}{
		"mode":       preset.Mode,
		"effective":  preset.Effective,
		"height":     preset.Height,
		"uploadMbps": preset.UploadMbps,
		"preset":     preset,
	}
}

func (s *Server) obsLatencyProfile() obsrtmp.LatencyProfile {
	appSettings := s.appSettings()
	profile := obsrtmp.ResolveLatencyProfile(appSettings.OBSLatencyMode, appSettings.NetworkUploadMbps)
	if appSettings.OBSDVREnabled {
		profile = obsrtmp.EnableDVR(profile)
	}
	return profile
}

func loadSettingsOrDefault() settings.Settings {
	appSettings, err := settings.Load()
	if err != nil {
		return settings.Settings{}
	}
	return appSettings
}

func (s *Server) appSettings() settings.Settings {
	s.settingsMu.RLock()
	if s.settingsCache != nil {
		appSettings := *s.settingsCache
		s.settingsMu.RUnlock()
		return appSettings
	}
	s.settingsMu.RUnlock()

	appSettings := loadSettingsOrDefault()
	s.settingsMu.Lock()
	s.settingsCache = &appSettings
	s.settingsMu.Unlock()
	return appSettings
}

func (s *Server) updateSettings(update func(*settings.Settings) error) error {
	if err := settings.Update(update); err != nil {
		return err
	}
	s.invalidateSettings()
	return nil
}

func (s *Server) invalidateSettings() {
	s.settingsMu.Lock()
	s.settingsCache = nil
	s.settingsMu.Unlock()
}

func (s *Server) obsState() obsrtmp.Status {
	if s.obs == nil {
		return obsrtmp.Status{Message: "OBS RTMP receiver is unavailable."}
	}
	status := s.obs.Status()
	status.Capabilities = obsrtmp.LatencyCapabilities()
	// RTSPT has no browser-playable surface; its copyable rtspt:// URL is carried
	// in status.RTSPTURL instead of a preview URL. Every HLS-family mode (HLS,
	// LHLS, LL-HLS) shares the same /stream entry; the handlers route by the
	// active transport.
	if status.MediaID != "" && obsrtmp.NormalizeLatencyMode(status.Latency.Mode) != obsrtmp.LatencyModeRTSPT {
		status.PreviewURL = s.adminPath("/stream/" + url.PathEscape(status.MediaID) + "/" + video.PlaylistName(status.MediaID))
	}
	return status
}
