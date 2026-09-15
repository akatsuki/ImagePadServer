package server

const dashboardScriptStateSync = `
    async function refreshState(requireFresh = false) {
      if (refreshInFlight) {
        const inFlight = refreshPromise;
        if (!requireFresh) {
          refreshAgain = true;
          return inFlight;
        }
        await inFlight;
        return refreshState();
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
        const res = await apiFetch('/api/state', { cache: 'no-store', timeoutMs: 8000 });
        const body = await res.text();
        if (!res.ok) throw new Error(body || ('HTTP ' + res.status));
        const data = JSON.parse(body);
        if (seq === lastAppliedStateSeq) {
          applyState(data);
          if (toast && toast.dataset.error === '1' && toast.dataset.errorSource === 'sync') {
            hideToast();
          }
		  return true;
        }
		return false;
      } catch (error) {
        if (toast && !document.hidden) {
          showToast(syncFailureMessage(error), { error: true, source: 'sync' });
        }
		return false;
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
      state.shareTargets = data.shareTargets || {};
      state.phoneURL = data.phoneURL;
      state.localImageURL = data.localImageURL;
      state.previewImageURL = data.previewImageURL;
      state.publicImageURL = data.publicImageURL;
      state.current = data.current || null;
      state.publishedRevision = data.publishedRevision || 0;
      state.history = data.history || [];
      state.videoQueue = data.videoQueue || [];
      state.videoQuality = data.videoQuality;
      state.ingest = data.ingest || null;
      state.video = data.video || null;
      state.obs = data.obs || null;
      state.airplay = data.airplay || null;
      state.airplayQuality = data.airplayQuality || null;
      state.pairing = data.pairing || null;
      state.ytdlpAuth = data.ytdlpAuth || null;
      state.ytdlpChannel = data.ytdlpChannel || null;
      state.toolInstall = data.toolInstall || null;
      state.videoPlayerEnabled = !!(data.videoPlayer && data.videoPlayer.enabled);
      state.videoPlayerToolsReady = !!(data.videoPlayer && data.videoPlayer.toolsReady);
      state.videoPlayerInstalling = !!(data.videoPlayer && data.videoPlayer.installing);
      state.videoPlayerError = String((data.videoPlayer && data.videoPlayer.error) || '');
      state.musicRenderer = (data.videoPlayer && data.videoPlayer.musicRenderer) || null;
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
      applyAirPlay(data.airplay, data.airplayQuality);
      applyPairing(data.pairing);
      updateYTDLPAuthState();
      updateYTDLPChannelState();
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

      scheduleRefresh((data.ingest && data.ingest.active) || (data.video && data.video.active) || (data.obs && data.obs.connected) || (data.airplay && data.airplay.running) ? 750 : 2000);
    }
`
