package server

import (
	"strings"
	"testing"
)

func TestUIContainsExclusiveOBSPlaylistAirPlayTabs(t *testing.T) {
	html := indexHTML
	obsTab := strings.Index(html, `id="obsModeButton"`)
	playlistTab := strings.Index(html, `id="playlistModeButton"`)
	airplayTab := strings.Index(html, `id="airplayModeButton"`)
	if obsTab < 0 || playlistTab < 0 || airplayTab < 0 || !(obsTab < playlistTab && playlistTab < airplayTab) {
		t.Fatal("Live input tabs must be ordered OBS, Playlist, AirPlay")
	}

	obsPanelStart := strings.Index(html, `id="obsUploadPanel"`)
	airplayPanelStart := strings.Index(html, `id="airplayUploadPanel"`)
	flowSecondaryStart := strings.Index(html, `<div class="flow-secondary">`)
	if obsPanelStart < 0 || airplayPanelStart < 0 || flowSecondaryStart < 0 || !(obsPanelStart < airplayPanelStart && airplayPanelStart < flowSecondaryStart) {
		t.Fatal("separate OBS and AirPlay tab panels are missing")
	}
	obsPanel := html[obsPanelStart:airplayPanelStart]
	airplayPanel := html[airplayPanelStart:flowSecondaryStart]
	if strings.Contains(obsPanel, `id="airplayCard"`) {
		t.Fatal("AirPlay controls must not be nested in the OBS panel")
	}
	for _, want := range []string{
		`role="tabpanel" aria-labelledby="airplayModeButton"`,
		`id="airplayCard"`,
		`id="airplayStartButton"`,
		`id="airplayEndButton"`,
		`id="airplayRetryButton"`,
		`id="airplayStatusText"`,
		`class="airplay-graphic"`,
	} {
		if !strings.Contains(airplayPanel, want) {
			t.Fatalf("AirPlay panel control is missing: %q", want)
		}
	}
	for _, want := range []string{
		`setUploadMode('airplay')`,
		`airplayUploadPanel.hidden = !airplayMode`,
		`airplayModeButton.setAttribute('aria-selected', String(airplayMode))`,
		`/api/airplay/start`,
		`/api/airplay/end`,
		`/api/airplay/retry`,
		`const connected = phase === "media-active";`,
		`const retryAvailable = phase === "delivery-failed"`,
		`iPhoneの画面を受信中です`,
		`.airplay-card`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("AirPlay UI wiring is missing: %q", want)
		}
	}
}

func TestUIHistoryNestsAirPlayPublisherRecordings(t *testing.T) {
	for _, want := range []string{
		`item.recordings && item.recordings.length`,
		`history-recordings`,
		`録画 ' + item.recordings.length + '件`,
		`recording.playbackURL`,
		`data-history-recording`,
		`target = '_blank'`,
		`recordings.addEventListener('click', (event) => event.stopPropagation());`,
		`recordingFailure.textContent = '利用不可'`,
		`recordings: (item.recordings || []).map`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Fatalf("nested AirPlay recording UI is missing %q", want)
		}
	}
}

func TestUILiveInputTabsSynchronizeRovingFocusAndKeyboardNavigation(t *testing.T) {
	expected := []string{
		`aria-controls="obsUploadPanel" tabindex="-1" hidden`,
		`aria-controls="musicPlaylistPanel" tabindex="-1" hidden>Playlist</button>`,
		`aria-controls="airplayUploadPanel" tabindex="-1" hidden`,
		`button.tabIndex = visible && selected ? 0 : -1;`,
		`button.tabIndex = playlistActive && selected ? 0 : -1;`,
		`bindInputTabKeyboardNavigation`,
		`event.key === 'ArrowLeft'`,
		`event.key === 'ArrowRight'`,
		`event.key === 'Home'`,
		`event.key === 'End'`,
		`target.focus();`,
		`target.click();`,
	}
	for _, snippet := range expected {
		if !strings.Contains(indexHTML, snippet) {
			t.Errorf(`missing roving-focus tab behavior: %s`, snippet)
		}
	}
}

func TestUILiveInputTabsStayOnOneRow(t *testing.T) {
	if !strings.Contains(indexHTML, `.mode-tabs.live-input {`) || !strings.Contains(indexHTML, `grid-template-columns: repeat(3, minmax(0, 1fr));`) {
		t.Fatal("LIVE input tabs must use a three-column layout")
	}
}

func TestUILeavingLiveInputResetsUploadMode(t *testing.T) {
	start := strings.Index(indexHTML, `function setMediaIntent(intent, syncLegacy = true, leaveLiveInput = true) {`)
	if start < 0 {
		t.Fatal("changing media intent must leave OBS/AirPlay input mode")
	}
	end := strings.Index(indexHTML[start:], `function applyVideoPlayer(data) {`)
	if end < 0 || !strings.Contains(indexHTML[start:start+end], `if (leaveLiveInput && isLiveInputMode(uploadMode)) {`) {
		t.Fatal("changing media intent must leave OBS/AirPlay input mode")
	}
	if !strings.Contains(indexHTML, `setMediaIntent(mediaIntent, false, false);`) {
		t.Fatal("video-player state refresh must preserve the selected LIVE input mode")
	}
}

func TestUILivePlaylistKeepsAirPlayTabVisible(t *testing.T) {
	if !strings.Contains(indexHTML, `[airplayModeButton, liveInput && videoEnabled`) {
		t.Fatal("AirPlay tab must remain available for LIVE Playlist input")
	}
	if strings.Contains(indexHTML, `[airplayModeButton, liveInput && mediaIntent === 'video' && videoEnabled`) {
		t.Fatal("AirPlay tab must not be limited to video intent because Playlist is also a LIVE input")
	}
	if !strings.Contains(indexHTML, `modeTabs.classList.toggle('live-input', liveInput);`) {
		t.Fatal("LIVE Playlist input tabs must use the three-column layout")
	}
}

func TestUIAirPlayVisibilityFollowsMediaNavigation(t *testing.T) {
	start := strings.Index(indexHTML, `function applyAirPlay(data) {`)
	if start < 0 {
		t.Fatal("AirPlay state handler is missing")
	}
	end := strings.Index(indexHTML[start:], `async function requestAirPlay(`)
	if end < 0 {
		t.Fatal("AirPlay state handler is missing")
	}
	handler := indexHTML[start : start+end]
	if strings.Contains(handler, `if (airplayModeButton) airplayModeButton.hidden = false;`) {
		t.Fatal("AirPlay state refresh must not show the tab outside LIVE input")
	}
	if !strings.Contains(handler, `updateMediaNavigation();`) {
		t.Fatal("AirPlay state refresh must reapply media-navigation visibility")
	}
}

func TestUIAirPlayStartShowsPendingStateBeforeWaitingForAPI(t *testing.T) {
	start := strings.Index(indexHTML, `async function requestAirPlay(path, successMessage) {`)
	if start < 0 {
		t.Fatal("AirPlay request handler is missing")
	}
	end := strings.Index(indexHTML[start:], `if (airplayStartButton) {`)
	if end < 0 {
		t.Fatal("AirPlay request handler boundary is missing")
	}
	handler := indexHTML[start : start+end]
	pending := strings.Index(handler, `airplayStatusText.textContent = "AirPlay受信を準備中です...";`)
	request := strings.Index(handler, `await apiFetch(path, { method: "POST", timeoutMs: 20000 });`)
	if pending < 0 || request < 0 || pending > request {
		t.Fatal("AirPlay start must show a pending state before waiting for the startup API")
	}
}

func TestUIAirPlayStartCannotRemainPendingForever(t *testing.T) {
	start := strings.Index(indexHTML, `async function requestAirPlay(path, successMessage) {`)
	if start < 0 {
		t.Fatal("AirPlay request handler is missing")
	}
	end := strings.Index(indexHTML[start:], `if (airplayStartButton) {`)
	if end < 0 {
		t.Fatal("AirPlay request handler boundary is missing")
	}
	handler := indexHTML[start : start+end]
	for _, want := range []string{
		`scheduleRefresh(100);`,
		`await apiFetch(path, { method: "POST", timeoutMs: 20000 });`,
		`requestError && requestError.name === "AbortError"`,
		`await refreshState(true);`,
		`airplayStartPending = false;`,
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("AirPlay request must recover from a stalled startup API: missing %q", want)
		}
	}
	for _, want := range []string{
		`const timeoutMs = Number(init.timeoutMs || 0);`,
		`delete init.timeoutMs;`,
		`const timeoutController = timeoutMs > 0 && !init.signal ? new AbortController() : null;`,
		`timeoutID = setTimeout(() => timeoutController.abort(), timeoutMs);`,
		`return request.finally(() => clearTimeout(timeoutID));`,
		`apiFetch('/api/state', { cache: 'no-store', timeoutMs: 8000 })`,
		`let airplayStartPending = false;`,
		`if (airplayStartPending && !data.running) {`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Fatalf("dashboard API requests must have bounded recovery: missing %q", want)
		}
	}
}

func TestUIAirPlayAbortWaitsForFreshStateAfterReleasingPending(t *testing.T) {
	start := strings.Index(indexHTML, `async function requestAirPlay(path, successMessage) {`)
	if start < 0 {
		t.Fatal("AirPlay request handler is missing")
	}
	end := strings.Index(indexHTML[start:], `if (airplayStartButton) {`)
	if end < 0 {
		t.Fatal("AirPlay request handler boundary is missing")
	}
	handler := indexHTML[start : start+end]
	requestError := strings.Index(handler, `let requestError = null;`)
	finallyBlock := strings.Index(handler, `finally {`)
	releasePending := strings.Index(handler, `airplayStartPending = false;`)
	freshState := strings.Index(handler, `await refreshState(true);`)
	if requestError < 0 || finallyBlock < 0 || releasePending < finallyBlock || freshState < releasePending {
		t.Fatal("AirPlay abort must release the pending state in finally before awaiting a fresh server state")
	}

	stateSyncStart := strings.Index(indexHTML, `async function refreshState(requireFresh = false) {`)
	if stateSyncStart < 0 {
		t.Fatal("state refresh must support a fresh-after-in-flight request")
	}
	stateSyncEnd := strings.Index(indexHTML[stateSyncStart:], `async function runRefreshState() {`)
	if stateSyncEnd < 0 {
		t.Fatal("state refresh boundary is missing")
	}
	stateSync := indexHTML[stateSyncStart : stateSyncStart+stateSyncEnd]
	for _, want := range []string{
		`const inFlight = refreshPromise;`,
		`if (!requireFresh) {`,
		`return inFlight;`,
		`await inFlight;`,
		`return refreshState();`,
	} {
		if !strings.Contains(stateSync, want) {
			t.Fatalf("fresh state recovery is missing %q", want)
		}
	}
}

func TestUIAirPlayAbortRequiresSuccessfulFreshStateAndMatchesRequestedAction(t *testing.T) {
	start := strings.Index(indexHTML, `async function requestAirPlay(path, successMessage) {`)
	if start < 0 {
		t.Fatal("AirPlay request handler is missing")
	}
	end := strings.Index(indexHTML[start:], `if (airplayStartButton) {`)
	if end < 0 {
		t.Fatal("AirPlay request handler boundary is missing")
	}
	handler := indexHTML[start : start+end]
	for _, want := range []string{
		`const isRetry = path.endsWith("/retry");`,
		`const expectsRunning = !path.endsWith("/end");`,
		`const refreshed = await refreshState(true);`,
		`if (!refreshed) {`,
		`const running = !!(state.airplay && state.airplay.running);`,
		`const reachedExpectedState = isRetry`,
		`? (running && phase !== "delivery-failed")`,
		`if (reachedExpectedState) {`,
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("AirPlay abort outcome must use a successful fresh state and the requested action: missing %q", want)
		}
	}

	stateSyncStart := strings.Index(indexHTML, `async function runRefreshState() {`)
	if stateSyncStart < 0 {
		t.Fatal("state refresh implementation is missing")
	}
	stateSyncEnd := strings.Index(indexHTML[stateSyncStart:], `function applyState(data) {`)
	if stateSyncEnd < 0 {
		t.Fatal("state refresh implementation boundary is missing")
	}
	stateSync := indexHTML[stateSyncStart : stateSyncStart+stateSyncEnd]
	if !strings.Contains(stateSync, `return true;`) || !strings.Contains(stateSync, `return false;`) {
		t.Fatal("state refresh must report whether a fresh state was obtained")
	}
}

func TestUIAirPlayRenderingUsesServerLifecyclePhase(t *testing.T) {
	start := strings.Index(indexHTML, `function applyAirPlay(data) {`)
	if start < 0 {
		t.Fatal("AirPlay state handler is missing")
	}
	end := strings.Index(indexHTML[start:], `async function requestAirPlay(`)
	if end < 0 {
		t.Fatal("AirPlay state handler boundary is missing")
	}
	handler := indexHTML[start : start+end]
	for _, want := range []string{
		`const phase = data.phase || "stopped";`,
		`const connected = phase === "media-active";`,
		`airplayCard.dataset.phase = phase;`,
	} {
		if !strings.Contains(handler, want) {
			t.Fatalf("AirPlay rendering must use the typed server lifecycle phase: missing %q", want)
		}
	}
	if strings.Contains(handler, `data.running && data.receiverRunning && data.bridgeRunning && state.obs && state.obs.connected`) {
		t.Fatal("AirPlay rendering must not independently reconstruct the server lifecycle phase")
	}
}

func TestAirPlayUIContainsQualityControlInsideConversionOptions(t *testing.T) {
	conversionStart := strings.Index(indexHTML, `<summary>変換オプション</summary>`)
	if conversionStart < 0 {
		t.Fatal("conversion options boundary is missing")
	}
	conversionEnd := strings.Index(indexHTML[conversionStart:], `</details>`)
	if conversionEnd < 0 {
		t.Fatal("conversion options boundary is missing")
	}
	conversion := indexHTML[conversionStart : conversionStart+conversionEnd]
	control := strings.Index(conversion, `id="airplayQualityMode"`)
	if control < 0 {
		t.Fatal("AirPlay quality select is missing from conversion options")
	}
	for _, option := range []string{
		`<option value="auto">Auto</option>`,
		`<option value="360">360p</option>`,
		`<option value="720">720p</option>`,
		`<option value="1080">1080p</option>`,
	} {
		if !strings.Contains(conversion[control:], option) {
			t.Fatalf("AirPlay quality option is missing: %q", option)
		}
	}
	if !strings.Contains(conversion, `id="airplayQualityRow"`) {
		t.Fatal("AirPlay quality row is missing from conversion options")
	}
	panelStart := strings.Index(indexHTML, `id="airplayUploadPanel"`)
	if panelStart < 0 {
		t.Fatal("AirPlay panel boundary is missing")
	}
	secondaryStart := strings.Index(indexHTML[panelStart:], `<div class="flow-secondary">`)
	if secondaryStart < 0 {
		t.Fatal("AirPlay panel boundary is missing")
	}
	panel := indexHTML[panelStart : panelStart+secondaryStart]
	if strings.Contains(panel, `id="airplayQualityMode"`) || strings.Contains(panel, `id="airplayQualityRow"`) {
		t.Fatal("AirPlay quality control must not remain inside the AirPlay panel")
	}
}

func TestAirPlayUIQualityStateSyncAndSaveRecovery(t *testing.T) {
	for _, want := range []string{
		`state.airplayQuality = data.airplayQuality || null;`,
		`function applyAirPlayQuality(data)`,
		`airplayQualityRow.hidden = uploadMode !== 'airplay';`,
		`airplayQualityMode.value = data.desiredMode;`,
		`data.activeHeight`,
		`data.restartRequired`,
		`次回のAirPlay開始時に適用します`,
		`/api/airplay/quality`,
		`airplayQualityPending = true;`,
		`const requestedMode = airplayQualityMode.value;`,
		`body: JSON.stringify({ mode: requestedMode })`,
		`await refreshState(true);`,
		`airplayQualityMode.disabled = airplayQualityPending;`,
		`finally`,
		`airplayQualityPending = false;`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Fatalf("AirPlay quality UI wiring is missing %q", want)
		}
	}
	if strings.Contains(indexHTML, `state.airplayQuality = previous;`) {
		t.Fatal("AirPlay quality save failure must not overwrite a newer synchronized state")
	}
}

func TestAirPlayQualityVisibilityUpdatesWhenUploadModeChanges(t *testing.T) {
	start := strings.Index(indexHTML, `function setUploadMode(mode) {`)
	if start < 0 {
		t.Fatal("upload mode handler is missing")
	}
	end := strings.Index(indexHTML[start:], `function uploadFromFile(action) {`)
	if end < 0 {
		t.Fatal("upload mode handler boundary is missing")
	}
	handler := indexHTML[start : start+end]
	if !strings.Contains(handler, `applyAirPlayQuality(state.airplayQuality);`) {
		t.Fatal("upload mode changes must refresh AirPlay quality visibility")
	}
}
