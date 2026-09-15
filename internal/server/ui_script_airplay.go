package server

const dashboardScriptAirPlay = `
    let airplayQualityPending = false;
    let airplayLatencyPending = false;
    let airplayCommittedPreviewKey = '';

    function airplayDeliveryChangePending(data) {
      return airplayQualityPending || airplayLatencyPending || !!(data && data.managed && data.changePending);
    }

    function applyAirPlayQuality(data) {
      if (!airplayQualityRow || !airplayQualityMode) return;
      airplayQualityRow.hidden = uploadMode !== 'airplay';
      if (!state.airplay || !state.airplay.enabled) airplayQualityRow.hidden = true;
      if (!data || !data.desiredMode) {
        airplayQualityMode.disabled = airplayQualityPending;
        airplayCommittedPreviewKey = '';
        return;
      }
      airplayQualityMode.value = data.desiredMode;
      airplayQualityMode.disabled = airplayDeliveryChangePending(data);
      if (obsLatencyMode) {
        obsLatencyMode.disabled = airplayDeliveryChangePending(data);
        if (data.managed && data.desiredLatencyMode) obsLatencyMode.value = data.desiredLatencyMode;
      }
      if (data.managed && data.sessionID && Number(data.generation) > 0) {
        // A committed route may still be retiring its old backend. Wait for
        // that transaction to finish before asking the player to reconnect.
        if (!data.changePending) {
          const key = data.sessionID + ':' + data.generation;
          if (airplayCommittedPreviewKey && airplayCommittedPreviewKey !== key) resetOBSPreview();
          airplayCommittedPreviewKey = key;
        }
      } else {
        airplayCommittedPreviewKey = '';
      }
      if (airplayQualityStatus) {
        const activeHeight = Number(data.activeHeight || 0);
        airplayQualityStatus.textContent = data.managed && data.changePending
          ? "配信設定を切替中（現在: " + activeHeight + "p）"
          : data.restartRequired
            ? (data.managed ? "保存済みの設定は未適用です（現在: " : "次回のAirPlay開始時に適用します（現在: ") + activeHeight + "p）"
            : "適用中: " + activeHeight + "p";
      }
    }

    function applyAirPlay(data) {
      const quality = arguments.length > 1 ? arguments[1] : null;
      if (quality) state.airplayQuality = quality;
      applyAirPlayQuality(state.airplayQuality);
      if (!airplayCard) return;
      if (!data || !data.enabled) {
        airplayCard.hidden = true;
        if (airplayQualityRow) airplayQualityRow.hidden = true;
        if (airplayModeButton) airplayModeButton.hidden = true;
        if (uploadMode === 'airplay') setUploadMode('obs');
        updateMediaNavigation();
        return;
      }
      airplayCard.hidden = false;
	  const phase = data.phase || "stopped";
	  airplayCard.dataset.phase = phase;
	  if (airplayStartPending && !data.running) {
		airplayStatusText.textContent = "AirPlay受信を準備中です...";
		airplayStatusText.classList.remove("is-error", "is-running");
		airplayReceiverPath.textContent = data.receiverPath ? "受信器: " + data.receiverPath : "";
		airplayStartButton.hidden = false;
		airplayRetryButton.hidden = true;
		airplayEndButton.hidden = true;
		airplayStartButton.disabled = true;
		airplayRetryButton.disabled = true;
		airplayEndButton.disabled = true;
		updateMediaNavigation();
		return;
	  }
	  const connected = phase === "media-active";
	  const retryAvailable = phase === "delivery-failed" && !!data.running && !!data.receiverRunning && !data.bridgeRunning;
      const message = connected
        ? "iPhoneの画面を受信中です。配信開始ボタンで公開できます。"
        : (data.message || (data.running ? "iPhoneからの画面ミラーリング接続を待っています。" : "AirPlay受信を開始できます"));
      airplayStatusText.textContent = message;
      airplayStatusText.classList.toggle("is-running", connected);
      airplayStatusText.classList.toggle("is-error", phase === "delivery-failed" || (!data.available && !data.running));
      airplayReceiverPath.textContent = data.receiverPath ? "受信器: " + data.receiverPath : "";
      airplayStartButton.hidden = !!data.running;
	  airplayRetryButton.hidden = !retryAvailable;
      airplayEndButton.hidden = !data.running;
      airplayStartButton.disabled = !!data.running || !data.available;
	  airplayRetryButton.disabled = !retryAvailable;
      airplayEndButton.disabled = !data.running;
      airplayStartButton.title = data.available ? "AirPlay受信を開始" : (data.message || "AirPlay受信器を設定してください");
      updateMediaNavigation();
    }

    async function requestAirPlay(path, successMessage) {
	  const isRetry = path.endsWith("/retry");
	  const expectsRunning = !path.endsWith("/end");
	  airplayStartPending = path.endsWith("/start");
      airplayStartButton.disabled = true;
	  airplayRetryButton.disabled = true;
      airplayEndButton.disabled = true;
	  if (airplayStartPending) {
	    airplayStatusText.textContent = "AirPlay受信を準備中です...";
	    airplayStatusText.classList.remove("is-error", "is-running");
		scheduleRefresh(100);
	  }
	  let requestError = null;
      try {
        const res = await apiFetch(path, { method: "POST", timeoutMs: 20000 });
        const body = await res.text();
        if (!res.ok) throw new Error(body || ("HTTP " + res.status));
        const payload = body ? JSON.parse(body) : {};
        if (payload.airplay) {
          state.airplay = payload.airplay;
          applyAirPlay(payload.airplay, payload.airplayQuality);
        }
        if (payload.obs) {
          state.obs = payload.obs;
          applyOBS(payload.obs);
        }
        showToast(successMessage);
        scheduleRefresh(250);
      } catch (error) {
		requestError = error;
	  } finally {
		airplayStartPending = false;
	  }
	  if (!requestError) return;
	  if (requestError && requestError.name === "AbortError") {
		const refreshed = await refreshState(true);
		if (!refreshed) {
		  applyAirPlay(state.airplay);
		  showToast("AirPlayの最新状態を確認できませんでした。状態を更新して再試行してください。", { error: true });
		  return;
		}
		const running = !!(state.airplay && state.airplay.running);
		const phase = state.airplay && state.airplay.phase;
		const reachedExpectedState = isRetry
		  ? (running && phase !== "delivery-failed")
		  : running === expectsRunning;
		if (reachedExpectedState) {
		  applyAirPlay(state.airplay);
		  showToast(successMessage);
		  return;
		}
		applyAirPlay(state.airplay);
		const timeoutMessage = isRetry
		  ? "AirPlay配信の再接続確認がタイムアウトしました。状態を更新して再試行してください。"
		  : (expectsRunning
		    ? "AirPlay受信の開始確認がタイムアウトしました。状態を更新して再試行してください。"
		    : "AirPlay受信の停止確認がタイムアウトしました。状態を更新して再試行してください。");
		showToast(timeoutMessage, { error: true });
		return;
      }
	  applyAirPlay(state.airplay);
	  showToast(requestError.message || "AirPlay操作に失敗しました", { error: true });
      }

    async function saveAirPlayQuality() {
      if (!airplayQualityMode) return;
      const previous = state.airplayQuality;
      const requestedMode = airplayQualityMode.value;
      airplayQualityPending = true;
      applyAirPlayQuality(previous);
      try {
        const res = await apiFetch('/api/airplay/quality', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: requestedMode }),
          timeoutMs: 8000
        });
        const body = await res.text();
        if (!res.ok) throw new Error(body || ('HTTP ' + res.status));
        const payload = body ? JSON.parse(body) : {};
        state.airplayQuality = payload.airplayQuality || payload;
        applyAirPlayQuality(state.airplayQuality);
        showToast(state.airplayQuality.managed
          ? (state.airplayQuality.changePending ? 'AirPlay画質の切替を受け付けました。' : 'AirPlay画質設定を更新しました。')
          : (state.airplayQuality.restartRequired
            ? 'AirPlay画質を保存しました。次回のAirPlay開始時に適用します。'
            : 'AirPlay画質を保存しました。'));
        scheduleRefresh(250);
      } catch (error) {
        await refreshState(true);
        applyAirPlayQuality(state.airplayQuality || previous);
        showToast(error.message || 'AirPlay画質の保存に失敗しました', { error: true });
      } finally {
        airplayQualityPending = false;
        applyAirPlayQuality(state.airplayQuality);
      }
    }

    if (airplayStartButton) {
      airplayStartButton.addEventListener("click", () => requestAirPlay("/api/airplay/start", "AirPlay受信を開始しました。iOSから画面ミラーリングを開始してください。"));
    }
    if (airplayEndButton) {
      airplayEndButton.addEventListener("click", () => requestAirPlay("/api/airplay/end", "AirPlay受信を停止しました。"));
    }
	if (airplayRetryButton) {
	  airplayRetryButton.addEventListener("click", () => requestAirPlay("/api/airplay/retry", "AirPlay配信の再接続を開始しました。"));
	}
    if (airplayQualityMode) {
      airplayQualityMode.addEventListener('change', saveAirPlayQuality);
    }
`
