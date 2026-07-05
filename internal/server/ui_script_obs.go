package server

const dashboardScriptOBS = `
    function applyOBS(data) {
      const server = document.getElementById('obsServerAddress');
      const key = document.getElementById('obsStreamKey');
      if (!server || !key) return;
      if (!data) {
        server.textContent = 'OBS受信機能は利用できません';
        key.textContent = '-';
        renderOBSConnections([]);
        updateOBSActionState(null);
        return;
      }
      server.textContent = data.serverAddress || 'OBS受信は停止中です';
      key.textContent = obsKeyVisible ? (data.streamKey || '-') : maskSecret(data.streamKey);
      const latency = data.latency || {};
      confirmedOBSLatencyMode = latency.mode || 'hls';
      if (obsLatencyMode) obsLatencyMode.value = confirmedOBSLatencyMode;
      renderOBSConnections(data.connections || []);
      updateOBSActionState(data);
    }

    function updateOBSActionState(data) {
      if (!uploadButton) return;
      if (obsLatencyDetailButton) {
        obsLatencyDetailButton.hidden = !(uploadMode === 'obs' && data && data.publishing);
      }
      if (uploadMode !== 'obs') return;
      const obs = data || {};
      if (obs.publishing) {
        uploadButton.textContent = '配信中...';
        uploadButton.disabled = true;
      } else if (obs.connected) {
        uploadButton.textContent = '配信開始';
        uploadButton.disabled = false;
      } else if (obs.listening) {
        uploadButton.textContent = 'OBS接続待ち...';
        uploadButton.disabled = true;
      } else {
        uploadButton.textContent = 'OBS準備中...';
        uploadButton.disabled = true;
      }
    }

    function renderOBSConnections(rows) {
      if (!obsConnectionsTableBody) return;
      const list = Array.isArray(rows) ? rows : [];
      if (!list.length) {
        obsConnectionsTableBody.innerHTML = '<tr><td colspan="6" class="connection-empty">接続なし</td></tr>';
        return;
      }
      obsConnectionsTableBody.innerHTML = list.map((row) => {
        const lag = Number(row.lagSeconds || 0);
        const level = normalizeLagLevel(row.lagLevel, lag);
        const width = Math.max(8, Math.min(100, (lag / 12) * 100));
        const note = row.note ? ' title="' + escapeHTML(row.note) + '"' : '';
        return '<tr' + note + '>' +
          '<td>' + escapeHTML(row.ip || '-') + '</td>' +
          '<td>' + escapeHTML(row.protocol || '-') + '</td>' +
          '<td>' + escapeHTML(row.device || '-') + '</td>' +
          '<td>' + escapeHTML(row.state || '-') + '</td>' +
          '<td>' + escapeHTML(row.quality || '-') + '</td>' +
          '<td><div class="lag-cell"><div class="lag-bar" aria-hidden="true"><span class="lag-bar-fill lag-' + level + '" style="width:' + width.toFixed(0) + '%"></span></div><span class="lag-value">' + formatLagSeconds(lag) + '</span></div></td>' +
          '</tr>';
      }).join('');
    }

    function normalizeLagLevel(level, seconds) {
      const value = String(level || '').toLowerCase();
      if (['good', 'ok', 'warn', 'bad'].includes(value)) return value;
      if (seconds <= 1.2) return 'good';
      if (seconds <= 2.5) return 'ok';
      if (seconds <= 5) return 'warn';
      return 'bad';
    }

    function formatLagSeconds(seconds) {
      if (!Number.isFinite(seconds) || seconds <= 0) return '-';
      if (seconds < 1) return seconds.toFixed(1) + 's';
      if (Math.abs(seconds - Math.round(seconds)) < 0.05) return String(Math.round(seconds)) + 's';
      return seconds.toFixed(1).replace(/\.0$/, '') + 's';
    }

    function maskSecret(value) {
      return value ? '*****' : '-';
    }

    function setPhoneProtection(active) {
      const nextActive = !!active;
      const enteringProtection = nextActive && !phoneProtectionActive;
      phoneProtectionActive = nextActive;
      document.querySelectorAll('[data-protected-reveal]').forEach((el) => {
        el.classList.toggle('protected', phoneProtectionActive);
        if (enteringProtection) {
          el.classList.remove('revealed');
        }
        if (!phoneProtectionActive) {
          el.classList.remove('revealed');
          el.removeAttribute('role');
          el.removeAttribute('tabindex');
          el.removeAttribute('aria-label');
          el.removeAttribute('aria-pressed');
          return;
        }
        el.setAttribute('role', 'button');
        el.setAttribute('tabindex', '0');
        el.setAttribute('aria-label', el.getAttribute('data-protect-label') || 'クリックして表示');
        el.setAttribute('aria-pressed', String(el.classList.contains('revealed')));
      });
    }

    function applyOBSProtection() {
      const protectedMode = uploadMode === 'obs';
      document.body.classList.toggle('obs-protect', protectedMode);
      setPhoneProtection(protectedMode);
      document.getElementById('phoneURL').textContent = state.phoneURL || '';
      document.getElementById('phoneURLMobile').textContent = state.phoneURL || '';
      const phoneDialogURL = document.getElementById('phoneDialogURL');
      if (phoneDialogURL) phoneDialogURL.textContent = state.phoneURL || '';
      if (clearButton) {
        clearButton.textContent = protectedMode ? '配信終了' : 'クリア';
        clearButton.classList.add('warn');
        clearButton.classList.remove('secondary');
        clearButton.title = protectedMode ? 'OBS配信を終了してVOD化し、次の待ち受けを開始' : '現在公開中の画像を消去';
      }
      if (uploadButton) {
        uploadButton.textContent = uploadActionLabel();
      }
	if (qualityRow) {
		qualityRow.hidden = protectedMode || (mediaIntent !== 'video' && mediaIntent !== 'music');
		qualityRow.classList.toggle('standalone', mediaIntent === 'video' || mediaIntent === 'music');
	}
      if (obsLatencyOption) {
        obsLatencyOption.hidden = !protectedMode;
      }
      updateOBSActionState(state.obs);
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
        toast.textContent = 'この場所にはドロップできません。ファイル選択を使ってください。';
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
      dropFileName.textContent = file ? file.name : 'ファイル未選択';
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
        toast.textContent = file.name + ' を選択しました' + extra;
      }
    }
`
