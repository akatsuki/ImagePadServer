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
      padding: 12px clamp(14px, 3vw, 28px);
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
      gap: 12px;
      padding: 12px clamp(14px, 3vw, 28px) 18px;
    }
    section {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 12px;
    }
    h2 {
      margin: 0 0 10px;
      font-size: 15px;
      letter-spacing: 0;
    }
    .qr {
      width: min(100%, 190px);
      aspect-ratio: 1 / 1;
      display: block;
      border: 1px solid var(--line);
      border-radius: 8px;
      margin: 0 auto 10px;
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
      gap: 10px;
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
      gap: 8px;
      margin-bottom: 10px;
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
      gap: 10px;
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
      gap: 8px;
      margin-top: 10px;
    }
    .pill {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      padding: 8px 9px;
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
      display: flex;
      align-items: center;
      justify-content: space-between;
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
      top: 3px;
      border-radius: 50%;
      background: #fff;
      transition: transform .16s ease;
      box-shadow: 0 1px 3px rgba(0,0,0,.25);
    }
    .switch input:checked + .switch-slider {
      background: var(--accent);
    }
    .switch input:checked + .switch-slider::before {
      transform: translateX(20px);
    }
    .switch input:disabled + .switch-slider {
      cursor: wait;
      opacity: .6;
    }
    .preview {
      width: 100%;
      height: min(46vh, 420px);
      min-height: 220px;
      display: grid;
      place-items: center;
      background: #edf1f3;
      border: 1px dashed #bcc9d1;
      border-radius: 8px;
      overflow: hidden;
    }
    .preview img {
      max-width: 100%;
      max-height: 100%;
      object-fit: contain;
      display: block;
    }
    .empty {
      color: var(--muted);
      text-align: center;
      padding: 32px;
    }
    .actions {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-top: 10px;
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
      margin: 0 clamp(14px, 3vw, 28px) 18px;
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
      main { grid-template-columns: 1fr; }
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
    <div>
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

      <section style="margin-top:12px">
        <h2>状態</h2>
        <div class="status">
          <div class="pill"><strong>外部公開</strong><span id="upnpText">確認中</span></div>
          <div class="pill"><strong>ImagePad URL</strong><span id="hasImage">未選択</span></div>
          <div class="toggle-row">
            <div><strong>SteamVR連携</strong><span id="steamvrText">確認中</span></div>
            <label class="switch" title="SteamVRへの登録を切り替え">
              <input id="steamvrToggle" type="checkbox">
              <span class="switch-slider"></span>
            </label>
          </div>
        </div>
      </section>
    </div>

    <div>
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

      <section style="margin-top:12px">
        <h2>現在公開中の画像</h2>
        <div class="preview" id="preview"><div class="empty">まだ画像が選択されていません</div></div>
        <div class="actions">
          <div class="urlbox" style="flex:1 1 320px">
            <code id="imageURL">{{.imageURL}}</code>
            <button type="button" data-copy="imageURL">コピー</button>
          </div>
          <button type="button" class="secondary" id="refreshButton">更新</button>
          <button type="button" class="warn" id="clearButton">画像クリア</button>
          <button type="button" class="secondary" id="localToggle">外部URLを表示</button>
        </div>
        <div class="local-panel" id="localPanel">
          <div class="urlbox">
            <code id="publicImageURL">{{.publicImageURL}}</code>
            <button type="button" data-copy="publicImageURL">コピー</button>
          </div>
        </div>
      </section>
    </div>
  </main>

  <footer class="about">
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
  </footer>
  <script>
    const state = {
      imageURL: {{printf "%q" .imageURL}},
      phoneURL: {{printf "%q" .phoneURL}},
      localImageURL: {{printf "%q" .localImageURL}},
      previewImageURL: {{printf "%q" .previewImageURL}},
      publicImageURL: {{printf "%q" .publicImageURL}},
      currentID: ""
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
    const steamvrToggle = document.getElementById('steamvrToggle');
    const steamvrText = document.getElementById('steamvrText');
    let uploadMode = 'file';

    async function refreshState() {
      const res = await fetch('/api/state', { cache: 'no-store' });
      const data = await res.json();
      applyState(data);
    }

    function applyState(data) {
      state.imageURL = data.imageURL;
      state.phoneURL = data.phoneURL;
      state.localImageURL = data.localImageURL;
      state.previewImageURL = data.previewImageURL;
      state.publicImageURL = data.publicImageURL;
      document.getElementById('phoneURL').textContent = data.phoneURL;
      document.getElementById('phoneURLMobile').textContent = data.phoneURL;
      document.getElementById('imageURL').textContent = data.imageURL || '画像URLは未取得です';
      document.getElementById('publicImageURL').textContent = data.publicImageURL || '外部URLは未取得です';
      document.getElementById('upnpText').textContent = publicText(data.tunnel, data.upnp);
      document.getElementById('hasImage').textContent = data.current ? (data.current.width + ' x ' + data.current.height) : '未選択';
      const nextCurrentID = data.current ? data.current.id : "";
      if (data.current && nextCurrentID !== state.currentID) {
        preview.innerHTML = '';
        const img = document.createElement('img');
        img.src = data.previewImageURL + (data.previewImageURL.includes('?') ? '&' : '?') + 'preview=1';
        img.alt = '現在公開中の画像';
        preview.appendChild(img);
      } else if (!data.current && state.currentID !== "") {
        preview.innerHTML = '<div class="empty">まだ画像が選択されていません</div>';
      }
      state.currentID = nextCurrentID;
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

    async function refreshSteamVR() {
      try {
        const res = await fetch('/api/steamvr', { cache: 'no-store' });
        const data = await res.json();
        applySteamVR(data);
      } catch (error) {
        steamvrToggle.disabled = true;
        steamvrText.textContent = '確認できません';
      }
    }

    function applySteamVR(data) {
      steamvrToggle.checked = !!data.enabled;
      steamvrToggle.disabled = !data.available;
      if (!data.available) {
        steamvrText.textContent = data.message || '利用不可';
      } else {
        steamvrText.textContent = data.enabled ? '有効' : '無効';
      }
    }

    uploadForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      uploadButton.disabled = true;
      toast.textContent = '変換中...';
      try {
        const res = uploadMode === 'link' ? await uploadFromLink() : await uploadFromFile();
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        toast.textContent = data.clipboardCopied ? '公開画像を更新し、URLをPCにコピーしました' : '公開画像を更新しました';
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
    document.getElementById('localToggle').addEventListener('click', () => {
      const panel = document.getElementById('localPanel');
      panel.classList.toggle('open');
      document.getElementById('localToggle').textContent = panel.classList.contains('open') ? '外部URLを隠す' : '外部URLを表示';
    });
    steamvrToggle.addEventListener('change', async () => {
      const enabled = steamvrToggle.checked;
      steamvrToggle.disabled = true;
      steamvrText.textContent = enabled ? '有効化中...' : '無効化中...';
      try {
        const res = await fetch('/api/steamvr', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ enabled })
        });
        const data = await res.json();
        applySteamVR(data);
        toast.textContent = data.enabled ? 'SteamVR連携を有効にしました' : 'SteamVR連携を無効にしました';
      } catch (error) {
        await refreshSteamVR();
        toast.textContent = 'SteamVR連携の切り替えに失敗しました';
      }
    });
    refreshState();
    refreshSteamVR();
    setInterval(refreshState, 2000);
  </script>
</body>
</html>`
