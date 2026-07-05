package server

const dashboardScriptPreview = `
    function renderShareURL(data) {
      const view = shareURLForCurrentMode(data);
      document.getElementById('shareURL').textContent = shareURLDisplayText(view);
      document.getElementById('shareURLLabel').textContent = displayShareLabel(view.shareURLLabel);
    }

    function shareURLForCurrentMode(data) {
      return shareURLForMode(data, uploadMode);
    }

    function shareURLForMode(data, mode) {
      data = data || {};
      const targets = data.shareTargets || {};
      return targets[mode] || { shareURL: data.shareURL || '', shareURLLabel: data.shareURLLabel || 'URL', obs: data.obs || {} };
    }

    function displayShareLabel(label) {
      switch (label) {
        case 'ImagePad URL':
        case 'Local URL':
          return '画像URL';
        case 'HLS URL':
        case 'MP4 URL':
          return 'VRChat用URL';
        case 'RTSP TCP URL':
          return '配信用URL';
        default:
          return label || 'URL';
      }
    }

    function displayedShareURL() {
      return shareURLForCurrentMode(state);
    }

    function shareURLDisplayText(data) {
      if (data && data.shareURL) return data.shareURL;
      const label = data && data.shareURLLabel ? String(data.shareURLLabel) : '';
      const message = data && data.obs && data.obs.message ? String(data.obs.message) : '';
      if (label === 'RTSP TCP URL' && message) {
        return '公開URLは未取得です: ' + message;
      }
      return '公開URLは未取得です';
    }

    function resetOBSPreview() {
      PreviewController.resetOBS();
    }

    function applyPairing(pairing) {
      const active = !!(pairing && pairing.active && pairing.pin);
      document.body.classList.toggle('pairing-active', active);
      if (!pairingPanel || !pairingPin || !pairingDetail) return;
      if (!active) {
        pairingPanel.classList.remove('active');
        return;
      }
      pairingPin.textContent = pairing.pin;
      const name = pairing.deviceName || pairing.clientName || 'BrowserRelayStreamer';
      pairingDetail.textContent = name + ' が接続を要求しています。このコードを相手PCで入力してください。';
      pairingPanel.classList.add('active');
    }

    function renderIngestPreview(phase, title, percent, progressText, force) {
      PreviewController.renderIngest(phase, title, percent, progressText, force);
    }

    function escapeHTML(value) {
      return String(value).replace(/[&<>"']/g, (char) => ({
        '&': '&amp;',
        '<': '&lt;',
        '>': '&gt;',
        '"': '&quot;',
        "'": '&#39;'
      }[char]));
    }
`
