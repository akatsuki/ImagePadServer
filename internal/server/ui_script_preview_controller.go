package server

const dashboardScriptPreviewController = `
    const PreviewController = (() => {
      let deps = {};
      let mode = '';
      let mediaID = '';
      let mediaURL = '';
      let obsMediaID = '';
      let obsMediaURL = '';
      let previewHLS = null;
      let previewVisible = true;

      function initPreviewController(nextDeps) {
        deps = nextDeps || {};
        setPreviewVisibleController(true);
      }

      function setPreviewVisibleController(visible) {
        previewVisible = !!visible;
        if (previewPanel) previewPanel.hidden = !previewVisible;
      }

      function destroyPreviewHLS() {
        if (!previewHLS) return;
        try {
          previewHLS.destroy();
        } catch (error) {
        }
        previewHLS = null;
      }

      function attachPreviewHLS(video, src) {
        destroyPreviewHLS();
        if (window.Hls && window.Hls.isSupported()) {
          previewHLS = new window.Hls({
            lowLatencyMode: true,
            backBufferLength: 30
          });
          previewHLS.loadSource(src);
          previewHLS.attachMedia(video);
          return true;
        }
        if (video.canPlayType('application/vnd.apple.mpegurl')) {
          video.src = src;
          return true;
        }
        return false;
      }

      function releaseIfLeavingVideo(nextMode) {
        if (nextMode !== mode) {
          destroyPreviewHLS();
        }
      }

      function sameOriginPreviewURL(value) {
        if (!value) return '';
        try {
          const parsed = new URL(value, window.location.href);
          const params = new URLSearchParams(parsed.search);
          const pageToken = new URLSearchParams(window.location.search).get('token');
          if (pageToken && !params.has('token')) params.set('token', pageToken);
          const query = params.toString();
          return parsed.pathname + (query ? '?' + query : '');
        } catch (error) {
          return value;
        }
      }

      function mediaPreviewURL(data) {
        return sameOriginPreviewURL(data.hlsURL || data.publicHLSURL || data.videoURL || data.publicVideoURL || '');
      }

      function videoPlayIcon(paused) {
        if (paused) {
          return '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M8 5v14l11-7Z"/></svg>';
        }
        return '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M7 5h4v14H7Zm6 0h4v14h-4Z"/></svg>';
      }

      function addVideoPlayButton(video) {
        const preview = deps.preview;
        if (!preview) return;
        const button = document.createElement('button');
        button.type = 'button';
        button.className = 'preview-play-button';
        button.setAttribute('aria-label', '動画を再生');
        const sync = () => {
          const paused = video.paused;
          button.classList.toggle('playing', !paused);
          button.setAttribute('aria-label', paused ? '動画を再生' : '動画を一時停止');
          button.innerHTML = videoPlayIcon(paused);
        };
        button.addEventListener('click', async (event) => {
          event.preventDefault();
          event.stopPropagation();
          try {
            if (video.paused) {
              await video.play();
            } else {
              video.pause();
            }
          } catch (error) {
            if (deps.showToast) {
              deps.showToast('動画を再生できませんでした: ' + (error && error.message ? error.message : error), { error: true });
            }
          }
          sync();
        });
        video.addEventListener('play', sync);
        video.addEventListener('pause', sync);
        video.addEventListener('ended', sync);
        sync();
        preview.appendChild(button);
      }

      function renderOBSPreview(data, context) {
        const preview = deps.preview;
        if (!preview) return;
        const obs = data.obs || {};
        const nextCurrentID = context.nextCurrentID || (data.current && data.current.id) || '';
        const obsID = obs.mediaID || nextCurrentID;
        const obsPreviewURL = sameOriginPreviewURL(obs.previewURL || '');
        if (obsPreviewURL && (mode !== 'obs' || obsID !== obsMediaID || obsPreviewURL !== obsMediaURL || !preview.querySelector('video'))) {
          preview.classList.add('obs-preview');
          preview.innerHTML = '';
          const video = document.createElement('video');
          video.controls = true;
          video.autoplay = true;
          video.muted = true;
          video.playsInline = true;
          if (attachPreviewHLS(video, obsPreviewURL)) {
            preview.appendChild(video);
          } else {
            const link = document.createElement('a');
            link.href = obsPreviewURL;
            link.textContent = 'HLSプレビューを開く';
            link.target = '_blank';
            link.rel = 'noreferrer';
            preview.innerHTML = '<div class="empty">このブラウザではHLSプレビューを直接再生できません。</div>';
            preview.appendChild(link);
          }
          mode = 'obs';
          obsMediaID = obsID;
          obsMediaURL = obsPreviewURL;
          state.previewMode = 'obs';
          state.obsPreviewID = obsID;
          state.obsPreviewURL = obsPreviewURL;
        }
        if (!obsPreviewURL && mode !== 'obs-waiting') {
          destroyPreviewHLS();
          preview.classList.add('obs-preview');
          preview.innerHTML = '<div class="empty">HLSプレビューを準備中です</div>';
          mode = 'obs-waiting';
          obsMediaID = obsID;
          obsMediaURL = '';
          state.previewMode = 'obs-waiting';
          state.obsPreviewID = obsID;
          state.obsPreviewURL = '';
        }
        state.currentID = obsID;
      }

      function renderEmpty() {
        const preview = deps.preview;
        if (!preview) return;
        releaseIfLeavingVideo('empty');
        preview.classList.remove('obs-preview');
        if (mode !== 'empty') {
          preview.innerHTML = '<div class="empty">まだ画像が選択されていません</div>';
          mode = 'empty';
          state.previewMode = 'empty';
        }
      }

      function renderIngestProgress(data) {
        const ingestLabel = data.ingest && data.ingest.active ? ingestPhaseLabel(data.ingest.phase) : '';
        if (!ingestLabel) return;
        renderIngestPreviewController(
          data.ingest.phase,
          data.ingest.title || '',
          Number(data.ingest.progressPercent || 0),
          data.ingest.progressText || '',
          false
        );
      }

      function renderVideoProgress(data) {
        const preview = deps.preview;
        if (!preview) return;
        releaseIfLeavingVideo('progress');
        const percent = Math.max(0, Math.min(99, Number(data.video.progressPercent || 0)));
        const detail = data.video.progressText || data.video.message || '変換中';
        preview.classList.remove('obs-preview');
        preview.innerHTML =
          '<div class="progress-preview">' +
            '<div>動画プレーヤー向けに変換中です</div>' +
            '<div class="progress-track" role="progressbar" aria-label="変換進捗" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' + Math.round(percent) + '">' +
              '<div class="progress-fill" style="width:' + Math.max(6, percent) + '%"></div>' +
            '</div>' +
            '<div class="progress-detail">' + escapeHTML(detail) + '</div>' +
          '</div>';
        mode = 'progress';
        state.previewMode = 'progress';
      }

      function renderVideo(data, nextCurrentID) {
        const preview = deps.preview;
        if (!preview) return;
        const videoPreviewURL = mediaPreviewURL(data);
        const existingVideo = preview.querySelector('video');
        if (videoPreviewURL && (mode !== 'video' || nextCurrentID !== mediaID || mediaURL !== videoPreviewURL || !existingVideo)) {
          preview.classList.remove('obs-preview');
          preview.innerHTML = '';
          const video = document.createElement('video');
          video.controls = true;
          video.playsInline = true;
          video.preload = 'metadata';
          let videoReady = false;
          if (String(videoPreviewURL).includes('.m3u8')) {
            videoReady = attachPreviewHLS(video, videoPreviewURL);
          } else {
            video.src = videoPreviewURL;
            videoReady = true;
          }
          if (videoReady) {
            preview.appendChild(video);
            addVideoPlayButton(video);
            mode = 'video';
            mediaID = nextCurrentID;
            mediaURL = videoPreviewURL;
            state.previewMode = 'video';
            state.previewVideoURL = videoPreviewURL;
          } else {
            preview.innerHTML = '<div class="empty">HLSプレビューを準備中です</div>';
            mode = 'video-waiting';
            mediaURL = '';
            state.previewMode = 'video-waiting';
            state.previewVideoURL = '';
            if (deps.scheduleRefresh) deps.scheduleRefresh(500);
          }
        } else if (!videoPreviewURL && mode !== 'video-empty') {
          destroyPreviewHLS();
          preview.innerHTML = '<div class="empty">動画URLを準備中です</div>';
          mode = 'video-empty';
          mediaURL = '';
          state.previewMode = 'video-empty';
          state.previewVideoURL = '';
        }
      }

      function renderImage(data, nextCurrentID) {
        const preview = deps.preview;
        if (!preview) return;
        releaseIfLeavingVideo('image');
        preview.classList.remove('obs-preview');
        const existingImage = preview.querySelector('img');
        if (mode !== 'image' || nextCurrentID !== mediaID || !existingImage) {
          preview.innerHTML = '';
          const img = document.createElement('img');
          const previewImageURL = nextCurrentID ? '/image/current?v=' + encodeURIComponent(nextCurrentID) : data.previewImageURL;
          img.src = previewImageURL + (previewImageURL.includes('?') ? '&' : '?') + 'preview=1';
          img.alt = '現在公開中の画像';
          preview.appendChild(img);
          mode = 'image';
          mediaID = nextCurrentID;
          state.previewMode = 'image';
        }
      }

      function renderIngestPreviewController(phase, title, percent, progressText, force) {
        const preview = deps.preview;
        if (!preview) return;
        const label = ingestPhaseLabel(phase);
        if (!label) return;
        releaseIfLeavingVideo('ingest');
        preview.classList.remove('obs-preview');
        const pct = Math.max(0, Math.min(100, Number(percent || 0)));
        const detail = progressText || title || '';
        const nextMode = 'ingest:' + phase + ':' + pct + ':' + detail;
        if (!force && mode === nextMode) return;
        preview.innerHTML =
          '<div class="progress-preview">' +
            '<div>' + escapeHTML(label) + '</div>' +
            '<div class="progress-track" role="progressbar" aria-label="処理状況" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' + Math.round(pct) + '">' +
              '<div class="progress-fill" style="width:' + Math.max(6, pct) + '%"></div>' +
            '</div>' +
            (detail ? '<div class="progress-detail">' + escapeHTML(detail) + '</div>' : '') +
          '</div>';
        mode = nextMode;
        state.previewMode = nextMode;
      }

      function renderPreviewController(data, context) {
        if (!previewVisible) return;
        data = data || {};
        context = context || {};
        const nextCurrentID = context.nextCurrentID || (data.current && data.current.id) || '';
        if (context.uploadMode === 'obs' && data.obs && (data.obs.connected || data.obs.publishing)) {
          renderOBSPreview(data, context);
          return;
        }
        if (deps.preview) {
          deps.preview.classList.remove('obs-preview');
        }
        state.obsPreviewID = '';
        state.obsPreviewURL = '';
        if (context.localUploadActive) {
          releaseIfLeavingVideo('local-upload');
          return;
        }
        const ingestLabel = data.ingest && data.ingest.active ? ingestPhaseLabel(data.ingest.phase) : '';
        if (ingestLabel) {
          renderIngestProgress(data);
          return;
        }
        if (!data.current) {
          renderEmpty();
          return;
        }
        if (data.video && data.video.active) {
          renderVideoProgress(data);
          return;
        }
        if (data.current.kind === 'video') {
          renderVideo(data, nextCurrentID);
          return;
        }
        renderImage(data, nextCurrentID);
      }

      function resetOBSPreviewController() {
        destroyPreviewHLS();
        const preview = deps.preview;
        if (preview && mode === 'obs') {
          const video = preview.querySelector('video');
          if (video) {
            video.pause();
            video.removeAttribute('src');
            video.load();
          }
          preview.innerHTML = '<div class="empty">OBSプレビューを再起動中です</div>';
        }
        mode = 'obs-restarting';
        obsMediaID = '';
        obsMediaURL = '';
        state.previewMode = 'obs-restarting';
        state.obsPreviewID = '';
        state.obsPreviewURL = '';
      }

      function resetPreviewController() {
        destroyPreviewHLS();
        mode = '';
        mediaID = '';
        mediaURL = '';
        obsMediaID = '';
        obsMediaURL = '';
      }

      return {
        init: initPreviewController,
        render: renderPreviewController,
        setVisible: setPreviewVisibleController,
        renderIngest: renderIngestPreviewController,
        reset: resetPreviewController,
        resetOBS: resetOBSPreviewController
      };
    })();
`
