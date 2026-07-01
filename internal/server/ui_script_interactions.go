package server

const uiScriptInteractions = `    fileDropZone.addEventListener('dragenter', (event) => {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      showGlobalDropOverlay();
      fileDropZone.classList.add('dragover');
    });
    fileDropZone.addEventListener('dragover', (event) => {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = 'copy';
      showGlobalDropOverlay();
      fileDropZone.classList.add('dragover');
    });
    fileDropZone.addEventListener('dragleave', (event) => {
      if (!fileDropZone.contains(event.relatedTarget)) {
        fileDropZone.classList.remove('dragover');
      }
    });
    fileDropZone.addEventListener('drop', handleFileDrop);
    uploadForm.addEventListener('drop', handleFileDrop);
    window.addEventListener('dragenter', (event) => {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      showGlobalDropOverlay();
    });
    window.addEventListener('dragover', (event) => {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = 'copy';
      showGlobalDropOverlay();
    });
    window.addEventListener('dragleave', (event) => {
      if (leavingWindow(event)) {
        hideGlobalDropOverlay();
      }
    });
    window.addEventListener('drop', (event) => {
      if (hasDroppedFiles(event)) {
        handleFileDrop(event);
      } else {
        hideGlobalDropOverlay();
      }
    });
    window.addEventListener('dragend', hideGlobalDropOverlay);
    window.addEventListener('blur', hideGlobalDropOverlay);
    imageInput.addEventListener('change', updateSelectedFileName);
    updateSelectedFileName();

    uploadForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      if (uploadMode === 'obs') {
        uploadButton.disabled = true;
        toast.textContent = 'OBS配信を開始中...';
        try {
          const res = await fetch('/api/obs/start', { method: 'POST' });
          if (!res.ok) throw new Error(await res.text());
          const data = await res.json();
          applyState(data);
          setUploadMode('obs');
          await copyStartedOBSURL(data);
          announceLocalChange();
          toast.textContent = data.obs && data.obs.connected ? 'OBS配信を公開しました' : 'OBS配信開始を予約しました';
        } catch (error) {
          toast.textContent = error.message || 'OBS配信開始に失敗しました';
        } finally {
          uploadButton.disabled = false;
        }
        return;
      }
      const action = event.submitter && event.submitter.value === 'queue' ? 'queue' : 'publish';
      if (uploadMode === 'file' && selectedRAWFile() && !await ensureFFmpegForRAWSelection()) {
        return;
      }
      uploadButton.disabled = true;
      queueUploadButton.disabled = true;
      toast.textContent = action === 'queue' ? '変換キューに追加中...' : 'アップロード中...';
      try {
        const res = uploadMode === 'link' ? await uploadFromLink(action) : await uploadFromFile(action);
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        announceLocalChange();
        scrollProgressIntoView();
        if (action === 'queue') {
          setWingMode('queue');
          toast.textContent = '変換キューに追加しました';
          return;
        }
        const isVideo = data.current && data.current.kind === 'video';
        if (isVideo) {
          toast.textContent = data.clipboardCopied ? '動画HLS変換を開始し、URLをPCにコピーしました' : '動画HLS変換を開始しました';
        } else {
          toast.textContent = data.clipboardCopied ? '公開画像を更新し、URLをPCにコピーしました' : '公開画像を更新しました';
        }
      } catch (error) {
        toast.textContent = error.message || 'アップロードに失敗しました';
      } finally {
        uploadButton.disabled = false;
        queueUploadButton.disabled = false;
      }
    });

    function setUploadMode(mode) {
      uploadMode = mode;
      const linkMode = mode === 'link';
      const obsMode = mode === 'obs';
      fileModeButton.classList.toggle('active', !linkMode && !obsMode);
      linkModeButton.classList.toggle('active', linkMode);
      obsModeButton.classList.toggle('active', obsMode);
      fileModeButton.setAttribute('aria-selected', String(!linkMode && !obsMode));
      linkModeButton.setAttribute('aria-selected', String(linkMode));
      obsModeButton.setAttribute('aria-selected', String(obsMode));
      fileUploadPanel.classList.toggle('active', !linkMode && !obsMode);
      linkUploadPanel.classList.toggle('active', linkMode);
      obsUploadPanel.classList.toggle('active', obsMode);
      updateUploadControlsVisibility();
      imageInput.required = !linkMode && !obsMode;
      imageURLInput.required = linkMode;
      uploadButton.hidden = false;
      queueUploadButton.hidden = obsMode || !state.videoPlayerEnabled;
      uploadButton.textContent = linkMode ? 'リンクから変換して公開' : '変換して公開';
      applyOBSProtection();
      applyOBS(state.obs);
      if (linkMode) {
        imageURLInput.focus();
      }
    }

    function uploadFromFile(action) {
      const formData = new FormData(uploadForm);
      return fetch(action === 'queue' ? '/api/upload-queue' : '/api/upload', { method: 'POST', body: formData });
    }

    async function copyStartedOBSURL(data) {
      const url = data && (data.shareURL || data.hlsURL || data.publicHLSURL);
      if (!url || !url.startsWith('http')) {
        return;
      }
      const source = document.getElementById('shareURL');
      try {
        await copyText(url, source);
      } catch (error) {
      }
      try {
        await copyURLOnPC('shareURL');
      } catch (error) {
      }
    }

    function uploadFromLink(action) {
      const formData = new FormData(uploadForm);
      return fetch(action === 'queue' ? '/api/upload-url-queue' : '/api/upload-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          url: imageURLInput.value.trim(),
          format: formData.get('format'),
          quality: formData.get('quality'),
          maxDimension: formData.get('maxDimension'),
          maxMB: formData.get('maxMB')
        })
      });
    }

    function selectedRAWFile() {
      const file = imageInput.files && imageInput.files[0];
      if (!file || !file.name) return false;
      const dot = file.name.lastIndexOf('.');
      if (dot < 0) return false;
      return rawExtensions.has(file.name.slice(dot).toLowerCase());
    }

    async function ensureFFmpegForRAWSelection() {
      if (!selectedRAWFile() || ffmpegReady) return true;
      if (ffmpegPromise) return ffmpegPromise;
      ffmpegPromise = runFFmpegCheckForRAWSelection();
      return ffmpegPromise;
    }

    async function runFFmpegCheckForRAWSelection() {
      ffmpegPending = true;
      uploadButton.disabled = true;
      queueUploadButton.disabled = true;
      toast.textContent = 'Checking FFmpeg for RAW conversion...';
      try {
        const res = await fetch('/api/ffmpeg', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        ffmpegReady = true;
        toast.textContent = 'FFmpeg is ready for RAW conversion.';
        return true;
      } catch (error) {
        toast.textContent = error.message || 'Failed to check FFmpeg for RAW conversion.';
        return false;
      } finally {
        ffmpegPending = false;
        ffmpegPromise = null;
        uploadButton.disabled = false;
        queueUploadButton.disabled = false;
      }
    }

    if (pasteURLButton) {
      pasteURLButton.addEventListener('click', async () => {
        try {
          const text = (await navigator.clipboard.readText()).trim();
          if (text) {
            imageURLInput.value = text;
          }
        } catch (error) {
          toast.textContent = 'クリップボードの読み取りに失敗しました';
        }
        imageURLInput.focus();
      });
    }

    clearButton.addEventListener('click', async () => {
      const obsEnd = uploadMode === 'obs';
      clearButton.disabled = true;
      toast.textContent = obsEnd ? 'OBS配信を終了中...' : '画像をクリア中...';
      try {
        const res = await fetch(obsEnd ? '/api/obs/end' : '/api/clear', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        announceLocalChange();
        toast.textContent = obsEnd ? 'OBS配信を終了し、次の待ち受けを開始しました' : '画像をクリアしました';
      } catch (error) {
        toast.textContent = error.message || (obsEnd ? 'OBS配信の終了に失敗しました' : '画像クリアに失敗しました');
      } finally {
        clearButton.disabled = false;
      }
    });

    if (historyList) {
      historyList.addEventListener('click', async (event) => {
        const publish = event.target.closest('[data-history-publish]');
        if (publish) {
          await selectHistory(publish.dataset.historyPublish);
          return;
        }
        const queue = event.target.closest('[data-history-queue]');
        if (queue) {
          await queueHistory(queue.dataset.historyQueue);
          return;
        }
        const heart = event.target.closest('[data-history-favorite]');
        if (heart) {
          event.preventDefault();
          event.stopPropagation();
          const favorite = heart.dataset.favorite !== '1';
          await setHistoryFavorite(heart.dataset.historyFavorite, favorite);
          return;
        }
      });
    }
    for (const button of wingTabButtons) {
      button.addEventListener('click', () => setWingMode(button.dataset.wingTab));
    }

    async function setHistoryFavorite(id, favorite) {
      if (!favorite && !(await dialog.confirm({
        title: 'お気に入りから削除',
        message: 'お気に入りから削除すると、保存済みファイルも削除されます。よろしいですか？',
        confirmText: '削除',
        danger: true
      }))) {
        return;
      }
      try {
        const res = await fetch('/api/history/favorite', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id, favorite })
        });
        if (!res.ok) throw new Error(await res.text());
        state.history = await res.json();
        renderHistory(state.history, state.currentID);
        toast.textContent = favorite ? 'お気に入りに保存しました' : 'お気に入りから削除しました';
      } catch (error) {
        toast.textContent = error.message || 'お気に入りの更新に失敗しました';
      }
    }

    let historyActionInFlight = false;

    async function queueHistory(id) {
      if (historyActionInFlight) return;
      historyActionInFlight = true;
      toast.textContent = '変換キューに追加中...';
      try {
        const res = await fetch('/api/history/queue', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        setWingMode('queue');
        announceLocalChange();
        toast.textContent = '変換キューに追加しました';
      } catch (error) {
        toast.textContent = error.message || '変換キューへの追加に失敗しました';
      } finally {
        historyActionInFlight = false;
      }
    }

    async function selectHistory(id) {
      if (historyActionInFlight) return;
      historyActionInFlight = true;
      toast.textContent = '履歴から復元中...';
      try {
        const res = await fetch('/api/history/select', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        announceLocalChange();
        toast.textContent = data.clipboardCopied ? '履歴から公開し、URLをPCにコピーしました' : '履歴から復元しました';
      } catch (error) {
        toast.textContent = error.message || '履歴の復元に失敗しました';
      } finally {
        historyActionInFlight = false;
      }
    }

    document.addEventListener('click', async (event) => {
      const target = event.target.closest('[data-copy]');
      if (!target) return;
      const id = target.getAttribute('data-copy');
      const source = document.getElementById(id);
      let text = source.textContent;
      if (id === 'obsStreamKey' && state.obs && state.obs.streamKey) {
        text = state.obs.streamKey;
      }
      if ((id === 'phoneURL' || id === 'phoneURLMobile') && uploadMode === 'obs') {
        text = '';
      }
      if (!text || text === '-' || text === '*****') {
        toast.textContent = 'コピーできるURLがありません';
        return;
      }
      let copied = false;
      let pcCopied = false;
      try {
        copied = await copyText(text, source);
      } catch (error) {
        selectElementText(source);
      }
      try {
        const pcResult = await copyURLOnPC(id);
        pcCopied = pcResult.pcClipboardCopied;
      } catch (error) {
        pcCopied = false;
      }

      if (copied && pcCopied) {
        toast.textContent = 'コピーしました。PCにもコピー済みです';
      } else if (copied) {
        toast.textContent = 'この端末にコピーしました';
      } else if (pcCopied) {
        selectElementText(source);
        toast.textContent = 'PCにコピーしました。この端末ではURLを選択しました';
      } else {
        selectElementText(source);
        toast.textContent = 'URLを選択しました。Ctrl+Cでコピーできます';
      }
    });

    async function copyURLOnPC(target) {
      const res = await fetch('/api/copy-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target })
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      return res.json();
    }

    async function copyText(text, source) {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return true;
      }
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.setAttribute('readonly', '');
      textarea.style.position = 'fixed';
      textarea.style.left = '-9999px';
      textarea.style.top = '0';
      document.body.appendChild(textarea);
      textarea.focus();
      textarea.select();
      try {
        if (!document.execCommand('copy')) {
          selectElementText(source);
          return false;
        }
        return true;
      } finally {
        textarea.remove();
      }
    }

    function selectElementText(element) {
      const range = document.createRange();
      range.selectNodeContents(element);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    }

`
