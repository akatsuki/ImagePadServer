package server

const dashboardScriptAirPlay = `
    function applyAirPlay(data) {
      if (!airplayCard) return;
      if (!data || !data.enabled) {
        airplayCard.hidden = true;
        return;
      }
      airplayCard.hidden = false;
      const message = data.message || (data.running ? "AirPlay受信中です" : "AirPlay受信を開始できます");
      airplayStatusText.textContent = message;
      airplayStatusText.classList.toggle("is-running", !!data.running);
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
