package server

import (
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/video"
)

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

// startVideoToolInstall ensures video tools in the background, then enables
// video player mode. On failure it leaves video player mode OFF. While video
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
	s.toolInstallMu.Unlock()

	go func() {
		defer func() {
			s.toolInstallMu.Lock()
			s.toolInstalling = false
			s.toolInstallMu.Unlock()
		}()

		const maxRounds = 4
		for round := 0; round < maxRounds; round++ {
			if videoToolsReady() {
				s.commitVideoPlayerEnabled()
				return
			}
			if err := ensureVideoTools(); err == nil {
				s.commitVideoPlayerEnabled()
				return
			}
			time.Sleep(videoToolInstallBackoff(round))
		}
		// Exhausted: ensure the toggle is reverted to OFF.
		_ = settings.Update(func(a *settings.Settings) error {
			a.VideoPlayerEnabled = false
			a.MusicModeEnabled = false
			return nil
		})
	}()
}

// commitVideoPlayerEnabled persists video-player ON and runs the same
// side-effects the synchronous toggle did.
func (s *Server) commitVideoPlayerEnabled() {
	_ = settings.Update(func(a *settings.Settings) error {
		a.VideoPlayerEnabled = true
		return nil
	})
	if imagePath, current, ok := s.store.CurrentPath(); ok {
		s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
	}
	s.SyncOBSReceiver()
}
