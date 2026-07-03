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
		`setUploadMode('obs')`,
		`setUploadMode('link')`,
		`setUploadMode('file')`,
		`applyHistoryTargetMode(data)`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("history publish navigation missing %q", want)
		}
	}
}

func TestUIModeSwitchRefreshesShareURLDisplay(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`function renderShareURL(data)`,
		`function shareURLForCurrentMode(data)`,
		`function shareURLForMode(data, mode)`,
		`if (mode === 'obs')`,
		`if (mode === 'link')`,
		`if (mode === 'file')`,
		`function mediaShareURL(data)`,
		`function fileShareURL(data)`,
		`function displayedShareURL()`,
		`const view = shareURLForCurrentMode(data)`,
		`text = displayedShareURL().shareURL || ''`,
		`body: JSON.stringify({ target, mode: uploadMode })`,
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
	for _, want := range []string{"ingestPhase", "ダウンロード中", "解析中", "function renderIngestPreview(", "動画をダウンロード中...", "progressPercent", "progressText"} {
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

func TestMusicModeUIIsNestedUnderVideoPlayerMode(t *testing.T) {
	html := getIndexHTML(t)
	for _, want := range []string{
		`id="musicModeRow"`,
		`id="musicModeToggle"`,
		`ミュージックモード`,
		`fetch('/api/music-mode'`,
		`musicModeRow.hidden = !data.enabled`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("music mode UI is missing %q", want)
		}
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
		`const url = data && data.obs && data.obs.rtsptURL ? String(data.obs.rtsptURL) : ''`,
		`https://cdn.jsdelivr.net/npm/hls.js@1/dist/hls.min.js`,
		`function attachHLSPreview(video, src)`,
		`window.Hls && window.Hls.isSupported()`,
		`data.obs.previewURL !== state.obsPreviewURL`,
		`最高画質HLS（10s+）`,
		`高画質HLS（5s）`,
		`低遅延RTSP（3-4s）`,
		`超低遅延RTSP（1-2s）`,
		`リアルタイムRTSP（0.5s+）`,
		`pendingOBSAutoCopy = true`,
		`function publicOBSRTSPURL(data)`,
		`function maybeAutoCopyOBSURL(data)`,
		`maybeAutoCopyOBSURL(data)`,
		`copyURLOnPC('shareURL')`,
		`グローバルRTSP URLをPCにコピーしました`,
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
