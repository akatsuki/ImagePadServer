package server

const dashboardScriptPlaylistController = `
    const PlaylistController = (() => {
      let active = false;
      let pollTimer = 0;
      let clockTimer = 0;
	let plState = { tracks: [], currentTrackId: '', running: false, playing: false, paused: false, shuffle: false, loop: false, deliveryProfile: 'rtsp-ultra', desiredDeliveryProfile: 'rtsp-ultra', activeDeliveryProfile: 'rtsp-ultra', desiredCanonicalHeight: 720, activeCanonicalHeight: 720, rtspUrl: '', rtspPublic: false, hlsUrl: '', publicHlsUrl: '', elapsedSeconds: 0 };
      let plFetchedAt = 0;
      let urlMode = 'hls';
      let dragTrackId = null;
      let savedNames = [];
      let refreshing = false;
      let plHLS = null;
      let previewAttached = false;

      function fmtTime(totalSeconds) {
        const s = Math.max(0, Math.floor(Number(totalSeconds) || 0));
        return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
      }

      async function refreshPlaylist() {
        if (refreshing) return;
        refreshing = true;
        try {
          const res = await apiFetch('/api/music/playlist', { cache: 'no-store' });
          if (!res.ok) throw new Error(await res.text());
		  plState = await res.json();
		  urlMode = String(plState.activeDeliveryProfile || plState.deliveryProfile || '').startsWith('hls') ? 'hls' : 'rtsp';
          plFetchedAt = Date.now();
          renderPlaylist();
        } catch (error) {
        } finally {
          refreshing = false;
        }
      }

      async function playlistPost(path, body) {
        try {
          const res = await apiFetch(path, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body || {})
          });
          const text = await res.text();
          if (!res.ok) throw new Error(text || 'リクエストに失敗しました');
          plState = JSON.parse(text || '{}');
          plFetchedAt = Date.now();
          renderPlaylist();
          return true;
        } catch (error) {
          showToast((error && error.message) || 'リクエストに失敗しました', { error: true });
          refreshPlaylist();
          return false;
        }
      }

      // addFromUploadForm routes the ordinary upload form (file / link tabs)
      // into the playlist queue while playlist mode is active.
      async function addFromUploadForm(mode) {
        if (mode === 'link') {
          const url = imageURLInput.value.trim();
          if (!url) {
            imageURLInput.reportValidity();
            return;
          }
          if (await playlistPost('/api/music/playlist/add', { input: url })) {
            imageURLInput.value = '';
            showToast('プレイリストに追加しました');
          }
          return;
        }
        const file = selectedUploadFile();
        if (!file) {
          imageInput.reportValidity();
          return;
        }
        const formData = new FormData();
        formData.append('file', file, file.name);
        try {
          const res = await apiFetch('/api/music/playlist/add', { method: 'POST', body: formData });
          const text = await res.text();
          if (!res.ok) throw new Error(text || 'アップロードに失敗しました');
          plState = JSON.parse(text || '{}');
          plFetchedAt = Date.now();
          renderPlaylist();
          imageInput.value = '';
          updateSelectedFileName();
          showToast('プレイリストに追加しました');
        } catch (error) {
          showToast((error && error.message) || 'プレイリストへの追加に失敗しました', { error: true });
        }
      }

      function currentTrack() {
        return (plState.tracks || []).find((t) => t.id === plState.currentTrackId) || null;
      }

      function estimatedElapsed() {
        if (!plState.playing) return 0;
        return (Number(plState.elapsedSeconds) || 0) + Math.max(0, (Date.now() - plFetchedAt) / 1000);
      }

      function renderClock() {
        const track = currentTrack();
        const duration = track ? Number(track.durationSeconds) || 0 : 0;
        const activeClock = plState.playing || plState.paused;
        const elapsed = Math.min(plState.paused ? (Number(plState.elapsedSeconds) || 0) : estimatedElapsed(), duration || Infinity);
        if (plTimeElapsed) plTimeElapsed.textContent = activeClock ? fmtTime(elapsed) : '0:00';
        if (plTimeRemaining) plTimeRemaining.textContent = activeClock && duration > 0 ? '-' + fmtTime(Math.max(0, duration - elapsed)) : '-0:00';
        if (plProgressFill) {
          const pct = activeClock && duration > 0 ? Math.min(100, elapsed / duration * 100) : 0;
          plProgressFill.style.width = pct + '%';
        }
        if (plProgressTrack) {
          plProgressTrack.setAttribute('aria-valuemin', '0');
          plProgressTrack.setAttribute('aria-valuemax', '100');
          plProgressTrack.setAttribute('aria-valuenow', String(Math.round(plState.playing && duration > 0 ? Math.min(100, estimatedElapsed() / duration * 100) : 0)));
        }
      }

      function destroyPlaylistHLS() {
        previewAttached = false;
        if (plHLS) {
          try {
            plHLS.destroy();
          } catch (error) {
          }
          plHLS = null;
        }
        if (plVideoPreview) {
          plVideoPreview.removeAttribute('src');
          try {
            plVideoPreview.load();
          } catch (error) {
          }
        }
      }

      function syncVideoPreview() {
        const shouldShow = active && urlMode === 'hls' && plState.playing && !!plState.hlsUrl;
        if (plVideoWrap) plVideoWrap.classList.toggle('pl-video-live', shouldShow);
        if (plVideoEmpty) plVideoEmpty.hidden = shouldShow;
        if (!plVideoPreview) return;
        if (!shouldShow) {
          if (previewAttached) destroyPlaylistHLS();
          return;
        }
        if (previewAttached) return;
        const src = '/radio/index.m3u8';
        if (window.Hls && window.Hls.isSupported()) {
          plHLS = new window.Hls({ lowLatencyMode: true, backBufferLength: 30 });
          // 配信立ち上がり直後の404などは破棄して次のポーリングで再接続する。
          plHLS.on(window.Hls.Events.ERROR, (event, data) => {
            if (data && data.fatal) destroyPlaylistHLS();
          });
          plHLS.loadSource(src);
          plHLS.attachMedia(plVideoPreview);
          previewAttached = true;
        } else if (plVideoPreview.canPlayType('application/vnd.apple.mpegurl')) {
          plVideoPreview.src = src;
          previewAttached = true;
        }
        if (previewAttached) {
          const playAttempt = plVideoPreview.play();
          if (playAttempt && playAttempt.catch) playAttempt.catch(() => {});
        }
      }

      function sourceLabel(track) {
        switch (track.sourceKind) {
          case 'soundcloud': return 'SoundCloud';
          case 'music': return 'リンク';
          case 'local_audio': return 'ファイル';
          case 'remote_audio': return 'リンク';
          default: return track.sourceKind || '';
        }
      }

      function renderPlaylist() {
        const track = currentTrack();
        const radioFailed = plState.phase === 'failed';
        if (plNowTitle) {
          plNowTitle.textContent = radioFailed
            ? '配信に失敗しました'
            : (plState.playing || plState.paused) && track
            ? track.title || track.originalName || '再生中'
            : plState.running ? '次の曲を待っています' : 'プレイリストは停止中';
        }
        if (plNowArtist) {
          plNowArtist.textContent = radioFailed
            ? '失敗: ' + (plState.lastError || '詳細不明')
            : plState.paused
            ? '一時停止中'
            : plState.playing && track
            ? (track.artist || ' ')
            : (plState.tracks || []).length ? '▶で再生を開始する' : '曲を追加して再生を始める';
        }
        renderClock();
        syncVideoPreview();
        if (plPlayButton) {
          plPlayButton.classList.toggle('is-playing', !!plState.playing);
          plPlayButton.setAttribute('aria-label', plState.playing ? '一時停止' : '再生');
          plPlayButton.title = plState.playing ? '一時停止' : '再生';
        }
        if (plStartStreamButton) {
          plStartStreamButton.disabled = !!plState.running;
          plStartStreamButton.classList.toggle('is-running', !!plState.running);
          const label = plState.running ? '配信中' : '配信開始';
          plStartStreamButton.setAttribute('aria-label', label);
          plStartStreamButton.title = label;
          const text = plStartStreamButton.querySelector('span');
          if (text) text.textContent = label;
        }
        if (plShuffleButton) {
          plShuffleButton.classList.toggle('active', !!plState.shuffle);
          plShuffleButton.setAttribute('aria-pressed', String(!!plState.shuffle));
        }
        if (plLoopButton) {
          plLoopButton.classList.toggle('active', !!plState.loop);
          plLoopButton.setAttribute('aria-pressed', String(!!plState.loop));
        }
        renderTrackList();
        if (plTrackCount) plTrackCount.textContent = (plState.tracks || []).length + ' 曲';
        renderShareURL();
      }

      function renderShareURL() {
        if (plUrlModeHLS) {
          plUrlModeHLS.setAttribute('aria-pressed', String(urlMode === 'hls'));
          plUrlModeHLS.classList.toggle('active', urlMode === 'hls');
        }
        if (plUrlModeRTSP) {
          plUrlModeRTSP.setAttribute('aria-pressed', String(urlMode === 'rtsp'));
          plUrlModeRTSP.classList.toggle('active', urlMode === 'rtsp');
        }
        if (!plShareUrl) return;
        const url = urlMode === 'rtsp'
          ? (plState.rtspPublic ? (plState.rtspUrl || '') : '')
          : (plState.publicHlsUrl || plState.hlsUrl || '');
        let placeholder = '再生を開始するとURLが表示されます';
        if (plState.phase === 'failed') placeholder = '失敗: ' + (plState.lastError || '詳細不明');
        else if (plState.running && urlMode === 'hls' && !plState.hlsReady) placeholder = '配信準備中';
        else if (plState.running && urlMode === 'rtsp' && !plState.rtspReady) placeholder = '配信準備中';
        else if (plState.running && urlMode === 'rtsp' && plState.rtspReady && !plState.rtspPublic) placeholder = '公開経路準備中';
        plShareUrl.textContent = url || placeholder;
      }

      function clearDropMarkers() {
        if (!plTrackList) return;
        plTrackList.querySelectorAll('.pl-drop-before, .pl-drop-after').forEach((el) => {
          el.classList.remove('pl-drop-before', 'pl-drop-after');
        });
      }

      function reorderTo(movedId, anchorId, before) {
        const ids = (plState.tracks || []).map((t) => t.id).filter((id) => id !== movedId);
        const anchorIndex = ids.indexOf(anchorId);
        if (anchorIndex < 0) return;
        ids.splice(before ? anchorIndex : anchorIndex + 1, 0, movedId);
        playlistPost('/api/music/playlist/reorder', { ids });
      }

      function trackRow(track, index) {
        const row = document.createElement('div');
        row.className = 'pl-row';
        row.dataset.trackId = track.id;
        row.draggable = true;
        row.classList.toggle('pl-now-playing', track.id === plState.currentTrackId && !!plState.playing);
        row.classList.toggle('pl-preparing', track.status === 'preparing');
        row.classList.toggle('pl-failed', track.status === 'failed');

        const num = document.createElement('span');
        num.className = 'pl-row-num';
        if (track.id === plState.currentTrackId && plState.playing) {
          num.innerHTML = '<svg class="pl-speaker" viewBox="0 0 24 24" aria-label="再生中" role="img"><path fill="currentColor" d="M4 9v6h4l5 4V5L8 9H4Z"/><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" d="M16.5 8.5a5 5 0 0 1 0 7"/></svg>';
        } else {
          num.textContent = String(index + 1);
        }
        row.appendChild(num);

        if (track.hasArtwork) {
          const art = document.createElement('img');
          art.className = 'pl-art';
          art.alt = '';
          art.src = '/api/music/playlist/artwork?id=' + encodeURIComponent(track.id) + (pageAdminToken ? '&token=' + encodeURIComponent(pageAdminToken) : '');
          row.appendChild(art);
        }

        if (track.status === 'preparing') {
          const bar = document.createElement('div');
          bar.className = 'pl-row-progress';
          const fill = document.createElement('div');
          fill.className = 'pl-row-progress-fill';
          fill.style.width = Math.max(2, Math.min(99, Number(track.progress) || 0)) + '%';
          bar.appendChild(fill);
          row.appendChild(bar);
        }
        const copy = document.createElement('div');
        copy.className = 'pl-row-copy';
        const title = document.createElement('strong');
        title.textContent = track.title || track.originalName || '(不明な曲)';
        copy.appendChild(title);
        const sub = document.createElement('span');
        if (track.status === 'preparing') {
          sub.textContent = '準備中… ' + (Number(track.progress) || 0) + '%';
          sub.className = 'pl-row-sub pl-row-status';
        } else if (track.status === 'failed') {
          sub.textContent = '失敗: ' + (track.error || '');
          sub.className = 'pl-row-sub pl-row-error';
          sub.title = track.error || '';
        } else {
          const detail = [track.artist, sourceLabel(track)].filter(Boolean).join(' — ');
          sub.textContent = detail || ' ';
          sub.className = 'pl-row-sub';
        }
        copy.appendChild(sub);
        row.appendChild(copy);

        const time = document.createElement('span');
        time.className = 'pl-row-time';
        time.textContent = track.durationSeconds ? fmtTime(track.durationSeconds) : '';
        row.appendChild(time);

        const actions = document.createElement('div');
        actions.className = 'pl-row-actions';
        const playNow = document.createElement('button');
        playNow.type = 'button';
        playNow.className = 'pl-row-action';
        playNow.title = '今すぐ再生';
        playNow.setAttribute('aria-label', (track.title || '曲') + ' を今すぐ再生');
        playNow.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="currentColor" d="M8 5v14l11-7L8 5Z"/></svg>';
        playNow.disabled = track.status !== 'ready';
        playNow.addEventListener('click', () => playlistPost('/api/music/playlist/play', { id: track.id }));
        actions.appendChild(playNow);
        const remove = document.createElement('button');
        remove.type = 'button';
        remove.className = 'pl-row-action pl-row-remove';
        remove.title = '削除';
        remove.setAttribute('aria-label', (track.title || '曲') + ' を削除');
        remove.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" d="M6 6l12 12M18 6 6 18"/></svg>';
        remove.addEventListener('click', () => playlistPost('/api/music/playlist/remove', { id: track.id }));
        actions.appendChild(remove);
        row.appendChild(actions);

        row.addEventListener('dragstart', (event) => {
          dragTrackId = track.id;
          row.classList.add('pl-dragging');
          if (event.dataTransfer) {
            event.dataTransfer.effectAllowed = 'move';
            event.dataTransfer.setData('text/plain', track.id);
          }
        });
        row.addEventListener('dragend', () => {
          dragTrackId = null;
          row.classList.remove('pl-dragging');
          clearDropMarkers();
        });
        row.addEventListener('dragover', (event) => {
          if (!dragTrackId || dragTrackId === track.id) return;
          event.preventDefault();
          event.stopPropagation();
          clearDropMarkers();
          const rect = row.getBoundingClientRect();
          row.classList.add(event.clientY < rect.top + rect.height / 2 ? 'pl-drop-before' : 'pl-drop-after');
        });
        row.addEventListener('drop', (event) => {
          if (!dragTrackId || dragTrackId === track.id) return;
          event.preventDefault();
          event.stopPropagation();
          const rect = row.getBoundingClientRect();
          const before = event.clientY < rect.top + rect.height / 2;
          clearDropMarkers();
          reorderTo(dragTrackId, track.id, before);
        });
        return row;
      }

      function renderTrackList() {
        if (!plTrackList) return;
        plTrackList.replaceChildren();
        const tracks = plState.tracks || [];
        if (tracks.length === 0) {
          const empty = document.createElement('div');
          empty.className = 'pl-list-empty';
          empty.textContent = '曲がありません。左のアップロードから追加してください';
          plTrackList.appendChild(empty);
          return;
        }
        tracks.forEach((track, index) => plTrackList.appendChild(trackRow(track, index)));
      }

      function setSavedMenuOpen(open) {
        if (!plMenu || !plMenuButton) return;
        plMenu.hidden = !open;
        plMenuButton.setAttribute('aria-expanded', String(open));
        if (open) refreshSavedNames();
      }

      async function refreshSavedNames() {
        try {
          const res = await apiFetch('/api/music/playlists', { cache: 'no-store' });
          if (!res.ok) throw new Error(await res.text());
          const data = await res.json();
          savedNames = Array.isArray(data.names) ? data.names : [];
        } catch (error) {
          savedNames = [];
        }
        renderSavedList();
      }

      function renderSavedList() {
        if (!plSavedList) return;
        plSavedList.replaceChildren();
        if (savedNames.length === 0) {
          const empty = document.createElement('div');
          empty.className = 'pl-saved-empty';
          empty.textContent = '保存済みプレイリストはありません';
          plSavedList.appendChild(empty);
          return;
        }
        savedNames.forEach((name) => {
          const row = document.createElement('div');
          row.className = 'pl-saved-row';
          const load = document.createElement('button');
          load.type = 'button';
          load.className = 'pl-saved-load';
          load.textContent = name;
          load.title = '「' + name + '」を読み込む';
          load.addEventListener('click', async () => {
            setSavedMenuOpen(false);
            await playlistPost('/api/music/playlists/load', { name });
          });
          row.appendChild(load);
          const del = document.createElement('button');
          del.type = 'button';
          del.className = 'pl-saved-delete';
          del.title = '「' + name + '」を削除';
          del.setAttribute('aria-label', '「' + name + '」を削除');
          del.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" d="M6 6l12 12M18 6 6 18"/></svg>';
          del.addEventListener('click', async (event) => {
            event.stopPropagation();
            try {
              const res = await apiFetch('/api/music/playlists/delete', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ name })
              });
              if (!res.ok) throw new Error(await res.text());
              const data = await res.json();
              savedNames = Array.isArray(data.names) ? data.names : [];
              renderSavedList();
            } catch (error) {
              showToast((error && error.message) || '削除に失敗しました', { error: true });
            }
          });
          row.appendChild(del);
          plSavedList.appendChild(row);
        });
      }

      async function saveCurrentPlaylist() {
        const name = window.prompt('プレイリスト名を入力してください');
        if (name === null) return;
        try {
          const res = await apiFetch('/api/music/playlists', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ name })
          });
          if (!res.ok) throw new Error(await res.text());
          const data = await res.json();
          savedNames = Array.isArray(data.names) ? data.names : [];
          renderSavedList();
          showToast('プレイリスト「' + name.trim() + '」を保存しました');
        } catch (error) {
          showToast((error && error.message) || '保存に失敗しました', { error: true });
        }
      }

      function startPolling() {
        stopPolling();
        pollTimer = setInterval(refreshPlaylist, 2000);
        clockTimer = setInterval(renderClock, 500);
      }

      function stopPolling() {
        if (pollTimer) clearInterval(pollTimer);
        if (clockTimer) clearInterval(clockTimer);
        pollTimer = 0;
        clockTimer = 0;
      }

      function setPlaylistActive(next) {
        const value = !!next;
        if (value === active) return;
        active = value;
        if (active) {
          refreshPlaylist();
          startPolling();
        } else {
          stopPolling();
          setSavedMenuOpen(false);
          destroyPlaylistHLS();
          if (plVideoWrap) plVideoWrap.classList.remove('pl-video-live');
        }
      }

      function isPlaylistActive() {
        return active;
      }

      function initPlaylistController() {
        if (plStartStreamButton) plStartStreamButton.addEventListener('click', () => playlistPost('/api/music/playlist/start', {}));
        if (plPlayButton) plPlayButton.addEventListener('click', () => {
          playlistPost(plState.playing ? '/api/music/playlist/pause' : '/api/music/playlist/play', {});
        });
        if (plStopButton) plStopButton.addEventListener('click', () => playlistPost('/api/music/playlist/stop', {}));
        if (plNextButton) plNextButton.addEventListener('click', () => playlistPost('/api/music/playlist/next', {}));
        if (plShuffleButton) plShuffleButton.addEventListener('click', () => playlistPost('/api/music/playlist/options', { shuffle: !plState.shuffle }));
        if (plLoopButton) plLoopButton.addEventListener('click', () => playlistPost('/api/music/playlist/options', { loop: !plState.loop }));
        if (plProgressTrack) plProgressTrack.addEventListener('click', (event) => {
          const track = currentTrack();
          const duration = track ? Number(track.durationSeconds) || 0 : 0;
          if (!duration || (!plState.playing && !plState.paused)) return;
          const rect = plProgressTrack.getBoundingClientRect();
          if (rect.width <= 0) return;
          const pct = Math.min(1, Math.max(0, (event.clientX - rect.left) / rect.width));
          playlistPost('/api/music/playlist/seek', { seconds: Math.floor(pct * duration) });
        });
        if (plUrlModeHLS) plUrlModeHLS.addEventListener('click', () => { urlMode = 'hls'; renderShareURL(); syncVideoPreview(); });
        if (plUrlModeRTSP) plUrlModeRTSP.addEventListener('click', () => { urlMode = 'rtsp'; renderShareURL(); syncVideoPreview(); });
        if (plMenuButton) plMenuButton.addEventListener('click', (event) => {
          event.stopPropagation();
          setSavedMenuOpen(!!plMenu && plMenu.hidden);
        });
        if (plSaveButton) plSaveButton.addEventListener('click', () => {
          setSavedMenuOpen(false);
          saveCurrentPlaylist();
        });
        document.addEventListener('click', (event) => {
          if (!plMenu || plMenu.hidden) return;
          if (plMenu.contains(event.target) || (plMenuButton && plMenuButton.contains(event.target))) return;
          setSavedMenuOpen(false);
        });
      }

      return {
        init: initPlaylistController,
        setActive: setPlaylistActive,
        isActive: isPlaylistActive,
        refresh: refreshPlaylist,
        addFromUploadForm
      };
    })();
`
