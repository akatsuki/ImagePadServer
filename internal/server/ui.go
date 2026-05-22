package server

const indexHTML = `<!doctype html>
<html lang="ja">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.appName}}</title>
  <link rel="icon" href="/favicon.ico" sizes="any">
  <link rel="shortcut icon" href="/favicon.ico">
  <style>
    :root {
      color-scheme: light;
      --bg: #f5f7f8;
      --panel: #ffffff;
      --ink: #17202a;
      --muted: #607080;
      --line: #d8e0e6;
      --accent: #1b7f6b;
      --accent-2: #d84f39;
      --soft: #eaf4f1;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: var(--bg);
      color: var(--ink);
    }
    header {
      padding: 8px clamp(12px, 2.4vw, 22px);
      border-bottom: 1px solid var(--line);
      background: var(--panel);
    }
    h1 {
      margin: 0;
      font-size: 22px;
      letter-spacing: 0;
    }
    main {
      display: grid;
      grid-template-columns: minmax(240px, 300px) minmax(0, 1fr);
      grid-template-areas: "sidebar content";
      gap: 10px;
      padding: 10px clamp(12px, 2.4vw, 22px) 12px;
    }
    .sidebar { grid-area: sidebar; }
    .content { grid-area: content; }
    section {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 10px;
    }
    h2 {
      margin: 0 0 10px;
      font-size: 15px;
      letter-spacing: 0;
    }
    .qr {
      width: min(100%, 142px);
      aspect-ratio: 1 / 1;
      display: block;
      border: 1px solid var(--line);
      border-radius: 8px;
      margin: 0 auto 8px;
    }
    .urlbox {
      display: grid;
      grid-template-columns: 1fr auto;
      gap: 6px;
      align-items: center;
      background: #f8fafb;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 6px;
    }
    code {
      overflow-wrap: anywhere;
      font-size: 12px;
      color: #263746;
    }
    .urlbox strong {
      display: block;
      margin-bottom: 2px;
      font-size: 11px;
      color: var(--muted);
    }
    button, .file-button {
      min-height: 32px;
      border: 0;
      border-radius: 8px;
      background: var(--accent);
      color: white;
      font-weight: 700;
      padding: 0 10px;
      cursor: pointer;
      white-space: nowrap;
      font-size: 13px;
    }
    button.secondary {
      background: #405564;
    }
    button.warn {
      background: var(--accent-2);
    }
    button:disabled {
      cursor: wait;
      opacity: .65;
    }
    form {
      display: grid;
      gap: 8px;
    }
    input[type="file"], input[type="url"], select, input[type="number"] {
      width: 100%;
      min-height: 34px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fff;
      padding: 6px 8px;
      font: inherit;
      font-size: 13px;
    }
    .mode-tabs {
      display: grid;
      grid-template-columns: 1fr auto 1fr;
      align-items: center;
      gap: 6px;
      margin-bottom: 8px;
    }
    .mode-tabs .divider {
      color: var(--muted);
      font-weight: 700;
    }
    .mode-tab {
      min-height: 36px;
      background: #e7edf1;
      color: #304554;
    }
    .mode-tab.active {
      background: var(--accent);
      color: #fff;
    }
    .upload-panel {
      display: none;
    }
    .upload-panel.active {
      display: grid;
      gap: 8px;
    }
    .controls {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 8px;
    }
    label span {
      display: block;
      margin-bottom: 4px;
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
    }
    .status {
      display: grid;
      gap: 6px;
      margin-top: 8px;
    }
    .pill {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      padding: 7px 8px;
      border-radius: 8px;
      background: var(--soft);
      color: #21443d;
      font-size: 12px;
      line-height: 1.35;
    }
    .pill span {
      text-align: right;
      overflow-wrap: anywhere;
    }
    .toggle-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 46px;
      align-items: center;
      gap: 12px;
      padding: 8px 9px;
      border-radius: 8px;
      background: #f8fafb;
      border: 1px solid var(--line);
      font-size: 12px;
    }
    .toggle-row strong {
      display: block;
      margin-bottom: 2px;
    }
    .toggle-row span {
      color: var(--muted);
    }
    .switch {
      position: relative;
      width: 46px;
      height: 26px;
      flex: 0 0 auto;
    }
    .switch input {
      position: absolute;
      opacity: 0;
      inset: 0;
    }
    .switch-slider {
      position: absolute;
      inset: 0;
      border-radius: 999px;
      background: #b5c1c9;
      cursor: pointer;
      transition: background .16s ease;
    }
    .switch-slider::before {
      content: "";
      position: absolute;
      width: 20px;
      height: 20px;
      left: 3px;
      top: 50%;
      border-radius: 50%;
      background: #fff;
      transform: translateY(-50%);
      transition: transform .16s ease;
      box-shadow: 0 1px 3px rgba(0,0,0,.25);
    }
    .switch input:checked + .switch-slider {
      background: var(--accent);
    }
    .switch input:checked + .switch-slider::before {
      transform: translate(20px, -50%);
    }
    .switch input:disabled + .switch-slider {
      cursor: wait;
      opacity: .6;
    }
    .preview {
      width: 100%;
      height: min(34vh, 250px);
      min-height: 160px;
      display: grid;
      place-items: center;
      background: #edf1f3;
      border: 1px dashed #bcc9d1;
      border-radius: 8px;
      overflow: hidden;
    }
    .preview img {
      width: 100%;
      height: 100%;
      max-width: 100%;
      max-height: 100%;
      object-fit: contain;
      display: block;
      min-width: 0;
      min-height: 0;
    }
    .empty {
      color: var(--muted);
      text-align: center;
      max-width: 100%;
      padding: 18px;
      overflow-wrap: anywhere;
    }
    .progress-preview {
      width: min(92%, 520px);
      display: grid;
      gap: 10px;
      color: #21443d;
      text-align: center;
      font-weight: 700;
    }
    .progress-track {
      width: 100%;
      height: 12px;
      overflow: hidden;
      border-radius: 999px;
      background: #d6e1e6;
    }
    .progress-fill {
      height: 100%;
      min-width: 8px;
      border-radius: inherit;
      background: var(--accent);
      transition: width .25s ease;
    }
    .progress-detail {
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
    }
    .actions {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      margin-top: 8px;
    }
    .video-links {
      display: grid;
      grid-template-columns: 1fr;
      gap: 8px;
      margin-top: 8px;
    }
    .quality-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 120px auto;
      gap: 8px;
      align-items: end;
      margin-top: 8px;
    }
    .local-panel {
      display: none;
      margin-top: 8px;
    }
    .local-panel.open {
      display: block;
    }
    .toast {
      min-height: 18px;
      color: var(--accent);
      font-weight: 700;
      font-size: 13px;
    }
    .mobile-only-hidden {
      display: none;
    }
    .about {
      display: grid;
      gap: 6px;
      margin: 10px 0 0;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.45;
    }
    .about strong {
      color: var(--ink);
    }
    .oss-list {
      margin: 4px 0 0;
      padding-left: 18px;
    }
    .oss-list li {
      margin: 2px 0;
    }
    @media (max-width: 860px) {
      main {
        grid-template-columns: 1fr;
        grid-template-areas:
          "content"
          "sidebar";
      }
      .controls { grid-template-columns: 1fr; }
      header {
        padding: 18px clamp(16px, 4vw, 42px);
      }
      h1 {
        font-size: 26px;
      }
      main {
        gap: 18px;
        padding: 18px clamp(16px, 4vw, 42px) 42px;
      }
      section {
        padding: 18px;
      }
      h2 {
        margin-bottom: 14px;
        font-size: 17px;
      }
      .qr {
        width: min(100%, 320px);
        margin-bottom: 14px;
      }
      button, .file-button {
        min-height: 40px;
        padding: 0 14px;
        font-size: 14px;
      }
      input[type="file"], input[type="url"], select, input[type="number"] {
        min-height: 42px;
        padding: 8px 10px;
        font-size: 16px;
      }
      .preview {
        height: auto;
        min-height: 320px;
      }
    }
    @media (min-width: 861px) and (max-height: 760px) {
      h1 { font-size: 20px; }
      h2 { margin-bottom: 8px; }
      button, .file-button { min-height: 30px; }
      input[type="file"], input[type="url"], select, input[type="number"] {
        min-height: 30px;
        padding: 5px 7px;
      }
      .preview {
        height: 210px;
        min-height: 150px;
      }
      .about {
        gap: 2px;
        font-size: 11px;
        line-height: 1.3;
      }
      .oss-list {
        margin-top: 2px;
      }
      details[open] {
        max-height: 88px;
        overflow: auto;
      }
    }
    @media (max-width: 720px) and (pointer: coarse) {
      .phone-connect {
        display: none;
      }
      .mobile-only-hidden {
        display: block;
      }
    }
  </style>
</head>
<body>
  <header>
    <h1>ImagePadServer</h1>
  </header>
  <main>
    <div class="sidebar">
      <section class="phone-connect">
        <h2>スマホ接続</h2>
        <img class="qr" src="{{.qrURL}}" alt="スマホ接続用QRコード">
        <div class="urlbox">
          <code id="phoneURL">{{.phoneURL}}</code>
          <button type="button" data-copy="phoneURL">コピー</button>
        </div>
      </section>
      <section class="mobile-only-hidden">
        <h2>スマホ接続済み</h2>
        <div class="urlbox">
          <code id="phoneURLMobile">{{.phoneURL}}</code>
          <button type="button" data-copy="phoneURLMobile">コピー</button>
        </div>
      </section>

      <section style="margin-top:10px">
        <h2>状態</h2>
        <div class="status">
          <div class="pill"><strong>外部公開</strong><span id="upnpText">確認中</span></div>
          <div class="pill"><strong>ImagePad URL</strong><span id="hasImage">未選択</span></div>
          <!-- SteamVR integration is frozen indefinitely. UI kept out of sight. -->
          <div class="toggle-row">
            <div><strong>ビデオプレーヤー対応</strong><span id="videoPlayerText">確認中</span></div>
            <label class="switch" title="VRChatビデオプレーヤー向けMP4/HLS生成を切り替え">
              <input id="videoPlayerToggle" type="checkbox">
              <span class="switch-slider"></span>
            </label>
          </div>
        </div>
      </section>
      <div class="about">
        <div><strong>{{.appName}} {{.version}}</strong></div>
        <div>Author: {{.author}}</div>
        <div>{{.copyright}}</div>
        <div>License: {{.license}}</div>
        <details>
          <summary>Open source notices</summary>
          <ul class="oss-list">
            {{range .openSource}}
            <li>{{.Name}}{{if .Version}} {{.Version}}{{end}} - {{.License}}</li>
            {{end}}
          </ul>
        </details>
      </div>
    </div>

    <div class="content">
      <section>
        <h2>画像アップロード</h2>
        <form id="uploadForm">
          <div class="mode-tabs" role="tablist" aria-label="アップロード方法">
            <button class="mode-tab active" id="fileModeButton" type="button" role="tab" aria-selected="true">画像</button>
            <span class="divider" aria-hidden="true">|</span>
            <button class="mode-tab" id="linkModeButton" type="button" role="tab" aria-selected="false">リンク</button>
          </div>
          <div class="upload-panel active" id="fileUploadPanel">
            <input id="imageInput" name="image" type="file" accept="image/png,image/jpeg,image/gif,image/webp,image/bmp,image/tiff,image/svg+xml,.jpg,.jpeg,.png,.gif,.webp,.bmp,.tif,.tiff,.svg" required>
          </div>
          <div class="upload-panel" id="linkUploadPanel">
            <input id="imageURLInput" name="imageURL" type="url" inputmode="url" placeholder="https://example.com/image.webp">
          </div>
          <div class="controls">
            <label><span>最大辺</span><input name="maxDimension" type="number" min="64" max="2048" value="2048"></label>
            <label><span>形式</span><select name="format"><option value="jpeg">JPEG</option><option value="png">PNG</option></select></label>
            <label><span>JPEG品質</span><input name="quality" type="number" min="40" max="95" value="88"></label>
            <label><span>最大MB</span><input name="maxMB" type="number" min="1" max="30" value="30"></label>
          </div>
          <button id="uploadButton" type="submit">変換して公開</button>
          <div class="toast" id="toast"></div>
        </form>
      </section>

      <section style="margin-top:10px">
        <h2>現在公開中の画像</h2>
        <div class="preview" id="preview"><div class="empty">まだ画像が選択されていません</div></div>
        <div class="actions">
          <div class="urlbox" style="flex:1 1 360px">
            <div>
              <strong id="shareURLLabel">{{.shareURLLabel}}</strong>
              <code id="shareURL">{{.shareURL}}</code>
            </div>
            <button type="button" data-copy="shareURL">コピー</button>
          </div>
          <button type="button" class="secondary" id="refreshButton">更新</button>
          <button type="button" class="warn" id="clearButton">画像クリア</button>
        </div>
        <div class="video-links">
          <div class="pill"><strong>VRChat Video</strong><span id="videoStatus">確認中</span></div>
          <div class="quality-row">
            <label><span>動画画質</span><select id="qualityMode"><option value="auto">Auto</option><option value="1080">1080p</option><option value="720">720p</option><option value="360">360p</option></select></label>
            <div class="pill"><strong>実効</strong><span id="qualityStatus">確認中</span></div>
            <button type="button" class="secondary" id="networkCheckButton">速度チェック</button>
          </div>
        </div>
      </section>
    </div>
  </main>
  <script>
    const state = {
      imageURL: {{printf "%q" .imageURL}},
      videoURL: {{printf "%q" .videoURL}},
      hlsURL: {{printf "%q" .hlsURL}},
      shareURL: {{printf "%q" .shareURL}},
      shareURLLabel: {{printf "%q" .shareURLLabel}},
      phoneURL: {{printf "%q" .phoneURL}},
      localImageURL: {{printf "%q" .localImageURL}},
      previewImageURL: {{printf "%q" .previewImageURL}},
      publicImageURL: {{printf "%q" .publicImageURL}},
      videoQuality: null,
      currentID: "",
      previewMode: "empty"
    };

    const toast = document.getElementById('toast');
    const uploadForm = document.getElementById('uploadForm');
    const uploadButton = document.getElementById('uploadButton');
    const imageInput = document.getElementById('imageInput');
    const imageURLInput = document.getElementById('imageURLInput');
    const fileModeButton = document.getElementById('fileModeButton');
    const linkModeButton = document.getElementById('linkModeButton');
    const fileUploadPanel = document.getElementById('fileUploadPanel');
    const linkUploadPanel = document.getElementById('linkUploadPanel');
    const preview = document.getElementById('preview');
    const videoPlayerToggle = document.getElementById('videoPlayerToggle');
    const videoPlayerText = document.getElementById('videoPlayerText');
    const qualityMode = document.getElementById('qualityMode');
    const qualityStatus = document.getElementById('qualityStatus');
    const networkCheckButton = document.getElementById('networkCheckButton');
    let uploadMode = 'file';
    let videoPlayerPending = false;
    const imageAccept = 'image/png,image/jpeg,image/gif,image/webp,image/bmp,image/tiff,image/svg+xml,.jpg,.jpeg,.png,.gif,.webp,.bmp,.tif,.tiff,.svg';
    const mediaAccept = imageAccept + ',video/*,video/mp4,video/quicktime,video/webm,video/x-matroska,.mp4,.mov,.m4v,.webm,.mkv,.avi';

    async function refreshState() {
      const res = await fetch('/api/state', { cache: 'no-store' });
      const data = await res.json();
      applyState(data);
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
      state.videoQuality = data.videoQuality;
      document.getElementById('phoneURL').textContent = data.phoneURL;
      document.getElementById('phoneURLMobile').textContent = data.phoneURL;
      document.getElementById('shareURL').textContent = data.shareURL || '公開URLは未取得です';
      document.getElementById('shareURLLabel').textContent = data.shareURLLabel || 'URL';
      document.getElementById('videoStatus').textContent = videoText(data.video);
      applyQuality(data.videoQuality);
      applyVideoPlayer(data.videoPlayer);
      document.getElementById('upnpText').textContent = publicText(data.tunnel, data.upnp);
      document.getElementById('hasImage').textContent = currentText(data.current);
      const nextCurrentID = data.current ? data.current.id : "";
      renderPreview(data, nextCurrentID);
      state.currentID = nextCurrentID;
    }

    function renderPreview(data, nextCurrentID) {
      if (!data.current) {
        if (state.previewMode !== 'empty') {
          preview.innerHTML = '<div class="empty">まだ画像が選択されていません</div>';
          state.previewMode = 'empty';
        }
        return;
      }
      if (data.video && data.video.active) {
        const percent = Math.max(0, Math.min(99, Number(data.video.progressPercent || 0)));
        const detail = data.video.progressText || data.video.message || '変換中';
        preview.innerHTML =
          '<div class="progress-preview">' +
            '<div>動画プレーヤー向けに変換中です</div>' +
            '<div class="progress-track" aria-label="変換進捗">' +
              '<div class="progress-fill" style="width:' + Math.max(6, percent) + '%"></div>' +
            '</div>' +
            '<div class="progress-detail">' + escapeHTML(detail) + '</div>' +
          '</div>';
        state.previewMode = 'progress';
        return;
      }
      if (data.current.kind === 'video') {
        if (state.previewMode !== 'video' || nextCurrentID !== state.currentID) {
          preview.innerHTML = '<div class="empty">動画をHLSとして配信できます</div>';
          state.previewMode = 'video';
        }
        return;
      }
      if (state.previewMode !== 'image' || nextCurrentID !== state.currentID) {
        preview.innerHTML = '';
        const img = document.createElement('img');
        img.src = data.previewImageURL + (data.previewImageURL.includes('?') ? '&' : '?') + 'preview=1';
        img.alt = '現在公開中の画像';
        preview.appendChild(img);
        state.previewMode = 'image';
      }
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

    function currentText(current) {
      if (!current) return '未選択';
      if (current.kind === 'video') {
        if (current.sizeBytes) return '動画 ' + Math.round(current.sizeBytes / 1024 / 1024) + ' MB';
        return '動画';
      }
      return current.width + ' x ' + current.height;
    }

    function publicText(tunnel, upnp) {
      if (tunnel && tunnel.ok && tunnel.url) return '公開HTTPS ' + tunnel.url;
      if (tunnel && tunnel.message) return tunnel.message;
      return upnpText(upnp);
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
      if (!video) return 'not checked';
      const formats = [];
      if (video.mp4) formats.push('MP4');
      if (video.hls) formats.push('HLS');
      if (formats.length && video.active) return formats.join(' / ') + ' streaming';
      if (formats.length) return formats.join(' / ') + ' ready';
      return video.message || 'not generated';
    }

    function applyQuality(data) {
      if (!data) {
        qualityStatus.textContent = '未測定';
        return;
      }
      qualityMode.value = data.mode || 'auto';
      const network = data.uploadMbps ? ' / ' + data.uploadMbps + ' Mbps' : '';
      const bitrateOnly = data.preset && data.preset.bitrateOnly ? ' / bitrate only' : '';
      qualityStatus.textContent = (data.effective || 'auto') + 'p' + network + bitrateOnly;
    }

    function applyVideoPlayer(data) {
      if (!data) {
        videoPlayerToggle.checked = false;
        videoPlayerText.textContent = '確認できません';
        return;
      }
      videoPlayerToggle.checked = !!data.enabled;
      videoPlayerToggle.disabled = videoPlayerPending;
      videoPlayerText.textContent = data.enabled ? '有効 / 自動コピーはHLS優先' : '無効 / 自動コピーは画像URL';
      imageInput.accept = data.enabled ? mediaAccept : imageAccept;
      fileModeButton.textContent = data.enabled ? '画像/動画' : '画像';
    }

    uploadForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      uploadButton.disabled = true;
      toast.textContent = 'アップロード中...';
      try {
        const res = uploadMode === 'link' ? await uploadFromLink() : await uploadFromFile();
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        const isVideo = data.current && data.current.kind === 'video';
        if (isVideo) {
          toast.textContent = data.clipboardCopied ? '動画HLS変換を開始し、URLをPCにコピーしました' : '動画HLS変換を開始しました';
        } else {
          toast.textContent = data.clipboardCopied ? '公開画像を更新し、URLをPCにコピーしました' : '公開画像を更新しました';
        }
      } catch (error) {
        toast.textContent = error.message || 'アップロードに失敗しました';
      } finally {
        uploadButton.disabled = false;
      }
    });

    function setUploadMode(mode) {
      uploadMode = mode;
      const linkMode = mode === 'link';
      fileModeButton.classList.toggle('active', !linkMode);
      linkModeButton.classList.toggle('active', linkMode);
      fileModeButton.setAttribute('aria-selected', String(!linkMode));
      linkModeButton.setAttribute('aria-selected', String(linkMode));
      fileUploadPanel.classList.toggle('active', !linkMode);
      linkUploadPanel.classList.toggle('active', linkMode);
      imageInput.required = !linkMode;
      imageURLInput.required = linkMode;
      uploadButton.textContent = linkMode ? 'リンクから変換して公開' : '変換して公開';
      if (linkMode) {
        imageURLInput.focus();
      }
    }

    function uploadFromFile() {
      const formData = new FormData(uploadForm);
      return fetch('/api/upload', { method: 'POST', body: formData });
    }

    function uploadFromLink() {
      const formData = new FormData(uploadForm);
      return fetch('/api/upload-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          url: imageURLInput.value.trim(),
          format: formData.get('format'),
          quality: formData.get('quality'),
          maxDimension: formData.get('maxDimension'),
          maxMB: formData.get('maxMB')
        })
      });
    }

    document.getElementById('clearButton').addEventListener('click', async () => {
      toast.textContent = '画像をクリア中...';
      try {
        const res = await fetch('/api/clear', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        toast.textContent = '画像をクリアしました';
      } catch (error) {
        toast.textContent = error.message || '画像クリアに失敗しました';
      }
    });

    document.addEventListener('click', async (event) => {
      const target = event.target.closest('[data-copy]');
      if (!target) return;
      const id = target.getAttribute('data-copy');
      const source = document.getElementById(id);
      const text = source.textContent;
      if (!text || !text.startsWith('http')) {
        toast.textContent = 'コピーできるURLがありません';
        return;
      }
      let copied = false;
      let pcCopied = false;
      try {
        copied = await copyText(text, source);
      } catch (error) {
        selectElementText(source);
      }
      try {
        const pcResult = await copyURLOnPC(id);
        pcCopied = pcResult.pcClipboardCopied;
      } catch (error) {
        pcCopied = false;
      }

      if (copied && pcCopied) {
        toast.textContent = 'コピーしました。PCにもコピー済みです';
      } else if (copied) {
        toast.textContent = 'この端末にコピーしました';
      } else if (pcCopied) {
        selectElementText(source);
        toast.textContent = 'PCにコピーしました。この端末ではURLを選択しました';
      } else {
        selectElementText(source);
        toast.textContent = 'URLを選択しました。Ctrl+Cでコピーできます';
      }
    });

    async function copyURLOnPC(target) {
      const res = await fetch('/api/copy-url', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target })
      });
      if (!res.ok) {
        throw new Error(await res.text());
      }
      return res.json();
    }

    async function copyText(text, source) {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
        return true;
      }
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.setAttribute('readonly', '');
      textarea.style.position = 'fixed';
      textarea.style.left = '-9999px';
      textarea.style.top = '0';
      document.body.appendChild(textarea);
      textarea.focus();
      textarea.select();
      try {
        if (!document.execCommand('copy')) {
          selectElementText(source);
          return false;
        }
        return true;
      } finally {
        textarea.remove();
      }
    }

    function selectElementText(element) {
      const range = document.createRange();
      range.selectNodeContents(element);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    }

    document.getElementById('refreshButton').addEventListener('click', refreshState);
    fileModeButton.addEventListener('click', () => setUploadMode('file'));
    linkModeButton.addEventListener('click', () => setUploadMode('link'));
    qualityMode.addEventListener('change', async () => {
      try {
        const res = await fetch('/api/video-quality', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: qualityMode.value })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyQuality(data);
        toast.textContent = '動画画質を更新しました';
      } catch (error) {
        toast.textContent = error.message || '動画画質の更新に失敗しました';
      }
    });
    networkCheckButton.addEventListener('click', async () => {
      networkCheckButton.disabled = true;
      toast.textContent = 'ネットワーク速度を確認中...';
      try {
        const res = await fetch('/api/network-check', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyQuality(data);
        toast.textContent = '速度チェックを更新しました';
      } catch (error) {
        toast.textContent = error.message || '速度チェックに失敗しました';
      } finally {
        networkCheckButton.disabled = false;
      }
    });
    videoPlayerToggle.addEventListener('change', async () => {
      const enabled = videoPlayerToggle.checked;
      videoPlayerPending = true;
      videoPlayerToggle.disabled = true;
      videoPlayerText.textContent = enabled ? 'FFmpeg確認中...' : '無効化中...';
      try {
        const res = await fetch('/api/video-player', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ enabled })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyVideoPlayer(data);
        await refreshState();
        toast.textContent = enabled ? 'ビデオプレーヤー対応を有効にしました' : 'ビデオプレーヤー対応を無効にしました';
      } catch (error) {
        await refreshState();
        toast.textContent = error.message || 'ビデオプレーヤー対応の切り替えに失敗しました';
      } finally {
        videoPlayerPending = false;
        videoPlayerToggle.disabled = false;
      }
    });
    refreshState();
    setInterval(refreshState, 2000);
  </script>
</body>
</html>`
