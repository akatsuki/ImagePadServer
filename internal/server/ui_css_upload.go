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
    #musicIntentButton .music-caret {
      width: 12px;
      height: 12px;
      margin: 0;
    }
    .media-kind-switch button.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
      box-shadow: var(--shadow-active);
    }
    .music-mode-menu {
      position: absolute;
      right: 0;
      top: calc(100% + 6px);
      z-index: 30;
      width: min(220px, 76vw);
      display: grid;
      gap: 4px;
      padding: 6px;
      border: 1px solid var(--line);
      border-radius: var(--radius-card);
      background: var(--panel);
      box-shadow: var(--shadow-overlay);
    }
    .music-mode-menu[hidden] {
      display: none;
    }
    .music-mode-menu button {
      min-height: 36px;
      justify-content: flex-start;
      border-radius: var(--radius-small);
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
      text-align: left;
    }
    .music-mode-menu button.active,
    .music-mode-menu button:hover {
      background: var(--control-bg-strong);
      color: var(--control-ink);
      transform: none;
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
    .music-workspace {
      display: grid;
      gap: 14px;
      padding: 16px;
      border-top: 1px solid var(--line);
      background: var(--flow-primary-bg);
    }
    .music-workspace[hidden] {
      display: none;
    }
    .music-mode-tabs {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 6px;
      padding: 3px;
      border: 1px solid var(--line);
      border-radius: var(--radius-pill);
      background: var(--control-bg);
      box-shadow: var(--shadow-inset-subtle);
    }
    .music-mode-tabs button {
      min-height: 34px;
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
    }
    .music-mode-tabs button.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
      box-shadow: var(--shadow-active);
      transform: none;
    }
    .music-pane {
      display: none;
    }
    .music-pane.active {
      display: grid;
    }
    .music-pane-main {
      display: grid;
      grid-template-columns: minmax(0, 1.1fr) minmax(260px, .75fr);
      gap: 12px;
      align-items: start;
    }
    .music-pane-main.playlist {
      grid-template-columns: minmax(0, 1.35fr) minmax(260px, .65fr);
    }
    .music-section {
      display: grid;
      gap: 10px;
      min-width: 0;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel);
      box-shadow: var(--shadow-soft);
    }
    .music-section-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      min-width: 0;
    }
    .music-section-head h3 {
      margin: 0;
      color: var(--ink);
      font-size: 14px;
      line-height: 1.25;
    }
    .music-section-head span {
      flex: 0 0 auto;
      color: var(--muted);
      font-size: 11px;
      font-weight: 850;
    }
    .music-control-grid,
    .music-saved-list {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 8px;
    }
    .music-track-list {
      display: grid;
      gap: 6px;
    }
    .music-track-row {
      display: grid;
      grid-template-columns: 48px minmax(0, 1fr) auto;
      gap: 8px;
      align-items: center;
      min-height: 42px;
      padding: 6px 8px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel-strong);
    }
    .music-track-row span {
      color: var(--muted);
      font-size: 12px;
      font-weight: 850;
    }
    .music-track-row strong {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      color: var(--control-ink);
      font-size: 13px;
    }
    .music-track-row.next {
      border-color: color-mix(in srgb, var(--accent) 32%, var(--line));
      background: var(--soft-blue);
    }
    .music-urlbox {
      min-height: 50px;
    }
    .music-party-placeholder {
      min-height: 180px;
      align-content: center;
    }
    .music-party-placeholder p {
      margin: 0;
      color: var(--muted);
      font-weight: 700;
    }
    .music-playlist-panel[hidden] {
      display: none;
    }
    .preview-panel[hidden] {
      display: none;
    }
    .music-player {
      display: grid;
      gap: 12px;
      padding: 12px;
    }
    .music-now {
      display: grid;
      gap: 4px;
      min-height: 58px;
      align-content: center;
      padding: 10px 12px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel-strong);
    }
    .music-now strong {
      color: var(--ink);
      font-size: 14px;
      line-height: 1.25;
    }
    .music-now span {
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
    }
    .music-player-controls {
      display: grid;
      grid-template-columns: repeat(4, minmax(44px, 1fr));
      gap: 8px;
    }
    .music-player-controls .icon-button {
      min-height: 44px;
      border-radius: var(--radius-small);
    }
    .music-player-controls svg {
      width: 20px;
      height: 20px;
    }
    .music-playlist-list {
      display: grid;
      gap: 6px;
      margin: 0;
      padding: 0;
      list-style: none;
    }
    .music-playlist-list li {
      display: grid;
      grid-template-columns: 34px minmax(0, 1fr) auto;
      gap: 8px;
      align-items: center;
      min-height: 44px;
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel);
      cursor: grab;
    }
    .music-playlist-list li:active {
      cursor: grabbing;
    }
    .music-playlist-list span,
    .music-playlist-list em {
      color: var(--muted);
      font-size: 12px;
      font-style: normal;
      font-weight: 800;
    }
    .music-playlist-list strong {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      color: var(--control-ink);
      font-size: 13px;
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
