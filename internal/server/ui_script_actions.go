package server

const uiScriptActions = `    document.getElementById('refreshButton').addEventListener('click', refreshState);
    document.getElementById('obsKeyRevealButton').addEventListener('click', () => {
      obsKeyVisible = !obsKeyVisible;
      const button = document.getElementById('obsKeyRevealButton');
      button.title = obsKeyVisible ? 'Stream Keyを隠す' : 'Stream Keyを表示';
      button.setAttribute('aria-label', button.title);
      applyOBS(state.obs);
    });
    obsKeyRotateButton.addEventListener('click', async () => {
      if (!(await dialog.confirm({
        title: 'OBS Stream Keyを更新',
        message: 'OBS側のキーも差し替える必要があります。よろしいですか？',
        confirmText: '更新',
        danger: true
      }))) {
        return;
      }
      obsKeyRotateButton.disabled = true;
      toast.textContent = 'OBS Stream Keyを更新中...';
      try {
        const res = await fetch('/api/obs/key', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        announceLocalChange();
        toast.textContent = 'OBS Stream Keyを更新しました';
      } catch (error) {
        toast.textContent = error.message || 'OBS Stream Keyの更新に失敗しました';
      } finally {
        obsKeyRotateButton.disabled = false;
      }
    });
    const obsRtsptCopyBtn = document.getElementById('obsRtsptCopy');
    if (obsRtsptCopyBtn) {
      obsRtsptCopyBtn.addEventListener('click', async () => {
        const url = (document.getElementById('obsRtsptURL') || {}).textContent || '';
        if (!url) return;
        try {
          await navigator.clipboard.writeText(url);
          obsRtsptCopyBtn.textContent = 'コピーしました';
          setTimeout(() => { obsRtsptCopyBtn.textContent = 'コピー'; }, 1500);
        } catch (e) {
          obsRtsptCopyBtn.textContent = 'コピー失敗';
        }
      });
    }
    obsLatencyMode.addEventListener('change', async () => {
      if (obsLatencyMode.value && obsLatencyMode.value.startsWith('rtsp-')) {
        const accepted = await dialog.confirm({
          title: 'RTSP公開モード',
          message: 'RTSPはPC向けの低遅延モードです。ルーターのUPnP設定やネットワーク環境によって外部公開できない場合があります。',
          confirmText: '切り替え',
          danger: true
        });
        if (!accepted) {
          applyOBS(state.obs);
          return;
        }
      }
      updateOBSLatency();
    });
    if (obsDVRToggle) obsDVRToggle.addEventListener('change', async () => {
      updateOBSLatency();
    });
    async function updateOBSLatency() {
      obsLatencyMode.disabled = true;
      if (obsDVRToggle) obsDVRToggle.disabled = true;
      try {
        const res = await fetch('/api/obs/latency', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: obsLatencyMode.value, dvr: false })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        state.obs = data || null;
        applyOBS(data);
        resetOBSPreview();
        refreshAgain = true;
        announceLocalChange();
        toast.textContent = 'OBS latency mode updated. Restarting preview...';
      } catch (error) {
        toast.textContent = error.message || 'Failed to update OBS latency mode';
      } finally {
        obsLatencyMode.disabled = false;
        if (obsDVRToggle) obsDVRToggle.disabled = false;
      }
    }
    document.getElementById('tunnelReconnectButton').addEventListener('click', async () => {
      const button = document.getElementById('tunnelReconnectButton');
      button.disabled = true;
      toast.textContent = '再接続を要求しています...';
      try {
        const res = await fetch('/api/tunnel/reconnect', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        toast.textContent = data.message || '再接続を要求しました';
      } catch (error) {
        toast.textContent = error.message || '再接続の要求に失敗しました';
      } finally {
        button.disabled = false;
      }
    });
    const quitButton = document.getElementById('quitButton');
    if (quitButton) {
      quitButton.addEventListener('click', async () => {
        if (!(await dialog.confirm({
          title: 'アプリを終了',
          message: 'アプリを終了しますか？\n配信中のストリームも停止します。',
          confirmText: '終了',
          danger: true
        }))) return;
        quitButton.disabled = true;
        toast.textContent = 'アプリを終了しています...';
        try {
          const res = await fetch('/api/quit', { method: 'POST' });
          if (!res.ok) throw new Error(await res.text());
          quitButton.classList.add('done');
          quitButton.textContent = '終了しました（この画面は閉じてかまいません）';
          toast.textContent = 'アプリを終了しました';
        } catch (error) {
          quitButton.disabled = false;
          toast.textContent = error.message || 'アプリの終了に失敗しました';
        }
      });
    }
    fileModeButton.addEventListener('click', () => setUploadMode('file'));
    linkModeButton.addEventListener('click', () => setUploadMode('link'));
    obsModeButton.addEventListener('click', () => setUploadMode('obs'));
    if (themeSelect) {
      const storedTheme = localStorage.getItem('imagepad-theme') || 'auto';
      applyTheme(storedTheme);
      themeSelect.addEventListener('change', () => {
        localStorage.setItem('imagepad-theme', themeSelect.value);
        applyTheme(themeSelect.value);
      });
      if (window.matchMedia) {
        window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
          if ((localStorage.getItem('imagepad-theme') || 'auto') === 'auto') applyTheme('auto');
        });
      }
    }
    if (formatSelect) {
      formatSelect.addEventListener('change', updateQualityOptions);
      updateQualityOptions();
    }
    imageInput.addEventListener('change', ensureFFmpegForRAWSelection);
    qualityMode.addEventListener('change', async () => {
      try {
        const res = await fetch('/api/video-quality', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: qualityMode.value })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyQuality(data);
        announceLocalChange();
        toast.textContent = '動画画質を更新しました';
      } catch (error) {
        toast.textContent = error.message || '動画画質の更新に失敗しました';
      }
    });
    networkCheckButton.addEventListener('click', async () => {
      networkCheckButton.disabled = true;
      toast.textContent = 'ネットワーク速度を確認中...';
      try {
        const res = await fetch('/api/network-check', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyQuality(data);
        announceLocalChange();
        toast.textContent = '速度チェックを更新しました';
      } catch (error) {
        toast.textContent = error.message || '速度チェックに失敗しました';
      } finally {
        networkCheckButton.disabled = false;
      }
    });
    videoPlayerToggle.addEventListener('change', async () => {
      const enabled = videoPlayerToggle.checked;
      videoPlayerPending = true;
      videoPlayerToggle.disabled = true;
      videoPlayerText.textContent = enabled ? 'FFmpeg確認中...' : '無効化中...';
      try {
        const res = await fetch('/api/video-player', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ enabled })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyVideoPlayer(data);
        await refreshState();
        announceLocalChange();
        toast.textContent = enabled ? 'ビデオプレーヤー対応を有効にしました' : 'ビデオプレーヤー対応を無効にしました';
      } catch (error) {
        await refreshState();
        toast.textContent = error.message || 'ビデオプレーヤー対応の切り替えに失敗しました';
      } finally {
        videoPlayerPending = false;
        videoPlayerToggle.disabled = false;
      }
    });
    musicModeToggle.addEventListener('change', async () => {
      const enabled = musicModeToggle.checked;
      musicModePending = true;
      musicModeToggle.disabled = true;
      musicModeText.textContent = enabled ? '有効化中...' : '無効化中...';
      try {
        const res = await fetch('/api/music-mode', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ enabled })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyVideoPlayer(data);
        await refreshState();
        announceLocalChange();
        toast.textContent = enabled ? 'ミュージックモードを有効にしました' : 'ミュージックモードを無効にしました';
      } catch (error) {
        await refreshState();
        toast.textContent = error.message || 'ミュージックモードの切り替えに失敗しました';
      } finally {
        musicModePending = false;
        musicModeToggle.disabled = !state.videoPlayerEnabled;
      }
    });
    function scheduleRefresh(delay) {
      clearTimeout(refreshTimer);
      refreshTimer = setTimeout(refreshState, delay);
    }

    function announceLocalChange() {
      try {
        if (localChangeChannel) {
          localChangeChannel.postMessage({ type: 'changed', at: Date.now() });
        }
        localStorage.setItem('imagepad:lastChange', String(Date.now()));
      } catch (error) {
      }
      scheduleRefresh(100);
    }

    function setupLiveSync() {
      try {
        if ('BroadcastChannel' in window) {
          localChangeChannel = new BroadcastChannel('imagepad-state');
          localChangeChannel.onmessage = () => scheduleRefresh(100);
        }
      } catch (error) {
      }
      window.addEventListener('storage', (event) => {
        if (event.key === 'imagepad:lastChange') {
          scheduleRefresh(100);
        }
      });
      window.addEventListener('focus', () => scheduleRefresh(50));
      window.addEventListener('pageshow', () => scheduleRefresh(50));
      window.addEventListener('online', () => scheduleRefresh(50));
      document.addEventListener('visibilitychange', () => {
        if (!document.hidden) {
          scheduleRefresh(50);
        }
      });
    }

    async function checkForUpdates() {
      try {
        const res = await fetch('/api/update-check', { cache: 'no-store' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        if (data.ok && data.newer) {
          updateText.innerHTML = '<a href="' + data.url + '" target="_blank" rel="noreferrer">' + escapeHTML(data.latest) + ' があります</a>';
        } else if (data.ok) {
          updateText.textContent = '最新版 ' + (data.current || '');
        } else {
          updateText.textContent = data.message || '確認失敗';
        }
      } catch (error) {
        updateText.textContent = '確認失敗';
      }
    }

    setupLiveSync();
    refreshState();
    checkForUpdates();
  </script>
`
