package server

const dashboardScriptAirPlay = `
    function applyAirPlay(data) {
      if (!airplayCard) return;
      if (!data || !data.enabled) {
        airplayCard.hidden = true;
        if (airplayModeButton) airplayModeButton.hidden = true;
        if (uploadMode === 'airplay') setUploadMode('obs');
        return;
      }
      airplayCard.hidden = false;
      if (airplayModeButton) airplayModeButton.hidden = false;
      const connected = !!(data.running && data.receiverRunning && data.bridgeRunning && state.obs && state.obs.connected);
      const message = connected
        ? "iPhoneの画面を受信中です。配信開始ボタンで公開できます。"
        : (data.message || (data.running ? "iPhoneからの画面ミラーリング接続を待っています。" : "AirPlay受信を開始できます"));
      airplayStatusText.textContent = message;
      airplayStatusText.classList.toggle("is-running", connected);
      airplayStatusText.classList.toggle("is-error", !data.available && !data.running);
      airplayReceiverPath.textContent = data.receiverPath ? "受信器: " + data.receiverPath : "";
      airplayStartButton.hidden = !!data.running;
      airplayEndButton.hidden = !data.running;
      airplayStartButton.disabled = !!data.running || !data.available;
      airplayEndButton.disabled = !data.running;
      airplayStartButton.title = data.available ? "AirPlay受信を開始" : (data.message || "AirPlay受信器を設定してください");
    }

    async function requestAirPlay(path, successMessage) {
      airplayStartButton.disabled = true;
      airplayEndButton.disabled = true;
      try {
        const res = await apiFetch(path, { method: "POST" });
        const body = await res.text();
        if (!res.ok) throw new Error(body || ("HTTP " + res.status));
        const payload = body ? JSON.parse(body) : {};
        if (payload.airplay) {
          state.airplay = payload.airplay;
          applyAirPlay(payload.airplay);
        }
        if (payload.obs) {
          state.obs = payload.obs;
          applyOBS(payload.obs);
        }
        showToast(successMessage);
        scheduleRefresh(250);
      } catch (error) {
        applyAirPlay(state.airplay);
        showToast(error.message || "AirPlay操作に失敗しました", { error: true });
      }
    }

    if (airplayStartButton) {
      airplayStartButton.addEventListener("click", () => requestAirPlay("/api/airplay/start", "AirPlay受信を開始しました。iOSから画面ミラーリングを開始してください。"));
    }
    if (airplayEndButton) {
      airplayEndButton.addEventListener("click", () => requestAirPlay("/api/airplay/end", "AirPlay受信を停止しました。"));
    }
`
