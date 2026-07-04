package server

const dashboardScriptProgressStatus = `
    function updateToolInstall(info) {
      const overlay = document.getElementById('toolInstallOverlay');
      const card = document.getElementById('toolInstallCard');
      const title = document.getElementById('toolInstallTitle');
      const fill = document.getElementById('toolInstallFill');
      const track = fill ? fill.parentElement : null;
      const detail = document.getElementById('toolInstallDetail');
      if (!overlay) return;
      const active = !!(info && info.active);
      const failed = !!(info && info.failed);
      if (!active && !failed) {
        overlay.classList.remove('open');
        overlay.setAttribute('aria-hidden', 'true');
        card.classList.remove('failed');
        fill.classList.remove('indeterminate');
        return;
      }
      overlay.classList.add('open');
      overlay.setAttribute('aria-hidden', 'false');
      const toolLabel = { ffmpeg: 'FFmpeg', ffprobe: 'ffprobe', 'yt-dlp': 'yt-dlp' }[info.tool] || 'ツール';
      if (failed) {
        card.classList.add('failed');
        title.textContent = 'ツールの準備に失敗しました';
        detail.textContent = info.message || '時間をおいて再度お試しください。';
        fill.classList.remove('indeterminate');
        setProgressValue(track, fill, 100);
        return;
      }
      card.classList.remove('failed');
      const phaseLabel = { download: 'ダウンロード中', extract: '展開中', validate: '検証中' }[info.phase] || '準備中';
      title.textContent = toolLabel + ' を' + phaseLabel + '…';
      const pct = Math.max(0, Math.min(100, Number(info.percent || 0)));
      if (info.phase === 'download' && pct > 0) {
        fill.classList.remove('indeterminate');
        setProgressValue(track, fill, pct);
        detail.textContent = pct + '%';
      } else {
        fill.classList.add('indeterminate');
        if (track) track.removeAttribute('aria-valuenow');
        detail.textContent = info.attempt > 1 ? ('再試行 ' + info.attempt + '回目') : '';
      }
    }

    function updateMobileProgress(data) {
      const ingest = data && data.ingest;
      const label = ingest && ingest.active ? ingestPhaseLabel(ingest.phase) : '';
      if (label) {
        const percent = Math.max(0, Math.min(100, Number(ingest.progressPercent || 0)));
        const detail = ingest.progressText || ingest.title || '';
        mobileProgress.classList.add('open');
        mobileProgressText.textContent = label + (detail ? ' — ' + detail : '');
        mobileProgressFill.classList.remove('indeterminate');
        setProgressValue(mobileProgressFill.parentElement, mobileProgressFill, percent);
        return;
      }
      const video = data && data.video;
      if (!video || !video.active) {
        mobileProgress.classList.remove('open');
        mobileProgressFill.classList.remove('indeterminate');
        return;
      }
      const percent = Math.max(0, Math.min(99, Number(video.progressPercent || 0)));
      mobileProgress.classList.add('open');
      mobileProgressFill.classList.remove('indeterminate');
      mobileProgressText.textContent = video.progressText || video.message || '変換中';
      setProgressValue(mobileProgressFill.parentElement, mobileProgressFill, percent);
    }

    function scrollProgressIntoView() {
      if (!isPhoneViewport()) return;
      window.setTimeout(() => {
        const target = uploadProgressPanel && uploadProgressPanel.classList.contains('open')
          ? uploadProgressPanel
          : (mobileProgress.classList.contains('open') ? mobileProgress : preview);
        target.scrollIntoView({ behavior: 'smooth', block: 'center' });
      }, 120);
    }

    function isPhoneViewport() {
      return window.matchMedia('(max-width: 860px)').matches;
    }

    function currentText(current) {
      if (!current) return '未選択';
      if (current.kind === 'video') {
        if (current.sizeBytes) return '動画 ' + Math.round(current.sizeBytes / 1024 / 1024) + ' MB';
        return '動画';
      }
      return current.width + ' x ' + current.height;
    }

    function publicText(tunnel, upnp) {
      if (tunnel && tunnel.ok && tunnel.url) return { text: '公開HTTPS 接続中', title: tunnel.url };
      if (tunnel && tunnel.message) return { text: tunnel.message, title: tunnel.message };
      const text = upnpText(upnp);
      return { text, title: text };
    }

    function upnpText(upnp) {
      if (!upnp) return '未確認';
      if (upnp.ok && upnp.externalIP) {
        return '成功 ' + upnp.externalIP;
      }
      if (upnp.ok) {
        return '成功';
      }
      return upnp.message || '未確認';
    }

    function videoText(video) {
      if (!video) return '未確認';
      const formats = [];
      if (video.mp4) formats.push('MP4');
      if (video.hls) formats.push('HLS');
      if (formats.length && video.active) return formats.join(' / ') + ' 変換中';
      if (formats.length) return formats.join(' / ') + ' 準備完了';
      const message = String(video.message || '');
      if (!message) return '未生成';
      if (/not generated/i.test(message)) return 'まだ生成されていません';
      if (/starting/i.test(message)) return '変換を開始しています';
      if (/disabled/i.test(message)) return '動画プレーヤー対応は無効です';
      return message;
    }
`
