package server

const uiScriptState = `  <script>
    const state = {
      imageURL: {{printf "%q" .imageURL}},
      videoURL: {{printf "%q" .videoURL}},
      hlsURL: {{printf "%q" .hlsURL}},
      shareURL: {{printf "%q" .shareURL}},
      shareURLLabel: {{printf "%q" .shareURLLabel}},
      phoneURL: {{printf "%q" .phoneURL}},
      localImageURL: {{printf "%q" .localImageURL}},
      previewImageURL: {{printf "%q" .previewImageURL}},
      publicImageURL: {{printf "%q" .publicImageURL}},
      history: [],
      videoQueue: [],
      videoPlayerEnabled: false,
      musicModeEnabled: false,
      videoQuality: null,
      obs: null,
      pairing: null,
      currentID: "",
      obsPreviewID: "",
      previewMode: "empty"
    };

    const toast = document.getElementById('toast');
    const uploadForm = document.getElementById('uploadForm');
    const themeSelect = document.getElementById('themeSelect');
    const uploadButton = document.getElementById('uploadButton');
    const queueUploadButton = document.getElementById('queueUploadButton');
    const modeTabs = document.querySelector('.mode-tabs');
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
    const fileUploadPanel = document.getElementById('fileUploadPanel');
    const linkUploadPanel = document.getElementById('linkUploadPanel');
    const obsUploadPanel = document.getElementById('obsUploadPanel');
    const uploadControls = uploadForm.querySelector('.controls');
    const formatSelect = document.getElementById('formatSelect');
    const qualitySelect = document.getElementById('qualitySelect');
    const preview = document.getElementById('preview');
    const historyList = document.getElementById('historyList');
    const wingTabButtons = Array.from(document.querySelectorAll('[data-wing-tab]'));
    const mobileProgress = document.getElementById('mobileProgress');
    const mobileProgressText = document.getElementById('mobileProgressText');
    const mobileProgressFill = document.getElementById('mobileProgressFill');
    const videoPlayerToggle = document.getElementById('videoPlayerToggle');
    const videoPlayerText = document.getElementById('videoPlayerText');
    const musicModeRow = document.getElementById('musicModeRow');
    const musicModeToggle = document.getElementById('musicModeToggle');
    const musicModeText = document.getElementById('musicModeText');
    const updateText = document.getElementById('updateText');
    const qualityMode = document.getElementById('qualityMode');
    const qualityStatus = document.getElementById('qualityStatus');
    const qualityRow = document.querySelector('.quality-row');
    const networkCheckButton = document.getElementById('networkCheckButton');
    const clearButton = document.getElementById('clearButton');
    const obsKeyRotateButton = document.getElementById('obsKeyRotateButton');
    const obsLatencyMode = document.getElementById('obsLatencyMode');
    const obsLatencyStatus = document.getElementById('obsLatencyStatus');
    const obsDVRToggle = document.getElementById('obsDVRToggle');
    const pairingPanel = document.getElementById('pairingPanel');
    const pairingPin = document.getElementById('pairingPin');
    const pairingDetail = document.getElementById('pairingDetail');
    let uploadMode = 'file';
    let videoPlayerPending = false;
    let musicModePending = false;
    let refreshTimer = 0;
    let refreshInFlight = false;
    let refreshPromise = null;
    let refreshAgain = false;
    let lastAppliedStateSeq = 0;
    let localChangeChannel = null;
    let wingMode = 'history';
    const imageQualityOptions = {
      png: [
        { value: 'lossless', label: '非劣化', selected: true },
        { value: 'highest', label: '最高' },
        { value: 'high', label: '高' },
        { value: 'medium', label: '中' },
        { value: 'low', label: '低' },
        { value: 'lowest', label: '最低' }
      ],
      webp: [
        { value: 'highest', label: '最高' },
        { value: 'high', label: '高', selected: true },
        { value: 'medium', label: '中' },
        { value: 'low', label: '低' },
        { value: 'lowest', label: '最低' }
      ],
      jpeg: [
        { value: 'highest', label: '最高' },
        { value: 'high', label: '高', selected: true },
        { value: 'medium', label: '中' },
        { value: 'low', label: '低' },
        { value: 'lowest', label: '最低' }
      ]
    };
    const imageAccept = 'image/png,image/jpeg,image/gif,image/webp,image/bmp,image/tiff,image/svg+xml,image/x-sony-arw,image/x-canon-crw,image/x-canon-cr2,image/x-canon-cr3,image/x-panasonic-rw2,image/x-olympus-orf,image/x-fuji-raf,image/x-nikon-nef,image/x-nikon-nrw,image/x-sigma-x3f,image/x-adobe-dng,.jpg,.jpeg,.png,.gif,.webp,.bmp,.tif,.tiff,.svg,.arw,.srf,.sr2,.crw,.cr2,.cr3,.rw2,.raw,.orf,.raf,.nef,.nrw,.x3f,.dng';
    const mediaAccept = imageAccept + ',video/*,video/mp4,video/quicktime,video/webm,video/x-matroska,.mp4,.mov,.m4v,.webm,.mkv,.avi';
    const rawExtensions = new Set(['.arw', '.srf', '.sr2', '.crw', '.cr2', '.cr3', '.rw2', '.raw', '.orf', '.raf', '.nef', '.nrw', '.x3f', '.dng']);
    let ffmpegPending = false;
    let ffmpegReady = false;
    let ffmpegPromise = null;
    let obsKeyVisible = false;

    function applyTheme(choice) {
      const resolved = choice === 'dark' || (choice === 'auto' && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) ? 'dark' : 'light';
      document.documentElement.dataset.theme = resolved;
      document.documentElement.dataset.themeChoice = choice;
      if (themeSelect) themeSelect.value = choice;
    }

    function updateQualityOptions() {
      if (!formatSelect || !qualitySelect) return;
      const current = qualitySelect.value;
      const opts = imageQualityOptions[formatSelect.value] || imageQualityOptions.webp;
      qualitySelect.innerHTML = '';
      for (const opt of opts) {
        const option = document.createElement('option');
        option.value = opt.value;
        option.textContent = opt.label;
        option.selected = current ? current === opt.value : !!opt.selected;
        qualitySelect.appendChild(option);
      }
    }
    function updateUploadControlsVisibility() {
      if (!uploadControls) return;
      uploadControls.hidden = uploadMode === 'obs' || state.videoPlayerEnabled;
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

    async function refreshState() {
      if (refreshInFlight) {
        refreshAgain = true;
        return refreshPromise;
      }
      refreshInFlight = true;
      refreshPromise = runRefreshState();
      try {
        return await refreshPromise;
      } finally {
        refreshPromise = null;
      }
    }

    async function runRefreshState() {
      const seq = ++lastAppliedStateSeq;
      try {
        const res = await fetch('/api/state', { cache: 'no-store' });
        const body = await res.text();
        if (!res.ok) throw new Error(body || ('HTTP ' + res.status));
        const data = JSON.parse(body);
        if (seq === lastAppliedStateSeq) {
          applyState(data);
          if (toast && toast.dataset.error === '1') {
            toast.textContent = '';
            delete toast.dataset.error;
          }
        }
      } catch (error) {
        if (toast && !document.hidden) {
          toast.textContent = syncFailureMessage(error);
          toast.dataset.error = '1';
        }
      } finally {
        refreshInFlight = false;
        if (refreshAgain) {
          refreshAgain = false;
          scheduleRefresh(100);
        }
      }
    }

    function applyState(data) {
      state.imageURL = data.imageURL;
      state.videoURL = data.videoURL;
      state.hlsURL = data.hlsURL;
      state.shareURL = data.shareURL;
      state.shareURLLabel = data.shareURLLabel;
      state.phoneURL = data.phoneURL;
      state.localImageURL = data.localImageURL;
      state.previewImageURL = data.previewImageURL;
      state.publicImageURL = data.publicImageURL;
      state.history = data.history || [];
      state.videoQueue = data.videoQueue || [];
      state.videoQuality = data.videoQuality;
      state.obs = data.obs || null;
      state.pairing = data.pairing || null;
      state.videoPlayerEnabled = !!(data.videoPlayer && data.videoPlayer.enabled);
      state.musicModeEnabled = !!(data.videoPlayer && data.videoPlayer.musicModeEnabled);
      document.getElementById('phoneURL').textContent = data.phoneURL;
      document.getElementById('phoneURLMobile').textContent = data.phoneURL;
      document.getElementById('shareURL').textContent = data.shareURL || '公開URLは未取得です';
      document.getElementById('shareURLLabel').textContent = data.shareURLLabel || 'URL';
      document.getElementById('videoStatus').textContent = videoText(data.video);
      updateMobileProgress(data);
      updateToolInstall(data.toolInstall);
      applyQuality(data.videoQuality);
      applyVideoPlayer(data.videoPlayer);
      applyOBS(data.obs);
      applyPairing(data.pairing);
      applyOBSProtection();
      document.getElementById('upnpText').textContent = publicText(data.tunnel, data.upnp);
      document.getElementById('hasImage').textContent = currentText(data.current);
      const nextCurrentID = data.current ? data.current.id : "";
      renderPreview(data, nextCurrentID);
      renderHistory(state.history, nextCurrentID);
      state.currentID = nextCurrentID;

      scheduleRefresh((data.ingest && data.ingest.active) || (data.video && data.video.active) || (data.obs && data.obs.connected) ? 750 : 2000);
    }

    function resetOBSPreview() {
      if (state.previewMode === 'obs') {
        const video = preview.querySelector('video');
        if (video) {
          video.pause();
          video.removeAttribute('src');
          video.load();
        }
        preview.innerHTML = '<div class="empty">OBS preview restarting...</div>';
      }
      state.previewMode = 'obs-restarting';
      state.obsPreviewID = "";
    }

    function applyPairing(pairing) {
      if (!pairingPanel || !pairingPin || !pairingDetail) return;
      if (!pairing || !pairing.active || !pairing.pin) {
        pairingPanel.classList.remove('active');
        return;
      }
      pairingPin.textContent = pairing.pin;
      const name = pairing.deviceName || pairing.clientName || 'BrowserRelayStreamer';
      pairingDetail.textContent = name + ' が接続を要求しています。相手側のPCでこのコードを入力してください。';
      pairingPanel.classList.add('active');
    }

    function renderPreview(data, nextCurrentID) {
      if (uploadMode === 'obs' && data.obs && data.obs.connected && data.obs.previewURL) {
        const obsID = data.obs.mediaID || nextCurrentID;
        if (state.previewMode !== 'obs' || obsID !== state.obsPreviewID) {
          preview.classList.add('obs-preview');
          preview.innerHTML = '';
          const video = document.createElement('video');
          video.src = data.obs.previewURL;
          video.controls = true;
          video.autoplay = true;
          video.muted = true;
          video.playsInline = true;
          preview.appendChild(video);
          state.previewMode = 'obs';
          state.obsPreviewID = obsID;
        }
        state.currentID = obsID;
        return;
      }
      preview.classList.remove('obs-preview');
      state.obsPreviewID = "";
      if (!data.current) {
        if (state.previewMode !== 'empty') {
          preview.innerHTML = '<div class="empty">まだ画像が選択されていません</div>';
          state.previewMode = 'empty';
        }
        return;
      }
      const ingestLabel = data.ingest && data.ingest.active ? ingestPhaseLabel(data.ingest.phase) : '';
      if (ingestLabel) {
        // Rebuild only when the phase changes so the indeterminate sweep
        // animation isn't restarted on every poll.
        const mode = 'ingest:' + data.ingest.phase;
        if (state.previewMode !== mode) {
          const title = data.ingest.title ? escapeHTML(data.ingest.title) : '';
          preview.innerHTML =
            '<div class="progress-preview">' +
              '<div>' + escapeHTML(ingestLabel) + '</div>' +
              '<div class="progress-track" aria-label="処理状況">' +
                '<div class="progress-fill indeterminate"></div>' +
              '</div>' +
              (title ? '<div class="progress-detail">' + title + '</div>' : '') +
            '</div>';
          state.previewMode = mode;
        }
        return;
      }
      if (data.video && data.video.active) {
        const percent = Math.max(0, Math.min(99, Number(data.video.progressPercent || 0)));
        const detail = percent + '% / ' + (data.video.progressText || data.video.message || '変換中');
        preview.innerHTML =
          '<div class="progress-preview">' +
            '<div>動画プレーヤー向けに変換中です</div>' +
            '<div class="progress-track" aria-label="変換進捗">' +
              '<div class="progress-fill" style="width:' + Math.max(6, percent) + '%"></div>' +
            '</div>' +
            '<div class="progress-detail">' + escapeHTML(detail) + '</div>' +
          '</div>';
        state.previewMode = 'progress';
        return;
      }
      if (data.current.kind === 'video') {
        if (state.previewMode !== 'video' || nextCurrentID !== state.currentID) {
          preview.innerHTML = '<div class="empty">動画をHLSとして配信できます</div>';
          state.previewMode = 'video';
        }
        return;
      }
      if (state.previewMode !== 'image' || nextCurrentID !== state.currentID) {
        preview.innerHTML = '';
        const img = document.createElement('img');
        img.src = data.previewImageURL + (data.previewImageURL.includes('?') ? '&' : '?') + 'preview=1';
        img.alt = '現在公開中の画像';
        preview.appendChild(img);
        state.previewMode = 'image';
      }
    }

    function escapeHTML(value) {
      return String(value).replace(/[&<>"']/g, (char) => ({
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#39;'
      }[char]));
    }

    function setWingMode(mode) {
      wingMode = mode;
      for (const button of wingTabButtons) {
        const active = button.dataset.wingTab === mode;
        button.classList.toggle('active', active);
        button.setAttribute('aria-selected', String(active));
      }
      renderHistory(state.history, state.currentID);
    }

`
