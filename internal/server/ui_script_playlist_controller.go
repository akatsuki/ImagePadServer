package server

const dashboardScriptPlaylistController = `
    const PlaylistController = (() => {
      let active = false;
      let pollTimer = 0;
      let clockTimer = 0;
      let plState = { tracks: [], currentTrackId: '', running: false, playing: false, shuffle: false, loop: false, rtspUrl: '', hlsUrl: '', publicHlsUrl: '', elapsedSeconds: 0 };
      let plFetchedAt = 0;
      let urlMode = 'hls';
      let dragTrackId = null;
      let playWhenReadyId = '';
      let savedNames = [];
      let refreshing = false;

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
          plFetchedAt = Date.now();
          maybeAutoPlayPrepared();
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

      function maybeAutoPlayPrepared() {
        if (!playWhenReadyId) return;
        const track = (plState.tracks || []).find((t) => t.id === playWhenReadyId);
        if (!track) {
          playWhenReadyId = '';
          return;
        }
        if (track.status === 'ready') {
          const id = playWhenReadyId;
          playWhenReadyId = '';
          playlistPost('/api/music/playlist/play', { id });
        } else if (track.status === 'failed') {
          playWhenReadyId = '';
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
        const elapsed = Math.min(estimatedElapsed(), duration || Infinity);
        if (plTimeElapsed) plTimeElapsed.textContent = plState.playing ? fmtTime(elapsed) : '0:00';
        if (plTimeRemaining) plTimeRemaining.textContent = plState.playing && duration > 0 ? '-' + fmtTime(Math.max(0, duration - elapsed)) : '-0:00';
        if (plProgressFill) {
          const pct = plState.playing && duration > 0 ? Math.min(100, elapsed / duration * 100) : 0;
          plProgressFill.style.width = pct + '%';
        }
        if (plProgressTrack) {
          plProgressTrack.setAttribute('aria-valuemin', '0');
          plProgressTrack.setAttribute('aria-valuemax', '100');
          plProgressTrack.setAttribute('aria-valuenow', String(Math.round(plState.playing && duration > 0 ? Math.min(100, estimatedElapsed() / duration * 100) : 0)));
        }
      }

      function sourceLabel(track) {
        switch (track.sourceKind) {
          case 'soundcloud': return 'SoundCloud';
          case 'music': return 'リンク';
          case 'local_audio': return 'ファイル';
          case 'remote_audio': return 'リンク';
          default: return track.sourceKind || '-';
        }
      }

      function renderPlaylist() {
        const track = currentTrack();
        if (plNowTitle) {
          plNowTitle.textContent = plState.playing && track
            ? track.title || track.originalName || '再生中'
            : plState.running ? '次の曲を待っています' : 'プレイリストは停止中';
        }
        if (plNowArtist) {
          plNowArtist.textContent = plState.playing && track
            ? (track.artist || ' ')
            : (plState.tracks || []).length ? '▶で再生を開始する' : '曲を追加して再生を始める';
        }
        renderClock();
        if (plPlayButton) plPlayButton.classList.toggle('active', !!plState.playing);
        if (plShuffleButton) {
          plShuffleButton.classList.toggle('active', !!plState.shuffle);
          plShuffleButton.setAttribute('aria-pressed', String(!!plState.shuffle));
        }
        if (plLoopButton) {
          plLoopButton.classList.toggle('active', !!plState.loop);
          plLoopButton.setAttribute('aria-pressed', String(!!plState.loop));
        }
        renderPlaylistTable();
        if (plTrackCount) plTrackCount.textContent = (plState.tracks || []).length + ' 曲';
        renderShareURL();
      }

      function renderShareURL() {
        if (plUrlModeHLS) plUrlModeHLS.setAttribute('aria-pressed', String(urlMode === 'hls'));
        if (plUrlModeRTSP) plUrlModeRTSP.setAttribute('aria-pressed', String(urlMode === 'rtsp'));
        if (plUrlModeHLS) plUrlModeHLS.classList.toggle('active', urlMode === 'hls');
        if (plUrlModeRTSP) plUrlModeRTSP.classList.toggle('active', urlMode === 'rtsp');
        if (!plShareUrl) return;
        const url = urlMode === 'rtsp' ? (plState.rtspUrl || '') : (plState.publicHlsUrl || plState.hlsUrl || '');
        plShareUrl.textContent = url || '再生を開始するとURLが表示されます';
      }

      function trackRow(track, index) {
        const row = document.createElement('tr');
        row.dataset.trackId = track.id;
        row.draggable = true;
        row.classList.toggle('pl-now-playing', track.id === plState.currentTrackId && !!plState.playing);
        row.classList.toggle('pl-preparing', track.status === 'preparing');
        row.classList.toggle('pl-failed', track.status === 'failed');

        const num = document.createElement('td');
        num.className = 'pl-col-num';
        if (track.id === plState.currentTrackId && plState.playing) {
          num.innerHTML = '<svg class="pl-speaker" viewBox="0 0 24 24" aria-label="再生中" role="img"><path fill="currentColor" d="M4 9v6h4l5 4V5L8 9H4Z"/><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" d="M16.5 8.5a5 5 0 0 1 0 7M19 6a8.5 8.5 0 0 1 0 12"/></svg>';
        } else {
          num.textContent = String(index + 1);
        }
        row.appendChild(num);

        const title = document.createElement('td');
        title.className = 'pl-col-title';
        if (track.hasArtwork) {
          const art = document.createElement('img');
          art.className = 'pl-art';
          art.alt = '';
          art.src = '/api/music/playlist/artwork?id=' + encodeURIComponent(track.id) + (pageAdminToken ? '&token=' + encodeURIComponent(pageAdminToken) : '');
          title.appendChild(art);
        }
        const titleText = document.createElement('span');
        titleText.textContent = track.title || track.originalName || '(不明な曲)';
        title.appendChild(titleText);
        if (track.status === 'preparing') {
          const badge = document.createElement('em');
          badge.className = 'pl-badge';
          badge.textContent = '準備中…';
          title.appendChild(badge);
        } else if (track.status === 'failed') {
          const badge = document.createElement('em');
          badge.className = 'pl-badge pl-badge-error';
          badge.textContent = '失敗';
          badge.title = track.error || '';
          title.appendChild(badge);
        }
        row.appendChild(title);

        const artist = document.createElement('td');
        artist.className = 'pl-col-artist';
        artist.textContent = track.artist || '-';
        row.appendChild(artist);

        const time = document.createElement('td');
        time.className = 'pl-col-time';
        time.textContent = track.durationSeconds ? fmtTime(track.durationSeconds) : '-';
        row.appendChild(time);

        const source = document.createElement('td');
        source.className = 'pl-col-source';
        source.textContent = sourceLabel(track);
        row.appendChild(source);

        const actions = document.createElement('td');
        actions.className = 'pl-col-actions';
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
          clearDropMarkers();
          const rect = row.getBoundingClientRect();
          row.classList.add(event.clientY < rect.top + rect.height / 2 ? 'pl-drop-before' : 'pl-drop-after');
        });
        row.addEventListener('drop', (event) => {
          if (!dragTrackId || dragTrackId === track.id) return;
          event.preventDefault();
          const rect = row.getBoundingClientRect();
          const before = event.clientY < rect.top + rect.height / 2;
          clearDropMarkers();
          reorderTo(dragTrackId, track.id, before);
        });
        return row;
      }

      function clearDropMarkers() {
        if (!plTrackTableBody) return;
        plTrackTableBody.querySelectorAll('.pl-drop-before, .pl-drop-after').forEach((el) => {
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

      function renderPlaylistTable() {
        if (!plTrackTableBody) return;
        plTrackTableBody.replaceChildren();
        const tracks = plState.tracks || [];
        if (tracks.length === 0) {
          const row = document.createElement('tr');
          row.className = 'pl-empty-row';
          const cell = document.createElement('td');
          cell.colSpan = 6;
          cell.textContent = '曲がありません。上の入力欄から追加してください';
          row.appendChild(cell);
          plTrackTableBody.appendChild(row);
          return;
        }
        tracks.forEach((track, index) => plTrackTableBody.appendChild(trackRow(track, index)));
      }

      async function addFromInput(playImmediately) {
        if (!plInput) return;
        const value = plInput.value.trim();
        if (!value) {
          showToast('URL またはファイルパスを入力してください', { error: true });
          return;
        }
        const beforeIds = new Set((plState.tracks || []).map((t) => t.id));
        if (await playlistPost('/api/music/playlist/add', { input: value })) {
          plInput.value = '';
          if (playImmediately) {
            const added = (plState.tracks || []).find((t) => !beforeIds.has(t.id));
            if (added) playWhenReadyId = added.id;
          }
        }
      }

      async function addFiles(files, playImmediately) {
        for (const file of Array.from(files || [])) {
          const beforeIds = new Set((plState.tracks || []).map((t) => t.id));
          const formData = new FormData();
          formData.append('file', file, file.name);
          try {
            const res = await apiFetch('/api/music/playlist/add', { method: 'POST', body: formData });
            const text = await res.text();
            if (!res.ok) throw new Error(text || 'アップロードに失敗しました');
            plState = JSON.parse(text || '{}');
            plFetchedAt = Date.now();
            if (playImmediately) {
              const added = (plState.tracks || []).find((t) => !beforeIds.has(t.id));
              if (added) playWhenReadyId = added.id;
              playImmediately = false;
            }
            renderPlaylist();
          } catch (error) {
            showToast((error && error.message) || (file.name + ' の追加に失敗しました'), { error: true });
          }
        }
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
          load.role = 'menuitem';
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
        }
      }

      function initPlaylistController() {
        if (plAddButton) plAddButton.addEventListener('click', () => addFromInput(false));
        if (plPlayNowButton) plPlayNowButton.addEventListener('click', () => {
          if (plInput && plInput.value.trim()) {
            addFromInput(true);
            return;
          }
          playlistPost('/api/music/playlist/play', {});
        });
        if (plInput) plInput.addEventListener('keydown', (event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            addFromInput(false);
          }
        });
        if (plFileButton && plFileInput) {
          plFileButton.addEventListener('click', () => plFileInput.click());
          plFileInput.addEventListener('change', () => {
            if (plFileInput.files && plFileInput.files.length) {
              addFiles(plFileInput.files, false);
              plFileInput.value = '';
            }
          });
        }
        if (plPlayButton) plPlayButton.addEventListener('click', () => playlistPost('/api/music/playlist/play', {}));
        if (plStopButton) plStopButton.addEventListener('click', () => playlistPost('/api/music/playlist/stop', {}));
        if (plNextButton) plNextButton.addEventListener('click', () => playlistPost('/api/music/playlist/next', {}));
        if (plShuffleButton) plShuffleButton.addEventListener('click', () => playlistPost('/api/music/playlist/options', { shuffle: !plState.shuffle }));
        if (plLoopButton) plLoopButton.addEventListener('click', () => playlistPost('/api/music/playlist/options', { loop: !plState.loop }));
        if (plUrlModeHLS) plUrlModeHLS.addEventListener('click', () => { urlMode = 'hls'; renderShareURL(); });
        if (plUrlModeRTSP) plUrlModeRTSP.addEventListener('click', () => { urlMode = 'rtsp'; renderShareURL(); });
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
        if (musicPlaylistPanel) {
          musicPlaylistPanel.addEventListener('dragover', (event) => {
            if (dragTrackId) return;
            if (event.dataTransfer && Array.from(event.dataTransfer.types || []).includes('Files')) {
              event.preventDefault();
              musicPlaylistPanel.classList.add('pl-file-hover');
            }
          });
          musicPlaylistPanel.addEventListener('dragleave', () => {
            musicPlaylistPanel.classList.remove('pl-file-hover');
          });
          musicPlaylistPanel.addEventListener('drop', (event) => {
            musicPlaylistPanel.classList.remove('pl-file-hover');
            if (dragTrackId) return;
            if (event.dataTransfer && event.dataTransfer.files && event.dataTransfer.files.length) {
              event.preventDefault();
              event.stopPropagation();
              addFiles(event.dataTransfer.files, false);
            }
          });
        }
      }

      return {
        init: initPlaylistController,
        setActive: setPlaylistActive,
        refresh: refreshPlaylist
      };
    })();
`
