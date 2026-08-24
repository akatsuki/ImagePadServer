package server

const dashboardScriptUploadState = `
    function applyQuality(data) {
	  if (!data) {
		qualityStatus.textContent = '未測定';
		return;
	  }
	  state.videoQuality = data;
	  qualityMode.value = data.mode || 'auto';
	  applyEncoderMode(data.encoderMode || 'auto');
	  applyMusicPlaylistDeliveryState(data);
	  applyMusicPlaylistCanonicalHeight(data.desiredCanonicalHeight || 720);
      const network = data.uploadMbps ? ' / ' + data.uploadMbps + ' Mbps' : '';
      const bitrateOnly = data.preset && data.preset.bitrateOnly ? ' / bitrate only' : '';
      qualityStatus.textContent = (data.effective || 'auto') + 'p' + network + bitrateOnly;
    }
    function applyEncoderMode(mode) {
      if (!encoderMode) return;
	  const normalized = 'gpu';
      encoderMode.value = normalized;
    }
	function applyMusicPlaylistDeliveryProfile(mode) {
	  if (!musicPlaylistDeliveryProfile) return;
	  const allowed = ['hls-high', 'hls', 'rtsp-low', 'rtsp-ultra', 'rtsp-realtime'];
	  musicPlaylistDeliveryProfile.value = allowed.includes(mode) ? mode : 'rtsp-ultra';
	}
	function applyMusicPlaylistDeliveryState(data) {
	  const desired = data.desiredDeliveryProfile || data.musicPlaylistDeliveryProfile || 'rtsp-ultra';
	  const active = data.activeDeliveryProfile || desired;
	  applyMusicPlaylistDeliveryProfile(desired);
	  if (musicPlaylistDeliveryStatus) {
		musicPlaylistDeliveryStatus.textContent = '適用中: ' + active + (data.deliveryRestartRequired ? ' / 再起動が必要' : '');
	  }
	}
	function applyMusicPlaylistCanonicalHeight(height) {
	  if (!musicPlaylistCanonicalHeight) return;
	  const allowed = [360, 720, 1080];
	  musicPlaylistCanonicalHeight.value = String(allowed.includes(Number(height)) ? Number(height) : 720);
	  if (musicPlaylistCanonicalStatus) {
		const active = Number(state.videoQuality && state.videoQuality.activeCanonicalHeight) || Number(height) || 720;
		const restartRequired = !!(state.videoQuality && state.videoQuality.canonicalRestartRequired);
		musicPlaylistCanonicalStatus.textContent = '適用中: ' + active + 'p' + (restartRequired ? ' / 再起動が必要' : '');
	  }
	}

    function updateQualityOptions() {
      if (!formatSelect || !qualitySelect) return;
      const format = qualityOptions[formatSelect.value] ? formatSelect.value : 'webp';
      const previous = qualitySelect.value;
      qualitySelect.replaceChildren();
      for (const [value, label] of qualityOptions[format]) {
        const option = document.createElement('option');
        option.value = value;
        option.textContent = label;
        qualitySelect.appendChild(option);
      }
      const defaults = { png: 'lossless', webp: 'high', jpeg: 'high' };
      qualitySelect.value = qualityOptions[format].some(([value]) => value === previous) ? previous : defaults[format];
    }

    function updateUploadControlsVisibility() {
      if (uploadControls) {
        uploadControls.hidden = false;
      }
    }

    function uploadActionLabel() {
      if (isLiveInputMode(uploadMode)) return '配信開始';
      if (mediaIntent === 'music') {
        if (MusicController && MusicController.mode && MusicController.mode() === 'playlist') {
          return 'プレイリストに追加';
        }
        return 'ミュージックHLSを生成';
      }
      if (uploadMode === 'link') {
        return mediaIntent === 'video' ? 'リンク動画を変換して公開' : 'リンク画像を公開';
      }
      return mediaIntent === 'video' ? '動画を変換して公開' : '画像を公開';
    }

    async function syncLegacyMusicMode(enabled) {
      const desired = !!enabled && !!state.videoPlayerEnabled;
      if (!musicWorkspaceEnabled) return;
      legacyMusicModeDesired = desired;
      if (state.musicModeEnabled === desired && !legacyMusicModeSyncPending) return;
      if (legacyMusicModeSyncPending) return;
      legacyMusicModeSyncPending = true;
      try {
        const res = await apiFetch('/api/music-mode', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ enabled: desired })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyVideoPlayer(data);
        announceLocalChange();
      } catch (error) {
        await refreshState();
        showToast(error.message || 'ミュージックモードの同期に失敗しました', { error: true });
      } finally {
        legacyMusicModeSyncPending = false;
        if (legacyMusicModeDesired !== state.musicModeEnabled) {
          syncLegacyMusicMode(legacyMusicModeDesired);
        }
      }
    }

    function updateMediaNavigation() {
      const playlistActive = mediaIntent === 'music' && typeof MusicController !== 'undefined' && MusicController.mode && MusicController.mode() === 'playlist';
      const activeGroup = isLiveInputMode(uploadMode) || playlistActive ? 'live' : mediaIntent;
      const videoEnabled = !!state.videoPlayerEnabled;
      const liveInput = activeGroup === 'live';

      mediaNavParentButtons.forEach((button) => {
        const intent = button.dataset.mediaIntent;
        const active = intent === activeGroup;
        button.classList.toggle('active', active);
        button.setAttribute('aria-pressed', String(active));
        button.disabled = intent !== 'image' && (!videoEnabled || (intent === 'music' && !musicWorkspaceEnabled));
      });

      const inputModes = [
        [fileModeButton, !liveInput],
        [linkModeButton, !liveInput],
        [obsModeButton, liveInput && videoEnabled],
        [playlistModeButton, liveInput && videoEnabled && musicWorkspaceEnabled],
        [airplayModeButton, liveInput && videoEnabled && !!(state.airplay && state.airplay.enabled)]
      ];
      inputModes.forEach(([button, visible]) => {
        if (!button) return;
        button.hidden = !visible;
        button.disabled = !visible;
        const mode = button.dataset.mediaInputMode;
        const selected = mode === 'playlist'
          ? playlistActive
          : mode === uploadMode && !playlistActive;
        button.classList.toggle('active', selected);
        button.setAttribute('aria-selected', String(selected));
        button.tabIndex = visible && selected ? 0 : -1;
      });
      const playlistInputModes = [
        [playlistFileModeButton, playlistActive && uploadMode === 'file'],
        [playlistLinkModeButton, playlistActive && uploadMode === 'link']
      ];
      playlistInputModes.forEach(([button, selected]) => {
        if (!button) return;
        button.disabled = !playlistActive;
        button.classList.toggle('active', selected);
        button.setAttribute('aria-selected', String(selected));
        button.tabIndex = playlistActive && selected ? 0 : -1;
      });
      if (playlistInputTabs) playlistInputTabs.hidden = !playlistActive;
      if (modeTabs) modeTabs.classList.toggle('live-input', liveInput);
    }

    function setMediaIntent(intent, syncLegacy = true) {
      if (!musicWorkspaceEnabled && intent === 'music') {
        intent = 'image';
      }
      mediaIntent = intent === 'music' && musicWorkspaceEnabled
        ? 'music'
        : intent === 'video' && state.videoPlayerEnabled
          ? 'video'
          : 'image';
      if (mediaIntent !== 'video' && isLiveInputMode(uploadMode)) {
        if (videoInfoPanel) videoInfoPanel.hidden = true;
        uploadMode = 'file';
      }
      if (syncLegacy) syncLegacyMusicMode(mediaIntent === 'music');
      if (imageIntentButton) imageIntentButton.classList.toggle('active', mediaIntent === 'image');
      if (videoIntentButton) videoIntentButton.classList.toggle('active', mediaIntent === 'video');
      if (musicIntentButton) musicIntentButton.classList.toggle('active', mediaIntent === 'music');
      if (imageIntentButton) imageIntentButton.setAttribute('aria-pressed', String(mediaIntent === 'image'));
      if (videoIntentButton) videoIntentButton.setAttribute('aria-pressed', String(mediaIntent === 'video'));
      if (musicIntentButton) musicIntentButton.setAttribute('aria-pressed', String(mediaIntent === 'music'));
      if (MusicController) MusicController.render({ active: mediaIntent === 'music' });
      if (playlistModeButton) {
        const playlistActive = mediaIntent === 'music' && MusicController && MusicController.mode && MusicController.mode() === 'playlist';
        playlistModeButton.classList.toggle('active', playlistActive);
        playlistModeButton.setAttribute('aria-selected', String(playlistActive));
      }
      if (dropHint) {
        dropHint.textContent = mediaIntent === 'music'
          ? '音楽ファイルをここにドロップ'
          : state.videoPlayerEnabled && mediaIntent === 'video'
          ? '動画または音声をここにドロップ'
          : '画像またはRAWをここにドロップ';
      }
      if (dragDropOverlayHint) {
        dragDropOverlayHint.textContent = mediaIntent === 'music'
          ? '音楽ファイルを選択します'
          : state.videoPlayerEnabled && mediaIntent === 'video'
          ? '動画または音声ファイルを選択します'
          : '画像またはRAWファイルを選択します';
      }
      if (uploadKicker) {
        uploadKicker.textContent = mediaIntent === 'music'
          ? 'ファイルまたはアドレスから音楽配信を準備する'
          : state.videoPlayerEnabled && mediaIntent === 'video'
          ? '動画や音声を変換し、VRChat向けURLとして公開する'
          : '静止画を変換して、ImagePad URLとしてすぐ公開する';
      }
      if (uploadHeading) {
        uploadHeading.textContent = mediaIntent === 'music'
          ? 'ミュージック（' + (MusicController && MusicController.label ? MusicController.label() : 'シングル') + '）'
          : mediaIntent === 'video' ? '動画アップロード' : '画像アップロード';
      }
      if (previewHeading) {
        previewHeading.textContent = mediaIntent === 'music' ? 'ミュージックプレビュー' : mediaIntent === 'video' ? '現在公開中の動画' : '現在公開中の画像';
      }
      if (previewKicker) {
        previewKicker.textContent = mediaIntent === 'music'
          ? '再生位置、次曲、配信状態をここで確認する'
          : mediaIntent === 'video'
          ? '公開後の確認、コピー、配信終了をここで行う'
          : '公開後の確認、コピー、クリアをここで行う';
      }
      if (uploadButton) {
        uploadButton.textContent = uploadActionLabel();
        uploadButton.disabled = false;
      }
      if (fileModeButton) {
        fileModeButton.textContent = mediaIntent === 'music' ? 'ファイル' : 'ファイル';
      }
      if (imageURLInput && mediaIntent === 'music') {
        imageURLInput.placeholder = 'https://example.com/music.mp3';
      }
      if (queueUploadButton) {
        queueUploadButton.hidden = !state.videoPlayerEnabled || isLiveInputMode(uploadMode) || mediaIntent !== 'video' || mediaIntent === 'music';
      }
      imageTransformOptions.forEach((option) => {
        option.hidden = mediaIntent !== 'image';
      });
	if (qualityRow) {
		qualityRow.hidden = isLiveInputMode(uploadMode) || (mediaIntent !== 'video' && mediaIntent !== 'music');
		qualityRow.classList.toggle('standalone', mediaIntent === 'video' || mediaIntent === 'music');
	}
	if (musicPlaylistDeliveryOption) {
	  musicPlaylistDeliveryOption.hidden = !(mediaIntent === 'music' && MusicController && MusicController.mode && MusicController.mode() === 'playlist');
	}
	if (musicPlaylistCanonicalOption) {
	  musicPlaylistCanonicalOption.hidden = !(mediaIntent === 'music' && MusicController && MusicController.mode && MusicController.mode() === 'playlist');
	}
      if (obsLatencyOption) {
        obsLatencyOption.hidden = !isLiveInputMode(uploadMode);
      }
      if (videoInfoPanel) {
        videoInfoPanel.hidden = mediaIntent !== 'video';
      }
      if (!isLiveInputMode(uploadMode)) {
        setUploadMode(uploadMode);
      }
      updateMediaNavigation();
    }

    function applyVideoPlayer(data) {
      if (!data) {
        updateUploadControlsVisibility();
        return;
      }
      state.videoPlayerEnabled = !!data.enabled;
      state.musicModeEnabled = !!data.musicModeEnabled;
      imageInput.accept = data.enabled ? '' : imageAccept;
      if (!data.enabled) mediaIntent = 'image';
      if (mediaKindSwitch) mediaKindSwitch.hidden = false;
      if (mediaKindSwitch) mediaKindSwitch.classList.toggle('has-music', !!data.enabled && musicWorkspaceEnabled);
      if (mediaKindSwitch) mediaKindSwitch.dataset.enabledLabel = '画像/音声/動画';
      if (dragDropOverlayHint) dragDropOverlayHint.dataset.enabledLabel = '画像、RAW、音声、動画ファイルを選択します';
      fileModeButton.textContent = 'ファイル';
      imageURLInput.placeholder = data.enabled ? 'https://example.com/image_or_video.webp' : 'https://example.com/image.webp';
      if ((!data.enabled || mediaIntent !== 'video') && isLiveInputMode(uploadMode)) {
        setUploadMode('file');
      }
      setMediaIntent(mediaIntent, false);
      updateUploadControlsVisibility();
    }
`
