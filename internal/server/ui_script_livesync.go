package server

const dashboardScriptLiveSync = `
    function scheduleRefresh(delay) {
      clearTimeout(refreshTimer);
      refreshTimer = setTimeout(refreshState, delay);
    }

    function announceLocalChange() {
      try {
        if (localChangeChannel) {
          localChangeChannel.postMessage({ type: 'changed', at: Date.now() });
        }
        localStorage.setItem('imagepad:lastChange', String(Date.now()));
      } catch (error) {
      }
      scheduleRefresh(100);
    }

    function setupLiveSync() {
      try {
        if ('BroadcastChannel' in window) {
          localChangeChannel = new BroadcastChannel('imagepad-state');
          localChangeChannel.onmessage = () => scheduleRefresh(100);
        }
      } catch (error) {
      }
      try {
        if ('EventSource' in window) {
          const stateEvents = new EventSource('/api/events');
          stateEvents.onopen = () => scheduleRefresh(50);
          stateEvents.onerror = () => scheduleRefresh(1000);
          stateEvents.addEventListener('state', () => scheduleRefresh(0));
        }
      } catch (error) {
      }
      window.addEventListener('storage', (event) => {
        if (event.key === 'imagepad:lastChange') {
          scheduleRefresh(100);
        }
      });
      window.addEventListener('focus', () => scheduleRefresh(50));
      window.addEventListener('pageshow', () => scheduleRefresh(50));
      window.addEventListener('online', () => scheduleRefresh(50));
      document.addEventListener('visibilitychange', () => {
        if (!document.hidden) {
          scheduleRefresh(50);
        }
      });
    }

    async function checkForUpdates() {
      try {
        const res = await apiFetch('/api/update-check', { cache: 'no-store' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        if (data.ok && data.newer) {
          updateText.innerHTML = '<a href="' + data.url + '" target="_blank" rel="noreferrer">' + escapeHTML(data.latest) + ' があります</a>';
        } else if (data.ok) {
          updateText.textContent = '最新版 ' + (data.current || '');
        } else {
          updateText.textContent = data.message || '確認失敗';
        }
      } catch (error) {
        updateText.textContent = '確認失敗';
      }
    }

    PreviewController.init({
      preview,
      showToast,
      scheduleRefresh
    });
    UploadController.init({
      toast
    });
    MusicController.init({
      choiceButtons: musicModeChoiceButtons
    });
    HistoryController.init({
      historyList
    });
    SettingsController.init({
      settingsModal,
      phoneConnectDialog
    });
    setupLiveSync();
    refreshState();
    checkForUpdates();
  `
