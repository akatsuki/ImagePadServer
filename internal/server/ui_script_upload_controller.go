package server

const dashboardScriptUploadController = `
    const UploadController = (() => {
      let deps = {};

      function initUploadController(nextDeps) {
        deps = nextDeps || {};
      }

      function setUploadModeController(mode) {
        setUploadMode(mode);
      }

      function setMediaIntentController(intent) {
        setMediaIntent(intent);
      }

      function beginLocalUploadProgress(file, action) {
        localUploadActive = true;
        if (deps.toast) {
          deps.toast.textContent = action === 'queue' ? '動画変換に追加中...' : 'アップロード中...';
        }
        renderUploadProgressController(0, '送信準備中…', file || 'ファイル');
      }

      function updateLocalUploadProgress(loaded, total, title) {
        if (total > 0) {
          const pct = Math.max(1, Math.min(99, Math.round((loaded / total) * 100)));
          const detail = formatBytes(loaded) + ' / ' + formatBytes(total);
          if (deps.toast) deps.toast.textContent = 'アップロード中… ' + pct + '%';
          renderUploadProgressController(pct, detail, title || 'ファイル');
          return;
        }
        if (deps.toast) deps.toast.textContent = 'アップロード中…';
        renderUploadProgressController(6, '送信中…', title || 'ファイル');
      }

      function finishLocalUploadProgress() {
        localUploadActive = false;
        if (uploadProgressPanel) {
          uploadProgressPanel.classList.remove('open');
          uploadProgressPanel.hidden = true;
        }
      }

      function renderUploadProgressController(percent, detail, title) {
        if (!uploadProgressPanel) return;
        const pct = Math.max(0, Math.min(100, Number(percent || 0)));
        uploadProgressPanel.hidden = false;
        uploadProgressPanel.classList.add('open');
        if (uploadProgressTitle) {
          uploadProgressTitle.textContent = title || 'ファイル';
        }
        if (uploadProgressText) {
          uploadProgressText.textContent = detail || '送信中…';
        }
        if (uploadProgressFill) {
          uploadProgressFill.classList.remove('indeterminate');
          setProgressValue(uploadProgressFill.parentElement, uploadProgressFill, pct);
        }
      }

      return {
        init: initUploadController,
        setUploadMode: setUploadModeController,
        setMediaIntent: setMediaIntentController,
        beginLocalUploadProgress,
        updateLocalUploadProgress,
        finishLocalUploadProgress,
        renderUploadProgress: renderUploadProgressController
      };
    })();
`
