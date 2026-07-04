package server

const dashboardScriptSettings = `
    function showPhoneConnectDialog() {
      if (!phoneConnectDialog) return;
      openManagedModal(phoneConnectDialog, phoneConnectCloseButton, phoneConnectButton);
    }
    function hidePhoneConnectDialog() {
      if (!phoneConnectDialog) return;
      closeManagedModal(phoneConnectDialog, phoneConnectButton);
    }
    if (phoneConnectButton) {
      phoneConnectButton.addEventListener('click', () => SettingsController.openPhoneConnect());
    }
    if (phoneConnectCloseButton) {
      phoneConnectCloseButton.addEventListener('click', () => SettingsController.closePhoneConnect());
    }
    if (quitButton) {
      quitButton.addEventListener('click', async () => {
        quitButton.disabled = true;
        toast.textContent = 'アプリを終了しています...';
        try {
          const res = await apiFetch('/api/quit', { method: 'POST' });
          if (!res.ok) throw new Error(await res.text());
          const data = await res.json();
          toast.textContent = data.message || 'アプリを終了します';
        } catch (error) {
          quitButton.disabled = false;
          toast.textContent = error.message || '終了に失敗しました';
        }
      });
    }
    if (phoneConnectDialog) {
      phoneConnectDialog.addEventListener('click', (event) => {
        if (event.target === phoneConnectDialog) hidePhoneConnectDialog();
      });
      phoneConnectDialog.addEventListener('keydown', (event) => {
        handleModalKeyboard(event, phoneConnectDialog, hidePhoneConnectDialog);
      });
    }
    document.getElementById('tunnelReconnectButton').addEventListener('click', async () => {
      const button = document.getElementById('tunnelReconnectButton');
      button.disabled = true;
      toast.textContent = '再接続を要求しています...';
      try {
        const res = await apiFetch('/api/tunnel/reconnect', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        toast.textContent = data.message || '再接続を要求しました';
      } catch (error) {
        toast.textContent = error.message || '再接続の要求に失敗しました';
      } finally {
        button.disabled = false;
      }
    });
    function updateYTDLPAuthState() {
      const saved = !!(state.ytdlpAuth && state.ytdlpAuth.saved);
      if (ytdlpCookieStatus) {
        ytdlpCookieStatus.textContent = saved ? 'Cookie保存済み' : 'Cookie未保存';
      }
      if (ytdlpCookieDeleteButton) {
        ytdlpCookieDeleteButton.disabled = !saved;
      }
      if (ytdlpAuthInitialized && !ytdlpAuthWasSaved && saved) {
        showToast('YouTubeログインCookieを保存しました');
      }
      ytdlpAuthInitialized = true;
      ytdlpAuthWasSaved = saved;
    }
    function normalizedThemePreference(value) {
      return value === 'light' || value === 'dark' || value === 'system' ? value : 'system';
    }
    function currentThemePreference() {
      try {
        return normalizedThemePreference(localStorage.getItem('imagepad:themePreference') || 'system');
      } catch (error) {
        return 'system';
      }
    }
    function effectiveTheme(preference) {
      const pref = normalizedThemePreference(preference);
      if (pref === 'dark') return 'dark';
      if (pref === 'light') return 'light';
      return themeMediaQuery && themeMediaQuery.matches ? 'dark' : 'light';
    }
    function applyThemePreference(preference, persist) {
      const pref = normalizedThemePreference(preference);
      if (persist) {
        try {
          localStorage.setItem('imagepad:themePreference', pref);
        } catch (error) {
        }
      }
      const theme = effectiveTheme(pref);
      document.documentElement.dataset.themePreference = pref;
      document.documentElement.dataset.theme = theme;
      themeButtons.forEach((button) => {
        const active = button.dataset.themeChoice === pref;
        button.classList.toggle('active', active);
        button.setAttribute('aria-pressed', String(active));
      });
      if (themeText) {
        const label = pref === 'system' ? 'OSに追従' : (pref === 'dark' ? 'ダーク' : 'ライト');
        themeText.textContent = label + ' / 現在は' + (theme === 'dark' ? 'ダーク' : 'ライト');
      }
    }
    function showSettingsModal() {
      if (!settingsModal) return;
      updateYTDLPAuthState();
      applyThemePreference(currentThemePreference(), false);
      openManagedModal(settingsModal, settingsCloseButton || ytdlpLoginButton, settingsButton);
    }
    function hideSettingsModal() {
      if (!settingsModal) return;
      closeManagedModal(settingsModal, settingsButton);
    }
    function confirmYTDLPLoginRisk() {
      return window.confirm('ログイン情報をこのアプリ用のCookieとして保存します。\n\nBOT認証エラーが出た時だけ使用してください。通常時の利用は推奨しません。\n\nデメリット:\n- このPC上のImagePadServerデータ内にCookieファイルが保存されます。\n- Cookieが残っている間はyt-dlpがログイン状態でアクセスします。\n- 共有PCではCookie削除を使って消してください。\n\n続行しますか？');
    }
    async function loginYTDLP() {
      if (!confirmYTDLPLoginRisk()) return;
      const buttons = [ytdlpLoginButton, toastYTDLPLoginButton].filter(Boolean);
      buttons.forEach((button) => { button.disabled = true; });
      showToast('ログイン画面を開いています。YouTubeにログインするとCookieを保存します。', { timeout: 0 });
      try {
        const res = await apiFetch('/api/ytdlp/login', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        state.ytdlpAuth = await res.json();
        updateYTDLPAuthState();
        showToast(state.ytdlpAuth && state.ytdlpAuth.saved ? 'YouTubeログインCookieを保存しました' : 'ログインCookieはまだ保存されていません', { timeout: 5200 });
        scheduleRefresh(100);
      } catch (error) {
        showToast(error.message || 'YouTubeログインCookieの保存に失敗しました', { error: true });
      } finally {
        buttons.forEach((button) => { button.disabled = false; });
      }
    }
    async function deleteYTDLPCookies() {
      if (!window.confirm('保存済みのYouTubeログインCookieを削除しますか？')) return;
      if (ytdlpCookieDeleteButton) ytdlpCookieDeleteButton.disabled = true;
      try {
        const res = await apiFetch('/api/ytdlp/cookies', { method: 'DELETE' });
        if (!res.ok) throw new Error(await res.text());
        state.ytdlpAuth = await res.json();
        updateYTDLPAuthState();
        showToast('YouTubeログインCookieを削除しました');
        scheduleRefresh(100);
      } catch (error) {
        showToast(error.message || 'YouTubeログインCookieの削除に失敗しました', { error: true });
      } finally {
        updateYTDLPAuthState();
      }
    }
    if (settingsButton) settingsButton.addEventListener('click', () => SettingsController.openSettings());
    if (settingsCloseButton) settingsCloseButton.addEventListener('click', () => SettingsController.closeSettings());
    themeButtons.forEach((button) => {
      button.addEventListener('click', () => SettingsController.applyThemePreference(button.dataset.themeChoice, true));
    });
    document.addEventListener('keydown', (event) => {
      if (event.key !== 'Enter' && event.key !== ' ') return;
      const protectedReveal = event.target.closest('[data-protected-reveal]');
      if (!protectedReveal || !protectedReveal.classList.contains('protected') || protectedReveal.classList.contains('revealed')) return;
      protectedReveal.classList.add('revealed');
      protectedReveal.setAttribute('aria-pressed', 'true');
      event.preventDefault();
    });
    if (themeMediaQuery) {
      const handleSystemThemeChange = () => {
        if (currentThemePreference() === 'system') {
          applyThemePreference('system', false);
        }
      };
      if (themeMediaQuery.addEventListener) {
        themeMediaQuery.addEventListener('change', handleSystemThemeChange);
      } else if (themeMediaQuery.addListener) {
        themeMediaQuery.addListener(handleSystemThemeChange);
      }
    }
    applyThemePreference(currentThemePreference(), false);
    if (settingsModal) {
      settingsModal.addEventListener('click', (event) => {
        if (event.target === settingsModal) hideSettingsModal();
      });
      settingsModal.addEventListener('keydown', (event) => {
        handleModalKeyboard(event, settingsModal, hideSettingsModal);
      });
    }
    if (mediaCandidateClose) mediaCandidateClose.addEventListener('click', hideMediaCandidateDialog);
    if (mediaCandidateDialog) {
      mediaCandidateDialog.addEventListener('click', (event) => {
        if (event.target === mediaCandidateDialog) hideMediaCandidateDialog();
      });
      mediaCandidateDialog.addEventListener('keydown', (event) => {
        handleModalKeyboard(event, mediaCandidateDialog, hideMediaCandidateDialog);
      });
    }
    if (ytdlpLoginButton) ytdlpLoginButton.addEventListener('click', loginYTDLP);
    if (toastYTDLPLoginButton) toastYTDLPLoginButton.addEventListener('click', loginYTDLP);
    if (ytdlpCookieDeleteButton) ytdlpCookieDeleteButton.addEventListener('click', deleteYTDLPCookies);
    const quitButton = document.getElementById('quitButton');
    if (quitButton) {
      quitButton.addEventListener('click', async () => {
        if (!confirm('アプリを終了しますか？\n配信中のストリームも停止します。')) return;
        quitButton.disabled = true;
        toast.textContent = 'アプリを終了しています...';
        try {
          const res = await apiFetch('/api/quit', { method: 'POST' });
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
    if (imageIntentButton) imageIntentButton.addEventListener('click', () => setMediaIntent('image'));
    if (videoIntentButton) videoIntentButton.addEventListener('click', () => setMediaIntent('video'));
    formatSelect.addEventListener('change', updateQualityOptions);
    updateQualityOptions();
    imageInput.addEventListener('change', ensureFFmpegForRAWSelection);
    qualityMode.addEventListener('change', async () => {
      try {
        const res = await apiFetch('/api/video-quality', {
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
        const res = await apiFetch('/api/network-check', { method: 'POST' });
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
        const res = await apiFetch('/api/video-player', {
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
        // fetch('/api/music-mode' is intentionally routed through apiFetch to include the admin token.
        const res = await apiFetch('/api/music-mode', {
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
    });`
