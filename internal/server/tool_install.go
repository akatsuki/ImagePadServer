package server

import (
	"errors"
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

var errVideoToolsUnavailable = errors.New("video_tools_unavailable")

// Seams for tests.
var (
	videoToolsReady  = video.ToolsReady
	ensureVideoTools = func() error {
		if _, err := video.EnsureFFmpeg(); err != nil {
			return err
		}
		_, err := video.EnsureFFprobe()
		return err
	}
	// videoToolInstallBackoff is the wait between retry rounds; overridable in tests.
	videoToolInstallBackoff = func(round int) time.Duration {
		return time.Duration(round+1) * 2 * time.Second
	}
)

func (s *Server) toolInstallingNow() bool {
	s.toolInstallMu.Lock()
	defer s.toolInstallMu.Unlock()
	return s.toolInstalling
}

func (s *Server) videoToolInstallErrorNow() string {
	s.toolInstallMu.Lock()
	defer s.toolInstallMu.Unlock()
	return s.toolInstallError
}

// StartVideoToolInstall starts the server-owned video dependency lifecycle
// when the persisted user intent is on and the dependencies are not ready.
// Keeping this entrypoint on Server makes startup and the UI retry path share
// the same state and completion side effects.
func (s *Server) StartVideoToolInstall() {
	if !s.videoPlayerEnabled() || videoToolsReady() {
		return
	}
	s.startVideoToolInstall()
}

// startVideoToolInstall ensures video tools in the background, then completes
// video player activation. A failed install leaves the user's video-player
// intent enabled so the UI can show the failure and offer a retry. While video
// player mode is intended-on but tools are missing, it keeps retrying with
// backoff so transient failures self-heal. Idempotent: a second call while one
// is running is a no-op.
func (s *Server) startVideoToolInstall() {
	s.toolInstallMu.Lock()
	if s.toolInstalling {
		s.toolInstallMu.Unlock()
		return
	}
	s.toolInstalling = true
	s.toolInstallError = ""
	s.toolInstallMu.Unlock()
	s.broadcastStateChanged()

	go func() {
		defer func() {
			s.toolInstallMu.Lock()
			s.toolInstalling = false
			s.toolInstallMu.Unlock()
			s.broadcastStateChanged()
		}()

		const maxRounds = 4
		for round := 0; round < maxRounds; round++ {
			if videoToolsReady() {
				s.toolInstallMu.Lock()
				s.toolInstallError = ""
				s.toolInstallMu.Unlock()
				s.commitVideoPlayerEnabled()
				return
			}
			if err := ensureVideoTools(); err == nil {
				s.toolInstallMu.Lock()
				s.toolInstallError = ""
				s.toolInstallMu.Unlock()
				s.commitVideoPlayerEnabled()
				return
			} else {
				s.toolInstallMu.Lock()
				s.toolInstallError = err.Error()
				s.toolInstallMu.Unlock()
			}
			s.broadcastStateChangedThrottled()
			if round == maxRounds-1 {
				break
			}
			time.Sleep(videoToolInstallBackoff(round))
		}
		// Exhausted: keep VideoPlayerEnabled=true. ToolInstallStatus carries the
		// failure details while the persisted intent remains available for retry.
	}()
}

// commitVideoPlayerEnabled persists video-player ON and runs the same
// side-effects the synchronous toggle did.
func (s *Server) commitVideoPlayerEnabled() {
	// The HTTP handler owns the persisted intent. Re-read it before applying
	// activation side effects so a worker that was released after a user
	// disabled the feature cannot silently turn it back on.
	appSettings, err := settings.Load()
	if err != nil || !appSettings.VideoPlayerEnabled {
		return
	}
	if imagePath, current, ok := s.store.CurrentPath(); ok {
		s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
	}
	s.SyncOBSReceiver()
}
