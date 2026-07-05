package server

const dashboardCSSComponents = `
    .preview {
      width: 100%;
      max-width: 100%;
      height: min(42vh, 360px);
      min-height: 220px;
      box-sizing: border-box;
      position: relative;
      display: grid;
      place-items: center;
      background:
        linear-gradient(45deg, var(--preview-check) 25%, transparent 25% 75%, var(--preview-check) 75%),
        linear-gradient(45deg, var(--preview-check) 25%, transparent 25% 75%, var(--preview-check) 75%),
        var(--preview-bg);
      background-position: 0 0, 10px 10px;
      background-size: 20px 20px;
      border: 1px dashed var(--line);
      border-radius: var(--radius-small);
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
    .preview.obs-preview {
      width: 100%;
      height: clamp(260px, 38vh, 430px);
      min-height: 260px;
      aspect-ratio: 16 / 9;
      background: var(--media-black);
      border-style: solid;
      overflow: hidden;
    }
    .preview video,
    .preview.obs-preview video {
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      max-width: 100%;
      max-height: 100%;
      box-sizing: border-box;
      object-fit: contain;
      display: block;
      border-radius: inherit;
      background: var(--media-black);
    }
    .preview-play-button {
      position: absolute;
      left: 50%;
      top: 50%;
      z-index: 2;
      width: 58px;
      min-width: 58px;
      height: 58px;
      min-height: 58px;
      padding: 0;
      border-radius: 50%;
      background: rgba(238, 238, 238, .72);
      color: rgba(0, 0, 0, .82);
      border: 1px solid rgba(255, 255, 255, .46);
      box-shadow: 0 8px 22px rgba(0, 0, 0, .16);
      transform: translate(-50%, -50%);
      backdrop-filter: blur(8px);
      transition: background-color .12s ease, opacity .12s ease;
    }
    .preview-play-button svg {
      width: 26px;
      height: 26px;
      display: block;
      margin: auto;
      fill: currentColor;
    }
    .preview-play-button.playing {
      opacity: .46;
      transform: translate(-50%, -50%);
    }
    .preview-play-button.playing:hover,
    .preview-play-button:hover,
    .preview-play-button:focus-visible {
      background: rgba(245, 245, 245, .84);
      opacity: 1;
      transform: translate(-50%, -50%);
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
      color: var(--accent);
      text-align: center;
      font-weight: 700;
    }
    .progress-track {
      width: 100%;
      height: 12px;
      overflow: hidden;
      border-radius: var(--radius-pill);
      background: var(--control-bg);
    }
    .progress-fill {
      height: 100%;
      min-width: 8px;
      border-radius: inherit;
      background: var(--accent);
      transition: width .25s ease;
    }
    .progress-fill.indeterminate {
      width: 35%;
      min-width: 35%;
      transition: none;
      animation: indetSweep 1.2s ease-in-out infinite;
    }
    @keyframes indetSweep {
      0%   { transform: translateX(-115%); }
      100% { transform: translateX(330%); }
    }
    .progress-detail {
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
    }
    .tool-install-overlay {
      position: fixed;
      inset: 0;
      z-index: 1000;
      display: none;
      align-items: center;
      justify-content: center;
      background: var(--scrim-strong);
      backdrop-filter: blur(2px);
    }
    .tool-install-overlay.open { display: flex; }
    .tool-install-card {
      width: min(90%, 420px);
      display: grid;
      gap: 14px;
      padding: 24px;
      border-radius: var(--radius-overlay);
      background: var(--panel);
      box-shadow: var(--shadow-overlay);
      text-align: center;
    }
    .tool-install-title { font-weight: 800; color: var(--accent); }
    .tool-install-detail { color: var(--muted); font-size: 13px; font-weight: 700; }
    .tool-install-card.failed .tool-install-title { color: var(--accent-2); }
    .modal-backdrop {
      position: fixed;
      inset: 0;
      z-index: 10000;
      display: grid;
      place-items: center;
      padding: 18px;
      background: var(--scrim);
    }
    .modal-backdrop[hidden] { display: none; }
    .modal-card {
      max-width: min(92vw, 720px);
      max-height: min(88vh, 720px);
      overflow: auto;
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: var(--radius-panel);
      padding: 16px;
      box-shadow: var(--shadow-overlay);
    }
    .modal-card h2 { margin-bottom: 6px; }
    .modal-actions {
      display: flex;
      justify-content: flex-end;
      gap: 8px;
    }
    .phone-connect-card {
      width: min(92vw, 820px);
      max-width: min(92vw, 820px);
    }
    .phone-dialog-grid {
      display: grid;
      grid-template-columns: minmax(220px, 300px) minmax(0, 1fr);
      gap: 18px;
      align-items: center;
      margin-top: 14px;
    }
    .phone-dialog-qr {
      width: 100%;
      aspect-ratio: 1;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: var(--radius-overlay);
      background: var(--panel);
      object-fit: contain;
    }
    .phone-dialog-copy {
      display: grid;
      gap: 10px;
      min-width: 0;
    }
    .phone-dialog-copy .urlbox {
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
    }
    .candidate-list { display: grid; gap: 8px; margin: 12px 0; }
    .candidate-item {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 8px;
      align-items: center;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: color-mix(in srgb, var(--panel) 72%, transparent);
    }
    .candidate-meta { display: grid; gap: 3px; min-width: 0; }
    .candidate-kind { font-weight: 800; color: var(--ink); }
    .candidate-url {
      overflow-wrap: anywhere;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.35;
    }
    .settings-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      padding: 10px 0;
      border-top: 1px solid var(--line);
    }
    .settings-row:first-of-type { border-top: 0; }
    .settings-row[hidden] { display: none; }
    .settings-row p { margin: 2px 0 0; color: var(--muted); }
    .settings-section {
      display: grid;
      gap: 0;
      padding-top: 8px;
    }
    .settings-section-title {
      padding: 12px 0 4px;
      color: var(--muted);
      font-size: 12px;
      font-weight: 850;
    }
    .settings-section .settings-row:first-of-type {
      border-top: 0;
    }
    .theme-choice {
      display: inline-grid;
      grid-template-columns: repeat(3, minmax(68px, 1fr));
      gap: 3px;
      padding: 3px;
      border: 1px solid var(--line);
      border-radius: var(--radius-control);
      background: var(--control-bg);
      box-shadow: var(--shadow-control);
    }
    .theme-choice button {
      min-height: 30px;
      padding: 0 10px;
      border-radius: calc(var(--radius-control) - 3px);
      background: transparent;
      color: var(--control-ink);
      box-shadow: none;
      font-size: 12px;
      font-weight: 850;
    }
    .theme-choice button.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
      box-shadow: var(--shadow-active);
    }
    .theme-choice button.active:hover {
      transform: none;
      box-shadow: var(--shadow-active);
    }
    .actions {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      margin-top: 8px;
    }
    .upload-actions {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 8px;
    }
    .obs-connections-button[hidden],
    .obs-latency-option[hidden] {
      display: none;
    }
    .mode-tab {
      min-height: 42px;
    }
    .mode-tab.active {
      box-shadow: var(--shadow-active);
    }
    .drop-zone {
      min-height: 166px;
      padding: 18px;
      border-radius: var(--radius-card);
    }
    .flow-secondary .controls {
      grid-template-columns: 1fr;
      gap: 10px;
    }
    .flow-secondary .upload-actions {
      grid-template-columns: 1fr;
    }
    .flow-secondary .upload-actions #uploadButton {
      min-height: 52px;
      font-size: 15px;
      background: linear-gradient(135deg, var(--color-positive), color-mix(in srgb, var(--color-positive) 76%, var(--color-default)));
      color: var(--on-positive);
      box-shadow: var(--shadow-positive);
    }
    .flow-secondary .upload-actions #queueUploadButton {
      min-height: 42px;
      background: var(--control-bg-strong);
      color: var(--control-ink);
    }
    .flow-secondary .upload-actions #obsLatencyDetailButton {
      min-height: 42px;
      background: var(--control-bg-strong);
      color: var(--control-ink);
    }
    .upload-progress-panel {
      display: none;
      gap: 8px;
      padding: 10px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel-strong);
      box-shadow: var(--shadow-control);
    }
    .upload-progress-panel.open {
      display: grid;
    }
    .upload-progress-panel[hidden] {
      display: none;
    }
    .upload-progress-top {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 10px;
      align-items: center;
      color: var(--control-ink);
      font-size: 12px;
      line-height: 1.35;
    }
    .upload-progress-top strong,
    .upload-progress-top span {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .upload-progress-top span {
      color: var(--muted);
      font-weight: 800;
      font-variant-numeric: tabular-nums;
    }
    .video-links {
      display: grid;
      grid-template-columns: 1fr;
      gap: 8px;
      margin-top: 8px;
    }
    .video-links[hidden] {
      display: none;
    }
    .obs-grid {
      display: grid;
      gap: 8px;
    }
    .secret-actions {
      display: flex;
      gap: 6px;
      align-items: center;
    }
    .icon-button {
      width: 34px;
      min-width: 34px;
      padding: 0;
      font-size: 15px;
    }
    .obs-latency-actions {
      display: block;
    }
    .obs-latency-actions select {
      width: 100%;
    }
    .obs-connections-card { width: min(100%, 760px); }
    .connection-table-wrap {
      overflow-x: auto;
      border: 1px solid var(--line);
      border-radius: var(--radius-card);
    }
    .connection-table {
      width: 100%;
      min-width: 640px;
      border-collapse: collapse;
      font-size: 13px;
    }
    .connection-table th,
    .connection-table td {
      padding: 10px 12px;
      border-bottom: 1px solid var(--line);
      text-align: left;
      white-space: nowrap;
    }
    .connection-table th {
      background: var(--panel-strong);
      color: var(--ink);
      font-weight: 800;
    }
    .connection-table tbody tr:last-child td { border-bottom: 0; }
    .connection-empty { color: var(--muted); text-align: center; }
    .lag-cell {
      display: flex;
      align-items: center;
      gap: 8px;
      min-width: 150px;
    }
    .lag-bar {
      width: 96px;
      height: 10px;
      overflow: hidden;
      border-radius: var(--radius-pill);
      background: var(--control-bg);
    }
    .lag-bar-fill {
      display: block;
      height: 100%;
      min-width: 8px;
      border-radius: inherit;
    }
    .lag-good { background: var(--success); }
    .lag-ok { background: var(--accent); }
    .lag-warn { background: var(--warning); }
    .lag-bad { background: var(--accent-2); }
    .lag-value {
      min-width: 38px;
      font-variant-numeric: tabular-nums;
      font-weight: 800;
      color: var(--ink);
    }
    .link-input-row {
      display: flex;
      gap: 8px;
      align-items: stretch;
    }
    .link-input-row input[type="url"] {
      flex: 1;
      min-width: 0;
    }
    .wing-tabs {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 6px;
      margin-bottom: 8px;
    }
    .wing-tab {
      min-height: 32px;
      padding: 0 6px;
      background: var(--control-bg);
      color: var(--control-ink);
      font-size: 12px;
      border-color: var(--line);
      line-height: 1.2;
      white-space: normal;
    }
    .wing-tab.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .wing-list {
      display: grid;
      gap: 7px;
      max-height: none;
      min-height: 0;
      overflow: auto;
      padding-right: 2px;
    }
    .history-item {
      display: grid;
      grid-template-columns: 48px minmax(0, 1fr) auto;
      gap: 8px;
      align-items: center;
      width: 100%;
      min-height: 58px;
      padding: 6px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel-strong);
      color: var(--ink);
      text-align: left;
    }
    .history-item:hover {
      border-color: var(--border-strong);
      box-shadow: var(--shadow-control-hover);
    }
    .history-item.current {
      border-color: var(--accent);
      background: var(--soft);
    }
    .history-thumb {
      width: 48px;
      height: 48px;
      display: grid;
      place-items: center;
      overflow: hidden;
      border-radius: calc(var(--radius-small) - 1px);
      background: var(--control-bg);
      color: var(--muted);
      font-size: 11px;
      font-weight: 800;
    }
    .history-thumb img {
      width: 100%;
      height: 100%;
      object-fit: cover;
      display: block;
    }
    .history-meta {
      min-width: 0;
      display: grid;
      gap: 3px;
    }
    .history-title {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-size: 12px;
      font-weight: 800;
    }
    .history-detail {
      color: var(--muted);
      font-size: 11px;
      font-weight: 700;
    }
    .history-actions {
      display: grid;
      grid-template-columns: repeat(2, auto) 34px;
      align-items: center;
      gap: 4px;
    }
    .history-action-button {
      min-height: 30px;
      padding: 0 7px;
      border-radius: var(--radius-small);
      font-size: 12px;
    }
    .queue-item {
      display: grid;
      gap: 6px;
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel-strong);
      background-position: center;
      background-size: cover;
    }
    .queue-item.has-thumb {
      background-color: var(--panel-strong);
    }
    .queue-top {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 8px;
    }
    .queue-title {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-size: 12px;
      font-weight: 800;
    }
    .queue-status {
      flex: 0 0 auto;
      color: var(--muted);
      font-size: 11px;
      font-weight: 800;
    }
    .queue-item.running {
      border-color: var(--accent);
      background: var(--soft);
    }
    .queue-item.featured {
      gap: 8px;
      padding: 10px;
      min-height: 86px;
    }
    .queue-item.featured .queue-title {
      font-size: 13px;
    }
    .queue-item.featured .history-detail {
      font-size: 12px;
      line-height: 1.35;
    }
    .queue-item.error {
      border-color: var(--accent-2);
      background: color-mix(in srgb, var(--accent-2) 13%, var(--panel));
    }
    .queue-divider {
      height: 1px;
      margin: 2px 0;
      background: var(--line);
      opacity: .65;
    }
    .heart-button {
      min-width: 34px;
      width: 34px;
      height: 34px;
      min-height: 34px;
      padding: 0;
      border-radius: 50%;
      background: transparent;
      color: var(--muted);
      line-height: 1;
    }
    .heart-button svg {
      width: 19px;
      height: 19px;
      display: block;
      fill: none;
      stroke: currentColor;
      stroke-width: 2.2;
      stroke-linecap: round;
      stroke-linejoin: round;
    }
    .heart-button.active {
      color: var(--accent-2);
      background: color-mix(in srgb, var(--accent-2) 14%, var(--panel));
    }
    .heart-button.active svg {
      fill: currentColor;
    }
    .quality-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 120px auto;
      gap: 8px;
      align-items: end;
      margin-top: 8px;
      padding-top: 8px;
      border-top: 1px solid var(--line);
    }
    .quality-row.standalone {
      margin-top: 0;
      padding-top: 0;
      border-top: 0;
    }
    .video-quality-options {
      grid-template-columns: minmax(0, 1fr);
      margin-top: 10px;
      padding-top: 10px;
    }
    .video-quality-options[hidden] {
      display: none;
    }
    .local-panel {
      display: none;
      margin-top: 8px;
    }
    .local-panel.open {
      display: block;
    }
    .toast {
      position: fixed;
      right: 18px;
      bottom: 18px;
      z-index: 30;
      width: min(420px, calc(100vw - 36px));
      min-height: 0;
      padding: 12px 14px;
      border: 1px solid var(--line);
      border-left: 5px solid var(--accent);
      border-radius: var(--radius-card);
      background: color-mix(in srgb, var(--panel) 96%, transparent);
      color: var(--ink);
      font-weight: 800;
      font-size: 13px;
      line-height: 1.45;
      box-shadow: var(--shadow-overlay);
      opacity: 0;
      transform: translateY(12px);
      pointer-events: none;
      transition: opacity .16s ease, transform .16s ease, bottom .16s ease;
    }
    .toast.active {
      opacity: 1;
      transform: translateY(0);
      pointer-events: auto;
    }
    .toast.error {
      border-left-color: var(--accent-2);
      color: var(--accent-2);
    }
    .toast-message {
      overflow-wrap: anywhere;
    }
    .toast-actions {
      display: none;
      justify-content: flex-end;
      gap: 6px;
      margin-top: 10px;
    }
    .toast.error .toast-actions {
      display: flex;
    }
    .toast-action {
      min-height: 28px;
      padding: 0 10px;
      border: 1px solid color-mix(in srgb, var(--color-negative) 28%, transparent);
      background: var(--negative-soft);
      color: var(--color-negative);
      font-size: 12px;
      font-weight: 900;
    }
    .toast-close {
      min-width: 30px;
      padding: 0;
    }
    .pairing-panel {
      display: none;
      position: fixed;
      inset: auto 18px 18px auto;
      z-index: 20;
      width: min(360px, calc(100vw - 36px));
      border: 2px solid var(--line);
      border-radius: var(--radius-small);
      background: var(--panel);
      box-shadow: var(--shadow-overlay);
      padding: 14px;
    }
    .pairing-panel.active {
      display: block;
    }
    body.pairing-active .toast {
      bottom: 210px;
    }
    .pairing-title {
      margin: 0 0 6px;
      color: var(--ink);
      font-size: 14px;
      font-weight: 800;
    }
    .pairing-pin {
      display: block;
      margin: 4px 0 8px;
      color: var(--ink);
      font-size: 64px;
      line-height: 1;
      font-weight: 900;
      letter-spacing: 0;
      font-variant-numeric: tabular-nums;
    }
    .pairing-detail {
      margin: 0;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.45;
    }
    .mobile-progress {
      display: none;
      gap: 7px;
      padding: 8px;
      border-radius: var(--radius-small);
      background: var(--soft);
      color: var(--accent);
      font-size: 12px;
      font-weight: 700;
    }
    .mobile-progress.open {
      display: none;
    }
    .mobile-only-hidden {
      display: none;
    }
    .about {
      display: grid;
      gap: 6px;
      margin: 0;
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
    }`
