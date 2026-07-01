package server

const uiScriptRender = `    function renderHistory(items, currentID) {
      if (!historyList) return;
      if (wingMode === 'queue') {
        renderVideoQueue(state.videoQueue);
        return;
      }
      const visibleItems = wingMode === 'favorites'
        ? (items || []).filter((item) => item.favorite).slice().reverse()
        : (items || []);
      if (!visibleItems.length) {
        historyList.innerHTML = '<div class="empty">' + (wingMode === 'favorites' ? 'まだお気に入りがありません' : 'まだ履歴がありません') + '</div>';
        return;
      }
      historyList.innerHTML = '';
      for (const item of visibleItems) {
        const row = document.createElement('div');
        row.className = 'history-item' + (item.id === currentID ? ' current' : '');
        row.title = item.title || '';

        const thumb = document.createElement('div');
        thumb.className = 'history-thumb';
        if (item.kind === 'video' && !item.hasThumbnail) {
          thumb.textContent = 'VIDEO';
        } else {
          const img = document.createElement('img');
          img.src = item.thumbnailURL;
          img.alt = '';
          thumb.appendChild(img);
        }

        const meta = document.createElement('div');
        meta.className = 'history-meta';
        const title = document.createElement('div');
        title.className = 'history-title';
        title.textContent = item.title || 'untitled';
        const detail = document.createElement('div');
        detail.className = 'history-detail';
        detail.textContent = historyDetail(item);
        meta.appendChild(title);
        meta.appendChild(detail);

        const actions = document.createElement('div');
        actions.className = 'history-actions';

        const publish = document.createElement('button');
        publish.type = 'button';
        publish.className = 'history-action-button secondary';
        publish.dataset.historyPublish = item.id;
        publish.textContent = item.id === currentID ? '公開中' : '公開';
        publish.title = item.id === currentID ? '現在公開中です' : 'この項目を公開';
        publish.setAttribute('aria-label', publish.title);
        publish.disabled = item.id === currentID;

        const queue = document.createElement('button');
        queue.type = 'button';
        queue.className = 'history-action-button secondary';
        queue.dataset.historyQueue = item.id;
        queue.textContent = '変換';
        queue.title = '変換キューに追加';
        queue.setAttribute('aria-label', queue.title);
        queue.hidden = !state.videoPlayerEnabled;

        const heart = document.createElement('button');
        heart.type = 'button';
        heart.className = 'heart-button' + (item.favorite ? ' active' : '');
        heart.dataset.historyFavorite = item.id;
        heart.dataset.favorite = item.favorite ? '1' : '0';
        heart.innerHTML = item.favorite ? '&#9829;' : '&#9825;';
        heart.title = item.favorite ? 'お気に入りから削除' : 'お気に入り';
        heart.setAttribute('aria-label', heart.title);

        actions.appendChild(publish);
        actions.appendChild(queue);
        actions.appendChild(heart);

        row.appendChild(thumb);
        row.appendChild(meta);
        row.appendChild(actions);
        historyList.appendChild(row);
      }
    }

    function renderVideoQueue(items) {
      if (!historyList) return;
      if (!items || !items.length) {
        historyList.innerHTML = '<div class="empty">変換キューは空です</div>';
        return;
      }
      historyList.innerHTML = '';
      const runningItems = items.filter((item) => item.status === 'running');
      const otherItems = items.filter((item) => item.status !== 'running');
      for (const item of runningItems) {
        historyList.appendChild(queueRow(item, true));
      }
      if (runningItems.length && otherItems.length) {
        const divider = document.createElement('div');
        divider.className = 'queue-divider';
        historyList.appendChild(divider);
      }
      for (const item of otherItems) {
        historyList.appendChild(queueRow(item, false));
      }
    }

    function queueRow(item, featured) {
      const row = document.createElement('div');
      row.className = 'queue-item ' + (item.status || '') + (featured ? ' featured' : '');
      if (item.thumbnailURL) {
        row.classList.add('has-thumb');
        row.style.backgroundImage = "linear-gradient(rgba(248,250,251,.90), rgba(248,250,251,.90)), url('" + item.thumbnailURL + "')";
      }

      const top = document.createElement('div');
      top.className = 'queue-top';
      const title = document.createElement('div');
      title.className = 'queue-title';
      title.textContent = item.title || '変換ジョブ';
      const status = document.createElement('div');
      status.className = 'queue-status';
      status.textContent = queueStatusText(item.status);
      top.appendChild(title);
      top.appendChild(status);

      const detail = document.createElement('div');
      detail.className = 'history-detail';
      detail.textContent = queueDetail(item);

      row.appendChild(top);
      if (item.status === 'running') {
        const track = document.createElement('div');
        track.className = 'progress-track';
        const fill = document.createElement('div');
        fill.className = 'progress-fill';
        fill.style.width = Math.max(6, Math.min(100, Number(item.progressPercent || 0))) + '%';
        track.appendChild(fill);
        row.appendChild(track);
      }
      row.appendChild(detail);
      return row;
    }

    function queueStatusText(status) {
      switch (status) {
        case 'pending': return '待機中';
        case 'running': return '変換中';
        case 'done': return '完了';
        case 'error': return '失敗';
        case 'canceled': return '中止';
        default: return status || '不明';
      }
    }

    function queueDetail(item) {
      const parts = [];
      parts.push(item.kind === 'video' ? '動画' : '画像');
      if (item.quality) parts.push(item.quality + 'p');
      if (item.status === 'running') parts.push(Math.max(0, Math.min(99, Number(item.progressPercent || 0))) + '%');
      if (item.progressText) parts.push(item.progressText);
      if (item.message && item.message !== item.progressText) parts.push(item.message);
      return parts.join(' / ');
    }

    function historyDetail(item) {
      const parts = [];
      if (item.kind === 'video') {
        parts.push('動画');
      } else if (item.width && item.height) {
        parts.push(item.width + ' x ' + item.height);
      } else {
        parts.push('画像');
      }
      if (item.sizeBytes) {
        const mb = item.sizeBytes / 1024 / 1024;
        parts.push((mb >= 10 ? Math.round(mb) : mb.toFixed(1)) + ' MB');
      }
      if (item.persistent) {
        parts.push('保存済み');
      }
      return parts.join(' / ');
    }

    function ingestPhaseLabel(phase) {
      return { downloading: 'ダウンロード中…', analyzing: '解析中…', processing: '処理中…' }[phase] || '';
    }

    function updateToolInstall(info) {
      const active = !!(info && info.active);
      const failed = !!(info && info.failed);
      if (!active && !failed) {
        dialog.close('tool-install');
        return;
      }
      const toolLabel = { ffmpeg: 'FFmpeg', ffprobe: 'ffprobe', 'yt-dlp': 'yt-dlp' }[info.tool] || 'ツール';
      if (failed) {
        dialog.showProgress('tool-install', {
          title: 'ツールの準備に失敗しました',
          message: (info.message || '自動再試行が終了しました。') + ' ビデオプレーヤーをもう一度オンにするか、FFmpeg/ffprobe を手動で設定してください。',
          detail: '100%',
          percent: 100,
          danger: true
        });
        return;
      }
      const phaseLabel = { download: 'ダウンロード中', extract: '展開中', validate: '検証中' }[info.phase] || '準備中';
      const pct = Math.max(0, Math.min(100, Number(info.percent || 0)));
      if (info.phase === 'download' && pct > 0) {
        dialog.showProgress('tool-install', {
          title: toolLabel + ' を' + phaseLabel + '…',
          message: '必要なツールを準備しています。',
          detail: pct + '%',
          percent: pct
        });
      } else {
        dialog.showProgress('tool-install', {
          title: toolLabel + ' を' + phaseLabel + '…',
          message: '必要なツールを準備しています。',
          detail: info.attempt > 1 ? ('再試行中（' + info.attempt + '回目）') : '再試行中',
          indeterminate: true
        });
      }
    }

    function updateMobileProgress(data) {
      const ingest = data && data.ingest;
      const label = ingest && ingest.active ? ingestPhaseLabel(ingest.phase) : '';
      if (label) {
        // Pre-render phases: show the phase with an indeterminate (no %) bar.
        mobileProgress.classList.add('open');
        mobileProgressText.textContent = label + (ingest.title ? ' — ' + ingest.title : '');
        mobileProgressFill.classList.add('indeterminate');
        mobileProgressFill.style.width = '';
        return;
      }
      const video = data && data.video;
      if (!video || !video.active) {
        mobileProgress.classList.remove('open');
        mobileProgressFill.classList.remove('indeterminate');
        return;
      }
      const percent = Math.max(0, Math.min(99, Number(video.progressPercent || 0)));
      mobileProgress.classList.add('open');
      mobileProgressFill.classList.remove('indeterminate');
      mobileProgressText.textContent = percent + '% / ' + (video.progressText || video.message || '変換中');
      mobileProgressFill.style.width = Math.max(6, percent) + '%';
    }

    function scrollProgressIntoView() {
      if (!window.matchMedia('(max-width: 860px)').matches) return;
      window.setTimeout(() => {
        const target = mobileProgress.classList.contains('open') ? mobileProgress : preview;
        target.scrollIntoView({ behavior: 'smooth', block: 'center' });
      }, 120);
    }

    function currentText(current) {
      if (!current) return '未選択';
      if (current.kind === 'video') {
        if (current.sizeBytes) return '動画 ' + Math.round(current.sizeBytes / 1024 / 1024) + ' MB';
        return '動画';
      }
      return current.width + ' x ' + current.height;
    }

    function publicText(tunnel, upnp) {
      if (tunnel && tunnel.ok && tunnel.url) return '公開HTTPS ' + tunnel.url;
      if (tunnel && tunnel.message) return tunnel.message;
      return upnpText(upnp);
    }

    function upnpText(upnp) {
      if (!upnp) return '未確認';
      if (upnp.ok && upnp.externalIP) {
        return '成功 ' + upnp.externalIP;
      }
      if (upnp.ok) {
        return '成功';
      }
      return upnp.message || '未確認';
    }

    function videoText(video) {
      if (!video) return 'not checked';
      const formats = [];
      if (video.mp4) formats.push('MP4');
      if (video.hls) formats.push('HLS');
      if (formats.length && video.active) return formats.join(' / ') + ' streaming';
      if (formats.length) return formats.join(' / ') + ' ready';
      return video.message || 'not generated';
    }

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

    function applyVideoPlayer(data) {
      if (!data) {
        videoPlayerToggle.checked = false;
        videoPlayerText.textContent = '確認できません';
        musicModeRow.hidden = true;
        musicModeToggle.checked = false;
        return;
      }
      videoPlayerToggle.checked = !!data.enabled;
      videoPlayerToggle.disabled = videoPlayerPending;
      videoPlayerText.textContent = data.enabled ? '有効 / 自動コピーはHLS優先' : '無効 / 自動コピーは画像URL';
      musicModeRow.hidden = !data.enabled;
      musicModeToggle.checked = !!data.musicModeEnabled;
      musicModeToggle.disabled = musicModePending || !data.enabled;
      musicModeText.textContent = data.musicModeEnabled ? '有効 / URLは音声のみ取得' : '無効 / URLは動画として取得';
      imageInput.accept = data.enabled ? '' : imageAccept;
      dropHint.textContent = data.enabled ? 'Drop image, audio, or video files here' : 'Drop image or RAW files here';
      dragDropOverlayHint.textContent = data.enabled ? '画像、RAW、音声、動画ファイルを選択します' : '画像またはRAWファイルを選択します';
      fileModeButton.textContent = data.enabled ? '画像/音声/動画' : '画像';
      const uploadHeading = document.querySelector('.content section:first-child h2');
      if (uploadHeading) {
        uploadHeading.textContent = data.enabled ? 'メディアアップロード' : '画像アップロード';
      }
      imageURLInput.placeholder = data.enabled ? 'https://example.com/image_or_video.webp' : 'https://example.com/image.webp';
      queueUploadButton.hidden = !data.enabled || uploadMode === 'obs';
      updateUploadControlsVisibility();
      obsModeButton.hidden = !data.enabled;
      if (modeTabs) {
        modeTabs.classList.toggle('has-obs', !!data.enabled);
      }
      if (!data.enabled && uploadMode === 'obs') {
        setUploadMode('file');
      }
    }

    function applyOBS(data) {
      const server = document.getElementById('obsServerAddress');
      const key = document.getElementById('obsStreamKey');
      const status = document.getElementById('obsStatus');
      if (!server || !key || !status) return;
      if (!data) {
        server.textContent = 'RTMP receiver is unavailable';
        key.textContent = '-';
        status.textContent = 'unavailable';
        if (obsLatencyStatus) obsLatencyStatus.textContent = 'unavailable';
        dialog.close('obs-status');
        return;
      }
      server.textContent = data.serverAddress || 'RTMP receiver is stopped';
      key.textContent = obsKeyVisible ? (data.streamKey || '-') : maskSecret(data.streamKey);
      const latency = data.latency || {};
      if (obsLatencyMode) obsLatencyMode.value = latency.mode || 'hls';
      if (obsDVRToggle) obsDVRToggle.checked = !!latency.dvr;
      if (obsLatencyStatus) {
        const target = latency.target && latency.target !== 'auto' ? ' / ' + latency.target : '';
        obsLatencyStatus.textContent = (latency.label || latency.mode || 'hls') + target;
        obsLatencyStatus.title = latency.message || '';
      }
      // RTSP modes surface a copyable URL once the session is ready.
      const obsRtspt = document.getElementById('obsRtspt');
      const obsRtsptURL = document.getElementById('obsRtsptURL');
      if (obsRtspt && obsRtsptURL) {
        if (data.rtsptURL) {
          obsRtsptURL.textContent = data.rtsptURL;
          obsRtspt.style.display = '';
        } else {
          obsRtsptURL.textContent = '';
          obsRtspt.style.display = 'none';
        }
      }
      if (data.connected && data.publishing) {
        status.textContent = 'publishing / HLS event';
      } else if (data.connected) {
        status.textContent = 'connected / preview only';
      } else if (data.listening) {
        status.textContent = 'waiting';
      } else {
        status.textContent = data.message || 'stopped';
      }
      updateOBSStatusDialog(data, status.textContent);
    }

    function updateOBSStatusDialog(data, statusText) {
      if (uploadMode !== 'obs' || !data) {
        dialog.close('obs-status');
        return;
      }
      const latency = data.latency || {};
      const message = [
        statusText || '確認中',
        data.serverAddress || '',
        latency.label || latency.mode || ''
      ].filter(Boolean).join(' / ');
      dialog.showStatus('obs-status', {
        title: 'OBS 接続状態',
        message,
        state: data.connected ? 'active' : (data.listening ? 'warning' : '')
      });
    }

    function maskSecret(value) {
      return value ? '*****' : '-';
    }

    function applyOBSProtection() {
      const protectedMode = uploadMode === 'obs';
      document.body.classList.toggle('obs-protect', protectedMode);
      const protectedText = '配信保護中';
      document.getElementById('phoneURL').textContent = protectedMode ? protectedText : (state.phoneURL || '');
      document.getElementById('phoneURLMobile').textContent = protectedMode ? protectedText : (state.phoneURL || '');
      if (clearButton) {
        clearButton.textContent = protectedMode ? '配信終了' : '画像クリア';
        clearButton.title = protectedMode ? 'OBS配信を終了してVOD化し、次の待ち受けを開始' : '';
      }
      if (uploadButton) {
        uploadButton.textContent = protectedMode ? '配信開始' : (uploadMode === 'link' ? 'リンクから変換して公開' : '変換して公開');
      }
      if (qualityRow) {
        qualityRow.hidden = protectedMode;
      }
    }

    function hasDroppedFiles(event) {
      return event.dataTransfer && Array.from(event.dataTransfer.types || []).includes('Files');
    }

    function showGlobalDropOverlay() {
      document.body.classList.add('drag-drop-active');
      dragDropOverlay.setAttribute('aria-hidden', 'false');
    }

    function hideGlobalDropOverlay() {
      document.body.classList.remove('drag-drop-active');
      dragDropOverlay.setAttribute('aria-hidden', 'true');
      fileDropZone.classList.remove('dragover');
    }

    function leavingWindow(event) {
      return event.clientX <= 0 || event.clientY <= 0 || event.clientX >= window.innerWidth || event.clientY >= window.innerHeight;
    }

    function setSelectedFile(file) {
      if (!file) return false;
      if (typeof DataTransfer === 'undefined') {
        toast.textContent = 'This browser cannot accept dropped files here. Use the file picker.';
        return false;
      }
      const transfer = new DataTransfer();
      transfer.items.add(file);
      imageInput.files = transfer.files;
      imageInput.dispatchEvent(new Event('change', { bubbles: true }));
      return true;
    }

    function updateSelectedFileName() {
      const file = imageInput.files && imageInput.files[0];
      dropFileName.textContent = file ? file.name : 'No file selected';
      dropFileName.title = file ? file.name : '';
    }

    function handleFileDrop(event) {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      event.stopPropagation();
      hideGlobalDropOverlay();
      const file = event.dataTransfer.files && event.dataTransfer.files[0];
      if (!file) return;
      if (uploadMode !== 'file') {
        setUploadMode('file');
      }
      if (setSelectedFile(file)) {
        const extra = event.dataTransfer.files.length > 1 ? ' (first file only)' : '';
        toast.textContent = 'Selected ' + file.name + extra;
      }
    }

`
