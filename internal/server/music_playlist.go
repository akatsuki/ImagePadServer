package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"imagepadserver/internal/obsrtmp"
	"imagepadserver/internal/playlist"
	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

// Test seams: the prepare pipeline shells out to ffmpeg twice (analysis and
// render); handler tests substitute fakes.
var (
	analyzeAudioForKind = video.AnalyzeAudioForKind
	renderRadioTrack    = video.RenderRadioTrack
)

// initMusicPlaylist wires the playlist queue, the persistence store, the
// radio manager, and the single background worker that serializes downloads
// and renders so CPU/network heavy jobs never run concurrently.
func (s *Server) initMusicPlaylist(host string) {
	s.musicQueue = playlist.NewQueue()
	s.playlistStore = playlist.NewStore(filepath.Join(s.store.Dir(), "playlists.json"))
	s.radio = obsrtmp.NewRadioManager(s.store.Dir(), host, s.nextRadioTrack, obsrtmp.RadioCallbacks{
		OnTrackStart: func(string) { s.broadcastStateChanged() },
		OnTrackEnd:   s.onRadioTrackEnd,
		OnIdle:       func() { s.broadcastStateChangedThrottled() },
		OnStopped:    func() { s.broadcastStateChangedThrottled() },
	})
	s.radio.SetFillerSource(func() (string, error) {
		ffmpeg, err := ensureFFmpeg()
		if err != nil {
			return "", err
		}
		return video.RenderRadioFiller(context.Background(), s.store.Dir(), ffmpeg, s.musicRadioPreset())
	})
	s.musicJobs = make(chan func(), 64)
	go func() {
		for job := range s.musicJobs {
			job()
		}
	}()
}

// musicRadioPreset is the dedicated lower-bitrate setup for playlist radio
// renders and the filler clip.
func (s *Server) musicRadioPreset() video.QualityPreset {
	appSettings, err := settings.Load()
	if err != nil {
		return video.MusicRadioQualityPreset("auto", 0, 0)
	}
	return video.MusicRadioQualityPreset(appSettings.VideoQualityMode, appSettings.NetworkMbps, appSettings.NetworkUploadMbps)
}

func (s *Server) enqueueMusicJob(job func()) bool {
	select {
	case s.musicJobs <- job:
		return true
	default:
		return false
	}
}

// nextRadioTrack is the radio's track source: while paused it starves the
// radio into idle (MediaMTX stays up so the share URLs survive); a pending
// interrupt (今すぐ再生 / 再開) wins, otherwise the queue advances by its own
// shuffle/loop policy.
func (s *Server) nextRadioTrack() (string, string, int, bool) {
	s.musicPendingMu.Lock()
	paused := s.musicPaused
	pending := s.musicPendingTrack
	offset := s.musicPendingOffset
	s.musicPendingTrack = ""
	s.musicPendingOffset = 0
	s.musicPendingMu.Unlock()
	if paused {
		return "", "", 0, false
	}
	if pending != "" {
		if tr, ok := s.musicQueue.Get(pending); ok && tr.Status == playlist.TrackReady {
			s.musicQueue.SetCurrent(pending)
			return tr.MediaPath, tr.ID, offset, true
		}
	}
	tr, ok := s.musicQueue.Next()
	if !ok {
		return "", "", 0, false
	}
	return tr.MediaPath, tr.ID, 0, true
}

func (s *Server) onRadioTrackEnd(trackID string, err error) {
	if err != nil {
		s.musicQueue.MarkFailed(trackID, "配信エラー: "+err.Error())
	}
	s.broadcastStateChangedThrottled()
}

func (s *Server) setPendingRadioTrack(id string, offset int) {
	s.musicPendingMu.Lock()
	s.musicPendingTrack = id
	s.musicPendingOffset = offset
	s.musicPaused = false
	s.musicPendingMu.Unlock()
}

func (s *Server) clearMusicPause() {
	s.musicPendingMu.Lock()
	s.musicPaused = false
	s.musicPausedTrack = ""
	s.musicPausedOffset = 0
	s.musicPendingMu.Unlock()
}

func (s *Server) clearMusicPlaybackRequest() {
	s.musicPendingMu.Lock()
	s.musicPaused = false
	s.musicPausedTrack = ""
	s.musicPausedOffset = 0
	s.musicPendingTrack = ""
	s.musicPendingOffset = 0
	s.musicPendingMu.Unlock()
}

// --- handlers -------------------------------------------------------------

func (s *Server) handleMusicPlaylist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) musicPlaylistState() map[string]interface{} {
	tracks := s.musicQueue.Snapshot()
	items := make([]map[string]interface{}, 0, len(tracks))
	for _, t := range tracks {
		items = append(items, map[string]interface{}{
			"id":              t.ID,
			"title":           t.Title,
			"artist":          t.Artist,
			"album":           t.Album,
			"durationSeconds": t.DurationSeconds,
			"status":          string(t.Status),
			"error":           t.Error,
			"sourceKind":      t.SourceKind,
			"originalName":    t.OriginalName,
			"hasArtwork":      t.ThumbnailPath != "",
		})
	}
	st := s.radio.Status()
	s.musicPendingMu.Lock()
	paused := s.musicPaused
	pausedTrack := s.musicPausedTrack
	pausedOffset := s.musicPausedOffset
	s.musicPendingMu.Unlock()
	s.mu.RLock()
	tunnelBase := s.tunnelURLBase
	s.mu.RUnlock()
	base := s.imageURLBase
	if tunnelBase != "" {
		base = tunnelBase
	}
	hlsURL, publicHLSURL := "", ""
	if st.Running {
		hlsURL = base + "radio/index.m3u8"
		if tunnelBase != "" {
			publicHLSURL = tunnelBase + "radio/index.m3u8"
		}
	}
	elapsed := 0
	if !st.TrackStartedAt.IsZero() {
		elapsed = st.BaseOffsetSeconds + int(time.Since(st.TrackStartedAt).Seconds())
	}
	currentID := st.CurrentTrackID
	if paused {
		currentID = pausedTrack
		elapsed = pausedOffset
	}
	return map[string]interface{}{
		"tracks":         items,
		"currentTrackId": currentID,
		"running":        st.Running,
		"playing":        st.Running && st.CurrentTrackID != "",
		"paused":         paused,
		"shuffle":        s.musicQueue.Shuffle(),
		"loop":           s.musicQueue.Loop(),
		"rtspUrl":        st.RTSPURL,
		"hlsUrl":         hlsURL,
		"publicHlsUrl":   publicHLSURL,
		"elapsedSeconds": elapsed,
	}
}

// handleMusicPlaylistAdd implements the unified input: a multipart file, an
// http(s) URL, or a local file path all land in the same queue. The response
// returns immediately; download and render happen on the music worker.
func (s *Server) handleMusicPlaylistAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.musicModeEnabled() {
		http.Error(w, "ミュージックモードが無効です", http.StatusConflict)
		return
	}

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "ファイルを読み取れませんでした", http.StatusBadRequest)
			return
		}
		defer file.Close()
		acquired, err := s.acquireUploadedAudio(r.Context(), file, header.Filename)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		track := s.musicQueue.Add(playlist.Track{
			Title:        header.Filename,
			OriginalName: header.Filename,
			SourceKind:   string(acquired.Kind),
		})
		if !s.enqueueMusicJob(func() { s.prepareRadioTrack(track.ID, acquired) }) {
			os.Remove(acquired.SourcePath)
			s.musicQueue.MarkFailed(track.ID, "処理キューが混雑しています")
		}
		writeJSON(w, s.musicPlaylistState())
		return
	}

	var req struct {
		Input string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		http.Error(w, "URL またはファイルパスを入力してください", http.StatusBadRequest)
		return
	}

	if strings.HasPrefix(strings.ToLower(input), "http://") || strings.HasPrefix(strings.ToLower(input), "https://") {
		if err := validateHTTPURL(input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		track := s.musicQueue.Add(playlist.Track{
			Title:        input,
			OriginalName: input,
			SourceKind:   string(video.SourceMusic),
		})
		if !s.enqueueMusicJob(func() {
			acquired, err := musicURLAcquirer(context.Background(), s, input)
			if err != nil {
				s.musicQueue.MarkFailed(track.ID, videoURLDownloadError(err))
				s.broadcastStateChangedThrottled()
				return
			}
			s.prepareRadioTrack(track.ID, acquired)
		}) {
			s.musicQueue.MarkFailed(track.ID, "処理キューが混雑しています")
		}
		writeJSON(w, s.musicPlaylistState())
		return
	}

	// Anything else is treated as a local file path on the host machine.
	localPath := filepath.Clean(input)
	if stat, err := os.Stat(localPath); err != nil || stat.IsDir() {
		http.Error(w, "ファイルが見つかりません: "+input, http.StatusBadRequest)
		return
	}
	track := s.musicQueue.Add(playlist.Track{
		Title:        filepath.Base(localPath),
		OriginalName: filepath.Base(localPath),
		SourceKind:   string(video.SourceLocalAudio),
	})
	if !s.enqueueMusicJob(func() {
		f, err := os.Open(localPath)
		if err != nil {
			s.musicQueue.MarkFailed(track.ID, "ファイルを開けませんでした: "+err.Error())
			s.broadcastStateChangedThrottled()
			return
		}
		defer f.Close()
		acquired, err := s.acquireUploadedAudio(context.Background(), f, filepath.Base(localPath))
		if err != nil {
			s.musicQueue.MarkFailed(track.ID, err.Error())
			s.broadcastStateChangedThrottled()
			return
		}
		s.prepareRadioTrack(track.ID, acquired)
	}) {
		s.musicQueue.MarkFailed(track.ID, "処理キューが混雑しています")
	}
	writeJSON(w, s.musicPlaylistState())
}

// prepareRadioTrack turns acquired audio into a ready playlist track:
// metadata, artwork thumbnail, loudness analysis, and the pre-rendered TS.
// The audio source file is deleted afterwards; only the TS is kept.
func (s *Server) prepareRadioTrack(trackID string, acquired video.AcquiredAudio) {
	ctx := context.Background()
	thumbnailPath := ""
	fail := func(err error) {
		os.Remove(acquired.SourcePath)
		if thumbnailPath != "" {
			os.Remove(thumbnailPath)
		}
		s.musicQueue.MarkFailed(trackID, err.Error())
		s.broadcastStateChangedThrottled()
	}
	ffmpeg, err := ensureFFmpeg()
	if err != nil {
		fail(err)
		return
	}
	meta := video.ResolveAudioMetadata(acquired.Kind, acquired.SourceName, acquired.EmbeddedMetadata, acquired.SoundCloudMetadata)
	artworkPath, err := video.SelectArtwork(acquired.EmbeddedArtwork, acquired.SoundCloudArtworkPath, acquired.Kind)
	if err != nil {
		artworkPath = ""
	}
	thumbnail := ""
	if artworkPath != "" {
		thumbnail = s.createVideoThumbnail(artworkPath)
	}
	if thumbnail != "" {
		thumbnailPath = filepath.Join(s.store.Dir(), thumbnail)
	}
	s.musicQueue.Mutate(trackID, func(t *playlist.Track) {
		if meta.Title != "" {
			t.Title = meta.Title
		}
		t.Artist = meta.Artist
		t.Album = meta.Album
		t.SourceKind = string(acquired.Kind)
		if t.OriginalName == "" {
			t.OriginalName = acquired.SourceName
		}
		if thumbnailPath != "" {
			t.ThumbnailPath = thumbnailPath
		}
	})
	s.broadcastStateChangedThrottled()

	analysis, err := analyzeAudioForKind(ctx, ffmpeg, acquired.SourcePath, acquired.Kind)
	if err != nil {
		fail(fmt.Errorf("音声解析に失敗しました: %w", err))
		return
	}
	usedArtwork := artworkPath
	if thumbnailPath != "" {
		usedArtwork = thumbnailPath
	}
	input := video.AudioRenderInput{
		SourcePath:  acquired.SourcePath,
		Kind:        acquired.Kind,
		Metadata:    meta,
		ArtworkPath: usedArtwork,
		Analysis:    analysis,
	}
	mediaPath, err := renderRadioTrack(ctx, s.store.Dir(), ffmpeg, input, trackID, s.musicRadioPreset())
	if err != nil {
		fail(err)
		return
	}
	os.Remove(acquired.SourcePath)
	if !s.musicQueue.MarkReady(trackID, mediaPath, int(analysis.Duration+0.5)) {
		os.Remove(mediaPath)
		if thumbnailPath != "" {
			os.Remove(thumbnailPath)
		}
		s.broadcastStateChangedThrottled()
		return
	}
	// A running-but-idle radio starts playing the new track right away.
	s.radio.Wake()
	s.broadcastStateChanged()
}

func (s *Server) handleMusicPlaylistRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	track, ok := s.musicQueue.Get(req.ID)
	if !ok {
		http.Error(w, "曲が見つかりません", http.StatusNotFound)
		return
	}
	wasCurrent := s.radio.Status().CurrentTrackID == req.ID
	s.musicQueue.Remove(req.ID)
	if wasCurrent {
		s.radio.SkipCurrent()
	}
	if track.MediaPath != "" && !s.mediaReferencedBySavedPlaylists(track.MediaPath) {
		os.Remove(track.MediaPath)
	}
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

// mediaReferencedBySavedPlaylists prevents deleting a rendered TS that a
// saved playlist still points at.
func (s *Server) mediaReferencedBySavedPlaylists(mediaPath string) bool {
	names, err := s.playlistStore.List()
	if err != nil {
		return true // be conservative
	}
	for _, name := range names {
		tracks, err := s.playlistStore.Load(name)
		if err != nil {
			continue
		}
		for _, t := range tracks {
			if filepath.Clean(t.MediaPath) == filepath.Clean(mediaPath) {
				return true
			}
		}
	}
	return false
}

func (s *Server) handleMusicPlaylistReorder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	if !s.musicQueue.SetOrder(req.IDs) {
		http.Error(w, "曲順が現在のプレイリストと一致しません", http.StatusConflict)
		return
	}
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) handleMusicPlaylistPlay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	if !s.musicModeEnabled() {
		http.Error(w, "ミュージックモードが無効です", http.StatusConflict)
		return
	}
	if req.ID != "" {
		track, ok := s.musicQueue.Get(req.ID)
		if !ok {
			http.Error(w, "曲が見つかりません", http.StatusNotFound)
			return
		}
		if track.Status != playlist.TrackReady {
			http.Error(w, "この曲はまだ準備中です", http.StatusConflict)
			return
		}
		s.setPendingRadioTrack(req.ID, 0)
		s.clearMusicPause()
	} else {
		// ID なしの再生は、一時停止中なら中断位置からの再開になる。
		s.musicPendingMu.Lock()
		if s.musicPaused && s.musicPausedTrack != "" {
			s.musicPendingTrack = s.musicPausedTrack
			s.musicPendingOffset = s.musicPausedOffset
		}
		s.musicPaused = false
		s.musicPausedTrack = ""
		s.musicPausedOffset = 0
		s.musicPendingMu.Unlock()
	}
	if !s.radio.Running() {
		if err := s.radio.Start(); err != nil {
			http.Error(w, "ラジオ配信を開始できませんでした: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else if req.ID != "" {
		s.radio.SkipCurrent()
	} else {
		s.radio.Wake()
	}
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

// handleMusicPlaylistPause suspends playback while keeping MediaMTX (and the
// share URLs) alive; the paused position is replayed on resume via -ss.
func (s *Server) handleMusicPlaylistPause(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	st := s.radio.Status()
	if !st.Running || st.CurrentTrackID == "" {
		http.Error(w, "再生していません", http.StatusConflict)
		return
	}
	offset := st.BaseOffsetSeconds
	if !st.TrackStartedAt.IsZero() {
		offset += int(time.Since(st.TrackStartedAt).Seconds())
	}
	s.musicPendingMu.Lock()
	s.musicPaused = true
	s.musicPausedTrack = st.CurrentTrackID
	s.musicPausedOffset = offset
	s.musicPendingMu.Unlock()
	s.radio.SkipCurrent()
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) handleMusicPlaylistNext(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.radio.Running() {
		http.Error(w, "再生していません", http.StatusConflict)
		return
	}
	s.clearMusicPause()
	s.radio.SkipCurrent()
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) handleMusicPlaylistStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.clearMusicPause()
	s.radio.Stop(8 * time.Second)
	s.musicQueue.ClearCurrent()
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) handleMusicPlaylistOptions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Shuffle *bool `json:"shuffle"`
		Loop    *bool `json:"loop"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	if req.Shuffle != nil {
		s.musicQueue.SetShuffle(*req.Shuffle)
	}
	if req.Loop != nil {
		s.musicQueue.SetLoop(*req.Loop)
	}
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) handleMusicPlaylistArtwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	track, ok := s.musicQueue.Get(r.URL.Query().Get("id"))
	if !ok || track.ThumbnailPath == "" {
		http.NotFound(w, r)
		return
	}
	// Thumbnails always live directly inside the store directory.
	if filepath.Dir(filepath.Clean(track.ThumbnailPath)) != filepath.Clean(s.store.Dir()) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, track.ThumbnailPath)
}

// --- saved playlists -------------------------------------------------------

func (s *Server) handleMusicPlaylists(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		names, err := s.playlistStore.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{"names": names})
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := s.playlistStore.Save(req.Name, s.musicQueue.Snapshot()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		names, _ := s.playlistStore.List()
		writeJSON(w, map[string]interface{}{"names": names})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMusicPlaylistsLoad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	tracks, err := s.playlistStore.Load(req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.clearMusicPlaybackRequest()
	s.musicQueue.ReplaceAll(tracks)
	if s.radio.Running() {
		s.radio.SkipCurrent()
	}
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) handleMusicPlaylistsDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	if err := s.playlistStore.Delete(req.Name); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	names, _ := s.playlistStore.List()
	writeJSON(w, map[string]interface{}{"names": names})
}

// --- public LL-HLS proxy ----------------------------------------------------

// handleRadioHLS serves the playlist radio LL-HLS stream on a fixed public
// path (/radio/index.m3u8) while the radio is running. Like the other media
// endpoints it is intentionally unauthenticated so VRChat players can fetch it.
func (s *Server) handleRadioHLS(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/radio/")
	if strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	if !s.radio.ProxyLLHLS(w, r, name) {
		http.NotFound(w, r)
	}
}

func decodePlaylistPost(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}
