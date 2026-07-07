package server

const dashboardScriptUploadEvents = `
    fileDropZone.addEventListener('dragenter', (event) => {
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
      if (mediaIntent === 'music') {
        MusicController.render({ active: true });
      }
      // プレイリストモード中は従来のアップロードUIをキュー追加に接続する。
      if (mediaIntent === 'music' && MusicController.mode() === 'playlist' && uploadMode !== 'obs') {
        uploadButton.disabled = true;
        try {
          await PlaylistController.addFromUploadForm(uploadMode);
        } finally {
          uploadButton.disabled = false;
        }
        return;
      }
      if (uploadMode === 'obs') {
        if (!(state.obs && state.obs.connected) || (state.obs && state.obs.publishing)) {
          updateOBSActionState(state.obs);
          return;
        }
        uploadButton.disabled = true;
        toast.textContent = 'OBS配信を開始中...';
        try {
          const res = await apiFetch('/api/obs/start', { method: 'POST' });
          if (!res.ok) throw new Error(await res.text());
          const data = await res.json();
          pendingOBSAutoCopy = true;
          applyState(data);
          setUploadMode('obs');
          announceLocalChange();
          toast.textContent = data.obs && data.obs.connected ? 'OBS配信を公開しました' : 'OBS配信開始を予約しました';
        } catch (error) {
          toast.textContent = error.message || 'OBS配信開始に失敗しました';
        } finally {
          updateOBSActionState(state.obs);
        }
        return;
      }
      const action = event.submitter && event.submitter.value === 'queue' ? 'queue' : 'publish';
      if (uploadMode === 'file' && !selectedUploadFile()) {
        imageInput.reportValidity();
        return;
      }
      if (uploadMode === 'file' && selectedRAWFile() && !await ensureFFmpegForRAWSelection()) {
        return;
      }
      uploadButton.disabled = true;
      queueUploadButton.disabled = true;
      const linkDownload = uploadMode === 'link';
      if (linkDownload) {
        const pendingURL = imageURLInput.value.trim();
        toast.textContent = mediaIntent === 'music' ? '音楽を取得中...' : '動画をダウンロード中...';
        renderIngestPreview('downloading', pendingURL, 0, '', true);
        scrollProgressIntoView();
      } else {
        UploadController.beginLocalUploadProgress(selectedUploadFileName(), action);
        scrollProgressIntoView();
      }
      try {
        const res = uploadMode === 'link' ? await uploadFromLink(action) : await uploadFromFile(action);
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        UploadController.finishLocalUploadProgress();
        handleUploadSuccess(data, action);
      } catch (error) {
        const offered = await maybeOfferBrowserMediaCandidates(action, error);
        await refreshState();
        if (!offered) {
          showToast(error.message || 'アップロードに失敗しました', { error: true });
        }
      } finally {
        UploadController.finishLocalUploadProgress();
        uploadButton.disabled = false;
        queueUploadButton.disabled = false;
      }
    });

    function setUploadMode(mode) {
      if (mode === 'obs' && (!state.videoPlayerEnabled || mediaIntent !== 'video')) {
        mode = 'file';
      }
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
      fileUploadPanel.hidden = linkMode || obsMode;
      linkUploadPanel.hidden = !linkMode;
      obsUploadPanel.hidden = !obsMode;
      updateUploadControlsVisibility();
      imageInput.required = !linkMode && !obsMode;
      imageURLInput.required = linkMode;
      uploadButton.hidden = false;
      if (!obsMode) {
        uploadButton.disabled = false;
      }
      queueUploadButton.hidden = obsMode || !state.videoPlayerEnabled || mediaIntent !== 'video' || mediaIntent === 'music';
      if (videoInfoPanel) {
        videoInfoPanel.hidden = mediaIntent !== 'video';
      }
      uploadButton.textContent = uploadActionLabel();
      renderShareURL(state);
      applyOBSProtection();
      applyOBS(state.obs);
      if (linkMode) {
        imageURLInput.focus();
      }
    }

    function uploadFromFile(action) {
      const formData = new FormData(uploadForm);
      formData.set('shareMode', shareModeForUpload(state));
      const file = selectedUploadFile();
      const title = file ? file.name : 'ファイル';
      return apiUploadForm(action === 'queue' ? '/api/upload-queue' : '/api/upload', formData, (event) => {
        if (event && event.phase === 'received') {
          toast.textContent = 'アップロード受信完了。解析を開始しています…';
          UploadController.renderUploadProgress(100, '送信完了 / 解析待ち', title);
          return;
        }
        if (event && event.phase === 'response') {
          toast.textContent = 'サーバー処理結果を確認しています…';
          UploadController.renderUploadProgress(100, 'サーバー処理結果を確認中', title);
          return;
        }
        if (event.lengthComputable && event.total > 0) {
          UploadController.updateLocalUploadProgress(event.loaded, event.total, title);
        } else {
          UploadController.updateLocalUploadProgress(0, 0, title);
        }
      });
    }

    function selectedUploadFile() {
      return imageInput && imageInput.files && imageInput.files.length ? imageInput.files[0] : null;
    }

    function selectedUploadFileName() {
      const file = selectedUploadFile();
      return file ? file.name : 'ファイル';
    }

    function renderUploadProgress(percent, detail, title) {
      UploadController.renderUploadProgress(percent, detail, title);
    }

    function formatBytes(bytes) {
      const value = Number(bytes || 0);
      if (value >= 1024 * 1024 * 1024) return (value / 1024 / 1024 / 1024).toFixed(2) + ' GB';
      if (value >= 1024 * 1024) return (value / 1024 / 1024).toFixed(value >= 10 * 1024 * 1024 ? 0 : 1) + ' MB';
      if (value >= 1024) return Math.round(value / 1024) + ' KB';
      return Math.round(value) + ' B';
    }

    function handleUploadSuccess(data, action) {
      applyState(data);
      announceLocalChange();
      scrollProgressIntoView();
      if (action === 'queue') {
        setWingMode('queue');
        toast.textContent = '動画変換に追加しました';
        return;
      }
      const isVideo = data.current && data.current.kind === 'video';
      if (isVideo) {
        toast.textContent = data.clipboardCopied ? '動画HLS変換を開始し、URLをPCにコピーしました' : '動画HLS変換を開始しました';
      } else {
        toast.textContent = data.clipboardCopied ? '公開画像を更新し、URLをPCにコピーしました' : '公開画像を更新しました';
      }
    }

    async function maybeAutoCopyOBSURL(data) {
      if (!pendingOBSAutoCopy) {
        return;
      }
      const view = shareURLForMode(data, shareModeForUpload(data));
      const url = view && view.shareURL ? String(view.shareURL) : '';
      if (!url || url === lastAutoCopiedOBSURL) {
        return;
      }
      pendingOBSAutoCopy = false;
      lastAutoCopiedOBSURL = url;
      const source = document.getElementById('shareURL');
      let browserCopied = false;
      let pcCopied = false;
      try {
        browserCopied = await copyText(url, source);
      } catch (error) {
      }
      try {
        const pcResult = await copyURLOnPC('shareURL');
        pcCopied = !!pcResult.pcClipboardCopied;
      } catch (error) {
      }
      if (pcCopied && browserCopied) {
        toast.textContent = '配信用URLをコピーしました。PCにもコピー済みです';
      } else if (pcCopied) {
        toast.textContent = '配信用URLをPCにコピーしました';
      } else if (browserCopied) {
        toast.textContent = '配信用URLをこの端末にコピーしました';
      } else {
        toast.textContent = '配信用URLを表示しました。コピーできない場合は手動でコピーしてください';
      }
    }

    function uploadFromLink(action, overrideURL) {
      const formData = new FormData(uploadForm);
      const targetURL = (overrideURL || imageURLInput.value).trim();
      return apiFetch(action === 'queue' ? '/api/upload-url-queue' : '/api/upload-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          url: targetURL,
          format: formData.get('format'),
          quality: formData.get('quality'),
          maxDimension: formData.get('maxDimension'),
          maxMB: formData.get('maxMB'),
          shareMode: shareModeForUpload(state)
        })
      });
    }

    async function findBrowserMediaCandidates(url) {
      const res = await apiFetch('/api/browser-media-candidates', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url })
      });
      if (!res.ok) throw new Error(await res.text());
      return res.json();
    }

    async function maybeOfferBrowserMediaCandidates(action, originalError) {
      if (uploadMode !== 'link') return false;
      const pageURL = imageURLInput.value.trim();
      if (!pageURL) return false;
      if (isKnownYTDLPPageURL(pageURL)) return false;
      toast.textContent = 'ページ内の動画候補を探しています...';
      let data;
      try {
        data = await findBrowserMediaCandidates(pageURL);
      } catch (error) {
        return false;
      }
      const candidates = Array.isArray(data.candidates) ? data.candidates : [];
      if (!candidates.length) {
        if (data.message && !data.unavailable) {
          toast.textContent = data.message;
          return true;
        }
        return false;
      }
      openMediaCandidateDialog(candidates, action, originalError);
      toast.textContent = 'ページ内動画候補を検出しました';
      return true;
    }

    function isKnownYTDLPPageURL(value) {
      let host = '';
      try {
        host = new URL(value).hostname.toLowerCase();
      } catch (error) {
        return false;
      }
      return host === 'youtu.be' ||
        host.endsWith('.youtube.com') ||
        host === 'youtube.com' ||
        host.endsWith('.x.com') ||
        host === 'x.com' ||
        host.endsWith('.twitter.com') ||
        host === 'twitter.com' ||
        host.endsWith('.soundcloud.com') ||
        host === 'soundcloud.com';
    }

    function openMediaCandidateDialog(candidates, action, originalError) {
      if (!mediaCandidateDialog || !mediaCandidateList) return;
      mediaCandidateList.innerHTML = '';
      candidates.forEach((candidate) => {
        const row = document.createElement('div');
        row.className = 'candidate-item';
        const meta = document.createElement('div');
        meta.className = 'candidate-meta';
        const kind = document.createElement('div');
        kind.className = 'candidate-kind';
        kind.textContent = candidate.label || candidate.kind || 'メディア';
        const url = document.createElement('div');
        url.className = 'candidate-url';
        url.textContent = candidate.url || '';
        meta.append(kind, url);
        const button = document.createElement('button');
        button.type = 'button';
        button.textContent = action === 'queue' ? 'キューに追加' : '公開';
        button.addEventListener('click', () => retryLinkCandidate(action, candidate));
        row.append(meta, button);
        mediaCandidateList.append(row);
      });
      if (originalError && originalError.message) {
        mediaCandidateDialog.setAttribute('data-original-error', originalError.message);
      }
      openManagedModal(mediaCandidateDialog, mediaCandidateClose, imageURLInput);
    }

    function hideMediaCandidateDialog() {
      if (!mediaCandidateDialog) return;
      closeManagedModal(mediaCandidateDialog, imageURLInput);
    }

    async function retryLinkCandidate(action, candidate) {
      if (!candidate || !candidate.url) return;
      hideMediaCandidateDialog();
      imageURLInput.value = candidate.url;
      uploadButton.disabled = true;
      queueUploadButton.disabled = true;
      toast.textContent = '選択した動画候補をダウンロード中...';
      renderIngestPreview('downloading', candidate.url, 0, '', true);
      scrollProgressIntoView();
      try {
        const res = await uploadFromLink(action, candidate.url);
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        handleUploadSuccess(data, action);
      } catch (error) {
        toast.textContent = error.message || '選択した動画候補の取得に失敗しました';
      } finally {
        uploadButton.disabled = false;
        queueUploadButton.disabled = false;
      }
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
      toast.textContent = 'RAW変換用FFmpegを確認しています...';
      try {
        const res = await apiFetch('/api/ffmpeg', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        ffmpegReady = true;
        toast.textContent = 'RAW変換用FFmpegを利用できます';
        return true;
      } catch (error) {
        toast.textContent = error.message || 'RAW変換用FFmpegの確認に失敗しました';
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
      if (!obsEnd && !window.confirm('現在公開中の画像をクリアしますか？')) return;
      clearButton.disabled = true;
      toast.textContent = obsEnd ? 'OBS配信を終了中...' : '画像をクリア中...';
      try {
        const res = await apiFetch(obsEnd ? '/api/obs/end' : '/api/clear', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        announceLocalChange();
        toast.textContent = obsEnd ? 'OBS配信を終了し、次の待ち受けを開始しました' : '画像をクリアしました';
      } catch (error) {
        toast.textContent = error.message || (obsEnd ? 'OBS配信の終了に失敗しました' : '画像のクリアに失敗しました');
      } finally {
        clearButton.disabled = false;
      }
    });

    if (historyList) {
      historyList.addEventListener('click', async (event) => {
        const publish = event.target.closest('[data-history-publish]');
        if (publish) {
          await HistoryController.publishHistoryItem(publish.dataset.historyPublish);
          return;
        }
        const queue = event.target.closest('[data-history-queue]');
        if (queue) {
          await HistoryController.queueHistoryItem(queue.dataset.historyQueue);
          return;
        }
        const heart = event.target.closest('[data-history-favorite]');
        if (heart) {
          event.preventDefault();
          event.stopPropagation();
          const favorite = heart.dataset.favorite !== '1';
          await HistoryController.toggleFavoriteHistoryItem(heart.dataset.historyFavorite, favorite);
          return;
        }
      });
    }
    for (const button of wingTabButtons) {
      button.addEventListener('click', () => setWingMode(button.dataset.wingTab));
    }

    async function setHistoryFavorite(id, favorite) {
      if (!favorite && !window.confirm('お気に入りから削除すると、保存済みファイルも削除されます。よろしいですか？')) {
        return;
      }
      try {
        const res = await apiFetch('/api/history/favorite', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id, favorite })
        });
        if (!res.ok) throw new Error(await res.text());
        state.history = await res.json();
        HistoryController.render(state);
        toast.textContent = favorite ? 'お気に入りに保存しました' : 'お気に入りから削除しました';
      } catch (error) {
        toast.textContent = error.message || 'お気に入りの更新に失敗しました';
      }
    }

    let historyActionInFlight = false;

    async function queueHistory(id) {
      if (historyActionInFlight) return;
      historyActionInFlight = true;
      toast.textContent = '動画変換に追加中...';
      try {
        const res = await apiFetch('/api/history/queue', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        setWingMode('queue');
        announceLocalChange();
        toast.textContent = '動画変換に追加しました';
      } catch (error) {
        toast.textContent = error.message || '動画変換への追加に失敗しました';
      } finally {
        historyActionInFlight = false;
      }
    }

    async function selectHistory(id) {
      if (historyActionInFlight) return;
      historyActionInFlight = true;
      if (historyList) historyList.setAttribute('aria-busy', 'true');
      toast.textContent = '公開内容を履歴項目へ切り替え中...';
      try {
        const res = await apiFetch('/api/history/select', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        applyHistoryTargetMode(data);
        announceLocalChange();
        toast.textContent = data.clipboardCopied ? '履歴項目を公開中に切り替え、URLをPCにコピーしました' : '履歴項目を公開中に切り替えました';
      } catch (error) {
        toast.textContent = error.message || '履歴項目への切り替えに失敗しました';
      } finally {
        historyActionInFlight = false;
        if (historyList) historyList.removeAttribute('aria-busy');
      }
    }

    function applyHistoryTargetMode(data) {
      const mode = data && data.historyTargetMode ? String(data.historyTargetMode) : '';
      if (data && data.current && data.current.kind === 'video') {
        setMediaIntent('video');
      } else if (data && data.current) {
        setMediaIntent('image');
      }
      if (mode === 'link') {
        setUploadMode('link');
      } else if (mode === 'file') {
        setUploadMode('file');
      }
    }

    document.addEventListener('click', async (event) => {
      const protectedReveal = event.target.closest('[data-protected-reveal]');
      if (protectedReveal && protectedReveal.classList.contains('protected') && !protectedReveal.classList.contains('revealed')) {
        protectedReveal.classList.add('revealed');
        protectedReveal.setAttribute('aria-pressed', 'true');
        event.preventDefault();
        return;
      }
      const target = event.target.closest('[data-copy]');
      if (!target) return;
      const id = target.getAttribute('data-copy');
      const source = document.getElementById(id);
      let text = source.textContent;
      if (id === 'shareURL') {
        text = displayedShareURL().shareURL || '';
      }
      if (id === 'obsStreamKey' && state.obs && state.obs.streamKey) {
        text = state.obs.streamKey;
      }
      if ((id === 'phoneURL' || id === 'phoneURLMobile' || id === 'phoneDialogURL') && phoneProtectionActive) {
        const guard = source.closest('[data-protected-reveal]');
        if (guard && !guard.classList.contains('revealed')) {
          toast.textContent = '先にアドレスを表示してください';
          return;
        }
      }
      if ((id === 'phoneURL' || id === 'phoneURLMobile' || id === 'phoneDialogURL') && uploadMode === 'obs' && !text) {
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
        const pcResult = await copyURLOnPC(id === 'phoneDialogURL' ? 'phoneURL' : id, text);
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

    async function copyURLOnPC(target, value) {
      const res = await apiFetch('/api/copy-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target, mode: shareModeForUpload(state), value: value || '' })
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

    document.getElementById('refreshButton').addEventListener('click', refreshState);
    document.getElementById('obsKeyRevealButton').addEventListener('click', () => {
      obsKeyVisible = !obsKeyVisible;
      const button = document.getElementById('obsKeyRevealButton');
      button.title = obsKeyVisible ? 'Stream Keyを隠す' : 'Stream Keyを表示';
      button.setAttribute('aria-label', button.title);
      applyOBS(state.obs);
    });
    const obsStreamKeyElement = document.getElementById('obsStreamKey');
    const requestOBSKeyChange = async () => {
      const nextKey = await showOBSKeyRiskDialog();
      if (!nextKey) return;
      await updateOBSStreamKey(nextKey);
    };
    if (obsStreamKeyElement) {
      obsStreamKeyElement.addEventListener('click', requestOBSKeyChange);
      obsStreamKeyElement.addEventListener('keydown', (event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          requestOBSKeyChange();
        }
      });
    }
    obsKeyRotateButton.addEventListener('click', requestOBSKeyChange);
    obsLatencyMode.addEventListener('change', async () => {
      const requestedMode = obsLatencyMode.value;
      if (requestedMode.startsWith('rtsp-') && !String(confirmedOBSLatencyMode || '').startsWith('rtsp-')) {
        const accepted = await showRTSPRiskDialog();
        if (!accepted) {
          obsLatencyMode.value = confirmedOBSLatencyMode;
          return;
        }
      }
      updateOBSLatency(requestedMode);
    });
    if (obsLatencyDetailButton) {
      obsLatencyDetailButton.addEventListener('click', async () => {
        await refreshState();
        showOBSConnectionsDialog();
      });
    }
    async function updateOBSLatency(mode) {
      obsLatencyMode.disabled = true;
      try {
        const res = await apiFetch('/api/obs/latency', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: mode || obsLatencyMode.value })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        state.obs = data || null;
        applyOBS(data);
        resetOBSPreview();
        refreshAgain = true;
        announceLocalChange();
        toast.textContent = 'OBSレイテンシ設定を更新しました。プレビューを再起動しています...';
      } catch (error) {
        toast.textContent = error.message || 'OBSレイテンシ設定の更新に失敗しました';
      } finally {
        obsLatencyMode.disabled = false;
      }
    }
    function showRTSPRiskDialog() {
      if (!rtspRiskDialog || !rtspRiskCancel || !rtspRiskConfirm) {
        return Promise.resolve(false);
      }
      return new Promise((resolve) => {
        let settled = false;
        const finish = (accepted) => {
          if (settled) return;
          settled = true;
          closeManagedModal(rtspRiskDialog, obsLatencyMode);
          rtspRiskDialog.removeEventListener('click', onBackdrop);
          rtspRiskCancel.removeEventListener('click', onCancel);
          rtspRiskConfirm.removeEventListener('click', onConfirm);
          document.removeEventListener('keydown', onKeyDown);
          resolve(accepted);
        };
        const onCancel = () => finish(false);
        const onConfirm = () => finish(true);
        const onBackdrop = (event) => {
          if (event.target === rtspRiskDialog) finish(false);
        };
        const onKeyDown = (event) => {
          handleModalKeyboard(event, rtspRiskDialog, () => finish(false));
        };
        openManagedModal(rtspRiskDialog, rtspRiskConfirm, obsLatencyMode);
        rtspRiskDialog.addEventListener('click', onBackdrop);
        rtspRiskCancel.addEventListener('click', onCancel);
        rtspRiskConfirm.addEventListener('click', onConfirm);
        document.addEventListener('keydown', onKeyDown);
      });
    }
    function showOBSKeyRiskDialog() {
      if (!obsKeyRiskDialog || !obsKeyRiskCancel || !obsKeyRiskConfirm || !obsKeyEditInput) {
        return Promise.resolve('');
      }
      obsKeyEditInput.value = (state.obs && state.obs.streamKey) || '';
      return new Promise((resolve) => {
        let settled = false;
        const finish = (value) => {
          if (settled) return;
          settled = true;
          const key = document.getElementById('obsStreamKey');
          closeManagedModal(obsKeyRiskDialog, key);
          obsKeyRiskDialog.removeEventListener('click', onBackdrop);
          obsKeyRiskCancel.removeEventListener('click', onCancel);
          obsKeyRiskConfirm.removeEventListener('click', onConfirm);
          obsKeyEditInput.removeEventListener('keydown', onInputKeyDown);
          document.removeEventListener('keydown', onKeyDown);
          resolve(value);
        };
        const validate = () => {
          const value = obsKeyEditInput.value.trim();
          if (!value) {
            toast.textContent = 'OBS Stream Keyを入力してください';
            obsKeyEditInput.focus();
            return '';
          }
          if (/[\\/\s?#]/.test(value)) {
            toast.textContent = 'OBS Stream Keyに空白、/、\\\\、?、# は使えません';
            obsKeyEditInput.focus();
            return '';
          }
          return value;
        };
        const onCancel = () => finish('');
        const onConfirm = () => {
          const value = validate();
          if (value) finish(value);
        };
        const onBackdrop = (event) => {
          if (event.target === obsKeyRiskDialog) finish('');
        };
        const onKeyDown = (event) => {
          handleModalKeyboard(event, obsKeyRiskDialog, () => finish(''));
        };
        const onInputKeyDown = (event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            onConfirm();
          }
        };
        const key = document.getElementById('obsStreamKey');
        openManagedModal(obsKeyRiskDialog, obsKeyEditInput, key);
        obsKeyRiskDialog.addEventListener('click', onBackdrop);
        obsKeyRiskCancel.addEventListener('click', onCancel);
        obsKeyRiskConfirm.addEventListener('click', onConfirm);
        obsKeyEditInput.addEventListener('keydown', onInputKeyDown);
        document.addEventListener('keydown', onKeyDown);
        window.setTimeout(() => obsKeyEditInput.select(), 0);
      });
    }
    async function updateOBSStreamKey(streamKey) {
      obsKeyRotateButton.disabled = true;
      toast.textContent = 'OBS Stream Keyを保存中...';
      try {
        const res = await apiFetch('/api/obs/key', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ streamKey })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        obsKeyVisible = true;
        applyState(data);
        announceLocalChange();
        toast.textContent = 'OBS Stream Keyを保存しました。OBS側の設定も同じ値へ変更してください';
      } catch (error) {
        toast.textContent = error.message || 'OBS Stream Keyの保存に失敗しました';
      } finally {
        obsKeyRotateButton.disabled = false;
      }
    }
    function showOBSConnectionsDialog() {
      if (!obsConnectionsDialog || !obsConnectionsClose) return;
      const close = () => {
        closeManagedModal(obsConnectionsDialog, obsLatencyDetailButton);
        obsConnectionsDialog.removeEventListener('click', onBackdrop);
        obsConnectionsClose.removeEventListener('click', close);
        document.removeEventListener('keydown', onKeyDown);
      };
      const onBackdrop = (event) => {
        if (event.target === obsConnectionsDialog) close();
      };
      const onKeyDown = (event) => {
        handleModalKeyboard(event, obsConnectionsDialog, close);
      };
      renderOBSConnections((state.obs && state.obs.connections) || []);
      openManagedModal(obsConnectionsDialog, obsConnectionsClose, obsLatencyDetailButton);
      obsConnectionsDialog.addEventListener('click', onBackdrop);
      obsConnectionsClose.addEventListener('click', close);
      document.addEventListener('keydown', onKeyDown);
    }`
