package server

const uiBodyMarkup = `<body>
  <header>
    <h1>ImagePadServer</h1>
    <label class="theme-control"><span>テーマ</span><select id="themeSelect" aria-label="テーマ"><option value="auto">自動</option><option value="light">ライト</option><option value="dark">ダーク</option></select></label>
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
          <div class="pill"><strong>外部公開</strong><span id="upnpText">確認中</span><button type="button" class="small secondary" id="tunnelReconnectButton">再接続</button></div>
          <div class="pill"><strong>ImagePad URL</strong><span id="hasImage">未選択</span></div>
          <div class="pill"><strong>更新</strong><span id="updateText">確認中</span></div>
          <!-- SteamVR integration is frozen indefinitely. UI kept out of sight. -->
          <div class="toggle-row">
            <div><strong>ビデオプレーヤー対応</strong><span id="videoPlayerText">確認中</span></div>
            <label class="switch" title="VRChatビデオプレーヤー向けMP4/HLS生成を切り替え">
              <input id="videoPlayerToggle" type="checkbox">
              <span class="switch-slider"></span>
            </label>
          </div>
          <div class="toggle-row" id="musicModeRow" hidden>
            <div><strong>ミュージックモード</strong><span id="musicModeText">無効</span></div>
            <label class="switch" title="YouTubeやニコニコ動画などを音声のみ取得してミュージックプレーヤーで再生">
              <input id="musicModeToggle" type="checkbox">
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
            <button class="mode-tab" id="obsModeButton" type="button" role="tab" aria-selected="false" hidden>OBS</button>
          </div>
          <div class="upload-panel active" id="fileUploadPanel">
            <div class="drop-zone" id="fileDropZone">
              <div class="drop-hint" id="dropHint">Drop image or RAW files here</div>
              <input id="imageInput" name="image" type="file" accept="image/png,image/jpeg,image/gif,image/webp,image/bmp,image/tiff,image/svg+xml,image/x-sony-arw,image/x-canon-crw,image/x-canon-cr2,image/x-canon-cr3,image/x-panasonic-rw2,image/x-olympus-orf,image/x-fuji-raf,image/x-nikon-nef,image/x-nikon-nrw,image/x-sigma-x3f,image/x-adobe-dng,.jpg,.jpeg,.png,.gif,.webp,.bmp,.tif,.tiff,.svg,.arw,.srf,.sr2,.crw,.cr2,.cr3,.rw2,.raw,.orf,.raf,.nef,.nrw,.x3f,.dng" required>
              <div class="drop-file-name" id="dropFileName">No file selected</div>
            </div>
          </div>
          <div class="upload-panel" id="linkUploadPanel">
            <div class="link-input-row">
              <input id="imageURLInput" name="imageURL" type="url" inputmode="url" placeholder="https://example.com/image.webp">
              <button type="button" id="pasteURLButton" class="icon-button" title="クリップボードから貼り付け" aria-label="クリップボードから貼り付け">📋</button>
            </div>
          </div>
          <div class="upload-panel" id="obsUploadPanel">
            <div class="obs-grid">
              <div class="urlbox">
                <div>
                  <strong>OBS Server</strong>
                  <code id="obsServerAddress">RTMP receiver is stopped</code>
                </div>
                <button type="button" data-copy="obsServerAddress">コピー</button>
              </div>
              <div class="urlbox">
                <div>
                  <strong>Stream Key</strong>
                  <code id="obsStreamKey">-</code>
                </div>
                <div class="secret-actions">
                  <button type="button" class="secondary icon-button" id="obsKeyRevealButton" title="Stream Keyを表示" aria-label="Stream Keyを表示">&#128065;</button>
                  <button type="button" data-copy="obsStreamKey">コピー</button>
                  <button type="button" class="secondary" id="obsKeyRotateButton">更新</button>
                </div>
              </div>
              <div class="pill"><strong>OBS</strong><span id="obsStatus">確認中</span></div>
              <div class="urlbox">
                <div>
                  <strong>OBS Latency</strong>
                  <span id="obsLatencyStatus">auto</span>
                </div>
                <select id="obsLatencyMode" aria-label="OBS latency mode">
                  <option value="hls">通常遅延（HLS）</option>
                  <option value="lhls">低遅延（LHLS, 実験）</option>
                  <option value="llhls">超低遅延（LL-HLS, 実験）</option>
                  <option value="rtspt">リアルタイム（RTSPT, PC専用）</option>
                </select>
                <label><input id="obsDVRToggle" type="checkbox"> DVR 30min</label>
                <div id="obsRtspt" class="obs-rtspt" style="display:none">
                  <strong>RTSPT URL</strong>
                  <code id="obsRtsptURL"></code>
                  <button id="obsRtsptCopy" type="button">コピー</button>
                  <span class="hint">PC専用。ブラウザプレビュー非対応のためURLをコピーして再生してください。</span>
                </div>
              </div>
            </div>
          </div>
          <div class="controls">
            <label><span>最大辺</span><select name="maxDimension"><option value="2048" selected>2048px</option><option value="1024">1024px</option><option value="512">512px</option><option value="256">256px</option><option value="128">128px</option></select></label>
            <label><span>形式</span><select name="format" id="formatSelect"><option value="webp" selected>高品質 (WebP)</option><option value="jpeg">高圧縮 (JPEG)</option><option value="png">非劣化 (PNG)</option></select></label>
            <label><span>品質</span><select name="quality" id="qualitySelect"></select></label>
            <label><span>最大MB</span><select name="maxMB"><option value="30" selected>30 MB</option><option value="20">20 MB</option><option value="10">10 MB</option><option value="5">5 MB</option><option value="1">1 MB</option></select></label>
          </div>
          <div class="upload-actions">
            <button id="uploadButton" type="submit" name="uploadAction" value="publish">変換して公開</button>
            <button id="queueUploadButton" type="submit" class="secondary" name="uploadAction" value="queue">変換キューへ</button>
          </div>
          <div class="toast" id="toast"></div>
          <div class="mobile-progress" id="mobileProgress">
            <div id="mobileProgressText">変換中</div>
            <div class="progress-track" aria-label="変換進捗">
              <div class="progress-fill" id="mobileProgressFill" style="width:6%"></div>
            </div>
          </div>
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

    <div class="history">
      <section>
        <div class="wing-tabs" role="tablist" aria-label="右ウイング">
          <button class="wing-tab active" id="historyTabButton" type="button" role="tab" aria-selected="true" data-wing-tab="history">履歴</button>
          <button class="wing-tab" id="favoritesTabButton" type="button" role="tab" aria-selected="false" data-wing-tab="favorites">お気に入り</button>
          <button class="wing-tab" id="queueTabButton" type="button" role="tab" aria-selected="false" data-wing-tab="queue">変換キュー</button>
        </div>
        <div class="wing-list" id="historyList">
          <div class="empty">まだ履歴がありません</div>
        </div>
      </section>
    </div>

    <div class="quit">
      <button type="button" class="quit-button" id="quitButton" title="サーバーアプリ本体を終了します">終了</button>
    </div>
  </main>
  <div class="drag-drop-overlay" id="dragDropOverlay" aria-hidden="true">
    <div class="drag-drop-message">
      <strong>ドロップして選択</strong>
      <span id="dragDropOverlayHint">画像またはRAWファイルを選択します</span>
    </div>
  </div>
  <div class="pairing-panel" id="pairingPanel" role="status" aria-live="polite">
    <p class="pairing-title">BrowserRelayStreamer ペアリングコード</p>
    <strong class="pairing-pin" id="pairingPin">0000</strong>
    <p class="pairing-detail" id="pairingDetail">相手側のPCでこのコードを入力してください。</p>
  </div>
`
