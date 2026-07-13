package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	analyzeAudioForKind  = video.AnalyzeAudioForKind
	renderRadioTrack     = video.RenderRadioTrack
	observePlaylistAsset = func(ctx context.Context, path string) (video.RadioAssetSpec, error) {
		ffprobe, err := video.EnsureFFprobe()
		if err != nil {
			return video.RadioAssetSpec{}, err
		}
		return video.ObserveRadioAsset(ctx, ffprobe, path)
	}
)

// prepare-phase progress checkpoints (0-100 per track).
const (
	trackProgressQueued   = 5
	trackProgressAcquired = 25
	trackProgressAnalyzed = 40
)

type musicRadioController interface {
	Start() error
	Stop(timeout time.Duration)
	Running() bool
	Status() obsrtmp.RadioStatus
	Wake()
	SkipCurrent()
	CancelGeneration(generation uint64) bool
	CurrentTrackGeneration() obsrtmp.TrackGeneration
	SetFallbackPreset(func() video.QualityPreset)
	SetLatencyProfile(func() obsrtmp.LatencyProfile)
	SetOutputMode(func() obsrtmp.RadioOutputMode)
	SetRTSPURL(sessionID, publicURL, message string) bool
	ProxyLLHLS(http.ResponseWriter, *http.Request, string) bool
}

// initMusicPlaylist wires the playlist queue, the persistence store, the
// radio manager, and the single background worker that serializes downloads
// and renders so CPU/network heavy jobs never run concurrently.
func (s *Server) initMusicPlaylist(host string) {
	s.musicQueue = playlist.NewQueue()
	s.playlistStore = playlist.NewStoreWithOptions(filepath.Join(s.store.Dir(), "playlists.json"), playlist.StoreOptions{
		ActiveRecipe: s.activeMusicRadioRecipe,
		ObserveAsset: func(ctx context.Context, path string) (video.RadioAssetSpec, error) {
			return observePlaylistAsset(ctx, path)
		},
	})
	cleanupAbandonedPlaylistRuntimeDirs(s.playlistRuntimeDir(), time.Now())
	radio := obsrtmp.NewRadioManager(s.store.Dir(), host, s.nextRadioTrack, obsrtmp.RadioCallbacks{
		OnTrackStart:       func(string) { s.broadcastStateChanged() },
		OnTrackEnd:         s.onRadioTrackEnd,
		OnIdle:             func() { s.broadcastStateChangedThrottled() },
		OnRTSPReady:        s.handleRadioRTSPReady,
		OnRTSPDone:         s.handleRadioRTSPDone,
		OnReadinessChanged: s.broadcastStateChangedThrottled,
		OnError:            func(obsrtmp.RadioError) { s.broadcastStateChangedThrottled() },
		OnStopped:          func() { s.broadcastStateChangedThrottled() },
	})
	s.radio = radio
	s.startMusicRadio = s.radio.Start
	s.setRadioRTSPURL = radio.SetRTSPEndpointURL
	s.radio.SetFallbackPreset(s.musicRadioPreset)
	s.radio.SetLatencyProfile(func() obsrtmp.LatencyProfile {
		return obsrtmp.NormalizeLatencyProfile(s.musicPlaylistDeliveryProfile())
	})
	s.radio.SetOutputMode(func() obsrtmp.RadioOutputMode {
		return obsrtmp.RadioOutputModeProgram
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
		preset := video.MusicRadioQualityPreset("auto", 0, 0)
		preset.Height = s.activeMusicPlaylistCanonicalHeight()
		preset.RadioLatency = "rtsp-ultra"
		return preset
	}
	preset := video.MusicRadioQualityPreset(appSettings.VideoQualityMode, appSettings.NetworkMbps, appSettings.NetworkUploadMbps)
	preset.Height = s.activeMusicPlaylistCanonicalHeight()
	profile := normalizeMusicPlaylistDeliveryProfile(appSettings.MusicPlaylistDeliveryProfile)
	if appSettings.MusicPlaylistDeliveryProfile == "" {
		profile = normalizeMusicPlaylistLatencyMode(appSettings.MusicPlaylistLatencyMode)
	}
	preset.RadioLatency = musicPlaylistRTSPLatency(profile)
	return preset
}

func (s *Server) activeMusicRadioRecipe() video.RadioRenderRecipe {
	return video.MusicRadioRenderRecipe(s.musicRadioPreset(), 180)
}

func (s *Server) musicPlaylistDeliveryProfile() string {
	appSettings, err := settings.Load()
	if err != nil {
		return "rtsp-ultra"
	}
	if appSettings.MusicPlaylistDeliveryProfile != "" {
		return normalizeMusicPlaylistDeliveryProfile(appSettings.MusicPlaylistDeliveryProfile)
	}
	return normalizeMusicPlaylistLatencyMode(appSettings.MusicPlaylistLatencyMode)
}

func (s *Server) activeMusicPlaylistCanonicalHeight() int {
	if s.activeCanonicalHeight != 0 {
		return s.activeCanonicalHeight
	}
	return settings.ActiveMusicPlaylistCanonicalHeight()
}

func (s *Server) activeMusicPlaylistDeliveryProfile(status obsrtmp.RadioStatus) string {
	if status.ActiveSession != nil && status.ActiveSession.DeliveryProfile != "" {
		return normalizeMusicPlaylistDeliveryProfile(status.ActiveSession.DeliveryProfile)
	}
	return s.musicPlaylistDeliveryProfile()
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
		err = obsrtmp.SanitizeRadioError(err)
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

func (s *Server) clearMusicPlaybackRequestFor(id string) {
	s.musicPendingMu.Lock()
	if s.musicPausedTrack == id {
		s.musicPaused = false
		s.musicPausedTrack = ""
		s.musicPausedOffset = 0
	}
	if s.musicPendingTrack == id {
		s.musicPendingTrack = ""
		s.musicPendingOffset = 0
	}
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
			"id":                t.ID,
			"title":             t.Title,
			"artist":            t.Artist,
			"album":             t.Album,
			"durationSeconds":   t.DurationSeconds,
			"status":            string(t.Status),
			"progress":          t.Progress,
			"error":             t.Error,
			"sourceKind":        t.SourceKind,
			"originalName":      t.OriginalName,
			"hasArtwork":        t.ThumbnailPath != "",
			"needsRegeneration": t.NeedsRegeneration,
			"incompatible":      t.Incompatible,
		})
	}
	st := s.radio.Status()
	desiredDeliveryProfile := s.musicPlaylistDeliveryProfile()
	activeDeliveryProfile := s.activeMusicPlaylistDeliveryProfile(st)
	desiredCanonicalHeight := 720
	if appSettings, err := settings.Load(); err == nil {
		desiredCanonicalHeight = settings.NormalizeMusicPlaylistCanonicalHeight(appSettings.MusicPlaylistCanonicalHeight)
	}
	activeCanonicalHeight := s.activeMusicPlaylistCanonicalHeight()
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
	if st.Running && st.HLSReady {
		hlsURL = base + "radio/index.m3u8"
		if tunnelBase != "" {
			publicHLSURL = tunnelBase + "radio/index.m3u8"
		}
	}
	rtspURL := ""
	rtspPublic := st.RTSPReady && st.RTSPPublic
	if rtspPublic {
		rtspURL = st.RTSPURL
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
	stoppedAt := interface{}(nil)
	if !st.StoppedAt.IsZero() {
		stoppedAt = st.StoppedAt.Format(time.RFC3339Nano)
	}
	return map[string]interface{}{
		"tracks":                   items,
		"currentTrackId":           currentID,
		"running":                  st.Running,
		"phase":                    st.Phase,
		"lastError":                st.LastError,
		"retryCount":               st.RetryCount,
		"stoppedAt":                stoppedAt,
		"playing":                  st.Running && st.CurrentTrackID != "",
		"paused":                   paused,
		"shuffle":                  s.musicQueue.Shuffle(),
		"loop":                     s.musicQueue.Loop(),
		"latencyMode":              musicPlaylistRTSPLatency(activeDeliveryProfile),
		"deliveryProfile":          activeDeliveryProfile,
		"desiredDeliveryProfile":   desiredDeliveryProfile,
		"activeDeliveryProfile":    activeDeliveryProfile,
		"desiredCanonicalHeight":   desiredCanonicalHeight,
		"activeCanonicalHeight":    activeCanonicalHeight,
		"deliveryRestartRequired":  st.Running && activeDeliveryProfile != desiredDeliveryProfile,
		"canonicalRestartRequired": activeCanonicalHeight != desiredCanonicalHeight,
		"rtspUrl":                  rtspURL,
		"rtspPublic":               rtspPublic,
		"rtspReady":                st.RTSPReady,
		"hlsUrl":                   hlsURL,
		"publicHlsUrl":             publicHLSURL,
		"hlsReady":                 st.HLSReady,
		"elapsedSeconds":           elapsed,
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
			s.musicQueue.SetProgress(track.ID, trackProgressQueued)
			s.broadcastStateChangedThrottled()
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
		s.musicQueue.SetProgress(track.ID, trackProgressQueued)
		s.broadcastStateChangedThrottled()
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
	s.musicQueue.SetProgress(trackID, trackProgressAcquired)
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
	s.musicQueue.SetProgress(trackID, trackProgressAnalyzed)
	s.broadcastStateChangedThrottled()
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
	preset := s.musicRadioPreset()
	executedRecipe := video.MusicRadioRenderRecipe(preset, analysis.Duration)
	mediaPath, err := renderRadioTrack(ctx, s.store.Dir(), ffmpeg, input, trackID, preset, func(fraction float64) {
		pct := trackProgressAnalyzed + int(fraction*float64(99-trackProgressAnalyzed))
		if s.musicQueue.SetProgress(trackID, pct) {
			s.broadcastStateChangedThrottled()
		}
	})
	if err != nil {
		fail(err)
		return
	}
	queueSourcePath := filepath.Join(s.store.Dir(), "radio-source-"+trackID+filepath.Ext(acquired.SourcePath))
	if err := copyQueueSource(queueSourcePath, acquired.SourcePath); err != nil {
		_ = os.Remove(mediaPath)
		fail(fmt.Errorf("音声ソースを保存できませんでした: %w", err))
		return
	}
	os.Remove(acquired.SourcePath)
	if !s.musicQueue.MarkReady(trackID, mediaPath, int(analysis.Duration+0.5)) {
		os.Remove(mediaPath)
		os.Remove(queueSourcePath)
		if thumbnailPath != "" {
			os.Remove(thumbnailPath)
		}
		s.broadcastStateChangedThrottled()
		return
	}
	s.musicQueue.Mutate(trackID, func(t *playlist.Track) {
		t.SourcePath = queueSourcePath
		contract := executedRecipe.NormalizedEncodingContract()
		t.EncodingContract = &contract
		renderContract := executedRecipe.AssetRenderRecipeContract()
		t.RenderRecipeContract = &renderContract
		t.RenderContentValues = executedRecipe.AssetRenderContentValues()
	})
	// A running-but-idle radio starts playing the new track right away.
	s.radio.Wake()
	s.broadcastStateChanged()
}

func copyQueueSource(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return err
	}
	committed = true
	return nil
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
	s.musicQueue.Remove(req.ID)
	s.clearMusicPlaybackRequestFor(req.ID)
	completion := s.radio.CurrentTrackGeneration()
	if completion.TrackID == req.ID && completion.Generation != 0 {
		_ = s.radio.CancelGeneration(completion.Generation)
		go s.removeQueueOwnedTrackFilesAfterGeneration(track, completion)
	} else {
		s.removeQueueOwnedTrackFiles(track)
	}
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
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

func (s *Server) handleMusicPlaylistStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.musicModeEnabled() {
		http.Error(w, "ミュージックモードが無効です", http.StatusConflict)
		return
	}
	if !s.radio.Running() {
		start := s.startMusicRadio
		if start == nil {
			start = s.radio.Start
		}
		if err := start(); err != nil {
			http.Error(w, "ラジオ配信を開始できませんでした: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		s.radio.Wake()
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
		resumingPaused := s.musicPaused && s.musicPausedTrack != ""
		if s.musicPaused && s.musicPausedTrack != "" {
			s.musicPendingTrack = s.musicPausedTrack
			s.musicPendingOffset = s.musicPausedOffset
		}
		s.musicPaused = false
		s.musicPausedTrack = ""
		s.musicPausedOffset = 0
		s.musicPendingMu.Unlock()
		if !resumingPaused {
			s.musicQueue.ResetPlaybackCycle()
		}
	}
	if !s.radio.Running() {
		start := s.startMusicRadio
		if start == nil {
			start = s.radio.Start
		}
		if err := start(); err != nil {
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

// handleMusicPlaylistSeek jumps the current track to the requested position.
// The feed restarts from the offset (through the black filler for a moment),
// so the broadcast — and with it the video preview — follows the seek.
func (s *Server) handleMusicPlaylistSeek(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Seconds int `json:"seconds"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	s.musicPendingMu.Lock()
	paused := s.musicPaused
	pausedTrack := s.musicPausedTrack
	s.musicPendingMu.Unlock()
	if paused && pausedTrack != "" {
		// 一時停止中は再開位置だけ差し替える。
		if track, ok := s.musicQueue.Get(pausedTrack); ok {
			offset := clampSeekSeconds(req.Seconds, track.DurationSeconds)
			s.musicPendingMu.Lock()
			s.musicPausedOffset = offset
			s.musicPendingMu.Unlock()
		}
		s.broadcastStateChangedThrottled()
		writeJSON(w, s.musicPlaylistState())
		return
	}
	st := s.radio.Status()
	if !st.Running || st.CurrentTrackID == "" {
		http.Error(w, "再生していません", http.StatusConflict)
		return
	}
	track, ok := s.musicQueue.Get(st.CurrentTrackID)
	if !ok {
		http.Error(w, "曲が見つかりません", http.StatusNotFound)
		return
	}
	s.setPendingRadioTrack(track.ID, clampSeekSeconds(req.Seconds, track.DurationSeconds))
	s.radio.SkipCurrent()
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func clampSeekSeconds(seconds, duration int) int {
	if seconds < 0 {
		return 0
	}
	if duration > 0 && seconds > duration-2 {
		seconds = duration - 2
		if seconds < 0 {
			seconds = 0
		}
	}
	return seconds
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
	s.clearMusicPlaybackRequest()
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
	path, ok := canonicalStoreFile(s.store.Dir(), track.ThumbnailPath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
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
		audits, _ := s.playlistStore.AuditCanonical(s.activeMusicRadioRecipe().NormalizedEncodingContract())
		entries, _ := s.playlistStore.ListEntries()
		writeJSON(w, map[string]interface{}{"names": names, "playlists": entries, "audits": audits})
	case http.MethodPost:
		var req struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var err error
		if req.ID != "" {
			err = s.playlistStore.SaveID(req.ID, req.Name, s.musicQueue.Snapshot())
		} else {
			err = s.playlistStore.Save(req.Name, s.musicQueue.Snapshot())
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		names, _ := s.playlistStore.List()
		entries, _ := s.playlistStore.ListEntries()
		writeJSON(w, map[string]interface{}{"names": names, "playlists": entries})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMusicPlaylistsLoad(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	var tracks []playlist.Track
	var err error
	if req.ID != "" {
		tracks, err = s.playlistStore.MaterializeID(req.ID, s.playlistRuntimeDir())
	} else {
		tracks, err = s.playlistStore.Materialize(req.Name, s.playlistRuntimeDir())
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.clearMusicPlaybackRequest()
	previous := s.musicQueue.ReplaceAll(tracks)
	completion := s.radio.CurrentTrackGeneration()
	if completion.Generation != 0 && tracksContainID(previous, completion.TrackID) {
		_ = s.radio.CancelGeneration(completion.Generation)
		go s.removeRuntimeTracksAfterGeneration(previous, completion)
	} else {
		s.removeRuntimeTracks(previous)
	}
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) playlistRuntimeDir() string {
	return filepath.Join(s.store.Dir(), "playlist-runtime")
}

func (s *Server) removeQueueOwnedTrackFiles(track playlist.Track) {
	for _, path := range []string{track.MediaPath, track.ThumbnailPath, track.SourcePath} {
		if path == "" || s.isSavedPlaylistPath(path) {
			continue
		}
		if runtimeDir, ok := s.runtimeLoadDir(path); ok {
			if !s.runtimeLoadDirInUse(runtimeDir) {
				if err := os.RemoveAll(runtimeDir); err != nil {
					log.Printf("remove playlist runtime directory %q: %v", runtimeDir, err)
				}
			}
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("remove playlist queue file %q: %v", path, err)
		}
	}
}

func (s *Server) removeRuntimeTracks(tracks []playlist.Track) {
	seen := make(map[string]struct{})
	for _, track := range tracks {
		for _, path := range []string{track.MediaPath, track.ThumbnailPath, track.SourcePath} {
			if dir, ok := s.runtimeLoadDir(path); ok {
				seen[dir] = struct{}{}
			}
		}
	}
	for dir := range seen {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("remove displaced playlist runtime directory %q: %v", dir, err)
		}
	}
}

func (s *Server) removeRuntimeTracksAfterGeneration(tracks []playlist.Track, completion obsrtmp.TrackGeneration) {
	if generationCleanupAllowed(completion) {
		s.removeRuntimeTracks(tracks)
	}
}

func (s *Server) removeQueueOwnedTrackFilesAfterGeneration(track playlist.Track, completion obsrtmp.TrackGeneration) {
	if generationCleanupAllowed(completion) {
		s.removeQueueOwnedTrackFiles(track)
	}
}

func generationCleanupAllowed(completion obsrtmp.TrackGeneration) bool {
	// Cancellation precedes child-process exit. Leave the files for the
	// confirmed session shutdown or the bounded startup cleanup instead.
	select {
	case <-completion.SessionCanceled:
		return false
	default:
	}
	select {
	case <-completion.Completed:
		return true
	case <-completion.SessionDone:
		return true
	case <-completion.SessionCanceled:
		return false
	}
}

func tracksContainID(tracks []playlist.Track, id string) bool {
	for _, track := range tracks {
		if track.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) isSavedPlaylistPath(path string) bool {
	return pathWithin(filepath.Join(s.store.Dir(), "playlist-media"), path)
}

func (s *Server) runtimeLoadDir(path string) (string, bool) {
	runtimeRoot := s.playlistRuntimeDir()
	if !pathWithin(runtimeRoot, path) {
		return "", false
	}
	rel, err := filepath.Rel(runtimeRoot, path)
	if err != nil {
		return "", false
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "." || parts[0] == "" {
		return "", false
	}
	return filepath.Join(runtimeRoot, parts[0]), true
}

func (s *Server) runtimeLoadDirInUse(dir string) bool {
	for _, track := range s.musicQueue.Snapshot() {
		for _, path := range []string{track.MediaPath, track.ThumbnailPath, track.SourcePath} {
			if current, ok := s.runtimeLoadDir(path); ok && current == dir {
				return true
			}
		}
	}
	return false
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func canonicalStoreFile(root, path string) (string, bool) {
	if containsParentPathSegment(path) {
		return "", false
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil || !sameVolume(rootResolved, pathAbs) {
		return "", false
	}
	pathResolved, err := filepath.EvalSymlinks(pathAbs)
	if err != nil || !sameVolume(rootResolved, pathResolved) {
		return "", false
	}
	rel, err := filepath.Rel(rootResolved, pathResolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Stat(pathResolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return pathResolved, true
}

func containsParentPathSegment(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return true
		}
	}
	return false
}

func sameVolume(left, right string) bool {
	return strings.EqualFold(filepath.VolumeName(left), filepath.VolumeName(right))
}

func cleanupAbandonedPlaylistRuntimeDirs(runtimeRoot string, now time.Time) {
	root, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := now.Add(-24 * time.Hour)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if !pathWithin(root, path) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			log.Printf("remove abandoned playlist runtime directory %q: %v", path, err)
		}
	}
}

func (s *Server) handleMusicPlaylistsDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decodePlaylistPost(w, r, &req) {
		return
	}
	var err error
	if req.ID != "" {
		err = s.playlistStore.DeleteID(req.ID)
	} else {
		err = s.playlistStore.Delete(req.Name)
	}
	if err != nil {
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
