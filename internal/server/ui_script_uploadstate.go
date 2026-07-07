package server

const dashboardScriptUploadState = `
    function applyQuality(data) {
      if (!data) {
        qualityStatus.textContent = '未測定';
        return;
      }
      qualityMode.value = data.mode || 'auto';
      const network = data.uploadMbps ? ' / ' + data.uploadMbps + ' Mbps' : '';
      const bitrateOnly = data.preset && data.preset.bitrateOnly ? ' / bitrate only' : '';
      qualityStatus.textContent = (data.effective || 'auto') + 'p' + network + bitrateOnly;
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
      if (uploadMode === 'obs') return '配信開始';
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

    function setMediaIntent(intent, syncLegacy = true) {
      if (!musicWorkspaceEnabled && intent === 'music') {
        intent = 'image';
      }
      mediaIntent = intent === 'music' && musicWorkspaceEnabled
        ? 'music'
        : intent === 'video' && state.videoPlayerEnabled
          ? 'video'
          : 'image';
      if (mediaIntent !== 'video' && uploadMode === 'obs') {
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
      if (obsModeButton) obsModeButton.hidden = !state.videoPlayerEnabled || mediaIntent !== 'video';
      if (modeTabs) modeTabs.classList.toggle('has-obs', !!state.videoPlayerEnabled && mediaIntent === 'video');
      if (MusicController) MusicController.render({ active: mediaIntent === 'music' });
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
        queueUploadButton.hidden = !state.videoPlayerEnabled || uploadMode === 'obs' || mediaIntent !== 'video' || mediaIntent === 'music';
      }
      imageTransformOptions.forEach((option) => {
        option.hidden = mediaIntent !== 'image';
      });
	if (qualityRow) {
		qualityRow.hidden = uploadMode === 'obs' || (mediaIntent !== 'video' && mediaIntent !== 'music');
		qualityRow.classList.toggle('standalone', mediaIntent === 'video' || mediaIntent === 'music');
	}
      if (obsLatencyOption) {
        obsLatencyOption.hidden = uploadMode !== 'obs';
      }
      if (videoInfoPanel) {
        videoInfoPanel.hidden = mediaIntent !== 'video';
      }
      if (uploadMode !== 'obs') {
        setUploadMode(uploadMode);
      }
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
      if (mediaKindSwitch) mediaKindSwitch.hidden = !data.enabled;
      if (mediaKindSwitch) mediaKindSwitch.classList.toggle('has-music', !!data.enabled && musicWorkspaceEnabled);
      if (musicIntentButton) musicIntentButton.hidden = !data.enabled || !musicWorkspaceEnabled;
      if (mediaKindSwitch) mediaKindSwitch.dataset.enabledLabel = '画像/音声/動画';
      if (dragDropOverlayHint) dragDropOverlayHint.dataset.enabledLabel = '画像、RAW、音声、動画ファイルを選択します';
      fileModeButton.textContent = 'ファイル';
      imageURLInput.placeholder = data.enabled ? 'https://example.com/image_or_video.webp' : 'https://example.com/image.webp';
      obsModeButton.hidden = !data.enabled || mediaIntent !== 'video';
      if (modeTabs) {
        modeTabs.classList.toggle('has-obs', !!data.enabled && mediaIntent === 'video');
      }
      if ((!data.enabled || mediaIntent !== 'video') && uploadMode === 'obs') {
        setUploadMode('file');
      }
      setMediaIntent(mediaIntent, false);
      updateUploadControlsVisibility();
    }
`
