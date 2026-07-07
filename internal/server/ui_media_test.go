package server

import (
	"strings"
	"testing"
)

func getIndexHTML(t *testing.T) string {
	t.Helper()
	return indexHTML
}

func TestUIContainsToolInstallOverlay(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="toolInstallOverlay"`,
		`id="toolInstallFill"`,
		`updateToolInstall(data.toolInstall)`,
		`function updateToolInstall(`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("served HTML missing %q", want)
		}
	}
}

func TestUIGenericToastNotificationBar(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="toast" role="status"`,
		`id="toastMessage"`,
		`id="toastCopyButton"`,
		`id="toastCloseButton"`,
		`.toast.active`,
		`.toast.error`,
		`.toast.error .toast-actions`,
		`function showToast(`,
		`function buildErrorReport(`,
		`connections: Array.isArray(obs.connections) ? obs.connections : []`,
		`lastToastErrorReport`,
		`Object.defineProperty(toast, 'textContent'`,
		`body.pairing-active .toast`,
		`document.body.classList.toggle('pairing-active'`,
		`showToast(syncFailureMessage(error), { error: true, source: 'sync' })`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("generic toast notification bar missing %q", want)
		}
	}
}

func TestUIYTDLPBotLoginPromptAndSettings(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="settingsButton"`,
		`id="settingsModal"`,
		`id="ytdlpLoginButton"`,
		`id="ytdlpCookieDeleteButton"`,
		`YoutubeがBOT認証エラーを起こしました。ログインを行うことでBOTではないことを証明できます。`,
		`ログインする`,
		`ログイン情報をこのアプリ用のCookieとして保存します。`,
		`/api/ytdlp/login`,
		`/api/ytdlp/cookies`,
		`isYTDLPBotError`,
		`isYTDLPFormatUnavailableError`,
		`YouTube側から動画/音声フォーマットが返っていません。ログインCookieは使えていますが、この動画はyt-dlpで取得できません。`,
		`ytdlpAuthInitialized`,
		`YouTubeログインCookieを保存しました`,
		`BOT認証エラーが出た時だけ使用してください。`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("yt-dlp login settings UI missing %q", want)
		}
	}
}

func TestUILiveSyncHandlesSSEReconnect(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`stateEvents.onopen = () => scheduleRefresh(50)`,
		`stateEvents.onerror = () => scheduleRefresh(1000)`,
		`stateEvents.addEventListener('state', () => scheduleRefresh(0))`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("live sync reconnect handling missing %q", want)
		}
	}
}

func TestUIHistoryPublishNavigatesToSourceMode(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function applyHistoryTargetMode(data)`,
		`const mode = data && data.historyTargetMode ? String(data.historyTargetMode) : ''`,
		`data.current.kind === 'video'`,
		`setMediaIntent('video')`,
		`setMediaIntent('image')`,
		`setUploadMode('link')`,
		`setUploadMode('file')`,
		`applyHistoryTargetMode(data)`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("history publish navigation missing %q", want)
		}
	}
	if strings.Contains(html, `if (mode === 'obs') {
        setUploadMode('obs');`) {
		t.Fatal("history publish must not switch saved OBS recordings back into live OBS mode")
	}
}

func TestUIModeSwitchRefreshesShareURLDisplay(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function renderShareURL(data)`,
		`function shareURLForCurrentMode(data)`,
		`function shareModeForUpload(data)`,
		`return latency.transport === 'rtspt' ? 'obs_rtsp' : 'obs_hls';`,
		`function shareURLForMode(data, mode)`,
		`const targets = data.shareTargets || {};`,
		`state.shareTargets = data.shareTargets || {};`,
		`return targets[mode] || { shareURL: data.shareURL || '', shareURLLabel: data.shareURLLabel || 'URL', obs: data.obs || {} };`,
		`function displayedShareURL()`,
		`const view = shareURLForCurrentMode(data)`,
		`text = displayedShareURL().shareURL || ''`,
		`body: JSON.stringify({ target, mode: shareModeForUpload(state) })`,
		`formData.set('shareMode', shareModeForUpload(state))`,
		`shareMode: shareModeForUpload(state)`,
		`renderShareURL(state)`,
		`setUploadMode('file')`,
		`setUploadMode('link')`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("mode switch share URL reset missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`state.shareURL = ''`,
		`state.shareURLLabel = 'URL'`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("mode switch must not destroy saved OBS share URL: %q", forbidden)
		}
	}
	if strings.Contains(html, `function mediaShareURL(data)`) || strings.Contains(html, `function fileShareURL(data)`) {
		t.Fatal("browser UI must use server-resolved shareTargets instead of reimplementing URL selection")
	}
}

func TestUIPreviewControllerIsWired(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`const PreviewController = (() => {`,
		`function initPreviewController(nextDeps)`,
		`function renderPreviewController(data, context)`,
		`function setPreviewVisibleController(visible)`,
		`PreviewController.setVisible(`,
		`PreviewController.init({`,
		`PreviewController.render(data, {`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("PreviewController wiring missing %q", want)
		}
	}
}

func TestUIPreviewControllerOwnsHLSLifecycle(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`let previewHLS = null`,
		`function destroyPreviewHLS()`,
		`window.Hls && window.Hls.isSupported()`,
		`if (video.canPlayType('application/vnd.apple.mpegurl'))`,
		`PreviewController.render(data, {`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("PreviewController HLS lifecycle missing %q", want)
		}
	}
	hlsIndex := strings.Index(html, `window.Hls && window.Hls.isSupported()`)
	nativeIndex := strings.Index(html, `video.canPlayType('application/vnd.apple.mpegurl')`)
	if hlsIndex < 0 || nativeIndex < 0 || hlsIndex > nativeIndex {
		t.Fatal("HLS.js must be preferred before native HLS sniffing")
	}
}

func TestUIPreviewControllerRendersPlayableVideo(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function sameOriginPreviewURL(value)`,
		`function mediaPreviewURL(data)`,
		`function addVideoPlayButton(video)`,
		`await video.play()`,
		`preview-play-button`,
		`state.previewVideoURL`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("playable video preview missing %q", want)
		}
	}
	if strings.Contains(html, `video.muted = true;
          video.preload = 'metadata';`) {
		t.Fatal("regular video preview must not force mute before user playback")
	}
}

func TestUIPreviewControllerOwnsAllPreviewModes(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function renderImage(data, nextCurrentID)`,
		`function renderEmpty()`,
		`function renderIngestProgress(data)`,
		`function renderVideoProgress(data)`,
		`function renderOBSPreview(data, context)`,
		`preview.classList.add('obs-preview')`,
		`preview.classList.remove('obs-preview')`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("PreviewController mode rendering missing %q", want)
		}
	}
}

func TestUIUploadControllerIsWired(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`const UploadController = (() => {`,
		`function initUploadController(nextDeps)`,
		`function setUploadModeController(mode)`,
		`function setMediaIntentController(intent)`,
		`UploadController.init({`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("UploadController wiring missing %q", want)
		}
	}
}

func TestUIUploadControllerOwnsUploadProgress(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="uploadProgressPanel"`,
		`id="uploadProgressFill"`,
		`id="uploadProgressText"`,
		`function beginLocalUploadProgress(file, action)`,
		`function updateLocalUploadProgress(loaded, total, title)`,
		`function finishLocalUploadProgress()`,
		`function renderUploadProgressController(percent, detail, title)`,
		`uploadProgressPanel.classList.add('open')`,
		`uploadProgressPanel.classList.remove('open')`,
		`localUploadActive = true`,
		`localUploadActive = false`,
		`UploadController.beginLocalUploadProgress(selectedUploadFileName(), action)`,
		`UploadController.updateLocalUploadProgress(event.loaded, event.total, title)`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("UploadController progress behavior missing %q", want)
		}
	}
	if strings.Contains(html, `PreviewController.renderIngest('uploading'`) {
		t.Fatal("local upload progress must render in the upload area, not the preview box")
	}
}

func TestUIHistoryControllerIsWired(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`const HistoryController = (() => {`,
		`function renderHistoryController(data)`,
		`function publishHistoryItem(id)`,
		`function toggleFavoriteHistoryItem(id, favorite)`,
		`function queueHistoryItem(id)`,
		`HistoryController.render(state)`,
		`HistoryController.publishHistoryItem(publish.dataset.historyPublish)`,
		`lastHistoryRenderSignature`,
		`if (signature === lastHistoryRenderSignature) return;`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("HistoryController wiring missing %q", want)
		}
	}
}

func TestUISettingsControllerIsWired(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="settingsButton" title="設定" aria-label="設定"`,
		`id="quitHeaderButton" title="終了" aria-label="終了"`,
		`id="quitButton" title="サーバーアプリ本体を終了します"`,
		`class="settings-button icon-only-button"`,
		`class="quit-header-button icon-only-button"`,
		`viewBox="0 0 24 24"`,
		`const quitButtons = [document.getElementById('quitHeaderButton'), document.getElementById('quitButton')].filter(Boolean);`,
		`quitButtons.forEach((quitButton) => {`,
		`apiFetch('/api/quit', { method: 'POST' })`,
		`const SettingsController = (() => {`,
		`function openSettingsController()`,
		`function openPhoneConnectController()`,
		`function applyThemePreferenceController(preference, persist)`,
		`SettingsController.openSettings()`,
		`SettingsController.openPhoneConnect()`,
		`SettingsController.applyThemePreference(button.dataset.themeChoice, true)`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("SettingsController wiring missing %q", want)
		}
	}
	for _, uniqueID := range []string{`id="quitHeaderButton"`, `id="quitButton"`} {
		if count := strings.Count(html, uniqueID); count != 1 {
			t.Fatalf("settings UI should render %s once, got %d", uniqueID, count)
		}
	}
	if count := strings.Count(html, `const quitButton = document.getElementById('quitButton');`); count != 1 {
		t.Fatalf("settings UI should declare quitButton once, got %d", count)
	}
}

func TestUIRendersVideoPreviewElement(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`data.current.kind === 'video'`,
		`function sameOriginPreviewURL(value)`,
		`function mediaPreviewURL(data)`,
		`const videoPreviewURL = mediaPreviewURL(data)`,
		`params.set('token', pageToken)`,
		`function addVideoPlayButton(video)`,
		`await video.play()`,
		`preview-play-button`,
		`const video = document.createElement('video')`,
		`attachPreviewHLS(video, videoPreviewURL)`,
		`state.previewVideoURL = videoPreviewURL`,
		`const existingVideo = preview.querySelector('video')`,
		`state.previewMode = 'video-waiting'`,
		`deps.scheduleRefresh(500)`,
		`.preview video`,
		`box-sizing: border-box`,
		`min-width: 0`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("video preview rendering missing %q", want)
		}
	}
	if strings.Contains(html, `preview.innerHTML = '<div class="empty">動画をHLSとして配信できます</div>';`) {
		t.Fatal("video preview must render a video element when an HLS/MP4 URL is available")
	}
	hlsIndex := strings.Index(html, `if (window.Hls && window.Hls.isSupported())`)
	nativeIndex := strings.Index(html, `if (video.canPlayType('application/vnd.apple.mpegurl'))`)
	if hlsIndex < 0 || nativeIndex < 0 || hlsIndex > nativeIndex {
		t.Fatal("HLS.js must be preferred before native HLS sniffing for Chromium previews")
	}
	if strings.Contains(html, `}
      destroyPreviewHLS();
      preview.classList.remove('obs-preview');`) {
		t.Fatal("regular video preview must not destroy the HLS instance on every state refresh")
	}
	if strings.Contains(html, `video.muted = true;
          video.preload = 'metadata';`) {
		t.Fatal("regular video preview must not force mute before user playback")
	}
}

func TestUIThemePreferenceSupportsSystemLightDark(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`data-theme-choice="system"`,
		`data-theme-choice="light"`,
		`data-theme-choice="dark"`,
		`imagepad:themePreference`,
		`prefers-color-scheme: dark`,
		`function applyThemePreference`,
		`document.documentElement.dataset.theme`,
		`:root[data-theme="dark"]`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("theme preference UI missing %q", want)
		}
	}
}

func TestUIShowsRTSPPublicationFailureWithoutCopyingMessage(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function shareURLDisplayText(data)`,
		`公開URLは未取得です: `,
		`if (id === 'shareURL')`,
		`text = displayedShareURL().shareURL || ''`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("RTSP publication failure display/copy guard missing %q", want)
		}
	}
}

func TestUIRendersIngestPhase(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{"ingestPhase", "受信中", "ダウンロード中", "解析中", "function renderIngestPreview(", "動画をダウンロード中...", "progressPercent", "progressText"} {
		if !strings.Contains(html, want) {
			t.Errorf("UI page missing %q", want)
		}
	}
	ingestIndex := strings.Index(html, `const ingestLabel = data.ingest && data.ingest.active ? ingestPhaseLabel(data.ingest.phase) : '';`)
	emptyIndex := strings.Index(html, `if (!data.current)`)
	if ingestIndex < 0 || emptyIndex < 0 {
		t.Fatalf("missing ingest/current preview guards: ingest=%d empty=%d", ingestIndex, emptyIndex)
	}
	if ingestIndex > emptyIndex {
		t.Fatal("ingest progress must render before empty-current preview fallback")
	}
	for _, want := range []string{
		`const linkDownload = uploadMode === 'link';`,
		`renderIngestPreview('downloading', pendingURL, 0, '', true);`,
		`scrollProgressIntoView();`,
		`mobileProgressFill.classList.remove('indeterminate');`,
		`'ingest:' + phase + ':' + pct + ':' + detail`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("link download immediate progress missing %q", want)
		}
	}
}

func TestUIOffersBrowserMediaCandidateDialogAfterLinkFailure(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="mediaCandidateDialog"`,
		`id="mediaCandidateList"`,
		`ページ内動画候補`,
		`function findBrowserMediaCandidates(`,
		`function openMediaCandidateDialog(`,
		`function retryLinkCandidate(`,
		`/api/browser-media-candidates`,
		`await maybeOfferBrowserMediaCandidates(action, error)`,
		`uploadFromLink(action, candidate.url)`,
		`function isKnownYTDLPPageURL(`,
		`if (isKnownYTDLPPageURL(pageURL)) return false;`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("browser media candidate UI missing %q", want)
		}
	}
}

func TestVideoPlayerEnabledMediaCopy(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{"画像/音声/動画", "メディアアップロード", "画像、RAW、音声、動画"} {
		if !strings.Contains(html, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestVideoPlayerEnabledModeRemoveAccept(t *testing.T) {
	html := getIndexHTML(t)
	if !strings.Contains(html, `data.enabled ? '' : imageAccept`) {
		t.Fatal("enabled mode should use empty string for accept (allow all media)")
	}
}

func TestVideoPlayerDisabledModeRestoresAccept(t *testing.T) {
	html := getIndexHTML(t)
	if !strings.Contains(html, `imageAccept = 'image/png,image/jpeg,image/gif,image/webp,image/avif,image/heic,image/heif,image/jxl,image/bmp`) {
		t.Fatal("imageAccept should contain image/RAW types for disabled mode")
	}
	if !strings.Contains(html, `data.enabled ? '' : imageAccept`) {
		t.Fatal("disabled mode should restore imageAccept via ternary")
	}
}

func TestUIContainsModernImageAcceptTypes(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`image/avif`,
		`image/heic`,
		`image/heif`,
		`image/jxl`,
		`.avif`,
		`.heic`,
		`.heif`,
		`.jxl`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("modern image accept list missing %q", want)
		}
	}
}

func TestUIContainsImagePresetControls(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="formatSelect"`,
		`id="qualitySelect"`,
		`name="maxDimension"`,
		`name="maxMB"`,
		`qualityOptions`,
		`updateUploadControlsVisibility`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("image preset controls missing %q", want)
		}
	}
}

func TestVideoAndMusicModeTogglesAreNotInSettings(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="musicIntentButton" data-media-intent="music"`,
		`syncLegacyMusicMode(mediaIntent === 'music')`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("upload music mode wiring is missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`id="videoPlayerToggle"`,
		`id="videoPlayerText"`,
		`id="musicModeRow"`,
		`id="musicModeToggle"`,
		`id="musicModeText"`,
		`<strong>ミュージックモード</strong>`,
		`videoPlayerToggle.addEventListener('change'`,
		`musicModeToggle.addEventListener('change'`,
		`musicModeRow.hidden = !musicWorkspaceEnabled || !data.enabled`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("settings must not expose video/music mode control %q", forbidden)
		}
	}
}

func TestMusicWorkspaceModeMenu(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`const musicWorkspaceEnabled = true`,
		`id="musicIntentButton" data-media-intent="music"`,
		`syncLegacyMusicMode(mediaIntent === 'music')`,
		`function syncLegacyMusicMode(enabled)`,
		`apiFetch('/api/music-mode'`,
		`if (mediaIntent === 'music') {
        MusicController.render({ active: true });
      }`,
		`return 'ミュージックHLSを生成';`,
		`MusicController.init({`,
		`MusicController.render({`,
		`mediaIntent = intent === 'music' && musicWorkspaceEnabled`,
		`PreviewController.setVisible(!active || mode === 'single')`,
		`.preview-panel[hidden]`,
		`obsModeButton.hidden = !state.videoPlayerEnabled || mediaIntent !== 'video'`,
		`modeTabs.classList.toggle('has-obs', !!state.videoPlayerEnabled && mediaIntent === 'video')`,
		// シングル/プレイリストの2モードメニュー。
		`id="musicModeMenu"`,
		`data-music-mode-choice="single"`,
		`data-music-mode-choice="playlist"`,
		`music-caret`,
		`musicModeMenu.addEventListener('click'`,
		`event.target.closest('[data-music-mode-choice]')`,
		// プレイリストモードでも従来のアップロードUIを残し、追加先だけ切り替える。
		`MusicController.mode() === 'playlist' && uploadMode !== 'obs'`,
		`PlaylistController.addFromUploadForm(uploadMode)`,
		`return 'プレイリストに追加';`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("music menu/controller missing %q", want)
		}
	}
	if strings.Contains(html, `if (previewPanel) previewPanel.hidden = active`) {
		t.Fatal("MusicController must not directly hide the preview panel")
	}
	if strings.Contains(html, `uploadHeading.textContent = mediaIntent === 'video' ? '動画アップロード' : '画像アップロード'`) {
		t.Fatal("applyVideoPlayer must not overwrite music headings back to image")
	}
	for _, forbidden := range []string{
		// パーティーモードは v1.6.2 まで出さない。
		`data-music-mode-choice="party"`,
		`パーティーモード`,
		`ミュージック機能はGUI準備中です`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("music menu must not expose %q", forbidden)
		}
	}
}

func TestMusicWorkspacePlaylistUI(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`シングル`,
		`ミュージックHLSを生成`,
		`https://example.com/music.mp3`,
		`qualityRow.hidden = uploadMode === 'obs' || (mediaIntent !== 'video' && mediaIntent !== 'music')`,
		`qualityRow.hidden = protectedMode || (mediaIntent !== 'video' && mediaIntent !== 'music')`,
		`qualityRow.classList.toggle('standalone', mediaIntent === 'video' || mediaIntent === 'music')`,
		`.quality-row.standalone`,
		`/api/video-quality`,
		// フラット（Apple Music 風）プレイリストパネル。右カラムのプレビュー位置に置く。
		`id="musicPlaylistPanel"`,
		`id="plVideoPreview"`,
		`id="plVideoEmpty"`,
		`id="plNowTitle"`,
		`id="plProgressFill"`,
		`id="plPlayButton"`,
		`id="plStopButton"`,
		`id="plNextButton"`,
		`id="plShuffleButton"`,
		`id="plLoopButton"`,
		`id="plTrackList"`,
		`id="plUrlModeHLS"`,
		`id="plUrlModeRTSP"`,
		`id="plShareUrl"`,
		`id="plMenuButton"`,
		`id="plSaveButton"`,
		`PlaylistController.init()`,
		`apiFetch('/api/music/playlist'`,
		`/api/music/playlist/add`,
		`/api/music/playlist/reorder`,
		`/api/music/playlist/play`,
		`/api/music/playlist/pause`,
		`/api/music/playlist/seek`,
		`pl-row-progress`,
		`/api/music/playlists/load`,
		`'/radio/index.m3u8'`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("playlist UI missing %q", want)
		}
	}
	for _, forbidden := range []string{
		// v1.6.2 のパーティーモードと、撤去済みの旧モックUI。
		`パーティーモード`,
		`YouTubeプレイリストURL`,
		`musicPlayerPlayButton`,
		`musicPlayerStopButton`,
		`musicPlayerRepeatButton`,
		`musicPlayerShuffleButton`,
		`aria-label="曲順をドラッグして変更"`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("playlist UI must not expose %q", forbidden)
		}
	}
	if strings.Contains(html, `ミュージックHLSアドレス`) {
		t.Fatal("music mode must not show the old music HLS address block")
	}
}

func TestOBSConnectionDetailsUIAndUnifiedRTSPURL(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="obsLatencyDetailButton"`,
		`id="obsConnectionsDialog"`,
		`IP</th>`,
		`プロトコル</th>`,
		`機種</th>`,
		`状態</th>`,
		`品質</th>`,
		`推定ラグ</th>`,
		`renderOBSConnections`,
		`https://cdn.jsdelivr.net/npm/hls.js@1/dist/hls.min.js`,
		`function attachPreviewHLS(video, src)`,
		`window.Hls && window.Hls.isSupported()`,
		`obsPreviewURL !== obsMediaURL`,
		`最高画質HLS（10s+）`,
		`高画質HLS（5s）`,
		`低遅延RTSP（3-4s）`,
		`超低遅延RTSP（1-2s）`,
		`リアルタイムRTSP（0.5s+）`,
		`pendingOBSAutoCopy = true`,
		`function maybeAutoCopyOBSURL(data)`,
		`const view = shareURLForMode(data, shareModeForUpload(data))`,
		`maybeAutoCopyOBSURL(data)`,
		`copyURLOnPC('shareURL')`,
		`配信用URLをPCにコピーしました`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("OBS connection detail UI missing %q", want)
		}
	}
	for _, forbidden := range []string{
		`id="obsRtspt"`,
		`id="obsRtsptURL"`,
		`id="obsRtsptCopy"`,
		`id="obsLatencyStatus"`,
		`id="obsDVRToggle"`,
		`DVR 30min`,
		`低遅延（LHLS, 実験）`,
		`超低遅延（LL-HLS, 実験）`,
		`リアルタイム（RTSPT, PC専用）`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("old dedicated RTSPT URL UI remains: %q", forbidden)
		}
	}
}

func TestBrowserCookieSourceIsNotExposed(t *testing.T) {
	html := getIndexHTML(t)
	for _, forbidden := range []string{
		`navigator.brave`,
		`/api/browser-cookie-source`,
		`--cookies-from-browser`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("frozen browser cookie integration remains in UI: %q", forbidden)
		}
	}
}
