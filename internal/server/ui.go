package server

const indexHTML = `<!doctype html>
<html lang="ja">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.appName}}</title>
  <link rel="icon" href="/favicon.ico?v=20260705-diamond-pad" sizes="any">
  <link rel="shortcut icon" href="/favicon.ico?v=20260705-diamond-pad">
  <script>` + themeBootScript + `</script>
  <style>` + dashboardCSS + `</style>
</head>
<body>
  <header>
    <div class="app-brand">
      <img class="app-icon" src="/app-icon.png?trim=1&v=20260705-diamond-pad" alt="" aria-hidden="true">
      <h1>ImagePadServer</h1>
      <span class="app-version">{{.version}}</span>
    </div>
    <div class="header-actions">
      <button type="button" class="phone-connect-button" id="phoneConnectButton" title="スマホ接続">
        <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
          <path fill="currentColor" d="M3 3h8v8H3V3Zm2 2v4h4V5H5Zm8-2h8v8h-8V3Zm2 2v4h4V5h-4ZM3 13h8v8H3v-8Zm2 2v4h4v-4H5Zm10-2h2v2h-2v-2Zm4 0h2v4h-4v-2h2v-2Zm-6 4h2v4h-2v-4Zm4 2h2v2h-2v-2Zm2-2h2v2h-2v-2Z"/>
        </svg>
        <span>スマホ接続</span>
      </button>
      <button type="button" class="settings-button icon-only-button" id="settingsButton" title="設定" aria-label="設定">
        <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
          <path fill="currentColor" d="M19.4 13.5c.1-.5.1-1 .1-1.5s0-1-.1-1.5l2-1.5-2-3.5-2.4 1a8 8 0 0 0-2.6-1.5L14 2h-4l-.4 2.5A8 8 0 0 0 7 6L4.6 5l-2 3.5 2 1.5a9 9 0 0 0 0 3l-2 1.5 2 3.5L7 18a8 8 0 0 0 2.6 1.5L10 22h4l.4-2.5A8 8 0 0 0 17 18l2.4 1 2-3.5-2-1.5ZM12 15.5A3.5 3.5 0 1 1 12 8a3.5 3.5 0 0 1 0 7.5Z"/>
        </svg>
      </button>
      <button type="button" class="quit-header-button icon-only-button" id="quitHeaderButton" title="終了" aria-label="終了">
        <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
          <path fill="currentColor" d="M11 2h2v10h-2V2Zm6.7 3.6-1.4 1.4A7 7 0 1 1 7.7 7L6.3 5.6a9 9 0 1 0 11.4 0Z"/>
        </svg>
      </button>
    </div>
  </header>
  <main>
    <div class="content">
      <section class="hero-panel" aria-label="メディアアップロード">
        <p class="visually-hidden">画像/音声/動画。画像、RAW、音声、動画をアップロードできます。</p>
        <div class="section-head">
          <div class="upload-title-row">
            <div class="section-title-copy">
              <h2 id="uploadHeading">画像アップロード</h2>
              <p class="section-kicker" id="uploadKicker">静止画を変換して、ImagePad URLとしてすぐ公開する</p>
            </div>
            <div class="media-kind-switch" id="mediaKindSwitch" role="group" aria-label="メディア種別" hidden>
              <button type="button" class="active" id="imageIntentButton" data-media-intent="image" aria-pressed="true">静止画</button>
              <button type="button" id="videoIntentButton" data-media-intent="video" aria-pressed="false">動画</button>
              <button type="button" id="musicIntentButton" data-media-intent="music" hidden aria-pressed="false" aria-haspopup="menu" aria-expanded="false">
                <span>ミュージック</span>
                <svg class="music-caret" viewBox="0 0 12 12" aria-hidden="true" focusable="false"><path fill="currentColor" d="M2.2 4.2 6 8l3.8-3.8H2.2Z"/></svg>
              </button>
              <div class="music-mode-menu" id="musicModeMenu" role="menu" aria-label="ミュージックモード" hidden>
                <button type="button" role="menuitemradio" aria-checked="true" data-music-mode-choice="single">シングル</button>
                <button type="button" role="menuitemradio" aria-checked="false" data-music-mode-choice="playlist">プレイリスト</button>
              </div>
            </div>
          </div>
        </div>
        <form id="uploadForm">
          <div class="flow-grid" id="flowGrid">
            <div class="flow-primary">
              <div class="source-card">
                <div class="flow-step source">
                  <svg class="step-badge" viewBox="0 0 28 28" aria-hidden="true" focusable="false">
                    <circle class="step-badge-bg" cx="14" cy="14" r="13"/>
                    <path class="step-badge-mark" d="M11.2 10.6 14 8.7v10.6"/>
                  </svg>
                  <span>入力</span>
                </div>
                <div class="mode-tabs" role="tablist" aria-label="アップロード方法">
                  <button class="mode-tab active" id="fileModeButton" type="button" role="tab" aria-selected="true" aria-controls="fileUploadPanel">画像</button>
                  <span class="divider" aria-hidden="true">|</span>
                  <button class="mode-tab" id="linkModeButton" type="button" role="tab" aria-selected="false" aria-controls="linkUploadPanel">リンク</button>
                  <button class="mode-tab" id="obsModeButton" type="button" role="tab" aria-selected="false" aria-controls="obsUploadPanel" hidden>OBS</button>
                </div>
                <div class="upload-panel active" id="fileUploadPanel" role="tabpanel" aria-labelledby="fileModeButton">
                  <div class="drop-zone" id="fileDropZone">
                    <div class="drop-hint" id="dropHint">画像またはRAWをここにドロップ</div>
                    <input id="imageInput" name="image" type="file" aria-label="アップロードする画像または動画ファイル" accept="image/png,image/jpeg,image/gif,image/webp,image/avif,image/heic,image/heif,image/jxl,image/bmp,image/tiff,image/svg+xml,image/x-sony-arw,image/x-canon-crw,image/x-canon-cr2,image/x-canon-cr3,image/x-panasonic-rw2,image/x-olympus-orf,image/x-fuji-raf,image/x-nikon-nef,image/x-nikon-nrw,image/x-sigma-x3f,image/x-adobe-dng,.jpg,.jpeg,.png,.gif,.webp,.avif,.heic,.heif,.jxl,.bmp,.tif,.tiff,.svg,.arw,.srf,.sr2,.crw,.cr2,.cr3,.rw2,.raw,.orf,.raf,.nef,.nrw,.x3f,.dng" required>
                    <div class="drop-file-name" id="dropFileName">ファイル未選択</div>
                  </div>
                </div>
                <div class="upload-panel" id="linkUploadPanel" role="tabpanel" aria-labelledby="linkModeButton" hidden>
                  <div class="link-input-row">
                    <input id="imageURLInput" name="imageURL" type="url" inputmode="url" aria-label="画像または動画のURL" placeholder="https://example.com/image.webp">
                    <button type="button" id="pasteURLButton" class="icon-button" title="クリップボードから貼り付け" aria-label="クリップボードから貼り付け">
                      <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M8 4h8m-6 0a2 2 0 0 1 4 0m-6 0H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2h-2m-9 5h6m-6 4h8m-8 4h5"/></svg>
                    </button>
                  </div>
                </div>
                <div class="upload-panel" id="obsUploadPanel" role="tabpanel" aria-labelledby="obsModeButton" hidden>
                  <div class="obs-grid">
                    <div class="urlbox">
                      <div>
                        <strong>OBS Server</strong>
                        <code id="obsServerAddress">RTMP receiver is stopped</code>
                      </div>
                      <button type="button" data-copy="obsServerAddress" aria-label="OBS Serverをコピー">コピー</button>
                    </div>
                    <div class="urlbox">
                      <div>
                        <strong>Stream Key</strong>
                        <code id="obsStreamKey">-</code>
                      </div>
                      <div class="secret-actions">
                        <button type="button" class="secondary icon-button" id="obsKeyRevealButton" title="Stream Keyを表示" aria-label="Stream Keyを表示">
                          <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6-10-6-10-6Z"/><circle cx="12" cy="12" r="3" fill="none" stroke="currentColor" stroke-width="2"/></svg>
                        </button>
                        <button type="button" data-copy="obsStreamKey" aria-label="Stream Keyをコピー">コピー</button>
                        <button type="button" class="secondary" id="obsKeyRotateButton">更新</button>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
            <div class="flow-secondary">
              <div class="output-card">
                <div class="flow-step output">
                  <svg class="step-badge" viewBox="0 0 28 28" aria-hidden="true" focusable="false">
                    <circle class="step-badge-bg" cx="14" cy="14" r="13"/>
                    <path class="step-badge-mark" d="M10 10.2c.7-1.1 1.9-1.7 3.6-1.7 2.2 0 3.8 1.2 3.8 3 0 1.4-.9 2.4-2.3 3.4L10.6 18h7.1"/>
                  </svg>
                  <span>変換</span>
                </div>
                <details class="advanced-options">
                  <summary>変換オプション</summary>
                  <div class="controls">
                    <label class="image-transform-option"><span>最大辺</span><select name="maxDimension"><option value="1024">1024px</option><option value="2048" selected>2048px</option><option value="4096">4096px</option><option value="8192">8192px</option></select></label>
                    <label class="image-transform-option"><span>形式</span><select name="format" id="formatSelect"><option value="png">非劣化 (PNG)</option><option value="webp" selected>高品質 (WebP)</option><option value="jpeg">高圧縮 (JPEG)</option></select></label>
                    <label class="image-transform-option"><span>品質</span><select name="quality" id="qualitySelect"></select></label>
                    <label class="image-transform-option"><span>最大MB</span><select name="maxMB"><option value="10">10 MB</option><option value="30" selected>30 MB</option><option value="60">60 MB</option><option value="120">120 MB</option></select></label>
                    <div class="quality-row video-quality-options" id="videoQualityOptions" hidden>
                      <label><span>動画画質</span><select id="qualityMode"><option value="auto">Auto</option><option value="1080">1080p</option><option value="720">720p</option><option value="360">360p</option></select></label>
                      <div class="pill"><strong>実効</strong><span id="qualityStatus">確認中</span></div>
                      <button type="button" class="secondary" id="networkCheckButton">速度チェック</button>
                    </div>
                    <label class="obs-latency-option" id="obsLatencyOption" hidden>
                      <span>OBSレイテンシ</span>
                      <select id="obsLatencyMode" aria-label="OBS latency mode">
                        <option value="hls-high">最高画質HLS（10s+）</option>
                        <option value="hls">高画質HLS（5s）</option>
                        <option value="rtsp-low">低遅延RTSP（3-4s）</option>
                        <option value="rtsp-ultra">超低遅延RTSP（1-2s）</option>
                        <option value="rtsp-realtime">リアルタイムRTSP（0.5s+）</option>
                      </select>
                    </label>
                  </div>
                </details>
              </div>
              <div class="publish-card">
                <div class="flow-step publish">
                  <svg class="step-badge" viewBox="0 0 28 28" aria-hidden="true" focusable="false">
                    <circle class="step-badge-bg" cx="14" cy="14" r="13"/>
                    <path class="step-badge-mark" d="M10.3 9.1h6.6l-3.1 3.7c2.3.1 3.8 1.2 3.8 3.1 0 2-1.6 3.4-4 3.4-1.6 0-2.9-.5-3.8-1.5"/>
                  </svg>
                  <span>公開</span>
                </div>
                <div class="upload-actions">
                  <button id="uploadButton" type="submit" name="uploadAction" value="publish">画像を公開</button>
                  <button id="obsLatencyDetailButton" type="button" class="secondary obs-connections-button" hidden>接続元リスト...</button>
                  <button id="queueUploadButton" type="submit" class="secondary" name="uploadAction" value="queue">動画変換へ</button>
                </div>
                <div class="upload-progress-panel" id="uploadProgressPanel" role="status" aria-live="polite" aria-atomic="true" hidden>
                  <div class="upload-progress-top">
                    <strong id="uploadProgressTitle">アップロード中</strong>
                    <span id="uploadProgressText">送信準備中…</span>
                  </div>
                  <div class="progress-track" role="progressbar" aria-label="アップロード進捗" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0">
                    <div class="progress-fill" id="uploadProgressFill" style="width:6%"></div>
                  </div>
                </div>
              </div>
            </div>
          </div>
          <div class="toast" id="toast" role="status" aria-live="polite" aria-atomic="true">
            <div class="toast-message" id="toastMessage"></div>
            <div class="toast-actions" id="toastActions">
              <button type="button" class="toast-action" id="toastYTDLPLoginButton" hidden>ログインする</button>
              <button type="button" class="toast-action" id="toastCopyButton">診断情報をコピー</button>
              <button type="button" class="toast-action toast-close" id="toastCloseButton" aria-label="通知を閉じる">×</button>
            </div>
          </div>
          <div class="mobile-progress" id="mobileProgress" role="status" aria-live="polite" aria-atomic="true">
            <div id="mobileProgressText">変換中</div>
            <div class="progress-track" role="progressbar" aria-label="変換進捗" aria-valuemin="0" aria-valuemax="100" aria-valuenow="6">
              <div class="progress-fill" id="mobileProgressFill" style="width:6%"></div>
            </div>
          </div>
        </form>
      </section>

      <div class="history">
        <section>
          <div class="wing-tabs" role="tablist" aria-label="右ウイング">
            <button class="wing-tab active" id="historyTabButton" type="button" role="tab" aria-selected="true" aria-controls="historyList" data-wing-tab="history">履歴</button>
            <button class="wing-tab" id="favoritesTabButton" type="button" role="tab" aria-selected="false" aria-controls="historyList" data-wing-tab="favorites">お気に入り</button>
            <button class="wing-tab" id="queueTabButton" type="button" role="tab" aria-selected="false" aria-controls="historyList" data-wing-tab="queue">動画変換</button>
          </div>
          <div class="wing-list" id="historyList" role="tabpanel" aria-labelledby="historyTabButton">
            <div class="empty">まだ履歴がありません</div>
          </div>
        </section>
      </div>
      <section class="hero-panel music-playlist-panel" id="musicPlaylistPanel" hidden aria-label="プレイリスト">
        <div class="pl-deck">
          <div class="pl-transport" role="group" aria-label="再生操作">
            <button type="button" class="pl-transport-button" id="plPlayButton" aria-label="再生">
              <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="currentColor" d="M8 5v14l11-7L8 5Z"/></svg>
            </button>
            <button type="button" class="pl-transport-button" id="plStopButton" aria-label="停止">
              <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="currentColor" d="M7 7h10v10H7V7Z"/></svg>
            </button>
            <button type="button" class="pl-transport-button" id="plNextButton" aria-label="次の曲">
              <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="currentColor" d="M6 5v14l9-7-9-7Zm10 0h2v14h-2V5Z"/></svg>
            </button>
          </div>
          <div class="pl-lcd" id="plLCD" role="status" aria-live="polite">
            <div class="pl-lcd-title" id="plNowTitle">プレイリストは停止中</div>
            <div class="pl-lcd-artist" id="plNowArtist">曲を追加して再生を始める</div>
            <div class="pl-lcd-progress-row">
              <span class="pl-lcd-time" id="plTimeElapsed">0:00</span>
              <div class="pl-lcd-progress" id="plProgressTrack" role="progressbar" aria-label="再生位置">
                <div class="pl-lcd-progress-fill" id="plProgressFill"></div>
              </div>
              <span class="pl-lcd-time" id="plTimeRemaining">-0:00</span>
            </div>
          </div>
          <div class="pl-mode-toggles" role="group" aria-label="再生オプション">
            <button type="button" class="pl-toggle" id="plShuffleButton" aria-pressed="false" title="シャッフル" aria-label="シャッフル">
              <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M16 3h5v5M4 7h3c2.6 0 4.2 2 5.8 5s3.2 5 5.8 5H21M21 16v5h-5M4 17h3c1.2 0 2.2-.4 3.1-1.2M14 8.2c1.1-.8 2.3-1.2 4-1.2h3"/></svg>
            </button>
            <button type="button" class="pl-toggle" id="plLoopButton" aria-pressed="false" title="ループ再生" aria-label="ループ再生">
              <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M17 2l4 4-4 4M3 11V9a3 3 0 0 1 3-3h15M7 22l-4-4 4-4m14-1v2a3 3 0 0 1-3 3H3"/></svg>
            </button>
            <div class="pl-saved-menu-wrap">
              <button type="button" class="pl-toggle" id="plMenuButton" aria-haspopup="menu" aria-expanded="false" title="プレイリストの保存と読み込み" aria-label="プレイリストの保存と読み込み">
                <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M4 6h16M4 12h16M4 18h10"/></svg>
              </button>
              <div class="pl-saved-menu" id="plMenu" role="menu" aria-label="保存済みプレイリスト" hidden>
                <button type="button" role="menuitem" id="plSaveButton">現在のリストを保存…</button>
                <div class="pl-saved-list" id="plSavedList"></div>
              </div>
            </div>
          </div>
        </div>
        <div class="pl-input-row">
          <input type="text" id="plInput" inputmode="url" placeholder="URL・ファイルパスを入力、またはファイルをドロップ" aria-label="曲のURLまたはファイルパス">
          <input type="file" id="plFileInput" accept="audio/*,.mp3,.wav,.flac,.ogg,.opus,.m4a,.aac,.wma" hidden aria-hidden="true">
          <button type="button" class="secondary icon-button" id="plFileButton" title="ファイルを選択" aria-label="ファイルを選択">
            <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" d="M21 12.5 12.4 21a5.6 5.6 0 0 1-8-8l8.8-8.7a3.7 3.7 0 0 1 5.3 5.3l-8.8 8.6a1.9 1.9 0 0 1-2.7-2.6l8.1-8"/></svg>
          </button>
          <button type="button" id="plAddButton">追加</button>
          <button type="button" class="secondary" id="plPlayNowButton">今すぐ再生</button>
        </div>
        <div class="pl-table-wrap">
          <table class="pl-table" aria-label="プレイリストの曲一覧">
            <thead>
              <tr><th class="pl-col-num" aria-label="再生状態"></th><th>曲名</th><th>アーティスト</th><th class="pl-col-time">時間</th><th class="pl-col-source">追加元</th><th class="pl-col-actions" aria-label="操作"></th></tr>
            </thead>
            <tbody id="plTrackTableBody">
              <tr class="pl-empty-row"><td colspan="6">曲がありません。上の入力欄から追加してください</td></tr>
            </tbody>
          </table>
        </div>
        <div class="pl-footer">
          <span class="pl-track-count" id="plTrackCount">0 曲</span>
          <div class="pl-share">
            <div class="pl-url-modes" role="group" aria-label="共有URLの種類">
              <button type="button" class="pl-url-mode" id="plUrlModeHLS" aria-pressed="true">HLS</button>
              <button type="button" class="pl-url-mode" id="plUrlModeRTSP" aria-pressed="false">RTSP</button>
            </div>
            <code id="plShareUrl">再生を開始するとURLが表示されます</code>
            <button type="button" data-copy="plShareUrl" aria-label="配信URLをコピー">コピー</button>
          </div>
        </div>
      </section>
    </div>

    <div class="preview-column">
      <section class="hero-panel preview-panel" id="previewPanel">
        <div class="section-head">
          <div class="section-title-copy">
            <h2 id="previewHeading">現在公開中の画像</h2>
            <p class="section-kicker" id="previewKicker">公開後の確認、コピー、クリアをここで行う</p>
          </div>
        </div>
        <div class="preview-body">
          <div class="preview" id="preview"><div class="empty">まだ画像が選択されていません</div></div>
          <div class="preview-controls">
            <div class="urlbox share-box">
              <div>
                <strong id="shareURLLabel">{{.shareURLLabel}}</strong>
                <code id="shareURL">{{.shareURL}}</code>
              </div>
              <button type="button" data-copy="shareURL" aria-label="公開URLをコピー">コピー</button>
            </div>
            <div class="preview-actions">
              <button type="button" class="secondary" id="refreshButton">更新</button>
              <button type="button" class="warn" id="clearButton" title="現在公開中の画像を消去">クリア</button>
            </div>
          </div>
          <div class="video-links" id="videoInfoPanel" hidden>
            <div class="pill"><strong>VRChat動画</strong><span id="videoStatus">確認中</span></div>
          </div>
        </div>
      </section>
    </div>

  </main>
  <div class="drag-drop-overlay" id="dragDropOverlay" aria-hidden="true">
    <div class="drag-drop-message">
      <strong>ドロップして選択</strong>
      <span id="dragDropOverlayHint">画像またはRAWファイルを選択します</span>
    </div>
  </div>
  <div class="tool-install-overlay" id="toolInstallOverlay" role="status" aria-live="polite" aria-hidden="true">
    <div class="tool-install-card" id="toolInstallCard">
      <div class="tool-install-title" id="toolInstallTitle">必要なツールを準備しています…</div>
      <div class="progress-track" role="progressbar" aria-label="インストール進捗" aria-valuemin="0" aria-valuemax="100" aria-valuenow="6">
        <div class="progress-fill" id="toolInstallFill" style="width:6%"></div>
      </div>
      <div class="tool-install-detail" id="toolInstallDetail"></div>
    </div>
  </div>
  <div class="modal-backdrop" id="rtspRiskDialog" hidden>
    <section class="modal-card" role="alertdialog" aria-modal="true" aria-labelledby="rtspRiskTitle" aria-describedby="rtspRiskDescription">
      <h2 id="rtspRiskTitle">RTSPを外部公開します</h2>
      <div id="rtspRiskDescription">
        <p>リアルタイム系RTSPは UPnP とグローバルIPを使って、VRChat から直接到達できるURLを作成します。</p>
        <ul>
          <li>ルーターに一時的なポート開放を要求します。</li>
          <li>生成されたURLを知っている相手は配信中の映像へ接続できます。</li>
          <li>配信終了時にポート開放は閉じますが、ネットワーク環境によって失敗する場合があります。</li>
        </ul>
      </div>
      <div class="modal-actions">
        <button type="button" class="secondary" id="rtspRiskCancel">キャンセル</button>
        <button type="button" class="warn" id="rtspRiskConfirm">リスクを理解して有効化</button>
      </div>
    </section>
  </div>
  <div class="modal-backdrop" id="obsKeyRiskDialog" hidden>
    <section class="modal-card" role="alertdialog" aria-modal="true" aria-labelledby="obsKeyRiskTitle" aria-describedby="obsKeyRiskDescription">
      <h2 id="obsKeyRiskTitle">OBS Stream Keyを変更します</h2>
      <div id="obsKeyRiskDescription">
        <p>Stream Key を変更すると、同じキーを知るOBS/中継元だけがこの受信口へ配信できます。</p>
        <ul>
          <li>変更後はOBS側の設定も同じ値へ差し替えてください。</li>
          <li>第三者に推測されにくい長いキーを使ってください。</li>
          <li>保存されたキーは次回起動時も維持されます。</li>
        </ul>
      </div>
      <label><span>新しい Stream Key</span><input id="obsKeyEditInput" type="text" autocomplete="off" spellcheck="false"></label>
      <div class="modal-actions">
        <button type="button" class="secondary" id="obsKeyRiskCancel">キャンセル</button>
        <button type="button" class="warn" id="obsKeyRiskConfirm">変更して保存</button>
      </div>
    </section>
  </div>
  <div class="modal-backdrop" id="phoneConnectDialog" hidden>
    <section class="modal-card phone-connect-card" role="dialog" aria-modal="true" aria-labelledby="phoneConnectTitle">
      <h2 id="phoneConnectTitle">スマホから接続</h2>
      <p class="section-kicker">スマホのカメラでQRを読み取るか、同じネットワーク内のブラウザでアドレスを開いてください。</p>
      <div class="phone-dialog-grid">
        <div class="protected-secret phone-dialog-qr-wrap" data-protect-label="クリックしてQRを表示" data-protected-reveal>
          <img class="phone-dialog-qr protected-secret-content" src="{{.qrURL}}" alt="スマホ接続用QRコード">
        </div>
        <div class="phone-dialog-copy">
          <div>
            <strong>アクセスアドレス</strong>
            <p class="section-kicker">スマホから画像や動画を送る時だけ使います。</p>
          </div>
          <div class="urlbox protected-secret" data-protect-label="クリックしてアドレスを表示" data-protected-reveal>
            <code id="phoneDialogURL" class="protected-secret-content">{{.phoneURL}}</code>
            <button type="button" data-copy="phoneDialogURL" aria-label="スマホ接続アドレスをコピー">コピー</button>
          </div>
        </div>
      </div>
      <div class="modal-actions">
        <button type="button" class="secondary" id="phoneConnectCloseButton">閉じる</button>
      </div>
    </section>
  </div>
  <div class="modal-backdrop" id="settingsModal" hidden>
    <section class="modal-card" role="dialog" aria-modal="true" aria-labelledby="settingsTitle">
      <h2 id="settingsTitle">設定</h2>
      <div class="settings-section">
        <div class="settings-section-title">一般</div>
        <div class="settings-row">
          <div>
            <strong>外観</strong>
            <p id="themeText">OSに追従</p>
          </div>
          <div class="theme-choice" role="group" aria-label="外観">
            <button type="button" data-theme-choice="system">OS</button>
            <button type="button" data-theme-choice="light">ライト</button>
            <button type="button" data-theme-choice="dark">ダーク</button>
          </div>
        </div>
        <div class="settings-row">
          <div>
            <strong>更新</strong>
            <p id="updateText">確認中</p>
          </div>
        </div>
      </div>
      <div class="settings-section">
        <div class="settings-section-title">接続</div>
        <div class="settings-row">
          <div>
            <strong>外部公開</strong>
            <p id="upnpText">確認中</p>
          </div>
          <button type="button" class="secondary" id="tunnelReconnectButton">再接続</button>
        </div>
        <div class="settings-row phone-connect">
          <div>
            <strong>スマホ接続</strong>
            <p>スマホからアップロードする場合だけ使います。</p>
            <div class="urlbox protected-secret" data-protect-label="クリックしてアドレスを表示" data-protected-reveal>
              <code id="phoneURL" class="protected-secret-content">{{.phoneURL}}</code>
              <button type="button" data-copy="phoneURL" aria-label="スマホ接続アドレスをコピー">コピー</button>
            </div>
          </div>
          <div class="protected-secret phone-qr-guard" data-protect-label="クリックしてQRを表示" data-protected-reveal>
            <img class="qr protected-secret-content" src="{{.qrURL}}" alt="スマホ接続用QRコード">
          </div>
        </div>
        <div class="settings-row mobile-only-hidden">
          <div>
            <strong>スマホ接続済み</strong>
            <div class="urlbox protected-secret" data-protect-label="クリックしてアドレスを表示" data-protected-reveal>
              <code id="phoneURLMobile" class="protected-secret-content">{{.phoneURL}}</code>
              <button type="button" data-copy="phoneURLMobile" aria-label="スマホ接続アドレスをコピー">コピー</button>
            </div>
          </div>
        </div>
      </div>
      <div class="settings-section">
        <div class="settings-section-title">外部サービス</div>
        <div class="settings-row">
          <div>
            <strong>YouTube / yt-dlp ログイン</strong>
            <p><span id="ytdlpCookieStatus">Cookie未保存</span>。BOT認証エラーが出た時だけ使用してください。</p>
          </div>
          <button type="button" id="ytdlpLoginButton">ログインする</button>
        </div>
        <div class="settings-row">
          <div>
            <strong>保存済みCookie</strong>
            <p>BOT認証回避に使うCookieを削除します。</p>
          </div>
          <button type="button" class="warn" id="ytdlpCookieDeleteButton">Cookie削除</button>
        </div>
      </div>
      <div class="settings-section">
        <div class="settings-section-title">アプリ情報</div>
        <div class="settings-row">
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
      </div>
      <div class="modal-actions">
        <button type="button" class="warn" id="quitButton" title="サーバーアプリ本体を終了します">アプリを終了</button>
        <button type="button" class="secondary" id="settingsCloseButton">閉じる</button>
      </div>
    </section>
  </div>
  <div class="modal-backdrop" id="mediaCandidateDialog" hidden>
    <section class="modal-card" role="dialog" aria-modal="true" aria-labelledby="mediaCandidateTitle" aria-describedby="mediaCandidateDescription">
      <h2 id="mediaCandidateTitle">ページ内動画候補</h2>
      <p id="mediaCandidateDescription">非表示ブラウザでページを開き、再生時に発生した動画リクエストを検出しました。使う候補を選んでください。</p>
      <div class="candidate-list" id="mediaCandidateList"></div>
      <div class="modal-actions">
        <button type="button" class="secondary" id="mediaCandidateClose">閉じる</button>
      </div>
    </section>
  </div>
  <div class="modal-backdrop" id="obsConnectionsDialog" hidden>
    <section class="modal-card obs-connections-card" role="dialog" aria-modal="true" aria-labelledby="obsConnectionsTitle" aria-describedby="obsConnectionsDescription">
      <h2 id="obsConnectionsTitle">接続元リスト</h2>
      <p id="obsConnectionsDescription">推定ラグはプロファイル、プロトコル、表示経路からの見積もりです。VRChat内の最終表示遅延とは異なる場合があります。</p>
      <div class="connection-table-wrap">
        <table class="connection-table">
          <thead><tr><th>IP</th><th>プロトコル</th><th>機種</th><th>状態</th><th>品質</th><th>推定ラグ</th></tr></thead>
          <tbody id="obsConnectionsTableBody">
            <tr><td colspan="6" class="connection-empty">接続なし</td></tr>
          </tbody>
        </table>
      </div>
      <div class="actions">
        <button type="button" class="secondary" id="obsConnectionsClose">閉じる</button>
      </div>
    </section>
  </div>
  <div class="pairing-panel" id="pairingPanel" role="status" aria-live="polite">
    <p class="pairing-title">BrowserRelayStreamer pairing code</p>
    <strong class="pairing-pin" id="pairingPin">0000</strong>
    <p class="pairing-detail" id="pairingDetail">Enter this code on the other computer.</p>
  </div>
  <script src="https://cdn.jsdelivr.net/npm/hls.js@1/dist/hls.min.js" defer></script>
  <script>` + dashboardScript + `</script>
</body>
</html>`
