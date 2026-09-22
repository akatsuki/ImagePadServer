package server

const dashboardScriptAirPlayVideoView = `
    let airplayVideoViewPending = false;

    function airplayVideoViewIsVisible() {
      return !!airplayVideoViewCover && uploadMode === 'airplay' && !!(state.airplay && state.airplay.enabled);
    }

    function applyAirPlayVideoView(data) {
      if (!airplayVideoViewCover) return;
      const visible = airplayVideoViewIsVisible();
      if (!visible || !data) {
        airplayVideoViewCover.disabled = !visible || airplayVideoViewPending;
        if (!visible && airplayVideoViewStatus) airplayVideoViewStatus.textContent = '';
        return;
      }
      const configuredMode = data.configuredMode === 'cover' ? 'cover' : 'contain';
      const configuredRevision = String(data.configuredRevision || '0');
      const appliedRevision = String(data.appliedRevision || '0');
      const phase = String(data.phase || 'idle');
      const persistenceFailed = data.persistenceState === 'failed';
      const deliveryPending = typeof airplayDeliveryChangePending === 'function' && airplayDeliveryChangePending(state.airplayQuality);
      const pending = airplayVideoViewPending || (!persistenceFailed && (deliveryPending || phase === 'pending' || phase === 'waiting-input' || phase === 'waiting_input' || configuredRevision !== appliedRevision));
      airplayVideoViewCover.checked = configuredMode === 'cover';
      airplayVideoViewCover.disabled = pending;
      if (airplayVideoViewStatus) {
        airplayVideoViewStatus.textContent = persistenceFailed
          ? '保存に失敗しました。もう一度切り替えて再試行してください。'
          : pending
          ? '画面表示を切り替えています…'
          : (configuredMode === 'cover' ? '画面いっぱいに表示（余白をクロップ）' : '映像全体を表示（余白あり）');
      }
    }

    async function saveAirPlayVideoView() {
      if (!airplayVideoViewCover || !state.airplayVideoView || airplayVideoViewPending) return;
      const current = state.airplayVideoView;
      const requestedMode = airplayVideoViewCover.checked ? 'cover' : 'contain';
      airplayVideoViewPending = true;
      applyAirPlayVideoView({ ...current, configuredMode: requestedMode, phase: 'pending' });
      try {
        const res = await apiFetch('/api/airplay/video-view', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            mode: requestedMode,
            expectedSessionId: String(current.sessionId || ''),
            expectedPublisherGeneration: String(current.publisherGeneration || '0'),
            expectedRevision: String(current.configuredRevision || '0')
          }),
          timeoutMs: 8000
        });
        const body = await res.text();
        if (!res.ok) throw new Error(body || ('HTTP ' + res.status));
        const payload = body ? JSON.parse(body) : {};
        if (payload.airplayVideoView) state.airplayVideoView = payload.airplayVideoView;
        applyAirPlayVideoView(state.airplayVideoView);
        showToast(requestedMode === 'cover' ? 'AirPlayを画面いっぱい表示へ切り替えました。' : 'AirPlayを映像全体表示へ切り替えました。');
        scheduleRefresh(250);
      } catch (error) {
        await refreshState(true);
        showToast(error.message || 'AirPlay画面表示の切替に失敗しました', { error: true });
      } finally {
        airplayVideoViewPending = false;
        applyAirPlayVideoView(state.airplayVideoView);
      }
    }

    if (airplayVideoViewCover) {
      airplayVideoViewCover.addEventListener('change', saveAirPlayVideoView);
    }
`
