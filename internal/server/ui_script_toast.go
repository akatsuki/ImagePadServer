package server

const dashboardScriptToast = `
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
      ytdlpAuth: null,
      ytdlpChannel: null,
      currentID: "",
      obsPreviewID: "",
      obsPreviewURL: "",
      previewMode: "empty"
    };

    const toast = document.getElementById('toast');
    const toastMessage = document.getElementById('toastMessage');
    const toastYTDLPLoginButton = document.getElementById('toastYTDLPLoginButton');
    const toastCopyButton = document.getElementById('toastCopyButton');
    const toastCloseButton = document.getElementById('toastCloseButton');
    const toastTextDescriptor = Object.getOwnPropertyDescriptor(Node.prototype, 'textContent');
    let toastTimer = null;
    let toastInternalUpdate = false;
    let lastToastErrorReport = '';
    function writeToastText(text) {
      if (!toastMessage && !toast) return;
      toastInternalUpdate = true;
      const target = toastMessage || toast;
      if (toastTextDescriptor && toastTextDescriptor.set) {
        toastTextDescriptor.set.call(target, text);
      } else {
        target.innerText = text;
      }
      toastInternalUpdate = false;
    }
    function showToast(message, options = {}) {
      if (!toast) return;
      const text = String(message || '');
      const visible = text.trim() !== '';
      const isError = !!options.error;
      const ytdlpBotError = isError && isYTDLPBotError(text);
      const ytdlpFormatUnavailable = isError && isYTDLPFormatUnavailableError(text);
      const displayText = ytdlpBotError
        ? 'YoutubeがBOT認証エラーを起こしました。ログインを行うことでBOTではないことを証明できます。'
        : (ytdlpFormatUnavailable
          ? 'YouTube側から動画/音声フォーマットが返っていません。ログインCookieは使えていますが、この動画はyt-dlpで取得できません。'
          : text);
      clearTimeout(toastTimer);
      writeToastText(displayText);
      if (!visible) {
        toast.classList.remove('active', 'error');
        delete toast.dataset.error;
        delete toast.dataset.errorSource;
        if (toastYTDLPLoginButton) toastYTDLPLoginButton.hidden = true;
        lastToastErrorReport = '';
        return;
      }
      toast.classList.add('active');
      toast.classList.toggle('error', isError);
      if (isError) {
        toast.dataset.error = '1';
        if (options.source) {
          toast.dataset.errorSource = String(options.source);
        } else {
          delete toast.dataset.errorSource;
        }
        if (toastYTDLPLoginButton) toastYTDLPLoginButton.hidden = !ytdlpBotError;
        lastToastErrorReport = buildErrorReport(displayText, (ytdlpBotError || ytdlpFormatUnavailable) ? { originalError: text } : (options.context || null));
      } else {
        delete toast.dataset.error;
        delete toast.dataset.errorSource;
        if (toastYTDLPLoginButton) toastYTDLPLoginButton.hidden = true;
        lastToastErrorReport = '';
      }
      const timeout = options.timeout === undefined ? (isError ? 0 : 4200) : options.timeout;
      if (timeout > 0) {
        toastTimer = setTimeout(() => showToast('', { timeout: 0 }), timeout);
      }
    }
    function hideToast() {
      showToast('', { timeout: 0 });
    }
    function isToastErrorMessage(value) {
      const text = String(value || '');
      return /失敗|Failed|Error|error|NetworkError|使えません|ありません|確認してください/.test(text);
    }
    function isYTDLPBotError(value) {
      const text = String(value || '').toLowerCase();
      return text.includes('yt-dlp') && (
        text.includes('sign in to confirm') ||
        text.includes('not a bot') ||
        text.includes('bot') ||
        text.includes('cookies') ||
        text.includes('cookie')
      );
    }
    function isYTDLPFormatUnavailableError(value) {
      const text = String(value || '').toLowerCase();
      return text.includes('yt-dlp') && text.includes('requested format is not available');
    }
    if (toast && toastTextDescriptor && toastTextDescriptor.set && toastTextDescriptor.get) {
      Object.defineProperty(toast, 'textContent', {
        get() { return toastTextDescriptor.get.call(toastMessage || this); },
        set(value) {
          if (toastInternalUpdate) {
            if (toastMessage) {
              toastTextDescriptor.set.call(toastMessage, value);
            } else {
              toastTextDescriptor.set.call(this, value);
            }
            return;
          }
          showToast(value, { error: this.dataset.error === '1' || isToastErrorMessage(value) });
        }
      });
    }
    function buildErrorReport(message, context) {
      const obs = state.obs || {};
      const latency = obs.latency || {};
      const current = state.current || (Array.isArray(state.history) ? state.history.find((item) => item && item.id === state.currentID) : null) || {};
      const share = displayedShareURL();
      const report = {
        app: 'ImagePadServer',
        generatedAt: new Date().toISOString(),
        pageURL: window.location.href,
        userAgent: navigator.userAgent,
        visibility: document.visibilityState,
        online: navigator.onLine,
        uploadMode,
        wingMode,
        error: {
          message: message,
          context: context || null
        },
        currentMedia: {
          id: state.currentID || '',
          name: current.name || '',
          kind: current.kind || '',
          mime: current.mime || '',
          url: current.url || ''
        },
        urls: {
          imageURL: state.imageURL || '',
          videoURL: state.videoURL || '',
          hlsURL: state.hlsURL || '',
          shareURL: share.shareURL || '',
          shareURLLabel: share.shareURLLabel || '',
          phoneURL: state.phoneURL || '',
          localImageURL: state.localImageURL || '',
          previewImageURL: state.previewImageURL || '',
          publicImageURL: state.publicImageURL || ''
        },
        preview: {
          mode: state.previewMode || '',
          obsPreviewID: state.obsPreviewID || ''
        },
        ingest: state.ingest || null,
        video: state.video || null,
        videoQuality: state.videoQuality || null,
        toolInstall: state.toolInstall || null,
        pairing: state.pairing || null,
        obs: {
          available: !!obs.available,
          connected: !!obs.connected,
          publishing: !!obs.publishing,
          serverAddress: obs.serverAddress || '',
          previewURL: obs.previewURL || '',
          publicHLSURL: obs.publicHLSURL || '',
          rtsptURL: obs.rtsptURL || '',
          latency: {
            mode: latency.mode || '',
            label: latency.label || '',
            target: latency.target || '',
            message: latency.message || ''
          },
          connections: Array.isArray(obs.connections) ? obs.connections : []
        }
      };
      return JSON.stringify(report, null, 2);
    }
    async function copyToastErrorReport() {
      if (!lastToastErrorReport) return;
      const original = toastCopyButton ? toastCopyButton.textContent : '';
      try {
        await copyText(lastToastErrorReport, toastMessage || toast);
        if (toastCopyButton) {
          toastCopyButton.textContent = 'コピーしました';
          setTimeout(() => { toastCopyButton.textContent = original || '診断情報をコピー'; }, 1500);
        }
      } catch (error) {
        if (toastCopyButton) {
          toastCopyButton.textContent = 'コピー失敗';
          setTimeout(() => { toastCopyButton.textContent = original || '診断情報をコピー'; }, 1500);
        }
      }
    }
    if (toastCopyButton) {
      toastCopyButton.addEventListener('click', copyToastErrorReport);
    }
    if (toastCloseButton) {
      toastCloseButton.addEventListener('click', hideToast);
    }`
