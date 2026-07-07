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
    /* --- クラシック iTunes 風プレイリスト --- */
    .music-playlist-panel {
      display: grid;
      gap: 0;
      overflow: hidden;
      padding: 0;
    }
    .pl-deck {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      gap: 14px;
      align-items: center;
      padding: 12px 16px;
      border-bottom: 1px solid var(--line);
      background: linear-gradient(180deg, color-mix(in srgb, var(--panel-strong) 82%, #ffffff 18%), var(--panel-strong) 55%, color-mix(in srgb, var(--panel-strong) 88%, #000000 12%));
    }
    .pl-transport {
      display: flex;
      gap: 8px;
      align-items: center;
    }
    .pl-transport-button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 40px;
      height: 40px;
      padding: 0;
      border: 1px solid color-mix(in srgb, var(--line) 70%, #000 30%);
      border-radius: 50%;
      background: radial-gradient(circle at 50% 32%, color-mix(in srgb, var(--panel) 60%, #ffffff 40%), var(--panel) 68%);
      color: var(--control-ink);
      box-shadow: 0 1px 2px rgba(0,0,0,.35), inset 0 1px 0 rgba(255,255,255,.28);
    }
    .pl-transport-button:first-child {
      width: 48px;
      height: 48px;
    }
    .pl-transport-button svg {
      width: 18px;
      height: 18px;
    }
    .pl-transport-button.active {
      color: var(--accent);
      box-shadow: 0 1px 2px rgba(0,0,0,.35), inset 0 2px 5px rgba(0,0,0,.3);
    }
    .pl-lcd {
      display: grid;
      gap: 2px;
      min-width: 0;
      min-height: 58px;
      align-content: center;
      padding: 7px 14px 8px;
      border: 1px solid #9aa48c;
      border-radius: 8px;
      background: linear-gradient(180deg, #eef3e4, #dfe8cf 58%, #d6e0c5);
      box-shadow: inset 0 2px 5px rgba(60, 70, 45, .35), inset 0 -1px 0 rgba(255,255,255,.6);
      color: #39442f;
      text-align: center;
    }
    .pl-lcd-title {
      overflow: hidden;
      font-size: 14px;
      font-weight: 800;
      line-height: 1.3;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-lcd-artist {
      min-height: 16px;
      overflow: hidden;
      font-size: 11.5px;
      font-weight: 700;
      opacity: .78;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-lcd-progress-row {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      gap: 8px;
      align-items: center;
      margin-top: 3px;
    }
    .pl-lcd-time {
      font-size: 10.5px;
      font-variant-numeric: tabular-nums;
      font-weight: 800;
      opacity: .82;
    }
    .pl-lcd-progress {
      height: 5px;
      overflow: hidden;
      border-radius: 3px;
      background: rgba(57, 68, 47, .22);
      box-shadow: inset 0 1px 1px rgba(57, 68, 47, .3);
    }
    .pl-lcd-progress-fill {
      width: 0%;
      height: 100%;
      border-radius: 3px;
      background: linear-gradient(180deg, #7c8a66, #5d6b49);
      transition: width .4s linear;
    }
    .pl-mode-toggles {
      display: flex;
      gap: 6px;
      align-items: center;
    }
    .pl-toggle {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 34px;
      height: 30px;
      padding: 0;
      border: 1px solid var(--line);
      border-radius: 7px;
      background: var(--control-bg);
      color: var(--muted);
      box-shadow: none;
    }
    .pl-toggle svg {
      width: 15px;
      height: 15px;
    }
    .pl-toggle.active {
      border-color: color-mix(in srgb, var(--accent) 45%, var(--line));
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .pl-saved-menu-wrap {
      position: relative;
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
    .pl-input-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto auto auto;
      gap: 8px;
      align-items: center;
      padding: 10px 16px;
      border-bottom: 1px solid var(--line);
      background: var(--panel);
    }
    .pl-input-row input[type="text"] {
      min-height: 34px;
    }
    .pl-input-row #plFileButton {
      width: 38px;
      min-height: 34px;
      padding: 0;
    }
    .music-playlist-panel.pl-file-hover .pl-input-row {
      outline: 2px dashed var(--accent);
      outline-offset: -4px;
    }
    .pl-table-wrap {
      max-height: 380px;
      min-height: 200px;
      overflow-y: auto;
      background: var(--panel);
    }
    .pl-table {
      width: 100%;
      border-collapse: collapse;
      font-size: 12.5px;
    }
    .pl-table thead th {
      position: sticky;
      top: 0;
      z-index: 5;
      padding: 5px 10px;
      border-right: 1px solid var(--line);
      border-bottom: 1px solid var(--line);
      background: linear-gradient(180deg, color-mix(in srgb, var(--panel-strong) 80%, #ffffff 20%), var(--panel-strong));
      color: var(--muted);
      font-size: 11px;
      font-weight: 850;
      text-align: left;
      white-space: nowrap;
    }
    .pl-table thead th:last-child {
      border-right: 0;
    }
    .pl-table tbody td {
      padding: 5px 10px;
      color: var(--control-ink);
      vertical-align: middle;
    }
    .pl-table tbody tr {
      height: 34px;
      cursor: grab;
    }
    .pl-table tbody tr:nth-child(even) {
      background: color-mix(in srgb, var(--accent) 5%, transparent);
    }
    .pl-table tbody tr:hover {
      background: color-mix(in srgb, var(--accent) 12%, transparent);
    }
    .pl-table tbody tr.pl-dragging {
      opacity: .45;
    }
    .pl-table tbody tr.pl-drop-before td {
      box-shadow: inset 0 2px 0 var(--accent);
    }
    .pl-table tbody tr.pl-drop-after td {
      box-shadow: inset 0 -2px 0 var(--accent);
    }
    .pl-table tbody tr.pl-now-playing {
      background: color-mix(in srgb, var(--accent) 22%, transparent);
      font-weight: 800;
    }
    .pl-table tbody tr.pl-preparing {
      opacity: .68;
    }
    .pl-table tbody tr.pl-failed .pl-col-title span {
      color: #d64545;
    }
    .pl-empty-row td {
      padding: 26px 12px !important;
      color: var(--muted);
      font-weight: 700;
      text-align: center;
      cursor: default;
    }
    .pl-col-num {
      width: 34px;
      color: var(--muted);
      font-variant-numeric: tabular-nums;
      text-align: center;
    }
    .pl-speaker {
      width: 15px;
      height: 15px;
      color: var(--accent);
      vertical-align: -3px;
    }
    .pl-col-title {
      max-width: 0;
    }
    .pl-col-title span {
      overflow: hidden;
      display: inline-block;
      max-width: 100%;
      text-overflow: ellipsis;
      vertical-align: middle;
      white-space: nowrap;
    }
    .pl-art {
      width: 22px;
      height: 22px;
      margin-right: 7px;
      border-radius: 3px;
      object-fit: cover;
      vertical-align: middle;
    }
    .pl-badge {
      margin-left: 7px;
      color: var(--muted);
      font-size: 10.5px;
      font-style: normal;
      font-weight: 800;
      vertical-align: middle;
    }
    .pl-badge-error {
      color: #d64545;
    }
    .pl-col-artist {
      max-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .pl-col-time {
      width: 52px;
      font-variant-numeric: tabular-nums;
      text-align: right;
    }
    .pl-col-source {
      width: 90px;
      color: var(--muted);
      white-space: nowrap;
    }
    .pl-col-actions {
      width: 66px;
      text-align: right;
      white-space: nowrap;
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
    .pl-table tbody tr:hover .pl-row-action,
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
    .pl-footer {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr);
      gap: 12px;
      align-items: center;
      padding: 9px 16px;
      border-top: 1px solid var(--line);
      background: linear-gradient(180deg, color-mix(in srgb, var(--panel-strong) 85%, #ffffff 15%), var(--panel-strong));
    }
    .pl-track-count {
      color: var(--muted);
      font-size: 11.5px;
      font-weight: 800;
      white-space: nowrap;
    }
    .pl-share {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      gap: 8px;
      align-items: center;
      justify-self: end;
      max-width: 100%;
    }
    .pl-url-modes {
      display: inline-grid;
      grid-template-columns: repeat(2, minmax(48px, auto));
      gap: 2px;
      padding: 2px;
      border: 1px solid var(--line);
      border-radius: var(--radius-pill);
      background: var(--control-bg-strong);
    }
    .pl-url-mode {
      min-height: 22px;
      padding: 0 10px;
      border-radius: var(--radius-pill);
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
      font-size: 11px;
      font-weight: 850;
      line-height: 22px;
    }
    .pl-url-mode.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
      box-shadow: var(--shadow-active);
    }
    .pl-share code {
      overflow: hidden;
      min-width: 0;
      color: var(--muted);
      font-size: 11.5px;
      text-overflow: ellipsis;
      white-space: nowrap;
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
