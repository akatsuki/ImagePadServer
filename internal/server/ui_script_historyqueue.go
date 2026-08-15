package server

const dashboardScriptHistoryQueue = `
    let lastHistoryRenderSignature = '';

    function setWingMode(mode) {
      wingMode = mode;
      for (const button of wingTabButtons) {
        const active = button.dataset.wingTab === mode;
        button.classList.toggle('active', active);
        button.setAttribute('aria-selected', String(active));
        if (active && historyList) {
          historyList.setAttribute('aria-labelledby', button.id);
        }
      }
      HistoryController.render(state);
    }

    function renderHistory(items, currentID) {
      if (!historyList) return;
      if (wingMode === 'queue') {
        renderVideoQueue(state.videoQueue);
        return;
      }
      const visibleItems = wingMode === 'favorites'
        ? (items || []).filter((item) => item.favorite).slice().reverse()
        : (items || []);
      const signature = historyRenderSignature(visibleItems, currentID);
      if (signature === lastHistoryRenderSignature) return;
      lastHistoryRenderSignature = signature;
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
        publish.textContent = item.id === currentID ? '公開中' : '公開に切替';
        publish.title = item.id === currentID ? '現在公開中です' : 'この項目を現在公開中に切り替え';
        publish.setAttribute('aria-label', publish.title);
        publish.disabled = item.id === currentID;

        const queue = document.createElement('button');
        queue.type = 'button';
        queue.className = 'history-action-button secondary';
        queue.dataset.historyQueue = item.id;
        queue.textContent = '変換';
        queue.title = '動画変換に追加';
        queue.setAttribute('aria-label', queue.title);
        queue.hidden = !state.videoPlayerEnabled;

        const heart = document.createElement('button');
        heart.type = 'button';
        heart.className = 'heart-button' + (item.favorite ? ' active' : '');
        heart.dataset.historyFavorite = item.id;
        heart.dataset.favorite = item.favorite ? '1' : '0';
        heart.innerHTML = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M20.8 4.6a5.5 5.5 0 0 0-7.8 0L12 5.6l-1-1a5.5 5.5 0 1 0-7.8 7.8l1 1L12 21l7.8-7.6 1-1a5.5 5.5 0 0 0 0-7.8Z"/></svg>';
        heart.title = item.favorite ? 'お気に入りから削除' : 'お気に入り';
        heart.setAttribute('aria-label', heart.title);

        const toggle = document.createElement('button');
        toggle.type = 'button';
        toggle.className = 'history-publish-toggle' + (item.published ? ' on' : '');
        toggle.dataset.historyPublishToggle = item.id;
        toggle.textContent = item.published ? '公開中' : '非公開';
        toggle.setAttribute('aria-pressed', String(!!item.published));

        actions.appendChild(toggle);
        actions.appendChild(publish);
        actions.appendChild(queue);
        actions.appendChild(heart);

        const pubRow = document.createElement('div');
        pubRow.className = 'history-address';
        const addrText = document.createElement('span');
        addrText.className = 'history-address-text';
        addrText.textContent = item.published ? (item.address || '') : '非公開（ERROR INACTIVE ADDRESS）';
        pubRow.appendChild(addrText);
        if (item.published && item.address) {
          const copyBtn = document.createElement('button');
          copyBtn.type = 'button';
          copyBtn.className = 'history-action-button secondary';
          copyBtn.dataset.historyCopy = item.id;
          copyBtn.textContent = 'コピー';
          pubRow.appendChild(copyBtn);
        }

        row.appendChild(thumb);
        row.appendChild(meta);
        row.appendChild(actions);
        row.appendChild(pubRow);
        historyList.appendChild(row);
      }
    }

    function renderVideoQueue(items) {
      if (!historyList) return;
      const signature = videoQueueRenderSignature(items);
      if (signature === lastHistoryRenderSignature) return;
      lastHistoryRenderSignature = signature;
      if (!items || !items.length) {
        historyList.innerHTML = '<div class="empty">動画変換は空です</div>';
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
        row.style.backgroundImage = "linear-gradient(color-mix(in srgb, var(--panel) 90%, transparent), color-mix(in srgb, var(--panel) 90%, transparent)), url('" + item.thumbnailURL + "')";
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
        track.setAttribute('role', 'progressbar');
        track.setAttribute('aria-label', '動画変換進捗');
        const fill = document.createElement('div');
        fill.className = 'progress-fill';
        setProgressValue(track, fill, item.progressPercent || 0);
        track.appendChild(fill);
        row.appendChild(track);
      }
      row.appendChild(detail);
      return row;
    }

    function historyRenderSignature(items, currentID) {
      return JSON.stringify({
        mode: wingMode,
        currentID: currentID || '',
        videoPlayerEnabled: !!state.videoPlayerEnabled,
        items: (items || []).map((item) => ({
          id: item.id || '',
          kind: item.kind || '',
          title: item.title || '',
          thumbnailURL: item.thumbnailURL || '',
          hasThumbnail: !!item.hasThumbnail,
          favorite: !!item.favorite,
          width: item.width || 0,
          height: item.height || 0,
          sizeBytes: item.sizeBytes || 0,
          persistent: !!item.persistent,
          published: !!item.published,
          address: item.address || '',
        })),
      });
    }

    function videoQueueRenderSignature(items) {
      return JSON.stringify({
        mode: wingMode,
        items: (items || []).map((item) => ({
          id: item.id || '',
          kind: item.kind || '',
          title: item.title || '',
          status: item.status || '',
          thumbnailURL: item.thumbnailURL || '',
          quality: item.quality || '',
          progressPercent: item.progressPercent || 0,
          progressText: item.progressText || '',
          message: item.message || '',
        })),
      });
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
      return { uploading: '受信中…', downloading: 'ダウンロード中…', analyzing: '解析中…', processing: '処理中…' }[phase] || '';
    }
`
