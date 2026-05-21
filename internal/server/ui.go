package server

const indexHTML = `<!doctype html>
<html lang="ja">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.appName}}</title>
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
    input[type="file"], select, input[type="number"] {
      width: 100%;
      min-height: 34px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fff;
      padding: 6px 8px;
      font: inherit;
      font-size: 13px;
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
      input[type="file"], select, input[type="number"] {
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
        </div>
      </section>
    </div>

    <div>
      <section>
        <h2>画像アップロード</h2>
        <form id="uploadForm">
          <input id="imageInput" name="image" type="file" accept="image/png,image/jpeg,image/gif" required>
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
          <button type="button" class="secondary" id="localToggle">ローカルURLを表示</button>
        </div>
        <div class="local-panel" id="localPanel">
          <div class="urlbox">
            <code id="localImageURL">{{.localImageURL}}</code>
            <button type="button" data-copy="localImageURL">コピー</button>
          </div>
        </div>
      </section>
    </div>
  </main>

  <script>
    const state = {
      imageURL: {{printf "%q" .imageURL}},
      phoneURL: {{printf "%q" .phoneURL}},
      localImageURL: {{printf "%q" .localImageURL}}
    };

    const toast = document.getElementById('toast');
    const uploadForm = document.getElementById('uploadForm');
    const uploadButton = document.getElementById('uploadButton');
    const preview = document.getElementById('preview');

    async function refreshState() {
      const res = await fetch('/api/state', { cache: 'no-store' });
      const data = await res.json();
      state.imageURL = data.imageURL;
      state.phoneURL = data.phoneURL;
      state.localImageURL = data.localImageURL;
      document.getElementById('phoneURL').textContent = data.phoneURL;
      document.getElementById('phoneURLMobile').textContent = data.phoneURL;
      document.getElementById('imageURL').textContent = data.imageURL;
      document.getElementById('localImageURL').textContent = data.localImageURL;
      document.getElementById('upnpText').textContent = upnpText(data.upnp);
      document.getElementById('hasImage').textContent = data.current ? (data.current.width + ' x ' + data.current.height) : '未選択';
      if (data.current) {
        preview.innerHTML = '';
        const img = document.createElement('img');
        img.src = data.localImageURL + '&preview=' + Date.now();
        img.alt = '現在公開中の画像';
        preview.appendChild(img);
      }
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

    uploadForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      uploadButton.disabled = true;
      toast.textContent = '変換中...';
      try {
        const formData = new FormData(uploadForm);
        const res = await fetch('/api/upload', { method: 'POST', body: formData });
        if (!res.ok) throw new Error(await res.text());
        await refreshState();
        toast.textContent = '公開画像を更新しました';
      } catch (error) {
        toast.textContent = error.message || 'アップロードに失敗しました';
      } finally {
        uploadButton.disabled = false;
      }
    });

    document.addEventListener('click', async (event) => {
      const target = event.target.closest('[data-copy]');
      if (!target) return;
      const id = target.getAttribute('data-copy');
      const text = document.getElementById(id).textContent;
      try {
        await navigator.clipboard.writeText(text);
        toast.textContent = 'コピーしました';
      } catch (error) {
        toast.textContent = 'コピーできませんでした。URLを手動で選択してください';
      }
    });

    document.getElementById('refreshButton').addEventListener('click', refreshState);
    document.getElementById('localToggle').addEventListener('click', () => {
      const panel = document.getElementById('localPanel');
      panel.classList.toggle('open');
      document.getElementById('localToggle').textContent = panel.classList.contains('open') ? 'ローカルURLを隠す' : 'ローカルURLを表示';
    });
    refreshState();
    setInterval(refreshState, 5000);
  </script>
</body>
</html>`
