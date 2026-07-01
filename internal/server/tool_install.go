package server

import (
	"log"
	"time"

	"imagepadserver/internal/settings"
	"imagepadserver/internal/toolchain"
)

// Seams for tests.
var (
	videoToolsReady  = toolchain.ToolsReady
	ensureVideoTools = func() error {
		if _, err := toolchain.EnsureFFmpeg(); err != nil {
			return err
		}
		_, err := toolchain.EnsureFFprobe()
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
				toolchain.ClearToolInstallStatus()
				s.commitVideoPlayerEnabled()
				return
			}
			if err := ensureVideoTools(); err == nil {
				toolchain.ClearToolInstallStatus()
				s.commitVideoPlayerEnabled()
				return
			}
			if round == maxRounds-1 {
				break
			}
			time.Sleep(videoToolInstallBackoff(round))
		}
		// Exhausted: ensure the toggle is reverted to OFF.
		if err := s.updateSettings(func(a *settings.Settings) error {
			a.VideoPlayerEnabled = false
			a.MusicModeEnabled = false
			return nil
		}); err != nil {
			log.Printf("failed to revert video-player settings after tool install failure: %v", err)
		}
	}()
}

// commitVideoPlayerEnabled persists video-player ON and runs the same
// side-effects the synchronous toggle did.
func (s *Server) commitVideoPlayerEnabled() {
	if err := s.updateSettings(func(a *settings.Settings) error {
		a.VideoPlayerEnabled = true
		return nil
	}); err != nil {
		log.Printf("failed to persist video-player settings after tool install: %v", err)
		return
	}
	if imagePath, current, ok := s.store.CurrentPath(); ok {
		s.enqueueStillConversion(imagePath, current.ID, current.OriginalName)
	}
	s.SyncOBSReceiver()
}
