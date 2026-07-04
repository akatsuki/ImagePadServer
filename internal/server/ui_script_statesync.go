package server

const dashboardScriptStateSync = `
    async function refreshState() {
      if (refreshInFlight) {
        refreshAgain = true;
        return refreshPromise;
      }
      refreshInFlight = true;
      refreshPromise = runRefreshState();
      try {
        return await refreshPromise;
      } finally {
        refreshPromise = null;
      }
    }

    async function runRefreshState() {
      const seq = ++lastAppliedStateSeq;
      try {
        const res = await apiFetch('/api/state', { cache: 'no-store' });
        const body = await res.text();
        if (!res.ok) throw new Error(body || ('HTTP ' + res.status));
        const data = JSON.parse(body);
        if (seq === lastAppliedStateSeq) {
          applyState(data);
          if (toast && toast.dataset.error === '1' && toast.dataset.errorSource === 'sync') {
            hideToast();
          }
        }
      } catch (error) {
        if (toast && !document.hidden) {
          showToast(syncFailureMessage(error), { error: true, source: 'sync' });
        }
      } finally {
        refreshInFlight = false;
        if (refreshAgain) {
          refreshAgain = false;
          scheduleRefresh(100);
        }
      }
    }

    function applyState(data) {
      state.imageURL = data.imageURL;
      state.videoURL = data.videoURL;
      state.hlsURL = data.hlsURL;
      state.shareURL = data.shareURL;
      state.shareURLLabel = data.shareURLLabel;
      state.phoneURL = data.phoneURL;
      state.localImageURL = data.localImageURL;
      state.previewImageURL = data.previewImageURL;
      state.publicImageURL = data.publicImageURL;
      state.current = data.current || null;
      state.history = data.history || [];
      state.videoQueue = data.videoQueue || [];
      state.videoQuality = data.videoQuality;
      state.ingest = data.ingest || null;
      state.video = data.video || null;
      state.obs = data.obs || null;
      state.pairing = data.pairing || null;
      state.ytdlpAuth = data.ytdlpAuth || null;
      state.toolInstall = data.toolInstall || null;
      state.videoPlayerEnabled = !!(data.videoPlayer && data.videoPlayer.enabled);
      state.musicModeEnabled = !!(data.videoPlayer && data.videoPlayer.musicModeEnabled);
      document.getElementById('phoneURL').textContent = data.phoneURL;
      document.getElementById('phoneURLMobile').textContent = data.phoneURL;
      const phoneDialogURL = document.getElementById('phoneDialogURL');
      if (phoneDialogURL) phoneDialogURL.textContent = data.phoneURL;
      renderShareURL(state);
      document.getElementById('videoStatus').textContent = videoText(data.video);
      updateMobileProgress(data);
      updateToolInstall(data.toolInstall);
      applyQuality(data.videoQuality);
      applyVideoPlayer(data.videoPlayer);
      applyOBS(data.obs);
      applyPairing(data.pairing);
      updateYTDLPAuthState();
      applyOBSProtection();
      const publicStatus = publicText(data.tunnel, data.upnp);
      const publicStatusEl = document.getElementById('upnpText');
      publicStatusEl.textContent = publicStatus.text;
      publicStatusEl.title = publicStatus.title || publicStatus.text;
      const currentStatusEl = document.getElementById('hasImage');
      if (currentStatusEl) {
        currentStatusEl.textContent = currentText(data.current);
      }
      const nextCurrentID = data.current ? data.current.id : "";
      PreviewController.render(data, {
        uploadMode,
        mediaIntent,
        localUploadActive,
        nextCurrentID
      });
      HistoryController.render(state);
      state.currentID = nextCurrentID;
      maybeAutoCopyOBSURL(data);
      if (!phoneConnectAutoShown && phoneConnectDialog && !isPhoneViewport()) {
        phoneConnectAutoShown = true;
        setTimeout(() => SettingsController.openPhoneConnect(), 240);
      }

      scheduleRefresh((data.ingest && data.ingest.active) || (data.video && data.video.active) || (data.obs && data.obs.connected) ? 750 : 2000);
    }
`
