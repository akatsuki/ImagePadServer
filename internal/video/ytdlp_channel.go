package video

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"imagepadserver/internal/settings"
)

// ytdlpAutoNightly records that a nightly fallback succeeded during this
// process, so subsequent downloads prefer nightly without re-running the
// failing stable path. It is process-scoped (not persisted) and resets on
// restart, matching the "auto" channel semantics.
var ytdlpAutoNightly atomic.Bool

// nightlyYTDLPResolver resolves the nightly binary (downloading it on demand).
// It is a var so tests can stub it without hitting the network.
var nightlyYTDLPResolver = EnsureNightlyYTDLP

// SetYTDLPAutoNightly marks the session as having auto-switched to nightly.
func SetYTDLPAutoNightly() { ytdlpAutoNightly.Store(true) }

// ResetYTDLPAutoNightlyForTest resets the session flag (tests only).
func ResetYTDLPAutoNightlyForTest() { ytdlpAutoNightly.Store(false) }

// effectiveYTDLPChannelFromSetting resolves the effective channel ("stable" or
// "nightly") from an already-loaded channel setting ("auto", "stable",
// "nightly"). Resolution order: IMAGEPAD_YTDLP_CHANNEL env → setting → session
// auto-switch flag. Defaults to "stable".
func effectiveYTDLPChannelFromSetting(normalizedSetting string) string {
	if ch := settings.NormalizeYTDLPChannel(os.Getenv("IMAGEPAD_YTDLP_CHANNEL")); ch == "stable" || ch == "nightly" {
		return ch
	}
	if ch := settings.NormalizeYTDLPChannel(normalizedSetting); ch == "stable" || ch == "nightly" {
		return ch
	}
	if ytdlpAutoNightly.Load() {
		return "nightly"
	}
	return "stable"
}

// EffectiveYTDLPChannel returns the effective yt-dlp channel ("stable" or
// "nightly") for a download.
func EffectiveYTDLPChannel() string {
	s, err := settings.Load()
	mode := "auto"
	if err == nil {
		mode = s.YTDLPChannel
	}
	return effectiveYTDLPChannelFromSetting(mode)
}

// CurrentYTDLPChannelSetting returns the persisted channel preference
// ("auto", "stable", "nightly"), normalized. Used by the server state API.
func CurrentYTDLPChannelSetting() string {
	s, err := settings.Load()
	if err != nil {
		return "auto"
	}
	return settings.NormalizeYTDLPChannel(s.YTDLPChannel)
}

// YTDLPChannelState returns the channel state for the /api/state payload.
func YTDLPChannelState() map[string]interface{} {
	s, err := settings.Load()
	mode := "auto"
	if err == nil {
		mode = settings.NormalizeYTDLPChannel(s.YTDLPChannel)
	}
	return map[string]interface{}{
		"mode":         mode,
		"current":      effectiveYTDLPChannelFromSetting(mode),
		"autoSwitched": ytdlpAutoNightly.Load(),
	}
}

// ResolveYTDLP returns the yt-dlp executable for a download. IMAGEPAD_YTDLP
// (an explicit path) wins; otherwise the effective channel selects the nightly
// or stable binary.
func ResolveYTDLP() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("IMAGEPAD_YTDLP")); configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured, nil
		}
		return "", fmt.Errorf("IMAGEPAD_YTDLP does not exist: %s", configured)
	}
	if EffectiveYTDLPChannel() == "nightly" {
		return EnsureNightlyYTDLP()
	}
	return EnsureYTDLP()
}
