package server

const dashboardCSSUpload = `
    .media-kind-switch {
      flex: 0 0 auto;
      position: relative;
      display: inline-grid;
      grid-template-columns: repeat(2, minmax(72px, 1fr));
      gap: 3px;
      padding: 2px;
      border: 1px solid var(--line);
      border-radius: var(--radius-pill);
      background: var(--control-bg-strong);
    }
    .media-kind-switch[hidden] {
      display: none;
    }
    .media-kind-switch.has-music {
      grid-template-columns: repeat(3, minmax(72px, 1fr));
    }
    .media-kind-switch button {
      min-height: 24px;
      padding: 0 10px;
      border-radius: var(--radius-pill);
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
      font-size: 12px;
      font-weight: 850;
      line-height: 24px;
    }
    #musicIntentButton {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 4px;
    }
    #musicIntentButton[hidden] {
      display: none;
    }
    .media-kind-switch button.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
      box-shadow: var(--shadow-active);
    }
    .section-kicker {
      margin: 0;
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
      line-height: 1.35;
    }
    .flow-grid {
      display: grid;
      grid-template-columns: minmax(0, 1.45fr) minmax(280px, .75fr);
      min-height: 314px;
    }
    .flow-grid[hidden] {
      display: none;
    }
    .music-caret {
      width: 10px;
      height: 10px;
      flex: 0 0 auto;
    }
    .music-mode-menu {
      position: absolute;
      top: calc(100% + 6px);
      right: 0;
      z-index: 30;
      display: grid;
      gap: 2px;
      min-width: 148px;
      padding: 4px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel);
      box-shadow: var(--shadow-soft);
    }
    .music-mode-menu[hidden] {
      display: none;
    }
    .music-mode-menu button {
      justify-content: flex-start;
      min-height: 32px;
      padding: 0 10px;
      border-radius: 6px;
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
      font-size: 12px;
      font-weight: 800;
      text-align: left;
    }
    .music-mode-menu button.active,
    .music-mode-menu button:hover {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .music-playlist-panel[hidden] {
      display: none;
    }
    .preview-panel[hidden] {
      display: none;
    }
    /* --- プレイリスト（フラット / Apple Music 風） --- */
    .music-playlist-panel {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      min-height: 0;
    }
    .music-playlist-panel .section-head {
      align-items: flex-start;
      display: flex;
      justify-content: space-between;
      gap: 10px;
    }
    .pl-menu-button {
      width: 34px;
      min-height: 30px;
      padding: 0;
      flex: 0 0 auto;
    }
    .pl-menu-button svg {
      width: 15px;
      height: 15px;
    }
    .pl-body {
      display: grid;
      grid-template-rows: auto auto minmax(120px, 1fr) auto;
      gap: 10px;
      min-height: 0;
    }
    .pl-player-row {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr);
      gap: 10px;
      align-items: stretch;
    }
    .pl-player-main {
      display: grid;
      grid-template-rows: auto auto auto;
      gap: 7px;
      min-width: 0;
      align-content: center;
    }
    .pl-video {
      position: relative;
      overflow: hidden;
      width: 176px;
      align-self: stretch;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel-strong);
    }
    .pl-video video {
      position: absolute;
      inset: 0;
      width: 100%;
      height: 100%;
      display: block;
      object-fit: contain;
      background: #000;
    }
    .pl-video:not(.pl-video-live) video {
      display: none;
    }
    .pl-video-empty {
      position: absolute;
      inset: 0;
      display: grid;
      place-items: center;
      padding: 8px;
      color: var(--muted);
      font-size: 11px;
      font-weight: 700;
      text-align: center;
    }
    .pl-now {
      display: grid;
      gap: 1px;
      min-height: 34px;
      text-align: center;
    }
    .pl-control-play .pl-icon-pause {
      display: none;
    }
    .pl-control-play.is-playing .pl-icon-pause {
      display: block;
    }
    .pl-control-play.is-playing .pl-icon-play {
      display: none;
    }
    .pl-now strong {
      overflow: hidden;
      color: var(--ink);
      font-size: 14px;
      line-height: 1.3;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-now span {
      overflow: hidden;
      min-height: 15px;
      color: var(--muted);
      font-size: 11.5px;
      font-weight: 700;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-progress-row {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      gap: 8px;
      align-items: center;
    }
    .pl-time {
      color: var(--muted);
      font-size: 10.5px;
      font-variant-numeric: tabular-nums;
      font-weight: 800;
    }
    .pl-progress {
      height: 4px;
      overflow: hidden;
      border-radius: 2px;
      background: color-mix(in srgb, var(--muted) 28%, transparent);
    }
    .pl-progress-fill {
      width: 0%;
      height: 100%;
      border-radius: 2px;
      background: var(--accent);
      transition: width .4s linear;
    }
    .pl-controls {
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 10px;
    }
    .pl-control {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 38px;
      height: 38px;
      padding: 0;
      border-radius: 50%;
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
    }
    .pl-control svg {
      width: 18px;
      height: 18px;
    }
    .pl-control:hover {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .pl-control-play {
      width: 46px;
      height: 46px;
      background: var(--accent);
      color: var(--on-positive);
    }
    .pl-control-play svg {
      width: 21px;
      height: 21px;
    }
    .pl-control-play:hover {
      background: var(--accent);
      color: var(--on-positive);
      filter: brightness(1.08);
    }
    .pl-toggle {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 32px;
      height: 32px;
      padding: 0;
      border-radius: 50%;
      background: transparent;
      color: var(--muted);
      box-shadow: none;
    }
    .pl-toggle svg {
      width: 15px;
      height: 15px;
    }
    .pl-toggle:hover {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .pl-toggle.active {
      background: color-mix(in srgb, var(--accent) 16%, transparent);
      color: var(--accent);
    }
    .pl-share > div {
      display: grid;
      gap: 4px;
      min-width: 0;
    }
    .pl-share code {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-url-modes {
      display: inline-grid;
      grid-template-columns: repeat(2, minmax(44px, auto));
      justify-self: start;
      gap: 2px;
      padding: 2px;
      border: 1px solid var(--line);
      border-radius: var(--radius-pill);
      background: var(--control-bg-strong);
    }
    .pl-url-mode {
      min-height: 20px;
      padding: 0 9px;
      border-radius: var(--radius-pill);
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
      font-size: 10.5px;
      font-weight: 850;
      line-height: 20px;
    }
    .pl-url-mode.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
      box-shadow: var(--shadow-active);
    }
    .pl-list {
      display: grid;
      align-content: start;
      overflow-y: auto;
      min-height: 0;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel);
    }
    .pl-list-empty {
      padding: 22px 12px;
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
      text-align: center;
    }
    .pl-row {
      display: grid;
      grid-template-columns: 26px auto minmax(0, 1fr) auto auto;
      gap: 8px;
      align-items: center;
      min-height: 42px;
      padding: 5px 8px;
      border-bottom: 1px solid color-mix(in srgb, var(--line) 55%, transparent);
      cursor: grab;
    }
    .pl-row:last-child {
      border-bottom: 0;
    }
    .pl-row:hover {
      background: color-mix(in srgb, var(--accent) 8%, transparent);
    }
    .pl-row.pl-dragging {
      opacity: .45;
    }
    .pl-row.pl-drop-before {
      box-shadow: inset 0 2px 0 var(--accent);
    }
    .pl-row.pl-drop-after {
      box-shadow: inset 0 -2px 0 var(--accent);
    }
    .pl-row.pl-preparing {
      opacity: .66;
    }
    .pl-row-num {
      color: var(--muted);
      font-size: 11.5px;
      font-variant-numeric: tabular-nums;
      font-weight: 800;
      text-align: center;
    }
    .pl-speaker {
      width: 15px;
      height: 15px;
      color: var(--accent);
      vertical-align: -3px;
    }
    .pl-art {
      width: 30px;
      height: 30px;
      border-radius: 5px;
      object-fit: cover;
    }
    .pl-row-copy {
      display: grid;
      gap: 0;
      min-width: 0;
    }
    .pl-row-copy strong {
      overflow: hidden;
      color: var(--control-ink);
      font-size: 12.5px;
      line-height: 1.3;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-row.pl-now-playing .pl-row-copy strong {
      color: var(--accent);
    }
    .pl-row-sub {
      overflow: hidden;
      color: var(--muted);
      font-size: 11px;
      font-weight: 700;
      line-height: 1.3;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-row-error {
      color: #d64545;
    }
    .pl-row.pl-failed .pl-row-copy strong {
      color: #d64545;
    }
    .pl-row-time {
      color: var(--muted);
      font-size: 11px;
      font-variant-numeric: tabular-nums;
      font-weight: 700;
    }
    .pl-row-actions {
      display: flex;
      gap: 2px;
    }
    .pl-row-action {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 26px;
      height: 26px;
      padding: 0;
      border-radius: 6px;
      background: transparent;
      color: var(--muted);
      box-shadow: none;
      opacity: 0;
    }
    .pl-row:hover .pl-row-action,
    .pl-row-action:focus-visible {
      opacity: 1;
    }
    .pl-row-action svg {
      width: 13px;
      height: 13px;
    }
    .pl-row-action:hover:not(:disabled) {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .pl-row-action:disabled {
      opacity: 0;
      pointer-events: none;
    }
    .pl-row-remove:hover {
      background: color-mix(in srgb, #d64545 18%, transparent) !important;
      color: #d64545 !important;
    }
    .pl-count {
      color: var(--muted);
      font-size: 11px;
      font-weight: 800;
      text-align: center;
    }
    .pl-saved-menu-wrap {
      position: relative;
      flex: 0 0 auto;
    }
    .pl-saved-menu {
      position: absolute;
      top: calc(100% + 6px);
      right: 0;
      z-index: 30;
      display: grid;
      gap: 4px;
      min-width: 210px;
      padding: 6px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel);
      box-shadow: var(--shadow-soft);
    }
    .pl-saved-menu[hidden] {
      display: none;
    }
    .pl-saved-menu > button,
    .pl-saved-load {
      justify-content: flex-start;
      min-height: 32px;
      padding: 0 10px;
      border-radius: 6px;
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
      font-size: 12px;
      font-weight: 800;
      text-align: left;
    }
    .pl-saved-menu > button:hover,
    .pl-saved-load:hover {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .pl-saved-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 4px;
      align-items: center;
    }
    .pl-saved-load {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-saved-delete {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 26px;
      height: 26px;
      padding: 0;
      border-radius: 6px;
      background: transparent;
      color: var(--muted);
      box-shadow: none;
    }
    .pl-saved-delete svg {
      width: 12px;
      height: 12px;
    }
    .pl-saved-delete:hover {
      background: color-mix(in srgb, #d64545 18%, transparent);
      color: #d64545;
    }
    .pl-saved-empty {
      padding: 8px 10px;
      color: var(--muted);
      font-size: 11.5px;
      font-weight: 700;
    }
    .flow-primary,
    .flow-secondary {
      display: grid;
      align-content: start;
      gap: 12px;
      padding: 16px;
    }
    .flow-primary {
      border-right: 1px solid var(--line);
      background: var(--flow-primary-bg);
    }
    .flow-secondary {
      background: var(--flow-secondary-bg);
      min-height: 0;
      overflow: visible;
    }
    .flow-step {
      display: flex;
      align-items: center;
      gap: 8px;
      color: var(--control-ink);
      font-size: 12px;
      font-weight: 900;
      letter-spacing: 0;
    }
    .flow-step::before {
      content: none;
    }
    .step-badge {
      width: 26px;
      height: 26px;
      flex: 0 0 26px;
      display: block;
      color: var(--step-ink);
      filter: drop-shadow(0 6px 10px color-mix(in srgb, var(--step-bg) 24%, transparent));
    }
    .step-badge-bg {
      fill: var(--step-bg);
    }
    .step-badge-mark {
      fill: none;
      stroke: currentColor;
      stroke-width: 2.2;
      stroke-linecap: round;
      stroke-linejoin: round;
    }
    .source-card,
    .output-card,
    .publish-card {
      display: grid;
      gap: 10px;
    }
    .publish-card {
      align-self: end;
      margin-top: auto;
      padding-top: 12px;
      border-top: 1px solid var(--line);
    }
    .advanced-options {
      border: 1px solid var(--line);
      border-radius: var(--radius-control);
      background: var(--panel);
      overflow: hidden;
    }
    .advanced-options summary {
      min-height: 42px;
      display: flex;
      align-items: center;
      padding: 0 12px;
      cursor: pointer;
      color: var(--control-ink);
      font-size: 13px;
      font-weight: 900;
      list-style-position: inside;
      border-radius: inherit;
    }
    .advanced-options .controls {
      padding: 0 12px 12px;
    }
    .status-panel {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr);
      gap: 10px;
      align-items: center;
      padding: 8px 10px;
      background: color-mix(in srgb, var(--panel) 82%, transparent);
      backdrop-filter: blur(10px);
    }
    .status-panel h2 {
      margin: 0;
      white-space: nowrap;
    }
    .status-panel .status {
      grid-template-columns: minmax(260px, 1.3fr) minmax(220px, .9fr);
      align-items: stretch;
      margin-top: 0;
    }
    .assist-dock {
      display: grid;
      grid-template-columns: minmax(240px, .8fr) minmax(0, 1.2fr);
      gap: 12px;
      align-items: start;
    }
    .assist-dock .about {
      margin: 0;
      align-content: start;
    }
    h2 {
      margin: 0 0 10px;
      font-size: 15px;
      letter-spacing: 0;
      color: var(--ink);
    }
    .qr {
      width: min(100%, 142px);
      aspect-ratio: 1 / 1;
      display: block;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      margin: 0 auto 8px;
    }
    .protected-secret {
      position: relative;
    }
    .protected-secret.protected {
      cursor: pointer;
    }
    .protected-secret.protected:not(.revealed) .protected-secret-content {
      filter: blur(9px);
      pointer-events: none;
      user-select: none;
    }
    .protected-secret.protected:not(.revealed)::after {
      content: attr(data-protect-label);
      position: absolute;
      inset: 0;
      display: grid;
      place-items: center;
      border-radius: var(--radius-small);
      background: var(--scrim-strong);
      color: var(--on-header);
      text-align: center;
      font-weight: 800;
      font-size: 13px;
      line-height: 1.3;
      padding: 8px;
    }
    .phone-qr-guard {
      width: min(100%, 142px);
      margin: 0 auto 8px;
    }
    .phone-qr-guard .qr {
      width: 100%;
      margin: 0;
    }
    .phone-dialog-qr-wrap {
      border-radius: var(--radius-overlay);
    }
    .urlbox {
      display: grid;
      grid-template-columns: 1fr auto;
      gap: 6px;
      align-items: center;
      background: var(--panel-strong);
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      padding: 6px;
    }
    code {
      overflow-wrap: anywhere;
      font-size: 12px;
      color: var(--url-ink);
    }
    .urlbox strong {
      display: block;
      margin-bottom: 2px;
      font-size: 11px;
      color: var(--muted);
    }
    button, .file-button {
      min-height: 32px;
      border: 1px solid transparent;
      border-radius: var(--radius-small);
      background: var(--accent);
      color: var(--on-positive);
      font-weight: 700;
      padding: 0 10px;
      cursor: pointer;
      white-space: nowrap;
      font-size: 13px;
      box-shadow: var(--shadow-soft);
      transition: background .14s ease, border-color .14s ease, transform .14s ease, box-shadow .14s ease;
    }
    button:hover, .file-button:hover {
      box-shadow: var(--shadow-control-hover);
      transform: translateY(-1px);
    }
    button svg {
      width: 17px;
      height: 17px;
      display: block;
      margin: auto;
    }
    button:disabled,
    button:disabled:hover {
      background: var(--disabled-bg) !important;
      border-color: var(--disabled-bg) !important;
      color: color-mix(in srgb, var(--panel) 80%, var(--ink)) !important;
      box-shadow: none !important;
      cursor: not-allowed;
      transform: none;
      opacity: .82;
    }
    button.secondary {
      background: var(--control-bg-strong);
      color: var(--control-ink);
    }
    button.warn {
      background: var(--accent-2);
      color: var(--on-negative);
    }
    button:disabled {
      cursor: not-allowed;
      opacity: .65;
    }
    form {
      display: grid;
      gap: 0;
    }
    input[type="file"], input[type="url"], select, input[type="number"] {
      width: 100%;
      min-height: 34px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel);
      padding: 6px 8px;
      font: inherit;
      font-size: 13px;
    }
    input[type="url"]:focus, select:focus, input[type="number"]:focus {
      outline: var(--focus-ring);
      border-color: var(--accent-blue);
    }
    .mode-tabs {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      align-items: center;
      gap: 6px;
      margin-bottom: 8px;
    }
    .mode-tabs.has-obs {
      grid-template-columns: repeat(3, minmax(0, 1fr));
    }
    .mode-tabs .divider {
      display: none;
    }
    .mode-tab {
      min-height: 36px;
      background: var(--control-bg);
      color: var(--control-ink);
      border-color: var(--line);
      line-height: 1.2;
      white-space: normal;
    }
    .mode-tab.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .upload-panel {
      display: none;
    }
    .upload-panel.active {
      display: grid;
      gap: 8px;
    }
    .drop-zone {
      display: grid;
      gap: 8px;
      place-items: center;
      min-height: 104px;
      padding: 12px;
      border: 2px dashed var(--line);
      border-radius: var(--radius-small);
      background: var(--panel-strong);
      text-align: center;
      transition: border-color .15s ease, background .15s ease, box-shadow .15s ease;
    }
    .drop-zone.dragover {
      border-color: var(--accent);
      background: var(--soft);
      box-shadow: var(--focus-ring);
    }
    .drop-zone input[type="file"] {
      max-width: 100%;
    }
    .drop-hint {
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
      line-height: 1.4;
    }
    .drop-file-name {
      max-width: 100%;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      color: var(--control-ink);
      font-size: 12px;
      font-weight: 800;
    }
    .drag-drop-overlay {
      position: fixed;
      inset: 0;
      z-index: 9999;
      display: grid;
      place-items: center;
      padding: 24px;
      background: var(--scrim-strong);
      color: var(--on-header);
      opacity: 0;
      visibility: hidden;
      pointer-events: none;
      transition: opacity .12s ease, visibility .12s ease;
    }
    body.drag-drop-active .drag-drop-overlay {
      opacity: 1;
      visibility: visible;
    }
    body.drag-drop-active header,
    body.drag-drop-active main {
      filter: grayscale(1);
    }
    .drag-drop-message {
      width: min(92vw, 420px);
      min-height: 160px;
      display: grid;
      place-items: center;
      gap: 8px;
      padding: 24px;
      border: 2px dashed color-mix(in srgb, var(--on-header) 78%, transparent);
      border-radius: var(--radius-small);
      background: var(--scrim);
      text-align: center;
      box-shadow: var(--shadow-overlay);
    }
    .drag-drop-message strong {
      font-size: 24px;
      letter-spacing: 0;
    }
    .drag-drop-message span {
      color: color-mix(in srgb, var(--on-header) 82%, transparent);
      font-size: 13px;
      font-weight: 700;
    }
    .controls {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 8px;
    }
    body.obs-protect .image-transform-option,
    body.obs-protect .video-quality-options {
      display: none !important;
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
      border-radius: var(--radius-small);
      background: var(--soft-blue);
      color: var(--ink);
      font-size: 12px;
      line-height: 1.35;
      border: 1px solid color-mix(in srgb, var(--accent-blue) 22%, transparent);
    }
    .pill strong {
      flex: 0 0 auto;
    }
    .pill span {
      min-width: 0;
      text-align: right;
      overflow-wrap: anywhere;
    }
    .pill.obs-status-pill {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      align-items: center;
    }
    .pill.obs-status-pill span {
      text-align: right;
    }
    .pill-action {
      min-height: 24px;
      padding: 0 8px;
      border: 1px solid var(--line);
      background: color-mix(in srgb, var(--panel) 68%, transparent);
      color: var(--control-ink);
      font-size: 11px;
      font-weight: 800;
      box-shadow: none;
    }
    .pill-action:hover {
      background: var(--surface-raised);
      transform: none;
    }
    .toggle-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 46px;
      align-items: center;
      gap: 12px;
      padding: 8px 9px;
      border-radius: var(--radius-small);
      background: var(--panel-strong);
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
      border-radius: var(--radius-pill);
      background: var(--disabled-bg);
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
      background: var(--surface-raised);
      transform: translateY(-50%);
      transition: transform .16s ease;
      box-shadow: var(--shadow-control);
    }
    .switch input:checked + .switch-slider {
      background: var(--switch-active-bg);
    }
    .switch input:checked + .switch-slider::before {
      transform: translate(20px, -50%);
    }
    .switch input:disabled + .switch-slider {
      cursor: not-allowed;
      opacity: .6;
    }`
