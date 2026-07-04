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
        const mode = MusicController && MusicController.mode ? MusicController.mode() : 'single';
        if (mode === 'playlist') return '曲を追加';
        if (mode === 'party') return '準備中';
        return 'ミュージックHLSを生成';
      }
      if (uploadMode === 'link') {
        return mediaIntent === 'video' ? 'リンク動画を変換して公開' : 'リンク画像を公開';
      }
      return mediaIntent === 'video' ? '動画を変換して公開' : '画像を公開';
    }

    function setMediaIntent(intent) {
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
        const mode = MusicController && MusicController.mode ? MusicController.mode() : 'single';
        uploadKicker.textContent = mediaIntent === 'music' && mode === 'playlist'
          ? '曲を追加して、再生順と配信状態を管理する'
          : mediaIntent === 'music' && mode === 'party'
          ? '外部入力を受け付ける準備中のモード'
          : mediaIntent === 'music'
          ? 'ファイルまたはアドレスから音楽配信を準備する'
          : state.videoPlayerEnabled && mediaIntent === 'video'
          ? '動画や音声を変換し、VRChat向けURLとして公開する'
          : '静止画を変換して、ImagePad URLとしてすぐ公開する';
      }
      if (uploadHeading) {
        uploadHeading.textContent = mediaIntent === 'music'
          ? (MusicController && MusicController.label ? MusicController.label() : 'シングル')
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
        uploadButton.disabled = mediaIntent === 'music' && MusicController && MusicController.mode && MusicController.mode() === 'party';
      }
      if (fileModeButton) {
        fileModeButton.textContent = mediaIntent === 'music' ? 'ファイル' : 'ファイル';
      }
      if (imageURLInput && mediaIntent === 'music') {
        imageURLInput.placeholder = MusicController && MusicController.mode && MusicController.mode() === 'playlist'
          ? 'YouTubeプレイリストURL'
          : 'https://example.com/music.mp3';
      }
      if (queueUploadButton) {
        queueUploadButton.hidden = !state.videoPlayerEnabled || uploadMode === 'obs' || mediaIntent !== 'video' || mediaIntent === 'music';
      }
      imageTransformOptions.forEach((option) => {
        option.hidden = mediaIntent !== 'image';
      });
      if (qualityRow) {
        qualityRow.hidden = uploadMode === 'obs' || mediaIntent !== 'video';
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
        videoPlayerToggle.checked = false;
        videoPlayerText.textContent = '確認できません';
        musicModeRow.hidden = true;
        musicModeToggle.checked = false;
        musicModeToggle.disabled = true;
        updateUploadControlsVisibility();
        return;
      }
      videoPlayerToggle.checked = !!data.enabled;
      videoPlayerToggle.disabled = videoPlayerPending;
      videoPlayerText.textContent = data.enabled ? '有効 / 自動コピーはHLS優先' : '無効 / 自動コピーは画像URL';
      musicModeRow.hidden = !musicWorkspaceEnabled || !data.enabled;
      musicModeToggle.checked = !!data.musicModeEnabled;
      musicModeToggle.disabled = !musicWorkspaceEnabled || musicModePending || !data.enabled;
      musicModeText.textContent = data.musicModeEnabled ? '有効 / URLは音声のみ取得' : '無効 / URLは動画として取得';
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
      setMediaIntent(mediaIntent);
      updateUploadControlsVisibility();
    }
`
