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
	analyzeAudioForKind     = video.AnalyzeAudioForKind
	renderRadioTrack        = video.RenderRadioTrack
	startPlaylistGPUWorker  = video.StartPlaylistGPUWorker
	preparePlaylistGPUTrack = func(ctx context.Context, worker *video.PlaylistGPUWorker, epoch uint64, assets video.TrackAssets) error {
		return worker.PrepareTrack(ctx, epoch, assets)
	}
	resumePlaylistGPUWorkerAfterStop = func(worker *video.PlaylistGPUWorker) error {
		return worker.ResumeRunningAfterStopTransition()
	}
	executePlaylistGPUContinuousTrack = func(ctx context.Context, worker *video.PlaylistGPUWorker, sourceAssets video.TrackAssets, source video.TrackTimeline, targetAssets video.TrackAssets, target video.TrackTimeline, plan video.TransitionPlan, targetEpoch uint64, sink func(video.EncodedH264Frame) error) error {
		return worker.ExecutePlaylistGPUContinuousTrack(ctx, sourceAssets, source, targetAssets, target, plan, targetEpoch, sink)
	}
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
	s.SetPlaylistGPUTransitionSink(s.submitPlaylistGPUTransition)
	s.SetPlaylistGPUContinuousTrackSink(s.submitPlaylistGPUContinuousTrack)
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
		OnPublisherSink:    s.onMusicPublisherSink,
		OnPublisherDone:    s.onMusicPublisherDone,
		OnIdle:             func() { s.broadcastStateChangedThrottled() },
		OnRTSPReady:        s.handleRadioRTSPReady,
		OnRTSPDone:         s.handleRadioRTSPDone,
		OnReadinessChanged: s.broadcastStateChangedThrottled,
		OnError:            func(obsrtmp.RadioError) { s.broadcastStateChangedThrottled() },
		OnStopped: func() {
			s.closePlaylistGPUEvaluationSession()
			s.setPlaylistGPUEvaluationArmed(false)
			s.broadcastStateChangedThrottled()
		},
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
	// Publisher profile is frozen when the radio session starts. The normal
	// music route remains the CPU production publisher; only the explicit
	// playlist GPU evaluation gate selects the separate evaluation starter.
	radio.SetPublisherProfile(func() obsrtmp.RadioPublisherProfile {
		if s.playlistGPUEvaluationArmedState() {
			return obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation
		}
		return obsrtmp.RadioPublisherProfileCPUDefault
	})
	// Track ownership is frozen by RadioManager at session start. In the
	// explicit GPU session, only a synchronously completed GPU continuous sink
	// may claim the returned track; an unclaimed track fails closed instead of
	// falling through to the normal CPU feeder.
	radio.SetTrackClaimResolver(s.resolvePlaylistGPUTrackClaim)
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
	if !s.playlistGPUEvaluationRouteActive() {
		s.musicPendingTrack = ""
		s.musicPendingOffset = 0
	}
	s.musicPendingMu.Unlock()
	if paused {
		return "", "", 0, false
	}
	if pending != "" {
		if tr, ok := s.musicQueue.Get(pending); ok && tr.Status == playlist.TrackReady {
			if s.playlistGPUEvaluationRouteActive() && !s.playlistGPUHandledTrackPrepared(tr.ID) {
				return "", "", 0, false
			}
			if !s.commitMusicTimelineTrack(tr) {
				s.clearPlaylistGPUHandledTrack()
				return "", "", 0, false
			}
			s.musicPendingMu.Lock()
			if s.musicPendingTrack == pending {
				s.musicPendingTrack = ""
				s.musicPendingOffset = 0
			}
			s.musicPendingMu.Unlock()
			s.musicQueue.SetCurrent(pending)
			return tr.MediaPath, tr.ID, offset, true
		}
	}
	var tr playlist.Track
	var ok bool
	if s.playlistGPUEvaluationRouteActive() {
		tr, ok = s.musicQueue.PreviewNext()
		if !ok {
			return "", "", 0, false
		}
		if err := s.ensurePlaylistGPUContinuousRuntime(); err != nil || !s.playlistGPUContinuousRouteAvailable() {
			return "", "", 0, false
		}
		if err := s.prepareExplicitPlaylistGPUTransition(video.PlaylistTransitionTrackChange); err != nil {
			return "", "", 0, false
		}
		if !s.commitMusicTimelineTrack(tr) {
			s.clearPlaylistGPUHandledTrack()
			return "", "", 0, false
		}
		tr, ok = s.musicQueue.CommitPreviewNext()
		if !ok {
			s.clearPlaylistGPUHandledTrack()
			return "", "", 0, false
		}
		s.musicTimelineMu.Lock()
		committedEpoch := s.musicTimelineEpoch
		s.musicTimelineMu.Unlock()
		if err := s.markPlaylistGPUHandledTrack(tr.ID, committedEpoch); err != nil {
			return "", "", 0, false
		}
	} else {
		tr, ok = s.musicQueue.Next()
		if ok && !s.commitMusicTimelineTrack(tr) {
			return "", "", 0, false
		}
	}
	if !ok {
		return "", "", 0, false
	}
	if s.playlistGPUEvaluationRouteActive() && !s.playlistGPUHandledTrackPrepared(tr.ID) {
		return "", "", 0, false
	}
	return tr.MediaPath, tr.ID, 0, true
}

func (s *Server) markPlaylistGPUHandledTrack(trackID string, epoch uint64) error {
	if trackID == "" || epoch == 0 {
		return fmt.Errorf("playlist GPU handled claim requires track and epoch")
	}
	s.musicTimelineMu.Lock()
	defer s.musicTimelineMu.Unlock()
	if s.musicPlaylistGPUHandledTrackID != "" {
		return fmt.Errorf("playlist GPU handled claim already exists for track %q", s.musicPlaylistGPUHandledTrackID)
	}
	if epoch != s.musicTimelineEpoch && epoch != s.musicTimelineEpoch+1 {
		return fmt.Errorf("playlist GPU handled claim epoch %d is not adjacent to current epoch %d", epoch, s.musicTimelineEpoch)
	}
	s.musicPlaylistGPUHandledTrackID = trackID
	s.musicPlaylistGPUHandledEpoch = epoch
	return nil
}

func (s *Server) playlistGPUHandledTrackPrepared(trackID string) bool {
	if trackID == "" {
		return false
	}
	s.musicTimelineMu.Lock()
	defer s.musicTimelineMu.Unlock()
	return s.musicPlaylistGPUHandledTrackID == trackID &&
		s.musicPlaylistGPUHandledEpoch != 0 &&
		(s.musicPlaylistGPUHandledEpoch == s.musicTimelineEpoch || s.musicPlaylistGPUHandledEpoch == s.musicTimelineEpoch+1)
}

func (s *Server) clearPlaylistGPUHandledTrack() {
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUHandledTrackID = ""
	s.musicPlaylistGPUHandledEpoch = 0
	s.musicTimelineMu.Unlock()
}

func (s *Server) resolvePlaylistGPUTrackClaim(mediaPath, trackID string, startSeconds int) (obsrtmp.RadioTrackClaimMode, error) {
	// A handled GPU track intentionally has no requirement for a CPU-owned
	// media path: the continuous GPU sink owns its encoded output and the
	// RadioManager must skip both CPU feeders. Keep the argument in the
	// resolver signature for compatibility with the existing callback seam.
	if trackID == "" {
		return obsrtmp.RadioTrackClaimCPU, nil
	}
	if !s.playlistGPUEvaluationRouteActive() {
		return obsrtmp.RadioTrackClaimCPU, nil
	}
	if startSeconds < 0 {
		return "", fmt.Errorf("playlist GPU track %q has invalid start offset %d", trackID, startSeconds)
	}
	if !s.playlistGPUHandledTrackPrepared(trackID) {
		return "", fmt.Errorf("playlist GPU track %q was not completely handled before RadioManager claim", trackID)
	}
	s.clearPlaylistGPUHandledTrack()
	return obsrtmp.RadioTrackClaimHandled, nil
}

func (s *Server) commitMusicTimelineTrack(track playlist.Track) bool {
	durationFrames := uint64(track.DurationSeconds) * 30
	if durationFrames == 0 {
		durationFrames = 1
	}
	s.musicTimelineMu.Lock()
	defer s.musicTimelineMu.Unlock()
	if s.musicTimelineEpoch == ^uint64(0) || s.musicNextSequence > ^uint64(0)-durationFrames {
		return false
	}
	if s.musicTransitionReservedNextSeq > s.musicNextSequence {
		if s.musicTransitionReservedNextSeq > ^uint64(0)-durationFrames {
			return false
		}
		s.musicNextSequence = s.musicTransitionReservedNextSeq
		s.musicTransitionReservedNextSeq = 0
	}
	if s.playlistGPUEvaluationRouteActive() {
		timeline, ok := s.musicPlaylistGPUTimelines[track.ID]
		if !ok {
			return false
		}
		frameDurationNS := int64(1_000_000_000 / timeline.FPS)
		if frameDurationNS <= 0 {
			return false
		}
		rebased, err := video.RebasePlaylistTimeline(timeline, s.musicNextSequence, int64(s.musicNextSequence)*frameDurationNS)
		if err != nil {
			return false
		}
		s.musicPlaylistGPUTimelines[track.ID] = rebased
	}
	s.musicTimelineEpoch++
	s.musicActiveTrackID = track.ID
	s.musicActiveBaseSeq = s.musicNextSequence
	s.musicActiveTailSeq = s.musicNextSequence + durationFrames - 1
	s.musicNextSequence = s.musicActiveTailSeq + 1
	s.musicActiveStartedAt = time.Now()
	return true
}

func (s *Server) onRadioTrackEnd(trackID string, err error) {
	if outputErr := s.finishPlaylistGPUActiveOutput(trackID); outputErr != nil && err == nil {
		err = outputErr
	}
	if err != nil {
		err = obsrtmp.SanitizeRadioError(err)
		s.musicQueue.MarkFailed(trackID, "配信エラー: "+err.Error())
	}
	s.broadcastStateChangedThrottled()
}

func (s *Server) finishPlaylistGPUActiveOutput(trackID string) error {
	s.musicPlaylistGPUExecutionMu.Lock()
	defer s.musicPlaylistGPUExecutionMu.Unlock()
	s.musicTimelineMu.Lock()
	out := s.musicPlaylistGPUActiveOutput
	s.musicPlaylistGPUActiveOutput = nil
	s.musicTimelineMu.Unlock()
	if out == nil {
		return nil
	}
	// Stop new program PCM callbacks before closing the mux inputs. The program
	// pipeline owns the output clock and can be between its tee lookup and the
	// actual write when a track boundary arrives. Quiescing the explicit tee
	// first prevents that final tick from observing inputsClosed and turning a
	// normal GPU drain into a terminal "playlist GPU mux is closed" error.
	if out.radio != nil {
		out.radio.SetPlaylistAudioTee(nil)
	}
	if err := out.closeInputsAndWait(); err != nil {
		out.close()
		return fmt.Errorf("finish playlist GPU output for track %q: %w", trackID, err)
	}
	out.close()
	return nil
}

func (s *Server) clearPlaylistGPUActiveOutput(out *playlistGPUOutputSession) {
	s.musicTimelineMu.Lock()
	if s.musicPlaylistGPUActiveOutput == out {
		s.musicPlaylistGPUActiveOutput = nil
	}
	s.musicTimelineMu.Unlock()
}

func (s *Server) onMusicPublisherSink(sink io.Writer) {
	s.musicPlaylistPublisherMu.Lock()
	s.musicPlaylistPublisherSink = sink
	s.musicPlaylistPublisherMu.Unlock()
}

func (s *Server) onMusicPublisherDone() {
	s.musicPlaylistPublisherMu.Lock()
	s.musicPlaylistPublisherSink = nil
	s.musicPlaylistPublisherMu.Unlock()
	// The publisher may disappear before the continuous track reaches its
	// normal OnTrackEnd callback. Drain the mux inputs first, then release the
	// GPU bridge, audio tee, and CPU-output ownership in one cleanup path.
	_ = s.finishPlaylistGPUActiveOutput("publisher done")
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

func explicitPlaylistGPUCapabilityAvailable() bool {
	return os.Getenv("IMAGEPAD_GPU_PLAYLIST_TIMELINE") == "1"
}

func (s *Server) playlistGPUEvaluationArmedState() bool {
	s.musicTimelineMu.Lock()
	defer s.musicTimelineMu.Unlock()
	return s.playlistGPUEvaluationArmed
}

func (s *Server) playlistGPUEvaluationRouteActive() bool {
	if s.radio == nil {
		return false
	}
	status := s.radio.Status()
	return status.Running && status.ActiveSession != nil && status.ActiveSession.PublisherProfile == obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation
}

func (s *Server) setPlaylistGPUEvaluationArmed(armed bool) {
	s.musicTimelineMu.Lock()
	s.playlistGPUEvaluationArmed = armed
	s.musicTimelineMu.Unlock()
}

func (s *Server) nextRadioPublisherProfile() obsrtmp.RadioPublisherProfile {
	if s.playlistGPUEvaluationArmedState() {
		return obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation
	}
	return obsrtmp.RadioPublisherProfileCPUDefault
}

// armPlaylistGPUEvaluation is the only server-side entry that turns the
// explicit evaluation capability into a session-scoped GPU request.
// Environment variables and sidecar presence alone never change the normal
// CPU music publisher.
func (s *Server) armPlaylistGPUEvaluation() error {
	if !explicitPlaylistGPUCapabilityAvailable() {
		return fmt.Errorf("playlist GPU evaluation capability is not enabled")
	}
	if s.radio.Running() {
		status := s.radio.Status()
		if status.ActiveSession == nil || status.ActiveSession.PublisherProfile != obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation {
			return fmt.Errorf("playlist GPU evaluation requires a stopped radio or an active GPU evaluation session")
		}
	}
	s.setPlaylistGPUEvaluationArmed(true)
	return nil
}

func (s *Server) startMusicRadioSession(profile obsrtmp.RadioPublisherProfile) error {
	s.musicRadioStartMu.Lock()
	defer s.musicRadioStartMu.Unlock()
	if s.radio.Running() {
		status := s.radio.Status()
		activeProfile := obsrtmp.RadioPublisherProfileCPUDefault
		if status.ActiveSession != nil {
			activeProfile = status.ActiveSession.PublisherProfile
		}
		if status.ActiveSession == nil || activeProfile != profile {
			return fmt.Errorf("radio session with publisher profile %q is already running", activeProfile)
		}
		if profile == obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation {
			s.setPlaylistGPUEvaluationArmed(true)
		}
		return nil
	}
	if profile == obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation {
		if err := s.armPlaylistGPUEvaluation(); err != nil {
			return err
		}
		s.musicTimelineMu.Lock()
		if s.musicPlaylistGPUSessionCtx != nil || s.musicPlaylistGPUSessionCancel != nil {
			s.musicTimelineMu.Unlock()
			return fmt.Errorf("playlist GPU evaluation session ownership is still active")
		}
		s.musicPlaylistGPUSessionCtx, s.musicPlaylistGPUSessionCancel = context.WithCancel(context.Background())
		s.musicTimelineMu.Unlock()
	} else {
		s.setPlaylistGPUEvaluationArmed(false)
	}
	start := s.startMusicRadio
	if start == nil {
		start = s.radio.Start
	}
	if err := start(); err != nil {
		if profile == obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation {
			s.closePlaylistGPUEvaluationSession()
			s.setPlaylistGPUEvaluationArmed(false)
		}
		return err
	}
	return nil
}

// closePlaylistGPUEvaluationSession tears down the explicit GPU worker only at
// the owning radio-session boundary. A transition must never close this worker:
// the same sidecar/session context is reused by subsequent track changes.
func (s *Server) closePlaylistGPUEvaluationSession() {
	_ = s.finishPlaylistGPUActiveOutput("GPU evaluation session stopped")

	s.musicTimelineMu.Lock()
	worker := s.musicPlaylistGPUWorker
	cancel := s.musicPlaylistGPUSessionCancel
	s.musicPlaylistGPUWorker = nil
	s.musicPlaylistGPUController = nil
	s.musicPlaylistGPUSessionCtx = nil
	s.musicPlaylistGPUSessionCancel = nil
	s.musicTimelineMu.Unlock()

	if worker != nil {
		_ = worker.Close()
	}
	if cancel != nil {
		cancel()
	}
}

func (s *Server) prepareExplicitPlaylistGPUTransition(reason video.PlaylistTransitionReason) error {
	return s.prepareExplicitPlaylistGPUTransitionTo(reason, "")
}

// prepareExplicitPlaylistGPUTransitionTo is the server-authoritative boundary
// for explicit playlist GPU track changes. An explicit target from the GUI is
// preferred; an empty target reserves the queue's PreviewNext selection. The
// normal CPU music route never calls this boundary.
func (s *Server) prepareExplicitPlaylistGPUTransitionTo(reason video.PlaylistTransitionReason, explicitTargetID string) error {
	if !s.playlistGPUEvaluationRouteActive() {
		return nil
	}
	status := s.radio.Status()
	if !status.Running || status.CurrentTrackID == "" {
		return fmt.Errorf("playlist GPU transition requires an active radio track")
	}
	const (
		fps             = uint64(30)
		frameDurationNS = int64(1_000_000_000 / fps)
		safetyFrames    = uint64(8)
		fadeFrames      = uint64(8)
	)
	s.musicTimelineMu.Lock()
	epoch := s.musicTimelineEpoch
	sourceTrackID := s.musicActiveTrackID
	baseSequence := s.musicActiveBaseSeq
	tailSequence := s.musicActiveTailSeq
	startedAt := s.musicActiveStartedAt
	s.musicTimelineMu.Unlock()
	if epoch == 0 || sourceTrackID == "" || sourceTrackID != status.CurrentTrackID || startedAt.IsZero() {
		return fmt.Errorf("playlist GPU transition authoritative timeline is not ready")
	}
	elapsed := time.Since(startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	headOffset := uint64(elapsed / time.Duration(frameDurationNS))
	headSequence := baseSequence + headOffset
	if headSequence > tailSequence {
		headSequence = tailSequence
	}
	headPTSNs := int64(headSequence) * frameDurationNS
	tailPTSNs := int64(tailSequence) * frameDurationNS
	if tailSequence < headSequence {
		return fmt.Errorf("playlist GPU transition queue tail precedes playback head")
	}
	targetTrackID := ""
	if reason == video.PlaylistTransitionTrackChange {
		if explicitTargetID != "" {
			target, ok := s.musicQueue.Get(explicitTargetID)
			if !ok || target.ID == "" || target.Status != playlist.TrackReady {
				return fmt.Errorf("playlist GPU track change target is not ready")
			}
			targetTrackID = target.ID
		} else {
			target, ok := s.musicQueue.PreviewNext()
			if !ok || target.ID == "" {
				return fmt.Errorf("playlist GPU track change has no authoritative target")
			}
			targetTrackID = target.ID
		}
	}
	request := video.TransitionRequest{
		Schema:               video.GPUPlaylistTimelineSchema,
		Epoch:                epoch,
		Reason:               reason,
		SourceTrackID:        sourceTrackID,
		PlaybackHeadSequence: headSequence,
		PlaybackHeadPTSNs:    headPTSNs,
	}
	audio := video.AudioFadePlan{
		DurationNS: int64(fadeFrames) * frameDurationNS,
		Curve:      "linear",
	}
	plan, err := video.CompilePlaylistTransition(request, targetTrackID, tailSequence, tailPTSNs, safetyFrames, fadeFrames, frameDurationNS, audio)
	if err != nil {
		return err
	}
	s.musicTimelineMu.Lock()
	sink := s.musicPlaylistGPUTransitionSink
	continuousSink := s.musicPlaylistGPUContinuousSink
	targetAssets := s.musicPlaylistGPUAssets[targetTrackID]
	targetTimeline := s.musicPlaylistGPUTimelines[targetTrackID]
	s.musicTimelineMu.Unlock()
	if reason == video.PlaylistTransitionTrackChange {
		if continuousSink == nil {
			return fmt.Errorf("playlist GPU continuous track executor is not connected")
		}
		if targetTrackID == "" || targetAssets.TrackID != targetTrackID || targetTimeline.TrackID != targetTrackID {
			return fmt.Errorf("playlist GPU continuous target assets are not prepared")
		}
		if err := continuousSink(targetAssets, targetTimeline, plan); err != nil {
			return fmt.Errorf("playlist GPU continuous track executor rejected plan: %w", err)
		}
		if plan.FadeEndSequence == ^uint64(0) {
			return fmt.Errorf("playlist GPU transition next sequence overflow")
		}
		s.musicTimelineMu.Lock()
		if plan.FadeEndSequence+1 > s.musicTransitionReservedNextSeq {
			s.musicTransitionReservedNextSeq = plan.FadeEndSequence + 1
		}
		s.musicTimelineMu.Unlock()
		return nil
	}
	if sink == nil {
		return fmt.Errorf("playlist GPU transition executor is not connected")
	}
	if err := sink(plan); err != nil {
		return fmt.Errorf("playlist GPU transition executor rejected plan: %w", err)
	}
	if reason == video.PlaylistTransitionTrackChange {
		if plan.FadeEndSequence == ^uint64(0) {
			return fmt.Errorf("playlist GPU transition next sequence overflow")
		}
		s.musicTimelineMu.Lock()
		if plan.FadeEndSequence+1 > s.musicTransitionReservedNextSeq {
			s.musicTransitionReservedNextSeq = plan.FadeEndSequence + 1
		}
		s.musicTimelineMu.Unlock()
	}
	return nil
}

// SetPlaylistGPUTransitionSink connects the explicit evaluation-only server
// boundary to the sidecar transition executor. The normal CPU music route
// never calls this sink.
func (s *Server) SetPlaylistGPUTransitionSink(sink func(video.TransitionPlan) error) {
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUTransitionSink = sink
	s.musicTimelineMu.Unlock()
}

// SetPlaylistGPUEncodedFrameSink connects the explicit evaluation route to its
// compressed-frame mux boundary. The normal CPU music route never uses it.
func (s *Server) SetPlaylistGPUEncodedFrameSink(sink func(video.EncodedH264Frame) error) {
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUFrameSink = sink
	s.musicTimelineMu.Unlock()
}

// SetPlaylistGPUContinuousPlaybackReady arms the explicit evaluation route
// for automatic track-boundary playback. Until the continuous GPU publisher
// owns the next track, nextRadioTrack fails closed instead of handing that
// track to the normal CPU radio renderer.
func (s *Server) SetPlaylistGPUContinuousPlaybackReady(ready bool) {
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUContinuousReady = ready
	s.musicTimelineMu.Unlock()
}

// SetPlaylistGPUContinuousTrackSink connects the explicit automatic
// track-boundary owner. The sink must own the complete target-track handoff;
// a fade-tail-only sink is not sufficient because returning MediaPath would
// otherwise fall back to the CPU radio renderer.
func (s *Server) SetPlaylistGPUContinuousTrackSink(sink func(video.TrackAssets, video.TrackTimeline, video.TransitionPlan) error) {
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUContinuousSink = sink
	s.musicTimelineMu.Unlock()
}

// SetPlaylistGPUContinuousRuntime binds the long-lived worker/controller and
// output ownership required by automatic GPU track-boundary playback. A ready
// flag or a fade-tail sink alone never arms this route.
func (s *Server) SetPlaylistGPUContinuousRuntime(worker *video.PlaylistGPUWorker, controller *video.PlaylistGPUTransitionController) {
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUWorker = worker
	s.musicPlaylistGPUController = controller
	s.musicTimelineMu.Unlock()
}

func (s *Server) playlistGPUSessionContext() (context.Context, error) {
	s.musicTimelineMu.Lock()
	defer s.musicTimelineMu.Unlock()
	if s.musicPlaylistGPUSessionCtx == nil {
		return nil, fmt.Errorf("playlist GPU runtime has no explicit session context")
	}
	return s.musicPlaylistGPUSessionCtx, nil
}

func (s *Server) ensurePlaylistGPUContinuousRuntime() error {
	s.musicTimelineMu.Lock()
	worker := s.musicPlaylistGPUWorker
	controller := s.musicPlaylistGPUController
	epoch := s.musicTimelineEpoch
	committed := s.musicActiveBaseSeq
	frameSink := s.musicPlaylistGPUFrameSink
	sessionCtx := s.musicPlaylistGPUSessionCtx
	s.musicTimelineMu.Unlock()
	if worker != nil && controller != nil && controller.State() == video.PlaylistGPUStateRunning {
		return nil
	}
	if worker != nil || controller != nil {
		return fmt.Errorf("playlist GPU continuous runtime has a non-running session worker")
	}
	s.musicPlaylistPublisherMu.RLock()
	publisherSink := s.musicPlaylistPublisherSink
	s.musicPlaylistPublisherMu.RUnlock()
	if frameSink == nil && publisherSink == nil {
		return fmt.Errorf("playlist GPU continuous output ownership is not connected")
	}
	if epoch == 0 {
		return fmt.Errorf("playlist GPU continuous runtime has no authoritative epoch")
	}
	if sessionCtx == nil {
		return fmt.Errorf("playlist GPU continuous runtime has no explicit session context")
	}
	executable := strings.TrimSpace(os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD"))
	if executable == "" {
		return fmt.Errorf("IMAGEPAD_PLAYLIST_COMPOSITORD is not configured")
	}
	worker, err := startPlaylistGPUWorker(sessionCtx, executable, "playlist-session")
	if err != nil {
		return err
	}
	if worker == nil {
		return fmt.Errorf("playlist GPU continuous runtime starter returned a nil worker")
	}
	controller, err = video.NewPlaylistGPUTransitionController(epoch, committed)
	if err != nil {
		_ = worker.Close()
		return err
	}
	if err := worker.AttachTransitionController(controller); err != nil {
		_ = worker.Close()
		return err
	}
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUWorker = worker
	s.musicPlaylistGPUController = controller
	s.musicTimelineMu.Unlock()
	return nil
}

func (s *Server) playlistGPUContinuousRouteAvailable() bool {
	s.musicTimelineMu.Lock()
	worker := s.musicPlaylistGPUWorker
	controller := s.musicPlaylistGPUController
	continuousSink := s.musicPlaylistGPUContinuousSink
	frameSink := s.musicPlaylistGPUFrameSink
	s.musicTimelineMu.Unlock()
	s.musicPlaylistPublisherMu.RLock()
	publisherSink := s.musicPlaylistPublisherSink
	s.musicPlaylistPublisherMu.RUnlock()
	return s.musicPlaylistGPUContinuousReady && continuousSink != nil && worker != nil && controller != nil && controller.State() == video.PlaylistGPUStateRunning && (frameSink != nil || publisherSink != nil)
}

// PlaylistGPUOutputTelemetry returns the latest successful explicit playlist
// GPU output receipt. Normal CPU music rendering never updates this snapshot.
func (s *Server) PlaylistGPUOutputTelemetry() video.PlaylistGPUOutputTelemetry {
	s.musicTimelineMu.Lock()
	defer s.musicTimelineMu.Unlock()
	return s.musicPlaylistGPUOutputTelemetry
}

type playlistGPUOutputSession struct {
	frameSink func(video.EncodedH264Frame) error
	bridge    *obsrtmp.PlaylistGPUMuxBridge
	radio     interface {
		SetPlaylistAudioTee(func([]byte, time.Duration) error)
		SetPlaylistGPUOutputActive(bool)
	}
}

func (o *playlistGPUOutputSession) closeInputsAndWait() error {
	if o == nil || o.bridge == nil {
		return nil
	}
	if err := o.bridge.CloseInputs(); err != nil {
		return err
	}
	return o.bridge.Wait()
}

func (o *playlistGPUOutputSession) closeVideoInput() error {
	if o == nil || o.bridge == nil {
		return nil
	}
	return o.bridge.CloseVideoInput()
}

func (o *playlistGPUOutputSession) close() {
	if o == nil {
		return
	}
	if o.radio != nil {
		o.radio.SetPlaylistAudioTee(nil)
		o.radio.SetPlaylistGPUOutputActive(false)
	}
	if o.bridge != nil {
		_ = o.bridge.Close()
	}
}

func (s *Server) openPlaylistGPUOutput(ctx context.Context, plan video.TransitionPlan) (*playlistGPUOutputSession, error) {
	if ctx == nil {
		return nil, fmt.Errorf("playlist GPU output requires an explicit session context")
	}
	s.musicTimelineMu.Lock()
	frameSink := s.musicPlaylistGPUFrameSink
	s.musicTimelineMu.Unlock()
	s.musicPlaylistPublisherMu.RLock()
	publisherSink := s.musicPlaylistPublisherSink
	s.musicPlaylistPublisherMu.RUnlock()
	if frameSink == nil && publisherSink == nil {
		return nil, fmt.Errorf("playlist GPU encoded-frame mux sink is not connected")
	}
	out := &playlistGPUOutputSession{frameSink: frameSink}
	if publisherSink == nil {
		return out, nil
	}
	type outputController interface {
		SetPlaylistAudioTee(func([]byte, time.Duration) error)
		SetPlaylistGPUOutputActive(bool)
	}
	radio, ok := s.radio.(outputController)
	if !ok {
		return nil, fmt.Errorf("radio does not expose explicit playlist GPU output control")
	}
	ffmpeg, err := video.EnsureFFmpeg()
	if err != nil {
		return nil, fmt.Errorf("ensure FFmpeg for playlist GPU mux: %w", err)
	}
	bridge, err := obsrtmp.StartPlaylistGPUMuxBridge(ctx, ffmpeg, publisherSink, 640, 360, 30, 48_000, 2, plan.FadeStartPTSNs)
	if err != nil {
		return nil, fmt.Errorf("start playlist GPU mux bridge: %w", err)
	}
	out.bridge = bridge
	out.radio = radio
	radio.SetPlaylistGPUOutputActive(true)
	radio.SetPlaylistAudioTee(func(samples []byte, pts time.Duration) error {
		faded, err := obsrtmp.ApplyPlaylistGPUAudioFadePCM(samples, pts.Nanoseconds(), 48_000, 2, plan.AudioFadePlan)
		if err != nil {
			return err
		}
		return bridge.AudioTee(faded, pts)
	})
	out.frameSink = bridge.VideoSink
	return out, nil
}

func (s *Server) playlistGPUWorkerForPlan(plan video.TransitionPlan) (*video.PlaylistGPUWorker, *video.PlaylistGPUTransitionController, error) {
	s.musicTimelineMu.Lock()
	worker := s.musicPlaylistGPUWorker
	controller := s.musicPlaylistGPUController
	s.musicTimelineMu.Unlock()
	if worker != nil && controller != nil && controller.State() == video.PlaylistGPUStateRunning {
		return worker, controller, nil
	}
	if worker != nil || controller != nil {
		return nil, nil, fmt.Errorf("playlist GPU transition has a non-running session worker")
	}
	sessionCtx, err := s.playlistGPUSessionContext()
	if err != nil {
		return nil, nil, err
	}
	executable := strings.TrimSpace(os.Getenv("IMAGEPAD_PLAYLIST_COMPOSITORD"))
	if executable == "" {
		return nil, nil, fmt.Errorf("IMAGEPAD_PLAYLIST_COMPOSITORD is not configured")
	}
	worker, err = startPlaylistGPUWorker(sessionCtx, executable, "playlist-session")
	if err != nil {
		return nil, nil, fmt.Errorf("start playlist GPU worker: %w", err)
	}
	if worker == nil {
		return nil, nil, fmt.Errorf("playlist GPU worker starter returned a nil worker")
	}
	controller, err = video.NewPlaylistGPUTransitionController(plan.Epoch, plan.PlaybackHeadSequence)
	if err != nil {
		_ = worker.Close()
		return nil, nil, fmt.Errorf("create playlist GPU transition controller: %w", err)
	}
	if err := worker.AttachTransitionController(controller); err != nil {
		_ = worker.Close()
		return nil, nil, fmt.Errorf("attach playlist GPU transition controller: %w", err)
	}
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUWorker = worker
	s.musicPlaylistGPUController = controller
	s.musicTimelineMu.Unlock()
	return worker, controller, nil
}

func (s *Server) submitPlaylistGPUTransition(plan video.TransitionPlan) error {
	s.musicPlaylistGPUExecutionMu.Lock()
	defer s.musicPlaylistGPUExecutionMu.Unlock()
	sessionCtx, err := s.playlistGPUSessionContext()
	if err != nil {
		return err
	}
	s.musicTimelineMu.Lock()
	assets := s.musicPlaylistGPUAssets[plan.SourceTrackID]
	timeline := s.musicPlaylistGPUTimelines[plan.SourceTrackID]
	s.musicTimelineMu.Unlock()
	if assets.TrackID != plan.SourceTrackID || timeline.TrackID != plan.SourceTrackID {
		return fmt.Errorf("playlist GPU source assets/timeline are not prepared for track %q", plan.SourceTrackID)
	}
	out, err := s.openPlaylistGPUOutput(sessionCtx, plan)
	if err != nil {
		return err
	}
	defer out.close()
	worker, controller, err := s.playlistGPUWorkerForPlan(plan)
	if err != nil {
		return err
	}
	if err := preparePlaylistGPUTrack(sessionCtx, worker, plan.Epoch, assets); err != nil {
		_ = worker.Close()
		return fmt.Errorf("prepare playlist GPU worker track: %w", err)
	}
	orderedMux, err := video.NewPlaylistGPUOrderedMux(plan.FadeStartSequence, plan.FadeStartPTSNs, plan.DurationFrames, out.frameSink)
	if err != nil {
		return err
	}
	if err := worker.ExecutePlaylistGPUTransition(sessionCtx, assets, timeline, plan, func(frame video.EncodedH264Frame) error {
		return orderedMux.PushBatch([]video.EncodedH264Frame{frame})
	}); err != nil {
		_ = worker.Close()
		return fmt.Errorf("execute playlist GPU transition: %w", err)
	}
	if err := orderedMux.Finalize(); err != nil {
		_ = worker.Close()
		return fmt.Errorf("finalize playlist GPU ordered output mux: %w", err)
	}
	if err := out.closeVideoInput(); err != nil {
		_ = worker.Close()
		return fmt.Errorf("close playlist GPU video input after output: %w", err)
	}
	if err := out.closeInputsAndWait(); err != nil {
		_ = worker.Close()
		return fmt.Errorf("drain playlist GPU mux output: %w", err)
	}
	telemetry := orderedMux.Telemetry()
	if err := telemetry.Validate(); err != nil {
		return fmt.Errorf("validate playlist GPU output telemetry: %w", err)
	}
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUOutputTelemetry = telemetry
	s.musicTimelineMu.Unlock()
	return s.commitPlaylistGPUTransitionSuccess(worker, controller, telemetry)
}

// submitPlaylistGPUContinuousTrack is the automatic track-boundary executor.
// It renders the source fade tail and the complete target timeline through the
// same bounded worker/output session. The radio loop is marked handled only
// after the target stream has been fully drained, so it cannot run the CPU
// feeder for a partially completed GPU transition.
func (s *Server) submitPlaylistGPUContinuousTrack(sourceAssets video.TrackAssets, sourceTimeline video.TrackTimeline, plan video.TransitionPlan) error {
	if plan.Reason != video.PlaylistTransitionTrackChange || plan.TargetTrackID == "" {
		return fmt.Errorf("playlist GPU continuous executor requires a track-change plan")
	}
	s.musicPlaylistGPUExecutionMu.Lock()
	defer s.musicPlaylistGPUExecutionMu.Unlock()
	sessionCtx, err := s.playlistGPUSessionContext()
	if err != nil {
		return err
	}
	s.musicTimelineMu.Lock()
	targetAssets := s.musicPlaylistGPUAssets[plan.TargetTrackID]
	targetTimeline := s.musicPlaylistGPUTimelines[plan.TargetTrackID]
	s.musicTimelineMu.Unlock()
	if targetAssets.TrackID != plan.TargetTrackID || targetTimeline.TrackID != plan.TargetTrackID {
		return fmt.Errorf("playlist GPU continuous target assets/timeline are not prepared for track %q", plan.TargetTrackID)
	}
	if targetTimeline.FPS == 0 || plan.FadeEndSequence == ^uint64(0) || plan.FadeEndPTSNs > int64(^uint64(0)>>1)-int64(1_000_000_000/targetTimeline.FPS) {
		return fmt.Errorf("playlist GPU continuous target timeline origin overflow")
	}
	targetEpoch := plan.Epoch + 1
	if targetEpoch == 0 {
		return fmt.Errorf("playlist GPU continuous target epoch overflow")
	}
	out, err := s.openPlaylistGPUOutput(sessionCtx, plan)
	if err != nil {
		return err
	}
	s.musicTimelineMu.Lock()
	s.musicPlaylistGPUActiveOutput = out
	s.musicTimelineMu.Unlock()
	worker, controller, err := s.playlistGPUWorkerForPlan(plan)
	if err != nil {
		s.clearPlaylistGPUActiveOutput(out)
		out.close()
		return err
	}
	if err := preparePlaylistGPUTrack(sessionCtx, worker, plan.Epoch, sourceAssets); err != nil {
		s.clearPlaylistGPUActiveOutput(out)
		out.close()
		_ = worker.Close()
		return fmt.Errorf("prepare playlist GPU source track: %w", err)
	}
	orderedMux, err := video.NewPlaylistGPUOrderedMux(plan.FadeStartSequence, plan.FadeStartPTSNs, plan.DurationFrames+targetTimeline.FrameCount, out.frameSink)
	if err != nil {
		s.clearPlaylistGPUActiveOutput(out)
		out.close()
		_ = worker.Close()
		return err
	}
	if err := executePlaylistGPUContinuousTrack(sessionCtx, worker, sourceAssets, sourceTimeline, targetAssets, targetTimeline, plan, targetEpoch, func(frame video.EncodedH264Frame) error {
		return orderedMux.PushBatch([]video.EncodedH264Frame{frame})
	}); err != nil {
		s.clearPlaylistGPUActiveOutput(out)
		out.close()
		_ = worker.Close()
		return fmt.Errorf("execute playlist GPU continuous track: %w", err)
	}
	if err := orderedMux.Finalize(); err != nil {
		s.clearPlaylistGPUActiveOutput(out)
		out.close()
		_ = worker.Close()
		return fmt.Errorf("finalize playlist GPU continuous output: %w", err)
	}
	if err := out.closeVideoInput(); err != nil {
		s.clearPlaylistGPUActiveOutput(out)
		out.close()
		_ = worker.Close()
		return fmt.Errorf("close playlist GPU continuous video input after output: %w", err)
	}
	telemetry := orderedMux.Telemetry()
	if err := telemetry.Validate(); err != nil {
		s.clearPlaylistGPUActiveOutput(out)
		out.close()
		_ = worker.Close()
		return fmt.Errorf("validate playlist GPU continuous output telemetry: %w", err)
	}
	if _, ok := s.radio.(interface {
		SetPlaylistAudioTee(func([]byte, time.Duration) error)
		SetPlaylistGPUOutputActive(bool)
	}); !ok {
		out.close()
		_ = worker.Close()
		return fmt.Errorf("radio does not expose playlist GPU output ownership")
	}
	if err := s.commitPlaylistGPUContinuousSuccess(worker, controller, telemetry); err != nil {
		s.clearPlaylistGPUActiveOutput(out)
		out.close()
		_ = worker.Close()
		return err
	}
	return nil
}

// commitPlaylistGPUContinuousSuccess publishes the completed transition
// without releasing the session-owned worker. The output bridge remains active
// until finishPlaylistGPUActiveOutput has quiesced the audio tee and completed
// mux Flush/EOS; only the owning radio-session cleanup may close the worker.
func (s *Server) commitPlaylistGPUContinuousSuccess(worker *video.PlaylistGPUWorker, controller *video.PlaylistGPUTransitionController, telemetry video.PlaylistGPUOutputTelemetry) error {
	if worker == nil || controller == nil {
		return fmt.Errorf("playlist GPU continuous success requires worker and controller ownership")
	}
	if controller.State() != video.PlaylistGPUStateRunning || worker.TransitionState() != video.PlaylistGPUStateRunning {
		return fmt.Errorf("playlist GPU continuous success requires a running session state")
	}
	if err := telemetry.Validate(); err != nil {
		return fmt.Errorf("validate playlist GPU continuous success telemetry: %w", err)
	}
	s.musicTimelineMu.Lock()
	sessionCtx := s.musicPlaylistGPUSessionCtx
	if sessionCtx == nil {
		s.musicTimelineMu.Unlock()
		return fmt.Errorf("playlist GPU continuous success has no explicit session context")
	}
	select {
	case <-sessionCtx.Done():
		s.musicTimelineMu.Unlock()
		return fmt.Errorf("playlist GPU continuous success session context is cancelled")
	default:
	}
	if s.musicPlaylistGPUWorker != nil && s.musicPlaylistGPUWorker != worker {
		s.musicTimelineMu.Unlock()
		return fmt.Errorf("playlist GPU continuous success would replace session worker")
	}
	if s.musicPlaylistGPUController != nil && s.musicPlaylistGPUController != controller {
		s.musicTimelineMu.Unlock()
		return fmt.Errorf("playlist GPU continuous success would replace session controller")
	}
	s.musicPlaylistGPUOutputTelemetry = telemetry
	s.musicPlaylistGPUWorker = worker
	s.musicPlaylistGPUController = controller
	s.musicTimelineMu.Unlock()
	return nil
}

// commitPlaylistGPUTransitionSuccess publishes a completed stop/pause
// transition without releasing the explicit GPU session owner. The transition
// owns only its output drain; worker/controller closure belongs to the final
// evaluation-session teardown.
func (s *Server) commitPlaylistGPUTransitionSuccess(worker *video.PlaylistGPUWorker, controller *video.PlaylistGPUTransitionController, telemetry video.PlaylistGPUOutputTelemetry) error {
	if worker == nil || controller == nil {
		return fmt.Errorf("playlist GPU transition success requires worker and controller ownership")
	}
	if controller.State() != video.PlaylistGPUStateTransitionComplete {
		return fmt.Errorf("playlist GPU transition success requires Flush/EOS completion")
	}
	if err := telemetry.Validate(); err != nil {
		return fmt.Errorf("validate playlist GPU transition success telemetry: %w", err)
	}
	s.musicTimelineMu.Lock()
	defer s.musicTimelineMu.Unlock()
	if s.musicPlaylistGPUSessionCtx == nil {
		return fmt.Errorf("playlist GPU transition success has no explicit session context")
	}
	select {
	case <-s.musicPlaylistGPUSessionCtx.Done():
		return fmt.Errorf("playlist GPU transition success session context is cancelled")
	default:
	}
	if s.musicPlaylistGPUWorker != nil && s.musicPlaylistGPUWorker != worker {
		return fmt.Errorf("playlist GPU transition success would replace session worker")
	}
	if s.musicPlaylistGPUController != nil && s.musicPlaylistGPUController != controller {
		return fmt.Errorf("playlist GPU transition success would replace session controller")
	}
	if err := resumePlaylistGPUWorkerAfterStop(worker); err != nil {
		return fmt.Errorf("resume playlist GPU worker after stop transition: %w", err)
	}
	s.musicPlaylistGPUOutputTelemetry = telemetry
	s.musicPlaylistGPUWorker = worker
	s.musicPlaylistGPUController = controller
	return nil
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
		"hlsURL":                   hlsURL,
		"publicHLSURL":             publicHLSURL,
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
			acquired, err := musicURLAcquirer(s.lifecycleContext(), s, input)
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
		acquired, err := s.acquireUploadedAudio(s.lifecycleContext(), f, filepath.Base(localPath))
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
	ctx := s.lifecycleContext()
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
	if s.playlistGPUEvaluationArmedState() {
		assets, timeline, err := video.CompilePlaylistGPUTrack(input, trackID)
		if err != nil {
			fail(fmt.Errorf("playlist GPU immutable track preparation failed: %w", err))
			return
		}
		s.musicTimelineMu.Lock()
		if s.musicPlaylistGPUAssets == nil {
			s.musicPlaylistGPUAssets = make(map[string]video.TrackAssets)
			s.musicPlaylistGPUTimelines = make(map[string]video.TrackTimeline)
		}
		s.musicPlaylistGPUAssets[trackID] = assets
		s.musicPlaylistGPUTimelines[trackID] = timeline
		s.musicTimelineMu.Unlock()
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
		if err := s.startMusicRadioSession(obsrtmp.RadioPublisherProfileCPUDefault); err != nil {
			http.Error(w, "ラジオ配信を開始できませんでした: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		s.radio.Wake()
	}
	s.broadcastStateChangedThrottled()
	writeJSON(w, s.musicPlaylistState())
}

func (s *Server) handleMusicPlaylistGPUEvaluationStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.musicModeEnabled() {
		http.Error(w, "ミュージックモードが無効です", http.StatusConflict)
		return
	}
	if s.radio.Running() {
		status := s.radio.Status()
		if status.ActiveSession == nil || status.ActiveSession.PublisherProfile != obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation {
			http.Error(w, "通常CPUラジオ配信が実行中です。停止してからGPU評価を開始してください", http.StatusConflict)
			return
		}
		s.setPlaylistGPUEvaluationArmed(true)
		s.radio.Wake()
		s.broadcastStateChangedThrottled()
		writeJSON(w, s.musicPlaylistState())
		return
	}
	if err := s.startMusicRadioSession(obsrtmp.RadioPublisherProfilePlaylistGPUEvaluation); err != nil {
		http.Error(w, "playlist GPU evaluationを開始できませんでした: "+err.Error(), http.StatusConflict)
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
		if s.radio.Running() {
			if err := s.prepareExplicitPlaylistGPUTransitionTo(video.PlaylistTransitionTrackChange, req.ID); err != nil {
				http.Error(w, "playlist GPU transition unavailable: "+err.Error(), http.StatusConflict)
				return
			}
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
		if err := s.startMusicRadioSession(obsrtmp.RadioPublisherProfileCPUDefault); err != nil {
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
	if err := s.prepareExplicitPlaylistGPUTransitionTo(video.PlaylistTransitionTrackChange, track.ID); err != nil {
		http.Error(w, "playlist GPU transition unavailable: "+err.Error(), http.StatusConflict)
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
	if err := s.prepareExplicitPlaylistGPUTransition(video.PlaylistTransitionStop); err != nil {
		http.Error(w, "playlist GPU transition unavailable: "+err.Error(), http.StatusConflict)
		return
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
	if err := s.prepareExplicitPlaylistGPUTransition(video.PlaylistTransitionTrackChange); err != nil {
		http.Error(w, "playlist GPU transition unavailable: "+err.Error(), http.StatusConflict)
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
	if err := s.prepareExplicitPlaylistGPUTransition(video.PlaylistTransitionStop); err != nil {
		http.Error(w, "playlist GPU transition unavailable: "+err.Error(), http.StatusConflict)
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
