package server

const dashboardScriptXPost = `
    const xPostOption = document.getElementById('xPostOption');
    const xPostVideoEnabled = document.getElementById('xPostVideoEnabled');
    const xPostVoiceFields = document.getElementById('xPostVoiceFields');
    const xPostVoiceSelect = document.getElementById('xPostVoice');
    const xPostSpeed = document.getElementById('xPostSpeed');
    const xPostVoiceHint = document.getElementById('xPostVoiceHint');
    const xPostRefresh = document.getElementById('xPostRefreshVoices');
    const xPostPreview = document.getElementById('xPostPreviewVoice');
    const xPostAudio = document.getElementById('xPostVoiceAudio');
    const xPostCancel = document.getElementById('xPostCancelExport');
    const xPostRuntimeHint = document.getElementById('xPostRuntimeHint');
    const xPostUseManaged = document.getElementById('xPostUseManagedVoice');
    const xPostRetryRuntime = document.getElementById('xPostRetryRuntime');
    const xPostCancelRuntime = document.getElementById('xPostCancelRuntime');
    let xPostSpeakers = [];
    let xPostEngineURL = 'http://127.0.0.1:50121';
    let xPostRequestedEngine = '';
    let xPostManaged = true;
    let xPostEngineEpoch = 0;
    let xPostRuntimeTimer = null;
    let xPostVoicesLoaded = false;
    let xPostVoicesAttempted = false;
    let xPostVoicesLoading = false;
    let xPostPreviewURL = null;
    let xPostExportController = null;

    function isXPostURL(raw) {
      try {
        const u = new URL(String(raw || '').trim());
        if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password) return false;
        if (!['x.com', 'www.x.com', 'twitter.com', 'www.twitter.com', 'mobile.twitter.com'].includes(u.hostname.toLowerCase())) return false;
        return /^\/(?:[A-Za-z0-9_]+\/status|i\/(?:web\/)?status)\/\d+(?:\/(?:photo|video)\/\d+)?\/?$/.test(u.pathname);
      } catch (_) { return false; }
    }

    function updateXPostOption() {
      if (!xPostOption) return;
      const visible = uploadMode === 'link' && isXPostURL(imageURLInput && imageURLInput.value);
      xPostOption.hidden = !visible;
      xPostVideoEnabled.disabled = !state.videoPlayerEnabled;
      xPostVideoEnabled.checked = mediaIntent === 'video' && !!state.videoPlayerEnabled;
      const video = visible && xPostVideoEnabled.checked;
      xPostVoiceFields.hidden = !video;
      if (!state.videoPlayerEnabled) xPostVoiceHint.textContent = '動画機能を有効にすると利用できます';
      else if (!video) xPostVoiceHint.textContent = 'オンにすると投稿内容の読み上げと添付メディアの動画を生成します';
      else if (!xPostVoicesLoaded && !xPostVoicesAttempted && !xPostVoicesLoading) loadXPostVoices();
      else if (xPostVoicesLoaded && !xPostVoiceSelect.value) xPostVoiceHint.textContent = '読み上げに使う声を選択してください';
      if (!video && xPostRuntimeTimer !== null) { clearTimeout(xPostRuntimeTimer); xPostRuntimeTimer = null; }
      else if (video && xPostManaged && xPostVoicesAttempted && !xPostVoicesLoaded && !xPostVoicesLoading) scheduleXPostRuntimePoll();
    }

    function renderXPostRuntime(status) {
      const busy = ['idle', 'downloading', 'verifying', 'extracting', 'starting'].includes(status.phase);
      xPostRuntimeHint.textContent = status.message + (busy && status.percent ? ' ' + status.percent + '%' : '') + (status.error ? ': ' + status.error : '');
      xPostRetryRuntime.hidden = !['failed', 'stopped'].includes(status.phase);
      xPostCancelRuntime.hidden = !busy;
      xPostUseManaged.hidden = true;
      xPostPreview.disabled = status.phase !== 'ready';
      return busy;
    }

    function scheduleXPostRuntimePoll() {
      if (xPostRuntimeTimer !== null || xPostOption.hidden || xPostVoiceFields.hidden || !xPostManaged) return;
      const epoch = xPostEngineEpoch;
      xPostRuntimeTimer = setTimeout(async () => {
        xPostRuntimeTimer = null;
        if (epoch !== xPostEngineEpoch || xPostOption.hidden || xPostVoiceFields.hidden) return;
        try {
          const res = await apiFetch('/api/xpost/voicevox-runtime', { timeoutMs: 10000 });
          if (!res.ok) throw new Error(await res.text());
          const status = await res.json();
          if (epoch !== xPostEngineEpoch) return;
          if (renderXPostRuntime(status)) scheduleXPostRuntimePoll();
          else if (status.phase === 'ready') loadXPostVoices();
        } catch (error) {
          xPostRuntimeHint.textContent = error.message || '準備状況を取得できませんでした';
          xPostRetryRuntime.hidden = false;
        }
      }, 2000);
    }

    async function loadXPostVoices() {
      if (xPostVoicesLoading) return;
      xPostVoicesLoading = true;
      xPostVoicesAttempted = true;
      const epoch = xPostEngineEpoch;
      xPostRefresh.disabled = true;
      xPostVoiceHint.textContent = 'VOICEVOXの声を取得中…';
      const previous = xPostVoiceSelect.value;
      try {
        const query = xPostRequestedEngine ? '?engineUrl=' + encodeURIComponent(xPostRequestedEngine) : '';
        const res = await apiFetch('/api/xpost/voices' + query, { timeoutMs: 10000 });
        if (epoch !== xPostEngineEpoch) return;
        if (res.status === 503 && (res.headers.get('Content-Type') || '').includes('application/json')) {
          const data = await res.json();
          if (epoch !== xPostEngineEpoch) return;
          xPostManaged = true;
          xPostVoicesLoaded = false;
          xPostVoiceHint.textContent = 'VOICEVOXの準備が完了すると声を選べます';
          if (renderXPostRuntime(data.runtime)) scheduleXPostRuntimePoll();
          return;
        }
        if (!res.ok && (res.headers.get('Content-Type') || '').includes('application/json')) {
          const error = await res.json();
          xPostManaged = !!error.managed;
          xPostRuntimeHint.textContent = xPostManaged ? 'アプリ内VOICEVOXに接続できません' : '外部VOICEVOXに接続できません';
          throw new Error(error.error || '声一覧を取得できません');
        }
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        if (epoch !== xPostEngineEpoch) return;
        xPostSpeakers = data.speakers || [];
        xPostEngineURL = data.engineUrl;
        xPostManaged = !!data.managed;
        xPostUseManaged.hidden = xPostManaged;
        xPostRetryRuntime.hidden = true;
        xPostCancelRuntime.hidden = true;
        xPostPreview.disabled = false;
        xPostRuntimeHint.textContent = xPostManaged ? 'アプリ内VOICEVOXを使用中' : '外部VOICEVOXを使用中';
        xPostVoiceSelect.replaceChildren(new Option('声を選択してください', ''));
        for (const speaker of xPostSpeakers) {
          const group = document.createElement('optgroup');
          group.label = speaker.name;
          for (const style of speaker.styles || []) {
            group.append(new Option(speaker.name + '（' + style.name + '）', JSON.stringify([speaker.speaker_uuid, style.id])));
          }
          xPostVoiceSelect.append(group);
        }
        const last = data.lastVoice;
        const wanted = previous || (last ? JSON.stringify([last.speakerUuid, last.styleId]) : '');
        xPostVoiceSelect.value = wanted;
        if (!xPostVoicesLoaded && last) xPostSpeed.value = String(last.speed);
        xPostVoicesLoaded = true;
        xPostVoiceHint.textContent = wanted && !xPostVoiceSelect.value
          ? '前回の声が利用できません。声を選び直してください'
          : '投稿内容だけを読み上げます。ID・URLは読みません。生成に使った設定を記憶します';
      } catch (error) {
        if (epoch !== xPostEngineEpoch) return;
        xPostVoicesLoaded = false;
        xPostVoiceHint.textContent = error.message || '声一覧を取得できませんでした';
        xPostUseManaged.hidden = false;
        xPostPreview.disabled = true;
      } finally {
        xPostVoicesLoading = false;
        xPostRefresh.disabled = false;
        if (epoch !== xPostEngineEpoch) loadXPostVoices();
      }
    }

    function selectedXPostVoice() {
      if (!xPostVoiceSelect.value) return null;
      if (!xPostVoicesLoaded) throw new Error('VOICEVOXの準備を完了して声一覧を更新してください');
      const [uuid, id] = JSON.parse(xPostVoiceSelect.value);
      const speaker = xPostSpeakers.find((s) => s.speaker_uuid === uuid);
      const style = speaker && speaker.styles.find((s) => s.id === id);
      const speed = Number(xPostSpeed.value);
      if (!style || !Number.isFinite(speed) || speed < 0.5 || speed > 2) throw new Error('声と読み上げ速度を確認してください');
      return { engineUrl: xPostEngineURL, managed: xPostManaged, speakerUuid: uuid, styleId: id, speed,
        speakerName: speaker.name, styleName: style.name };
    }

    function xPostUploadOptions(url) {
      if (!isXPostURL(url)) return undefined;
      return { mode: mediaIntent === 'video' ? 'video' : 'image',
        voice: mediaIntent === 'video' ? selectedXPostVoice() : undefined };
    }

    if (xPostVideoEnabled) xPostVideoEnabled.addEventListener('change', () => {
      setMediaIntent(xPostVideoEnabled.checked ? 'video' : 'image', true, false);
      updateXPostOption();
    });
    if (xPostRefresh) xPostRefresh.addEventListener('click', loadXPostVoices);
    if (xPostUseManaged) xPostUseManaged.addEventListener('click', () => {
      xPostEngineEpoch++;
      xPostRequestedEngine = 'managed';
      xPostManaged = true;
      xPostVoicesLoaded = false;
      xPostVoiceSelect.value = '';
      if (xPostRuntimeTimer !== null) { clearTimeout(xPostRuntimeTimer); xPostRuntimeTimer = null; }
      loadXPostVoices();
    });
    async function changeXPostRuntime(action) {
      xPostRetryRuntime.disabled = true;
      xPostCancelRuntime.disabled = true;
      try {
        const res = await apiFetch('/api/xpost/voicevox-runtime', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action }), timeoutMs: 15000 });
        if (!res.ok) throw new Error(await res.text());
        const status = await res.json();
        xPostVoicesLoaded = false;
        if (xPostRuntimeTimer !== null) { clearTimeout(xPostRuntimeTimer); xPostRuntimeTimer = null; }
        if (renderXPostRuntime(status)) scheduleXPostRuntimePoll();
        else if (status.phase === 'ready') loadXPostVoices();
      } catch (error) { xPostRuntimeHint.textContent = error.message || '準備状態を変更できませんでした'; }
      finally { xPostRetryRuntime.disabled = false; xPostCancelRuntime.disabled = false; }
    }
    if (xPostRetryRuntime) xPostRetryRuntime.addEventListener('click', () => changeXPostRuntime('retry'));
    if (xPostCancelRuntime) xPostCancelRuntime.addEventListener('click', () => changeXPostRuntime('cancel'));
    if (xPostCancel) xPostCancel.addEventListener('click', () => {
      if (xPostExportController) xPostExportController.abort();
    });
    if (xPostPreview) xPostPreview.addEventListener('click', async () => {
      xPostPreview.disabled = true;
      try {
        const voice = selectedXPostVoice();
        if (!voice) throw new Error('試聴する声を選択してください');
        const res = await apiFetch('/api/xpost/voice-preview', { method: 'POST',
          headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(voice), timeoutMs: 60000 });
        if (!res.ok) throw new Error(await res.text());
        xPostAudio.pause();
        if (xPostPreviewURL) URL.revokeObjectURL(xPostPreviewURL);
        xPostPreviewURL = URL.createObjectURL(await res.blob());
        xPostAudio.src = xPostPreviewURL;
        xPostAudio.hidden = false;
        await xPostAudio.play();
      } catch (error) {
        xPostVoiceHint.textContent = error.message || '試聴に失敗しました';
      } finally { xPostPreview.disabled = false; }
    });
`
