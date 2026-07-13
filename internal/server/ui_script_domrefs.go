package server

const dashboardScriptDomRefs = `
    const uploadForm = document.getElementById('uploadForm');
    const uploadButton = document.getElementById('uploadButton');
    const queueUploadButton = document.getElementById('queueUploadButton');
    const modeTabs = document.querySelector('.mode-tabs');
    const flowGrid = document.getElementById('flowGrid');
    const imageInput = document.getElementById('imageInput');
    const fileDropZone = document.getElementById('fileDropZone');
    const dropHint = document.getElementById('dropHint');
    const dropFileName = document.getElementById('dropFileName');
    const dragDropOverlay = document.getElementById('dragDropOverlay');
    const dragDropOverlayHint = document.getElementById('dragDropOverlayHint');
    const imageURLInput = document.getElementById('imageURLInput');
    const pasteURLButton = document.getElementById('pasteURLButton');
    const fileModeButton = document.getElementById('fileModeButton');
    const linkModeButton = document.getElementById('linkModeButton');
    const obsModeButton = document.getElementById('obsModeButton');
    const uploadHeading = document.getElementById('uploadHeading');
    const uploadKicker = document.getElementById('uploadKicker');
    const mediaKindSwitch = document.getElementById('mediaKindSwitch');
    const imageIntentButton = document.getElementById('imageIntentButton');
    const videoIntentButton = document.getElementById('videoIntentButton');
    const musicIntentButton = document.getElementById('musicIntentButton');
    const musicModeMenu = document.getElementById('musicModeMenu');
    const musicModeChoiceButtons = Array.from(document.querySelectorAll('[data-music-mode-choice]'));
    const fileUploadPanel = document.getElementById('fileUploadPanel');
    const linkUploadPanel = document.getElementById('linkUploadPanel');
    const obsUploadPanel = document.getElementById('obsUploadPanel');
    const uploadProgressPanel = document.getElementById('uploadProgressPanel');
    const uploadProgressTitle = document.getElementById('uploadProgressTitle');
    const uploadProgressText = document.getElementById('uploadProgressText');
    const uploadProgressFill = document.getElementById('uploadProgressFill');
    const uploadControls = uploadForm.querySelector('.controls');
    const imageTransformOptions = Array.from(document.querySelectorAll('.image-transform-option'));
    const formatSelect = document.getElementById('formatSelect');
    const qualitySelect = document.getElementById('qualitySelect');
    const preview = document.getElementById('preview');
    const previewPanel = document.getElementById('previewPanel');
    const musicPlaylistPanel = document.getElementById('musicPlaylistPanel');
    const plNowTitle = document.getElementById('plNowTitle');
    const plNowArtist = document.getElementById('plNowArtist');
    const plTimeElapsed = document.getElementById('plTimeElapsed');
    const plTimeRemaining = document.getElementById('plTimeRemaining');
    const plProgressTrack = document.getElementById('plProgressTrack');
    const plProgressFill = document.getElementById('plProgressFill');
    const plPlayButton = document.getElementById('plPlayButton');
    const plStartStreamButton = document.getElementById('plStartStreamButton');
    const plStopButton = document.getElementById('plStopButton');
    const plNextButton = document.getElementById('plNextButton');
    const plShuffleButton = document.getElementById('plShuffleButton');
    const plLoopButton = document.getElementById('plLoopButton');
    const plMenuButton = document.getElementById('plMenuButton');
    const plMenu = document.getElementById('plMenu');
    const plSaveButton = document.getElementById('plSaveButton');
    const plSavedList = document.getElementById('plSavedList');
    const plVideoWrap = document.getElementById('plVideoWrap');
    const plVideoPreview = document.getElementById('plVideoPreview');
    const plVideoEmpty = document.getElementById('plVideoEmpty');
    const plTrackList = document.getElementById('plTrackList');
    const plTrackCount = document.getElementById('plTrackCount');
    const plUrlModeHLS = document.getElementById('plUrlModeHLS');
    const plUrlModeRTSP = document.getElementById('plUrlModeRTSP');
    const plShareUrl = document.getElementById('plShareUrl');
    const previewHeading = document.getElementById('previewHeading');
    const previewKicker = document.getElementById('previewKicker');
    const videoInfoPanel = document.getElementById('videoInfoPanel');
    const historyList = document.getElementById('historyList');
    const wingTabButtons = Array.from(document.querySelectorAll('[data-wing-tab]'));
    const mobileProgress = document.getElementById('mobileProgress');
    const mobileProgressText = document.getElementById('mobileProgressText');
    const mobileProgressFill = document.getElementById('mobileProgressFill');
    const updateText = document.getElementById('updateText');
    const qualityMode = document.getElementById('qualityMode');
    const encoderMode = document.getElementById('encoderMode');
	const musicPlaylistDeliveryOption = document.getElementById('musicPlaylistDeliveryOption');
	const musicPlaylistDeliveryProfile = document.getElementById('musicPlaylistDeliveryProfile');
	const musicPlaylistDeliveryStatus = document.getElementById('musicPlaylistDeliveryStatus');
	const musicPlaylistCanonicalOption = document.getElementById('musicPlaylistCanonicalOption');
	const musicPlaylistCanonicalHeight = document.getElementById('musicPlaylistCanonicalHeight');
	const musicPlaylistCanonicalStatus = document.getElementById('musicPlaylistCanonicalStatus');
    const qualityStatus = document.getElementById('qualityStatus');
    const qualityRow = document.getElementById('videoQualityOptions');
    const networkCheckButton = document.getElementById('networkCheckButton');
    const clearButton = document.getElementById('clearButton');
    const obsKeyRotateButton = document.getElementById('obsKeyRotateButton');
    const obsLatencyOption = document.getElementById('obsLatencyOption');
    const obsLatencyMode = document.getElementById('obsLatencyMode');
    const obsLatencyDetailButton = document.getElementById('obsLatencyDetailButton');
    const rtspRiskDialog = document.getElementById('rtspRiskDialog');
    const rtspRiskCancel = document.getElementById('rtspRiskCancel');
    const rtspRiskConfirm = document.getElementById('rtspRiskConfirm');
    const obsKeyRiskDialog = document.getElementById('obsKeyRiskDialog');
    const obsKeyRiskCancel = document.getElementById('obsKeyRiskCancel');
    const obsKeyRiskConfirm = document.getElementById('obsKeyRiskConfirm');
    const obsKeyEditInput = document.getElementById('obsKeyEditInput');
    const phoneConnectButton = document.getElementById('phoneConnectButton');
    const phoneConnectDialog = document.getElementById('phoneConnectDialog');
    const phoneConnectCloseButton = document.getElementById('phoneConnectCloseButton');
    const settingsButton = document.getElementById('settingsButton');
    const quitButton = document.getElementById('quitButton');
    const settingsModal = document.getElementById('settingsModal');
    const settingsCloseButton = document.getElementById('settingsCloseButton');
    const themeText = document.getElementById('themeText');
    const themeButtons = Array.from(document.querySelectorAll('[data-theme-choice]'));
    const ytdlpLoginButton = document.getElementById('ytdlpLoginButton');
    const ytdlpCookieDeleteButton = document.getElementById('ytdlpCookieDeleteButton');
    const ytdlpCookieStatus = document.getElementById('ytdlpCookieStatus');
    const obsConnectionsDialog = document.getElementById('obsConnectionsDialog');
    const obsConnectionsClose = document.getElementById('obsConnectionsClose');
    const obsConnectionsTableBody = document.getElementById('obsConnectionsTableBody');
    const mediaCandidateDialog = document.getElementById('mediaCandidateDialog');
    const mediaCandidateList = document.getElementById('mediaCandidateList');
    const mediaCandidateClose = document.getElementById('mediaCandidateClose');
    const pairingPanel = document.getElementById('pairingPanel');
    const pairingPin = document.getElementById('pairingPin');
    const pairingDetail = document.getElementById('pairingDetail');
    let uploadMode = 'file';
    let mediaIntent = 'image';
    let legacyMusicModeSyncPending = false;
    let legacyMusicModeDesired = null;
    let refreshTimer = 0;
    let refreshInFlight = false;
    let refreshPromise = null;
    let refreshAgain = false;
    let phoneConnectAutoShown = false;
    let phoneProtectionActive = false;
    let lastAppliedStateSeq = 0;
    let localChangeChannel = null;
    let confirmedOBSLatencyMode = 'hls';
    let wingMode = 'history';
    let pendingOBSAutoCopy = false;
    let lastAutoCopiedOBSURL = '';
    let obsPreviewHLS = null;
    const themeMediaQuery = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;
    const imageAccept = 'image/png,image/jpeg,image/gif,image/webp,image/avif,image/heic,image/heif,image/jxl,image/bmp,image/tiff,image/svg+xml,image/x-sony-arw,image/x-canon-crw,image/x-canon-cr2,image/x-canon-cr3,image/x-panasonic-rw2,image/x-olympus-orf,image/x-fuji-raf,image/x-nikon-nef,image/x-nikon-nrw,image/x-sigma-x3f,image/x-adobe-dng,.jpg,.jpeg,.png,.gif,.webp,.avif,.heic,.heif,.jxl,.bmp,.tif,.tiff,.svg,.arw,.srf,.sr2,.crw,.cr2,.cr3,.rw2,.raw,.orf,.raf,.nef,.nrw,.x3f,.dng';
    const mediaAccept = imageAccept + ',video/*,video/mp4,video/quicktime,video/webm,video/x-matroska,.mp4,.mov,.m4v,.webm,.mkv,.avi';
    const musicWorkspaceEnabled = true;
    const rawExtensions = new Set(['.arw', '.srf', '.sr2', '.crw', '.cr2', '.cr3', '.rw2', '.raw', '.orf', '.raf', '.nef', '.nrw', '.x3f', '.dng']);
    const qualityOptions = {
      png: [
        ['lossless', '非劣化'],
        ['highest', '最高'],
        ['high', '高'],
        ['medium', '中'],
        ['low', '低']
      ],
      webp: [
        ['highest', '最高'],
        ['high', '高'],
        ['medium', '中'],
        ['low', '低'],
        ['lowest', '最低']
      ],
      jpeg: [
        ['highest', '最高'],
        ['high', '高'],
        ['medium', '中'],
        ['low', '低'],
        ['lowest', '最低']
      ]
    };
    let ffmpegPending = false;
    let ffmpegReady = false;
    let ffmpegPromise = null;
    let obsKeyVisible = false;
    let ytdlpAuthInitialized = false;
    let ytdlpAuthWasSaved = false;
    let localUploadActive = false;
    const modalFocusState = new WeakMap();
    const pageAdminToken = new URLSearchParams(window.location.search).get('token') || '';

    function visibleFocusableElements(root) {
      if (!root) return [];
      const selectors = [
        'a[href]',
        'area[href]',
        'button:not([disabled])',
        'input:not([disabled])',
        'select:not([disabled])',
        'textarea:not([disabled])',
        'summary',
        '[tabindex]:not([tabindex="-1"])'
      ].join(',');
      return Array.from(root.querySelectorAll(selectors)).filter((element) => {
        if (!(element instanceof HTMLElement)) return false;
        if (element.hidden || element.getAttribute('aria-hidden') === 'true') return false;
        const style = window.getComputedStyle(element);
        return style.visibility !== 'hidden' && style.display !== 'none';
      });
    }

    function openManagedModal(backdrop, preferredFocus, returnFocusElement) {
      if (!backdrop) return;
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      modalFocusState.set(backdrop, { returnFocus: returnFocusElement || active });
      backdrop.hidden = false;
      window.setTimeout(() => {
        const target = preferredFocus || visibleFocusableElements(backdrop)[0];
        if (target && typeof target.focus === 'function') target.focus();
      }, 0);
    }

    function closeManagedModal(backdrop, fallbackFocusElement) {
      if (!backdrop) return;
      backdrop.hidden = true;
      const state = modalFocusState.get(backdrop);
      modalFocusState.delete(backdrop);
      const target = (state && state.returnFocus && document.contains(state.returnFocus))
        ? state.returnFocus
        : fallbackFocusElement;
      if (target && typeof target.focus === 'function') {
        window.setTimeout(() => target.focus(), 0);
      }
    }

    function handleModalKeyboard(event, backdrop, onEscape) {
      if (!backdrop || backdrop.hidden) return false;
      if (event.key === 'Escape') {
        event.preventDefault();
        if (typeof onEscape === 'function') onEscape();
        return true;
      }
      if (event.key !== 'Tab') return false;
      const focusables = visibleFocusableElements(backdrop);
      if (focusables.length === 0) {
        event.preventDefault();
        return true;
      }
      const first = focusables[0];
      const last = focusables[focusables.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
        return true;
      }
      if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
        return true;
      }
      return false;
    }

    function setProgressValue(track, fill, percent) {
      const pct = Math.max(0, Math.min(100, Number(percent || 0)));
      if (fill) fill.style.width = Math.max(6, pct) + '%';
      if (track) {
        track.setAttribute('aria-valuemin', '0');
        track.setAttribute('aria-valuemax', '100');
        track.setAttribute('aria-valuenow', String(Math.round(pct)));
      }
    }

    function apiFetch(input, options = {}) {
      const init = { ...options };
      if (pageAdminToken) {
        const headers = new Headers(init.headers || {});
        if (!headers.has('X-ImagePad-Token')) headers.set('X-ImagePad-Token', pageAdminToken);
        init.headers = headers;
      }
      return fetch(input, init);
    }

    function apiUploadForm(input, formData, onProgress) {
      return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open('POST', input);
        if (pageAdminToken) xhr.setRequestHeader('X-ImagePad-Token', pageAdminToken);
        const uploadFile = formData && typeof formData.get === 'function' ? formData.get('image') : null;
        if (uploadFile && uploadFile.name) {
          xhr.setRequestHeader('X-ImagePad-Upload-Name', encodeURIComponent(uploadFile.name));
        }
        xhr.upload.onprogress = (event) => {
          if (typeof onProgress === 'function') onProgress(event);
        };
        xhr.upload.onload = () => {
          if (typeof onProgress === 'function') onProgress({ phase: 'received' });
        };
        xhr.onload = () => {
          if (typeof onProgress === 'function') onProgress({ phase: 'response' });
          const body = xhr.responseText || '';
          resolve({
            ok: xhr.status >= 200 && xhr.status < 300,
            status: xhr.status,
            text: async () => body,
            json: async () => JSON.parse(body || '{}')
          });
        };
        xhr.onerror = () => reject(new Error('アップロード送信に失敗しました。接続先とネットワークを確認してください'));
        xhr.onabort = () => reject(new Error('アップロードを中断しました'));
        xhr.send(formData);
      });
    }

    function syncFailureMessage(error) {
      const text = String((error && error.message) || error || '').trim();
      if (text.includes('admin access requires')) {
        return '状態の同期に失敗しました。管理画面は http://127.0.0.1:8080/ から開いてください（Tunnel の公開 URL では使えません）';
      }
      if (text.includes('Failed to fetch') || text.includes('NetworkError') || text.includes('Load failed')) {
        return '状態の同期に失敗しました。ImagePadServer が起動しているか、このタブのアドレスが正しいか確認してください';
      }
      if (text) {
        return '状態の同期に失敗しました: ' + text.slice(0, 140);
      }
      return '状態の同期に失敗しました';
    }
`
