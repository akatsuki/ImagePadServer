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
      grid-template-columns: minmax(240px, 300px) minmax(0, 1fr) minmax(260px, 320px);
      grid-template-areas:
        "sidebar content history"
        "sidebar content quit";
      gap: 10px;
      padding: 10px clamp(12px, 2.4vw, 22px) 12px;
    }
    .sidebar { grid-area: sidebar; }
    .content { grid-area: content; }
    .history { grid-area: history; }
    .quit { grid-area: quit; align-self: start; }
    .quit-button {
      width: 100%;
      min-height: 40px;
      background: #c0392b;
      color: #fff;
      font-weight: 700;
    }
    .quit-button:hover { background: #a93226; }
    .quit-button.done {
      background: #5b6b75;
      cursor: default;
    }
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
    body.obs-protect .phone-connect {
      position: relative;
    }
    body.obs-protect .phone-connect .qr {
      filter: blur(8px);
    }
    body.obs-protect .phone-connect::after {
      content: "配信保護中";
      position: absolute;
      top: 46px;
      left: 50%;
      width: min(100% - 20px, 142px);
      aspect-ratio: 1 / 1;
      display: grid;
      place-items: center;
      transform: translateX(-50%);
      border-radius: 8px;
      background: rgba(23, 32, 42, .52);
      color: #fff;
      text-align: center;
      font-weight: 800;
      font-size: 13px;
      line-height: 1.3;
      pointer-events: none;
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
    .drop-zone {
      display: grid;
      gap: 8px;
      place-items: center;
      min-height: 104px;
      padding: 12px;
      border: 2px dashed #aebdc6;
      border-radius: 8px;
      background: #f8fafb;
      text-align: center;
      transition: border-color .15s ease, background .15s ease, box-shadow .15s ease;
    }
    .drop-zone.dragover {
      border-color: var(--accent);
      background: var(--soft);
      box-shadow: 0 0 0 3px rgba(27, 127, 107, .12);
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
      color: #263746;
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
      background: rgba(23, 32, 42, .58);
      color: #fff;
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
      border: 2px dashed rgba(255, 255, 255, .78);
      border-radius: 8px;
      background: rgba(23, 32, 42, .36);
      text-align: center;
      box-shadow: 0 16px 48px rgba(0, 0, 0, .28);
    }
    .drag-drop-message strong {
      font-size: 24px;
      letter-spacing: 0;
    }
    .drag-drop-message span {
      color: rgba(255, 255, 255, .82);
      font-size: 13px;
      font-weight: 700;
    }
    .controls {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 8px;
    }
    body.obs-protect .controls,
    body.obs-protect .quality-row {
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
      border: 1px solid #bdd7d0;
      background: rgba(255, 255, 255, 0.58);
      color: #21443d;
      font-size: 11px;
      font-weight: 800;
      box-shadow: none;
    }
    .pill-action:hover {
      background: #fff;
      transform: none;
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
    .preview.obs-preview {
      height: auto;
      min-height: 0;
      aspect-ratio: 16 / 9;
      background: #05080b;
      border-style: solid;
      overflow: visible;
    }
    .preview.obs-preview video {
      width: 100%;
      height: 100%;
      max-width: 100%;
      max-height: min(54vh, 420px);
      object-fit: contain;
      display: block;
      background: #000;
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
      background: rgba(15, 28, 25, 0.55);
      backdrop-filter: blur(2px);
    }
    .tool-install-overlay.open { display: flex; }
    .tool-install-card {
      width: min(90%, 420px);
      display: grid;
      gap: 14px;
      padding: 24px;
      border-radius: 16px;
      background: #fff;
      box-shadow: 0 18px 50px rgba(0,0,0,0.25);
      text-align: center;
    }
    .tool-install-title { font-weight: 800; color: #21443d; }
    .tool-install-detail { color: var(--muted); font-size: 13px; font-weight: 700; }
    .tool-install-card.failed .tool-install-title { color: #b3261e; }
    .modal-backdrop {
      position: fixed;
      inset: 0;
      z-index: 10000;
      display: grid;
      place-items: center;
      padding: 18px;
      background: rgba(23, 32, 42, .46);
    }
    .modal-backdrop[hidden] { display: none; }
    .modal-card {
      max-width: min(92vw, 720px);
      max-height: min(88vh, 720px);
      overflow: auto;
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 14px;
      padding: 16px;
      box-shadow: 0 18px 50px rgba(0,0,0,0.25);
    }
    .modal-card h2 { margin-bottom: 6px; }
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
    .video-links {
      display: grid;
      grid-template-columns: 1fr;
      gap: 8px;
      margin-top: 8px;
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
      border: 1px solid #d9e6e0;
      border-radius: 12px;
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
      border-bottom: 1px solid #e7f0ec;
      text-align: left;
      white-space: nowrap;
    }
    .connection-table th {
      background: #f4faf7;
      color: #24483f;
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
      border-radius: 999px;
      background: #e8eee9;
    }
    .lag-bar-fill {
      display: block;
      height: 100%;
      min-width: 8px;
      border-radius: inherit;
    }
    .lag-good { background: #159947; }
    .lag-ok { background: #78b92e; }
    .lag-warn { background: #d99116; }
    .lag-bad { background: #c3422f; }
    .lag-value {
      min-width: 38px;
      font-variant-numeric: tabular-nums;
      font-weight: 800;
      color: #23443d;
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
      background: #e7edf1;
      color: #304554;
      font-size: 12px;
    }
    .wing-tab.active {
      background: var(--accent);
      color: #fff;
    }
    .wing-list {
      display: grid;
      gap: 7px;
      max-height: calc(100vh - 92px);
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
      border-radius: 8px;
      background: #f8fafb;
      color: var(--ink);
      text-align: left;
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
      border-radius: 7px;
      background: #e7edf1;
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
      border-radius: 8px;
      font-size: 12px;
    }
    .queue-item {
      display: grid;
      gap: 6px;
      padding: 8px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #f8fafb;
      background-position: center;
      background-size: cover;
    }
    .queue-item.has-thumb {
      background-color: #f8fafb;
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
      background: #fff0ed;
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
      color: #85939d;
      font-size: 22px;
      line-height: 1;
    }
    .heart-button.active {
      color: #d83b4c;
      background: #fde8ec;
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
      position: fixed;
      right: 18px;
      bottom: 18px;
      z-index: 30;
      width: min(420px, calc(100vw - 36px));
      min-height: 0;
      padding: 12px 14px;
      border: 1px solid #bdd7d0;
      border-left: 5px solid var(--accent);
      border-radius: 12px;
      background: rgba(255, 255, 255, .96);
      color: #21443d;
      font-weight: 800;
      font-size: 13px;
      line-height: 1.45;
      box-shadow: 0 14px 40px rgba(0, 0, 0, .20);
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
      color: #7f2a1c;
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
      border: 1px solid rgba(127, 42, 28, .24);
      background: rgba(255, 255, 255, .76);
      color: #7f2a1c;
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
      border: 2px solid #101820;
      border-radius: 8px;
      background: #ffffff;
      box-shadow: 0 14px 40px rgba(0, 0, 0, .22);
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
      color: #17202a;
      font-size: 14px;
      font-weight: 800;
    }
    .pairing-pin {
      display: block;
      margin: 4px 0 8px;
      color: #111827;
      font-size: 64px;
      line-height: 1;
      font-weight: 900;
      letter-spacing: 0;
      font-variant-numeric: tabular-nums;
    }
    .pairing-detail {
      margin: 0;
      color: #405564;
      font-size: 12px;
      line-height: 1.45;
    }
    .mobile-progress {
      display: none;
      gap: 7px;
      padding: 8px;
      border-radius: 8px;
      background: var(--soft);
      color: #21443d;
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
          "history"
          "sidebar"
          "quit";
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
      .preview.obs-preview {
        min-height: 0;
      }
      .wing-list {
        max-height: none;
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
      .preview.obs-preview {
        height: auto;
        min-height: 0;
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
    @media (max-width: 860px) {
      .mobile-progress.open {
        display: grid;
      }
    }
    @media (max-width: 720px) {
      body.pairing-active .toast {
        bottom: 230px;
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
              <div class="pill obs-status-pill"><strong>OBS</strong><span id="obsStatus">確認中</span><button type="button" class="pill-action" id="obsLatencyDetailButton">接続詳細</button></div>
              <div class="urlbox">
                <div>
                  <strong>OBS Latency</strong>
                </div>
                <div class="obs-latency-actions">
                  <select id="obsLatencyMode" aria-label="OBS latency mode">
                    <option value="hls-high">最高画質HLS（10s+）</option>
                    <option value="hls">高画質HLS（5s）</option>
                    <option value="rtsp-low">低遅延RTSP（3-4s）</option>
                    <option value="rtsp-ultra">超低遅延RTSP（1-2s）</option>
                    <option value="rtsp-realtime">リアルタイムRTSP（0.5s+）</option>
                  </select>
                </div>
              </div>
            </div>
          </div>
          <div class="controls">
            <label><span>最大辺</span><input name="maxDimension" type="number" min="64" max="8192" value="2048"></label>
            <label><span>形式</span><select name="format"><option value="jpeg">JPEG</option><option value="png">PNG</option></select></label>
            <label><span>JPEG品質</span><input name="quality" type="number" min="40" max="95" value="88"></label>
            <label><span>最大MB</span><input name="maxMB" type="number" min="1" max="120" value="30"></label>
          </div>
          <div class="upload-actions">
            <button id="uploadButton" type="submit" name="uploadAction" value="publish">変換して公開</button>
            <button id="queueUploadButton" type="submit" class="secondary" name="uploadAction" value="queue">動画変換へ</button>
          </div>
          <div class="toast" id="toast" role="status" aria-live="polite" aria-atomic="true">
            <div class="toast-message" id="toastMessage"></div>
            <div class="toast-actions" id="toastActions">
              <button type="button" class="toast-action" id="toastCopyButton">診断情報をコピー</button>
              <button type="button" class="toast-action toast-close" id="toastCloseButton" aria-label="通知を閉じる">×</button>
            </div>
          </div>
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
          <button class="wing-tab" id="queueTabButton" type="button" role="tab" aria-selected="false" data-wing-tab="queue">動画変換</button>
        </div>
        <div class="wing-list" id="historyList">
          <div class="empty">まだ履歴がありません</div>
        </div>
      </section>
    </div>

    <div class="quit">
      <button type="button" class="quit-button" id="quitButton" title="サーバーアプリ本体を終了します">アプリを終了</button>
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
      <div class="progress-track" aria-label="インストール進捗">
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
  <div class="modal-backdrop" id="obsConnectionsDialog" hidden>
    <section class="modal-card obs-connections-card" role="dialog" aria-modal="true" aria-labelledby="obsConnectionsTitle" aria-describedby="obsConnectionsDescription">
      <h2 id="obsConnectionsTitle">OBS接続詳細</h2>
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
      history: [],
      videoQueue: [],
      videoPlayerEnabled: false,
      musicModeEnabled: false,
      videoQuality: null,
      obs: null,
      pairing: null,
      currentID: "",
      obsPreviewID: "",
      obsPreviewURL: "",
      previewMode: "empty"
    };

    const toast = document.getElementById('toast');
    const toastMessage = document.getElementById('toastMessage');
    const toastCopyButton = document.getElementById('toastCopyButton');
    const toastCloseButton = document.getElementById('toastCloseButton');
    const toastTextDescriptor = Object.getOwnPropertyDescriptor(Node.prototype, 'textContent');
    let toastTimer = null;
    let toastInternalUpdate = false;
    let lastToastErrorReport = '';
    function writeToastText(text) {
      if (!toastMessage && !toast) return;
      toastInternalUpdate = true;
      const target = toastMessage || toast;
      if (toastTextDescriptor && toastTextDescriptor.set) {
        toastTextDescriptor.set.call(target, text);
      } else {
        target.innerText = text;
      }
      toastInternalUpdate = false;
    }
    function showToast(message, options = {}) {
      if (!toast) return;
      const text = String(message || '');
      const visible = text.trim() !== '';
      const isError = !!options.error;
      clearTimeout(toastTimer);
      writeToastText(text);
      if (!visible) {
        toast.classList.remove('active', 'error');
        delete toast.dataset.error;
        lastToastErrorReport = '';
        return;
      }
      toast.classList.add('active');
      toast.classList.toggle('error', isError);
      if (isError) {
        toast.dataset.error = '1';
        lastToastErrorReport = buildErrorReport(text, options.context || null);
      } else {
        delete toast.dataset.error;
        lastToastErrorReport = '';
      }
      const timeout = options.timeout === undefined ? (isError ? 0 : 4200) : options.timeout;
      if (timeout > 0) {
        toastTimer = setTimeout(() => showToast('', { timeout: 0 }), timeout);
      }
    }
    function hideToast() {
      showToast('', { timeout: 0 });
    }
    function isToastErrorMessage(value) {
      const text = String(value || '');
      return /失敗|Failed|Error|error|NetworkError|使えません|ありません|確認してください/.test(text);
    }
    if (toast && toastTextDescriptor && toastTextDescriptor.set && toastTextDescriptor.get) {
      Object.defineProperty(toast, 'textContent', {
        get() { return toastTextDescriptor.get.call(toastMessage || this); },
        set(value) {
          if (toastInternalUpdate) {
            if (toastMessage) {
              toastTextDescriptor.set.call(toastMessage, value);
            } else {
              toastTextDescriptor.set.call(this, value);
            }
            return;
          }
          showToast(value, { error: this.dataset.error === '1' || isToastErrorMessage(value) });
        }
      });
    }
    function buildErrorReport(message, context) {
      const obs = state.obs || {};
      const latency = obs.latency || {};
      const current = state.current || (Array.isArray(state.history) ? state.history.find((item) => item && item.id === state.currentID) : null) || {};
      const report = {
        app: 'ImagePadServer',
        generatedAt: new Date().toISOString(),
        pageURL: window.location.href,
        userAgent: navigator.userAgent,
        visibility: document.visibilityState,
        online: navigator.onLine,
        uploadMode,
        wingMode,
        error: {
          message: message,
          context: context || null
        },
        currentMedia: {
          id: state.currentID || '',
          name: current.name || '',
          kind: current.kind || '',
          mime: current.mime || '',
          url: current.url || ''
        },
        urls: {
          imageURL: state.imageURL || '',
          videoURL: state.videoURL || '',
          hlsURL: state.hlsURL || '',
          shareURL: state.shareURL || '',
          shareURLLabel: state.shareURLLabel || '',
          phoneURL: state.phoneURL || '',
          localImageURL: state.localImageURL || '',
          previewImageURL: state.previewImageURL || '',
          publicImageURL: state.publicImageURL || ''
        },
        preview: {
          mode: state.previewMode || '',
          obsPreviewID: state.obsPreviewID || ''
        },
        ingest: state.ingest || null,
        video: state.video || null,
        videoQuality: state.videoQuality || null,
        toolInstall: state.toolInstall || null,
        pairing: state.pairing || null,
        obs: {
          available: !!obs.available,
          connected: !!obs.connected,
          publishing: !!obs.publishing,
          serverAddress: obs.serverAddress || '',
          previewURL: obs.previewURL || '',
          publicHLSURL: obs.publicHLSURL || '',
          rtsptURL: obs.rtsptURL || '',
          latency: {
            mode: latency.mode || '',
            label: latency.label || '',
            target: latency.target || '',
            message: latency.message || ''
          },
          connections: Array.isArray(obs.connections) ? obs.connections : []
        }
      };
      return JSON.stringify(report, null, 2);
    }
    async function copyToastErrorReport() {
      if (!lastToastErrorReport) return;
      const original = toastCopyButton ? toastCopyButton.textContent : '';
      try {
        await copyText(lastToastErrorReport, toastMessage || toast);
        if (toastCopyButton) {
          toastCopyButton.textContent = 'コピーしました';
          setTimeout(() => { toastCopyButton.textContent = original || '診断情報をコピー'; }, 1500);
        }
      } catch (error) {
        if (toastCopyButton) {
          toastCopyButton.textContent = 'コピー失敗';
          setTimeout(() => { toastCopyButton.textContent = original || '診断情報をコピー'; }, 1500);
        }
      }
    }
    if (toastCopyButton) {
      toastCopyButton.addEventListener('click', copyToastErrorReport);
    }
    if (toastCloseButton) {
      toastCloseButton.addEventListener('click', hideToast);
    }
    const uploadForm = document.getElementById('uploadForm');
    const uploadButton = document.getElementById('uploadButton');
    const queueUploadButton = document.getElementById('queueUploadButton');
    const modeTabs = document.querySelector('.mode-tabs');
    const imageInput = document.getElementById('imageInput');
    const fileDropZone = document.getElementById('fileDropZone');
    const dropHint = document.getElementById('dropHint');
    const dropFileName = document.getElementById('dropFileName');
    const dragDropOverlay = document.getElementById('dragDropOverlay');
    const dragDropOverlayHint = document.getElementById('dragDropOverlayHint');
    const imageURLInput = document.getElementById('imageURLInput');
    const pasteURLButton = document.getElementById('pasteURLButton');
    const fileModeButton = document.getElementById('fileModeButton');
    const linkModeButton = document.getElementById('linkModeButton');
    const obsModeButton = document.getElementById('obsModeButton');
    const fileUploadPanel = document.getElementById('fileUploadPanel');
    const linkUploadPanel = document.getElementById('linkUploadPanel');
    const obsUploadPanel = document.getElementById('obsUploadPanel');
    const uploadControls = uploadForm.querySelector('.controls');
    const preview = document.getElementById('preview');
    const historyList = document.getElementById('historyList');
    const wingTabButtons = Array.from(document.querySelectorAll('[data-wing-tab]'));
    const mobileProgress = document.getElementById('mobileProgress');
    const mobileProgressText = document.getElementById('mobileProgressText');
    const mobileProgressFill = document.getElementById('mobileProgressFill');
    const videoPlayerToggle = document.getElementById('videoPlayerToggle');
    const videoPlayerText = document.getElementById('videoPlayerText');
    const musicModeRow = document.getElementById('musicModeRow');
    const musicModeToggle = document.getElementById('musicModeToggle');
    const musicModeText = document.getElementById('musicModeText');
    const updateText = document.getElementById('updateText');
    const qualityMode = document.getElementById('qualityMode');
    const qualityStatus = document.getElementById('qualityStatus');
    const qualityRow = document.querySelector('.quality-row');
    const networkCheckButton = document.getElementById('networkCheckButton');
    const clearButton = document.getElementById('clearButton');
    const obsKeyRotateButton = document.getElementById('obsKeyRotateButton');
    const obsLatencyMode = document.getElementById('obsLatencyMode');
    const obsLatencyDetailButton = document.getElementById('obsLatencyDetailButton');
    const rtspRiskDialog = document.getElementById('rtspRiskDialog');
    const rtspRiskCancel = document.getElementById('rtspRiskCancel');
    const rtspRiskConfirm = document.getElementById('rtspRiskConfirm');
    const obsKeyRiskDialog = document.getElementById('obsKeyRiskDialog');
    const obsKeyRiskCancel = document.getElementById('obsKeyRiskCancel');
    const obsKeyRiskConfirm = document.getElementById('obsKeyRiskConfirm');
    const obsKeyEditInput = document.getElementById('obsKeyEditInput');
    const obsConnectionsDialog = document.getElementById('obsConnectionsDialog');
    const obsConnectionsClose = document.getElementById('obsConnectionsClose');
    const obsConnectionsTableBody = document.getElementById('obsConnectionsTableBody');
    const pairingPanel = document.getElementById('pairingPanel');
    const pairingPin = document.getElementById('pairingPin');
    const pairingDetail = document.getElementById('pairingDetail');
    let uploadMode = 'file';
    let videoPlayerPending = false;
    let musicModePending = false;
    let refreshTimer = 0;
    let refreshInFlight = false;
    let refreshPromise = null;
    let refreshAgain = false;
    let lastAppliedStateSeq = 0;
    let localChangeChannel = null;
    let confirmedOBSLatencyMode = 'hls';
    let wingMode = 'history';
    let pendingOBSAutoCopy = false;
    let lastAutoCopiedOBSURL = '';
    let obsPreviewHLS = null;
    const imageAccept = 'image/png,image/jpeg,image/gif,image/webp,image/bmp,image/tiff,image/svg+xml,image/x-sony-arw,image/x-canon-crw,image/x-canon-cr2,image/x-canon-cr3,image/x-panasonic-rw2,image/x-olympus-orf,image/x-fuji-raf,image/x-nikon-nef,image/x-nikon-nrw,image/x-sigma-x3f,image/x-adobe-dng,.jpg,.jpeg,.png,.gif,.webp,.bmp,.tif,.tiff,.svg,.arw,.srf,.sr2,.crw,.cr2,.cr3,.rw2,.raw,.orf,.raf,.nef,.nrw,.x3f,.dng';
    const mediaAccept = imageAccept + ',video/*,video/mp4,video/quicktime,video/webm,video/x-matroska,.mp4,.mov,.m4v,.webm,.mkv,.avi';
    const rawExtensions = new Set(['.arw', '.srf', '.sr2', '.crw', '.cr2', '.cr3', '.rw2', '.raw', '.orf', '.raf', '.nef', '.nrw', '.x3f', '.dng']);
    let ffmpegPending = false;
    let ffmpegReady = false;
    let ffmpegPromise = null;
    let obsKeyVisible = false;

    function syncFailureMessage(error) {
      const text = String((error && error.message) || error || '').trim();
      if (text.includes('admin access requires')) {
        return '状態の同期に失敗しました。管理画面は http://127.0.0.1:8080/ から開いてください（Tunnel の公開 URL では使えません）';
      }
      if (text.includes('Failed to fetch') || text.includes('NetworkError') || text.includes('Load failed')) {
        return '状態の同期に失敗しました。ImagePadServer が起動しているか、このタブのアドレスが正しいか確認してください';
      }
      if (text) {
        return '状態の同期に失敗しました: ' + text.slice(0, 140);
      }
      return '状態の同期に失敗しました';
    }

    async function refreshState() {
      if (refreshInFlight) {
        refreshAgain = true;
        return refreshPromise;
      }
      refreshInFlight = true;
      refreshPromise = runRefreshState();
      try {
        return await refreshPromise;
      } finally {
        refreshPromise = null;
      }
    }

    async function runRefreshState() {
      const seq = ++lastAppliedStateSeq;
      try {
        const res = await fetch('/api/state', { cache: 'no-store' });
        const body = await res.text();
        if (!res.ok) throw new Error(body || ('HTTP ' + res.status));
        const data = JSON.parse(body);
        if (seq === lastAppliedStateSeq) {
          applyState(data);
          if (toast && toast.dataset.error === '1') {
            hideToast();
          }
        }
      } catch (error) {
        if (toast && !document.hidden) {
          showToast(syncFailureMessage(error), { error: true });
        }
      } finally {
        refreshInFlight = false;
        if (refreshAgain) {
          refreshAgain = false;
          scheduleRefresh(100);
        }
      }
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
      state.current = data.current || null;
      state.history = data.history || [];
      state.videoQueue = data.videoQueue || [];
      state.videoQuality = data.videoQuality;
      state.ingest = data.ingest || null;
      state.video = data.video || null;
      state.obs = data.obs || null;
      state.pairing = data.pairing || null;
      state.toolInstall = data.toolInstall || null;
      state.videoPlayerEnabled = !!(data.videoPlayer && data.videoPlayer.enabled);
      state.musicModeEnabled = !!(data.videoPlayer && data.videoPlayer.musicModeEnabled);
      document.getElementById('phoneURL').textContent = data.phoneURL;
      document.getElementById('phoneURLMobile').textContent = data.phoneURL;
      renderShareURL(state);
      document.getElementById('videoStatus').textContent = videoText(data.video);
      updateMobileProgress(data);
      updateToolInstall(data.toolInstall);
      applyQuality(data.videoQuality);
      applyVideoPlayer(data.videoPlayer);
      applyOBS(data.obs);
      applyPairing(data.pairing);
      applyOBSProtection();
      document.getElementById('upnpText').textContent = publicText(data.tunnel, data.upnp);
      document.getElementById('hasImage').textContent = currentText(data.current);
      const nextCurrentID = data.current ? data.current.id : "";
      renderPreview(data, nextCurrentID);
      renderHistory(state.history, nextCurrentID);
      state.currentID = nextCurrentID;
      maybeAutoCopyOBSURL(data);

      scheduleRefresh((data.ingest && data.ingest.active) || (data.video && data.video.active) || (data.obs && data.obs.connected) ? 750 : 2000);
    }

    function renderShareURL(data) {
      const view = shareURLForCurrentMode(data);
      document.getElementById('shareURL').textContent = shareURLDisplayText(view);
      document.getElementById('shareURLLabel').textContent = view.shareURLLabel || 'URL';
    }

    function shareURLForCurrentMode(data) {
      const label = data && data.shareURLLabel ? String(data.shareURLLabel) : '';
      if (uploadMode !== 'obs' && label === 'RTSP TCP URL') {
        return { shareURL: '', shareURLLabel: 'URL' };
      }
      return data || {};
    }

    function shareURLDisplayText(data) {
      if (data && data.shareURL) return data.shareURL;
      const label = data && data.shareURLLabel ? String(data.shareURLLabel) : '';
      const message = data && data.obs && data.obs.message ? String(data.obs.message) : '';
      if (label === 'RTSP TCP URL' && message) {
        return '公開URLは未取得です: ' + message;
      }
      return '公開URLは未取得です';
    }

    function resetOBSPreview() {
      destroyOBSPreviewHLS();
      if (state.previewMode === 'obs') {
        const video = preview.querySelector('video');
        if (video) {
          video.pause();
          video.removeAttribute('src');
          video.load();
        }
        preview.innerHTML = '<div class="empty">OBS preview restarting...</div>';
      }
      state.previewMode = 'obs-restarting';
      state.obsPreviewID = "";
      state.obsPreviewURL = "";
    }

    function destroyOBSPreviewHLS() {
      if (!obsPreviewHLS) return;
      try {
        obsPreviewHLS.destroy();
      } catch (error) {
      }
      obsPreviewHLS = null;
    }

    function attachHLSPreview(video, src) {
      destroyOBSPreviewHLS();
      if (video.canPlayType('application/vnd.apple.mpegurl')) {
        video.src = src;
        return true;
      }
      if (window.Hls && window.Hls.isSupported()) {
        obsPreviewHLS = new window.Hls({
          lowLatencyMode: true,
          backBufferLength: 30
        });
        obsPreviewHLS.loadSource(src);
        obsPreviewHLS.attachMedia(video);
        return true;
      }
      return false;
    }

    function applyPairing(pairing) {
      const active = !!(pairing && pairing.active && pairing.pin);
      document.body.classList.toggle('pairing-active', active);
      if (!pairingPanel || !pairingPin || !pairingDetail) return;
      if (!active) {
        pairingPanel.classList.remove('active');
        return;
      }
      pairingPin.textContent = pairing.pin;
      const name = pairing.deviceName || pairing.clientName || 'BrowserRelayStreamer';
      pairingDetail.textContent = name + ' is requesting access. Enter this code on that computer.';
      pairingPanel.classList.add('active');
    }

    function renderPreview(data, nextCurrentID) {
      if (uploadMode === 'obs' && data.obs && data.obs.connected && data.obs.previewURL) {
        const obsID = data.obs.mediaID || nextCurrentID;
        if (state.previewMode !== 'obs' || obsID !== state.obsPreviewID || data.obs.previewURL !== state.obsPreviewURL) {
          preview.classList.add('obs-preview');
          preview.innerHTML = '';
          const video = document.createElement('video');
          video.controls = true;
          video.autoplay = true;
          video.muted = true;
          video.playsInline = true;
          if (attachHLSPreview(video, data.obs.previewURL)) {
            preview.appendChild(video);
          } else {
            const link = document.createElement('a');
            link.href = data.obs.previewURL;
            link.textContent = 'HLSプレビューを開く';
            link.target = '_blank';
            link.rel = 'noreferrer';
            preview.innerHTML = '<div class="empty">このブラウザではHLSプレビューを直接再生できません。</div>';
            preview.appendChild(link);
          }
          state.previewMode = 'obs';
          state.obsPreviewID = obsID;
          state.obsPreviewURL = data.obs.previewURL;
        }
        state.currentID = obsID;
        return;
      }
      destroyOBSPreviewHLS();
      preview.classList.remove('obs-preview');
      state.obsPreviewID = "";
      state.obsPreviewURL = "";
      if (!data.current) {
        if (state.previewMode !== 'empty') {
          preview.innerHTML = '<div class="empty">まだ画像が選択されていません</div>';
          state.previewMode = 'empty';
        }
        return;
      }
      const ingestLabel = data.ingest && data.ingest.active ? ingestPhaseLabel(data.ingest.phase) : '';
      if (ingestLabel) {
        // Rebuild only when the phase changes so the indeterminate sweep
        // animation isn't restarted on every poll.
        const mode = 'ingest:' + data.ingest.phase;
        if (state.previewMode !== mode) {
          const title = data.ingest.title ? escapeHTML(data.ingest.title) : '';
          preview.innerHTML =
            '<div class="progress-preview">' +
              '<div>' + escapeHTML(ingestLabel) + '</div>' +
              '<div class="progress-track" aria-label="処理状況">' +
                '<div class="progress-fill indeterminate"></div>' +
              '</div>' +
              (title ? '<div class="progress-detail">' + title + '</div>' : '') +
            '</div>';
          state.previewMode = mode;
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

    function setWingMode(mode) {
      wingMode = mode;
      for (const button of wingTabButtons) {
        const active = button.dataset.wingTab === mode;
        button.classList.toggle('active', active);
        button.setAttribute('aria-selected', String(active));
      }
      renderHistory(state.history, state.currentID);
    }

    function renderHistory(items, currentID) {
      if (!historyList) return;
      if (wingMode === 'queue') {
        renderVideoQueue(state.videoQueue);
        return;
      }
      const visibleItems = wingMode === 'favorites'
        ? (items || []).filter((item) => item.favorite).slice().reverse()
        : (items || []);
      if (!visibleItems.length) {
        historyList.innerHTML = '<div class="empty">' + (wingMode === 'favorites' ? 'まだお気に入りがありません' : 'まだ履歴がありません') + '</div>';
        return;
      }
      historyList.innerHTML = '';
      for (const item of visibleItems) {
        const row = document.createElement('div');
        row.className = 'history-item' + (item.id === currentID ? ' current' : '');
        row.title = item.title || '';

        const thumb = document.createElement('div');
        thumb.className = 'history-thumb';
        if (item.kind === 'video' && !item.hasThumbnail) {
          thumb.textContent = 'VIDEO';
        } else {
          const img = document.createElement('img');
          img.src = item.thumbnailURL;
          img.alt = '';
          thumb.appendChild(img);
        }

        const meta = document.createElement('div');
        meta.className = 'history-meta';
        const title = document.createElement('div');
        title.className = 'history-title';
        title.textContent = item.title || 'untitled';
        const detail = document.createElement('div');
        detail.className = 'history-detail';
        detail.textContent = historyDetail(item);
        meta.appendChild(title);
        meta.appendChild(detail);

        const actions = document.createElement('div');
        actions.className = 'history-actions';

        const publish = document.createElement('button');
        publish.type = 'button';
        publish.className = 'history-action-button secondary';
        publish.dataset.historyPublish = item.id;
        publish.textContent = item.id === currentID ? '公開中' : '公開';
        publish.title = item.id === currentID ? '現在公開中です' : 'この項目を公開';
        publish.setAttribute('aria-label', publish.title);
        publish.disabled = item.id === currentID;

        const queue = document.createElement('button');
        queue.type = 'button';
        queue.className = 'history-action-button secondary';
        queue.dataset.historyQueue = item.id;
        queue.textContent = '変換';
        queue.title = '動画変換に追加';
        queue.setAttribute('aria-label', queue.title);
        queue.hidden = !state.videoPlayerEnabled;

        const heart = document.createElement('button');
        heart.type = 'button';
        heart.className = 'heart-button' + (item.favorite ? ' active' : '');
        heart.dataset.historyFavorite = item.id;
        heart.dataset.favorite = item.favorite ? '1' : '0';
        heart.innerHTML = item.favorite ? '&#9829;' : '&#9825;';
        heart.title = item.favorite ? 'お気に入りから削除' : 'お気に入り';
        heart.setAttribute('aria-label', heart.title);

        actions.appendChild(publish);
        actions.appendChild(queue);
        actions.appendChild(heart);

        row.appendChild(thumb);
        row.appendChild(meta);
        row.appendChild(actions);
        historyList.appendChild(row);
      }
    }

    function renderVideoQueue(items) {
      if (!historyList) return;
      if (!items || !items.length) {
        historyList.innerHTML = '<div class="empty">動画変換は空です</div>';
        return;
      }
      historyList.innerHTML = '';
      const runningItems = items.filter((item) => item.status === 'running');
      const otherItems = items.filter((item) => item.status !== 'running');
      for (const item of runningItems) {
        historyList.appendChild(queueRow(item, true));
      }
      if (runningItems.length && otherItems.length) {
        const divider = document.createElement('div');
        divider.className = 'queue-divider';
        historyList.appendChild(divider);
      }
      for (const item of otherItems) {
        historyList.appendChild(queueRow(item, false));
      }
    }

    function queueRow(item, featured) {
      const row = document.createElement('div');
      row.className = 'queue-item ' + (item.status || '') + (featured ? ' featured' : '');
      if (item.thumbnailURL) {
        row.classList.add('has-thumb');
        row.style.backgroundImage = "linear-gradient(rgba(248,250,251,.90), rgba(248,250,251,.90)), url('" + item.thumbnailURL + "')";
      }

      const top = document.createElement('div');
      top.className = 'queue-top';
      const title = document.createElement('div');
      title.className = 'queue-title';
      title.textContent = item.title || '変換ジョブ';
      const status = document.createElement('div');
      status.className = 'queue-status';
      status.textContent = queueStatusText(item.status);
      top.appendChild(title);
      top.appendChild(status);

      const detail = document.createElement('div');
      detail.className = 'history-detail';
      detail.textContent = queueDetail(item);

      row.appendChild(top);
      if (item.status === 'running') {
        const track = document.createElement('div');
        track.className = 'progress-track';
        const fill = document.createElement('div');
        fill.className = 'progress-fill';
        fill.style.width = Math.max(6, Math.min(100, Number(item.progressPercent || 0))) + '%';
        track.appendChild(fill);
        row.appendChild(track);
      }
      row.appendChild(detail);
      return row;
    }

    function queueStatusText(status) {
      switch (status) {
        case 'pending': return '待機中';
        case 'running': return '変換中';
        case 'done': return '完了';
        case 'error': return '失敗';
        case 'canceled': return '中止';
        default: return status || '不明';
      }
    }

    function queueDetail(item) {
      const parts = [];
      parts.push(item.kind === 'video' ? '動画' : '画像');
      if (item.quality) parts.push(item.quality + 'p');
      if (item.progressText) parts.push(item.progressText);
      if (item.message && item.message !== item.progressText) parts.push(item.message);
      return parts.join(' / ');
    }

    function historyDetail(item) {
      const parts = [];
      if (item.kind === 'video') {
        parts.push('動画');
      } else if (item.width && item.height) {
        parts.push(item.width + ' x ' + item.height);
      } else {
        parts.push('画像');
      }
      if (item.sizeBytes) {
        const mb = item.sizeBytes / 1024 / 1024;
        parts.push((mb >= 10 ? Math.round(mb) : mb.toFixed(1)) + ' MB');
      }
      if (item.persistent) {
        parts.push('保存済み');
      }
      return parts.join(' / ');
    }

    function ingestPhaseLabel(phase) {
      return { downloading: 'ダウンロード中…', analyzing: '解析中…', processing: '処理中…' }[phase] || '';
    }

    function updateToolInstall(info) {
      const overlay = document.getElementById('toolInstallOverlay');
      const card = document.getElementById('toolInstallCard');
      const title = document.getElementById('toolInstallTitle');
      const fill = document.getElementById('toolInstallFill');
      const detail = document.getElementById('toolInstallDetail');
      if (!overlay) return;
      const active = !!(info && info.active);
      const failed = !!(info && info.failed);
      if (!active && !failed) {
        overlay.classList.remove('open');
        overlay.setAttribute('aria-hidden', 'true');
        card.classList.remove('failed');
        fill.classList.remove('indeterminate');
        return;
      }
      overlay.classList.add('open');
      overlay.setAttribute('aria-hidden', 'false');
      const toolLabel = { ffmpeg: 'FFmpeg', ffprobe: 'ffprobe', 'yt-dlp': 'yt-dlp' }[info.tool] || 'ツール';
      if (failed) {
        card.classList.add('failed');
        title.textContent = 'ツールの準備に失敗しました';
        detail.textContent = info.message || '時間をおいて再度お試しください。';
        fill.classList.remove('indeterminate');
        fill.style.width = '100%';
        return;
      }
      card.classList.remove('failed');
      const phaseLabel = { download: 'ダウンロード中', extract: '展開中', validate: '検証中' }[info.phase] || '準備中';
      title.textContent = toolLabel + ' を' + phaseLabel + '…';
      const pct = Math.max(0, Math.min(100, Number(info.percent || 0)));
      if (info.phase === 'download' && pct > 0) {
        fill.classList.remove('indeterminate');
        fill.style.width = Math.max(6, pct) + '%';
        detail.textContent = pct + '%';
      } else {
        fill.classList.add('indeterminate');
        detail.textContent = info.attempt > 1 ? ('再試行 ' + info.attempt + '回目') : '';
      }
    }

    function updateMobileProgress(data) {
      const ingest = data && data.ingest;
      const label = ingest && ingest.active ? ingestPhaseLabel(ingest.phase) : '';
      if (label) {
        // Pre-render phases: show the phase with an indeterminate (no %) bar.
        mobileProgress.classList.add('open');
        mobileProgressText.textContent = label + (ingest.title ? ' — ' + ingest.title : '');
        mobileProgressFill.classList.add('indeterminate');
        mobileProgressFill.style.width = '';
        return;
      }
      const video = data && data.video;
      if (!video || !video.active) {
        mobileProgress.classList.remove('open');
        mobileProgressFill.classList.remove('indeterminate');
        return;
      }
      const percent = Math.max(0, Math.min(99, Number(video.progressPercent || 0)));
      mobileProgress.classList.add('open');
      mobileProgressFill.classList.remove('indeterminate');
      mobileProgressText.textContent = video.progressText || video.message || '変換中';
      mobileProgressFill.style.width = Math.max(6, percent) + '%';
    }

    function scrollProgressIntoView() {
      if (!window.matchMedia('(max-width: 860px)').matches) return;
      window.setTimeout(() => {
        const target = mobileProgress.classList.contains('open') ? mobileProgress : preview;
        target.scrollIntoView({ behavior: 'smooth', block: 'center' });
      }, 120);
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
        musicModeRow.hidden = true;
        musicModeToggle.checked = false;
        return;
      }
      videoPlayerToggle.checked = !!data.enabled;
      videoPlayerToggle.disabled = videoPlayerPending;
      videoPlayerText.textContent = data.enabled ? '有効 / 自動コピーはHLS優先' : '無効 / 自動コピーは画像URL';
      musicModeRow.hidden = !data.enabled;
      musicModeToggle.checked = !!data.musicModeEnabled;
      musicModeToggle.disabled = musicModePending || !data.enabled;
      musicModeText.textContent = data.musicModeEnabled ? '有効 / URLは音声のみ取得' : '無効 / URLは動画として取得';
      imageInput.accept = data.enabled ? '' : imageAccept;
      dropHint.textContent = data.enabled ? 'Drop image, audio, or video files here' : 'Drop image or RAW files here';
      dragDropOverlayHint.textContent = data.enabled ? '画像、RAW、音声、動画ファイルを選択します' : '画像またはRAWファイルを選択します';
      fileModeButton.textContent = data.enabled ? '画像/音声/動画' : '画像';
      const uploadHeading = document.querySelector('.content section:first-child h2');
      if (uploadHeading) {
        uploadHeading.textContent = data.enabled ? 'メディアアップロード' : '画像アップロード';
      }
      imageURLInput.placeholder = data.enabled ? 'https://example.com/image_or_video.webp' : 'https://example.com/image.webp';
      queueUploadButton.hidden = !data.enabled || uploadMode === 'obs';
      obsModeButton.hidden = !data.enabled;
      if (modeTabs) {
        modeTabs.classList.toggle('has-obs', !!data.enabled);
      }
      if (!data.enabled && uploadMode === 'obs') {
        setUploadMode('file');
      }
    }

    function applyOBS(data) {
      const server = document.getElementById('obsServerAddress');
      const key = document.getElementById('obsStreamKey');
      const status = document.getElementById('obsStatus');
      if (!server || !key || !status) return;
      if (!data) {
        server.textContent = 'RTMP receiver is unavailable';
        key.textContent = '-';
        status.textContent = 'unavailable';
        renderOBSConnections([]);
        return;
      }
      server.textContent = data.serverAddress || 'RTMP receiver is stopped';
      key.textContent = obsKeyVisible ? (data.streamKey || '-') : maskSecret(data.streamKey);
      const latency = data.latency || {};
      confirmedOBSLatencyMode = latency.mode || 'hls';
      if (obsLatencyMode) obsLatencyMode.value = confirmedOBSLatencyMode;
      renderOBSConnections(data.connections || []);
      if (data.connected && data.publishing) {
        status.textContent = 'publishing / HLS event';
      } else if (data.connected) {
        status.textContent = 'connected / preview only';
      } else if (data.listening) {
        status.textContent = 'waiting';
      } else {
        status.textContent = data.message || 'stopped';
      }
    }

    function renderOBSConnections(rows) {
      if (!obsConnectionsTableBody) return;
      const list = Array.isArray(rows) ? rows : [];
      if (!list.length) {
        obsConnectionsTableBody.innerHTML = '<tr><td colspan="6" class="connection-empty">接続なし</td></tr>';
        return;
      }
      obsConnectionsTableBody.innerHTML = list.map((row) => {
        const lag = Number(row.lagSeconds || 0);
        const level = normalizeLagLevel(row.lagLevel, lag);
        const width = Math.max(8, Math.min(100, (lag / 12) * 100));
        const note = row.note ? ' title="' + escapeHTML(row.note) + '"' : '';
        return '<tr' + note + '>' +
          '<td>' + escapeHTML(row.ip || '-') + '</td>' +
          '<td>' + escapeHTML(row.protocol || '-') + '</td>' +
          '<td>' + escapeHTML(row.device || '-') + '</td>' +
          '<td>' + escapeHTML(row.state || '-') + '</td>' +
          '<td>' + escapeHTML(row.quality || '-') + '</td>' +
          '<td><div class="lag-cell"><div class="lag-bar" aria-hidden="true"><span class="lag-bar-fill lag-' + level + '" style="width:' + width.toFixed(0) + '%"></span></div><span class="lag-value">' + formatLagSeconds(lag) + '</span></div></td>' +
          '</tr>';
      }).join('');
    }

    function normalizeLagLevel(level, seconds) {
      const value = String(level || '').toLowerCase();
      if (['good', 'ok', 'warn', 'bad'].includes(value)) return value;
      if (seconds <= 1.2) return 'good';
      if (seconds <= 2.5) return 'ok';
      if (seconds <= 5) return 'warn';
      return 'bad';
    }

    function formatLagSeconds(seconds) {
      if (!Number.isFinite(seconds) || seconds <= 0) return '-';
      if (seconds < 1) return seconds.toFixed(1) + 's';
      if (Math.abs(seconds - Math.round(seconds)) < 0.05) return String(Math.round(seconds)) + 's';
      return seconds.toFixed(1).replace(/\.0$/, '') + 's';
    }

    function maskSecret(value) {
      return value ? '*****' : '-';
    }

    function applyOBSProtection() {
      const protectedMode = uploadMode === 'obs';
      document.body.classList.toggle('obs-protect', protectedMode);
      const protectedText = '配信保護中';
      document.getElementById('phoneURL').textContent = protectedMode ? protectedText : (state.phoneURL || '');
      document.getElementById('phoneURLMobile').textContent = protectedMode ? protectedText : (state.phoneURL || '');
      if (clearButton) {
        clearButton.textContent = protectedMode ? '配信終了' : '画像クリア';
        clearButton.title = protectedMode ? 'OBS配信を終了してVOD化し、次の待ち受けを開始' : '';
      }
      if (uploadButton) {
        uploadButton.textContent = protectedMode ? '配信開始' : (uploadMode === 'link' ? 'リンクから変換して公開' : '変換して公開');
      }
      if (qualityRow) {
        qualityRow.hidden = protectedMode;
      }
    }

    function hasDroppedFiles(event) {
      return event.dataTransfer && Array.from(event.dataTransfer.types || []).includes('Files');
    }

    function showGlobalDropOverlay() {
      document.body.classList.add('drag-drop-active');
      dragDropOverlay.setAttribute('aria-hidden', 'false');
    }

    function hideGlobalDropOverlay() {
      document.body.classList.remove('drag-drop-active');
      dragDropOverlay.setAttribute('aria-hidden', 'true');
      fileDropZone.classList.remove('dragover');
    }

    function leavingWindow(event) {
      return event.clientX <= 0 || event.clientY <= 0 || event.clientX >= window.innerWidth || event.clientY >= window.innerHeight;
    }

    function setSelectedFile(file) {
      if (!file) return false;
      if (typeof DataTransfer === 'undefined') {
        toast.textContent = 'This browser cannot accept dropped files here. Use the file picker.';
        return false;
      }
      const transfer = new DataTransfer();
      transfer.items.add(file);
      imageInput.files = transfer.files;
      imageInput.dispatchEvent(new Event('change', { bubbles: true }));
      return true;
    }

    function updateSelectedFileName() {
      const file = imageInput.files && imageInput.files[0];
      dropFileName.textContent = file ? file.name : 'No file selected';
      dropFileName.title = file ? file.name : '';
    }

    function handleFileDrop(event) {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      event.stopPropagation();
      hideGlobalDropOverlay();
      const file = event.dataTransfer.files && event.dataTransfer.files[0];
      if (!file) return;
      if (uploadMode !== 'file') {
        setUploadMode('file');
      }
      if (setSelectedFile(file)) {
        const extra = event.dataTransfer.files.length > 1 ? ' (first file only)' : '';
        toast.textContent = 'Selected ' + file.name + extra;
      }
    }

    fileDropZone.addEventListener('dragenter', (event) => {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      showGlobalDropOverlay();
      fileDropZone.classList.add('dragover');
    });
    fileDropZone.addEventListener('dragover', (event) => {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = 'copy';
      showGlobalDropOverlay();
      fileDropZone.classList.add('dragover');
    });
    fileDropZone.addEventListener('dragleave', (event) => {
      if (!fileDropZone.contains(event.relatedTarget)) {
        fileDropZone.classList.remove('dragover');
      }
    });
    fileDropZone.addEventListener('drop', handleFileDrop);
    uploadForm.addEventListener('drop', handleFileDrop);
    window.addEventListener('dragenter', (event) => {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      showGlobalDropOverlay();
    });
    window.addEventListener('dragover', (event) => {
      if (!hasDroppedFiles(event)) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = 'copy';
      showGlobalDropOverlay();
    });
    window.addEventListener('dragleave', (event) => {
      if (leavingWindow(event)) {
        hideGlobalDropOverlay();
      }
    });
    window.addEventListener('drop', (event) => {
      if (hasDroppedFiles(event)) {
        handleFileDrop(event);
      } else {
        hideGlobalDropOverlay();
      }
    });
    window.addEventListener('dragend', hideGlobalDropOverlay);
    window.addEventListener('blur', hideGlobalDropOverlay);
    imageInput.addEventListener('change', updateSelectedFileName);
    updateSelectedFileName();

    uploadForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      if (uploadMode === 'obs') {
        uploadButton.disabled = true;
        toast.textContent = 'OBS配信を開始中...';
        try {
          const res = await fetch('/api/obs/start', { method: 'POST' });
          if (!res.ok) throw new Error(await res.text());
          const data = await res.json();
          pendingOBSAutoCopy = true;
          applyState(data);
          setUploadMode('obs');
          announceLocalChange();
          toast.textContent = data.obs && data.obs.connected ? 'OBS配信を公開しました' : 'OBS配信開始を予約しました';
        } catch (error) {
          toast.textContent = error.message || 'OBS配信開始に失敗しました';
        } finally {
          uploadButton.disabled = false;
        }
        return;
      }
      const action = event.submitter && event.submitter.value === 'queue' ? 'queue' : 'publish';
      if (uploadMode === 'file' && selectedRAWFile() && !await ensureFFmpegForRAWSelection()) {
        return;
      }
      uploadButton.disabled = true;
      queueUploadButton.disabled = true;
      toast.textContent = action === 'queue' ? '動画変換に追加中...' : 'アップロード中...';
      try {
        const res = uploadMode === 'link' ? await uploadFromLink(action) : await uploadFromFile(action);
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        announceLocalChange();
        scrollProgressIntoView();
        if (action === 'queue') {
          setWingMode('queue');
          toast.textContent = '動画変換に追加しました';
          return;
        }
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
        queueUploadButton.disabled = false;
      }
    });

    function setUploadMode(mode) {
      uploadMode = mode;
      const linkMode = mode === 'link';
      const obsMode = mode === 'obs';
      fileModeButton.classList.toggle('active', !linkMode && !obsMode);
      linkModeButton.classList.toggle('active', linkMode);
      obsModeButton.classList.toggle('active', obsMode);
      fileModeButton.setAttribute('aria-selected', String(!linkMode && !obsMode));
      linkModeButton.setAttribute('aria-selected', String(linkMode));
      obsModeButton.setAttribute('aria-selected', String(obsMode));
      fileUploadPanel.classList.toggle('active', !linkMode && !obsMode);
      linkUploadPanel.classList.toggle('active', linkMode);
      obsUploadPanel.classList.toggle('active', obsMode);
      if (uploadControls) {
        uploadControls.hidden = obsMode;
      }
      imageInput.required = !linkMode && !obsMode;
      imageURLInput.required = linkMode;
      uploadButton.hidden = false;
      queueUploadButton.hidden = obsMode || !state.videoPlayerEnabled;
      uploadButton.textContent = linkMode ? 'リンクから変換して公開' : '変換して公開';
      renderShareURL(state);
      applyOBSProtection();
      applyOBS(state.obs);
      if (linkMode) {
        imageURLInput.focus();
      }
    }

    function uploadFromFile(action) {
      const formData = new FormData(uploadForm);
      return fetch(action === 'queue' ? '/api/upload-queue' : '/api/upload', { method: 'POST', body: formData });
    }

    function publicOBSRTSPURL(data) {
      const url = data && data.obs && data.obs.rtsptURL ? String(data.obs.rtsptURL) : '';
      if (!url.startsWith('rtsp://')) {
        return '';
      }
      try {
        const host = new URL(url).hostname;
        if (/^(localhost|127\.|10\.|192\.168\.|169\.254\.|0\.0\.0\.0$)/i.test(host)) return '';
        if (/^172\.(1[6-9]|2[0-9]|3[0-1])\./.test(host)) return '';
        if (/^100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\./.test(host)) return '';
      } catch (error) {
        return '';
      }
      return url;
    }

    async function maybeAutoCopyOBSURL(data) {
      if (!pendingOBSAutoCopy) {
        return;
      }
      const url = publicOBSRTSPURL(data);
      if (!url || url === lastAutoCopiedOBSURL) {
        return;
      }
      pendingOBSAutoCopy = false;
      lastAutoCopiedOBSURL = url;
      const source = document.getElementById('shareURL');
      let browserCopied = false;
      let pcCopied = false;
      try {
        browserCopied = await copyText(url, source);
      } catch (error) {
      }
      try {
        const pcResult = await copyURLOnPC('shareURL');
        pcCopied = !!pcResult.pcClipboardCopied;
      } catch (error) {
      }
      if (pcCopied && browserCopied) {
        toast.textContent = 'グローバルRTSP URLをコピーしました。PCにもコピー済みです';
      } else if (pcCopied) {
        toast.textContent = 'グローバルRTSP URLをPCにコピーしました';
      } else if (browserCopied) {
        toast.textContent = 'グローバルRTSP URLをこの端末にコピーしました';
      } else {
        toast.textContent = 'グローバルRTSP URLを表示しました。コピーできない場合は手動でコピーしてください';
      }
    }

    function uploadFromLink(action) {
      const formData = new FormData(uploadForm);
      return fetch(action === 'queue' ? '/api/upload-url-queue' : '/api/upload-url', {
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

    function selectedRAWFile() {
      const file = imageInput.files && imageInput.files[0];
      if (!file || !file.name) return false;
      const dot = file.name.lastIndexOf('.');
      if (dot < 0) return false;
      return rawExtensions.has(file.name.slice(dot).toLowerCase());
    }

    async function ensureFFmpegForRAWSelection() {
      if (!selectedRAWFile() || ffmpegReady) return true;
      if (ffmpegPromise) return ffmpegPromise;
      ffmpegPromise = runFFmpegCheckForRAWSelection();
      return ffmpegPromise;
    }

    async function runFFmpegCheckForRAWSelection() {
      ffmpegPending = true;
      uploadButton.disabled = true;
      queueUploadButton.disabled = true;
      toast.textContent = 'Checking FFmpeg for RAW conversion...';
      try {
        const res = await fetch('/api/ffmpeg', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        ffmpegReady = true;
        toast.textContent = 'FFmpeg is ready for RAW conversion.';
        return true;
      } catch (error) {
        toast.textContent = error.message || 'Failed to check FFmpeg for RAW conversion.';
        return false;
      } finally {
        ffmpegPending = false;
        ffmpegPromise = null;
        uploadButton.disabled = false;
        queueUploadButton.disabled = false;
      }
    }

    if (pasteURLButton) {
      pasteURLButton.addEventListener('click', async () => {
        try {
          const text = (await navigator.clipboard.readText()).trim();
          if (text) {
            imageURLInput.value = text;
          }
        } catch (error) {
          toast.textContent = 'クリップボードの読み取りに失敗しました';
        }
        imageURLInput.focus();
      });
    }

    clearButton.addEventListener('click', async () => {
      const obsEnd = uploadMode === 'obs';
      clearButton.disabled = true;
      toast.textContent = obsEnd ? 'OBS配信を終了中...' : '画像をクリア中...';
      try {
        const res = await fetch(obsEnd ? '/api/obs/end' : '/api/clear', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        announceLocalChange();
        toast.textContent = obsEnd ? 'OBS配信を終了し、次の待ち受けを開始しました' : '画像をクリアしました';
      } catch (error) {
        toast.textContent = error.message || (obsEnd ? 'OBS配信の終了に失敗しました' : '画像クリアに失敗しました');
      } finally {
        clearButton.disabled = false;
      }
    });

    if (historyList) {
      historyList.addEventListener('click', async (event) => {
        const publish = event.target.closest('[data-history-publish]');
        if (publish) {
          await selectHistory(publish.dataset.historyPublish);
          return;
        }
        const queue = event.target.closest('[data-history-queue]');
        if (queue) {
          await queueHistory(queue.dataset.historyQueue);
          return;
        }
        const heart = event.target.closest('[data-history-favorite]');
        if (heart) {
          event.preventDefault();
          event.stopPropagation();
          const favorite = heart.dataset.favorite !== '1';
          await setHistoryFavorite(heart.dataset.historyFavorite, favorite);
          return;
        }
      });
    }
    for (const button of wingTabButtons) {
      button.addEventListener('click', () => setWingMode(button.dataset.wingTab));
    }

    async function setHistoryFavorite(id, favorite) {
      if (!favorite && !window.confirm('お気に入りから削除すると、保存済みファイルも削除されます。よろしいですか？')) {
        return;
      }
      try {
        const res = await fetch('/api/history/favorite', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id, favorite })
        });
        if (!res.ok) throw new Error(await res.text());
        state.history = await res.json();
        renderHistory(state.history, state.currentID);
        toast.textContent = favorite ? 'お気に入りに保存しました' : 'お気に入りから削除しました';
      } catch (error) {
        toast.textContent = error.message || 'お気に入りの更新に失敗しました';
      }
    }

    let historyActionInFlight = false;

    async function queueHistory(id) {
      if (historyActionInFlight) return;
      historyActionInFlight = true;
      toast.textContent = '動画変換に追加中...';
      try {
        const res = await fetch('/api/history/queue', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        setWingMode('queue');
        announceLocalChange();
        toast.textContent = '動画変換に追加しました';
      } catch (error) {
        toast.textContent = error.message || '動画変換への追加に失敗しました';
      } finally {
        historyActionInFlight = false;
      }
    }

    async function selectHistory(id) {
      if (historyActionInFlight) return;
      historyActionInFlight = true;
      toast.textContent = '履歴から復元中...';
      try {
        const res = await fetch('/api/history/select', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyState(data);
        applyHistoryTargetMode(data);
        announceLocalChange();
        toast.textContent = data.clipboardCopied ? '履歴から公開し、URLをPCにコピーしました' : '履歴から復元しました';
      } catch (error) {
        toast.textContent = error.message || '履歴の復元に失敗しました';
      } finally {
        historyActionInFlight = false;
      }
    }

    function applyHistoryTargetMode(data) {
      const mode = data && data.historyTargetMode ? String(data.historyTargetMode) : '';
      if (mode === 'obs') {
        setUploadMode('obs');
      } else if (mode === 'link') {
        setUploadMode('link');
      } else if (mode === 'file') {
        setUploadMode('file');
      }
    }

    document.addEventListener('click', async (event) => {
      const target = event.target.closest('[data-copy]');
      if (!target) return;
      const id = target.getAttribute('data-copy');
      const source = document.getElementById(id);
      let text = source.textContent;
      if (id === 'shareURL') {
        text = shareURLForCurrentMode(state).shareURL || '';
      }
      if (id === 'obsStreamKey' && state.obs && state.obs.streamKey) {
        text = state.obs.streamKey;
      }
      if ((id === 'phoneURL' || id === 'phoneURLMobile') && uploadMode === 'obs') {
        text = '';
      }
      if (!text || text === '-' || text === '*****') {
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
    document.getElementById('obsKeyRevealButton').addEventListener('click', () => {
      obsKeyVisible = !obsKeyVisible;
      const button = document.getElementById('obsKeyRevealButton');
      button.title = obsKeyVisible ? 'Stream Keyを隠す' : 'Stream Keyを表示';
      button.setAttribute('aria-label', button.title);
      applyOBS(state.obs);
    });
    const obsStreamKeyElement = document.getElementById('obsStreamKey');
    const requestOBSKeyChange = async () => {
      const nextKey = await showOBSKeyRiskDialog();
      if (!nextKey) return;
      await updateOBSStreamKey(nextKey);
    };
    if (obsStreamKeyElement) {
      obsStreamKeyElement.addEventListener('click', requestOBSKeyChange);
      obsStreamKeyElement.addEventListener('keydown', (event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          requestOBSKeyChange();
        }
      });
    }
    obsKeyRotateButton.addEventListener('click', requestOBSKeyChange);
    obsLatencyMode.addEventListener('change', async () => {
      const requestedMode = obsLatencyMode.value;
      if (requestedMode.startsWith('rtsp-') && !String(confirmedOBSLatencyMode || '').startsWith('rtsp-')) {
        const accepted = await showRTSPRiskDialog();
        if (!accepted) {
          obsLatencyMode.value = confirmedOBSLatencyMode;
          return;
        }
      }
      updateOBSLatency(requestedMode);
    });
    if (obsLatencyDetailButton) {
      obsLatencyDetailButton.addEventListener('click', async () => {
        await refreshState();
        showOBSConnectionsDialog();
      });
    }
    async function updateOBSLatency(mode) {
      obsLatencyMode.disabled = true;
      try {
        const res = await fetch('/api/obs/latency', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: mode || obsLatencyMode.value })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        state.obs = data || null;
        applyOBS(data);
        resetOBSPreview();
        refreshAgain = true;
        announceLocalChange();
        toast.textContent = 'OBS latency mode updated. Restarting preview...';
      } catch (error) {
        toast.textContent = error.message || 'Failed to update OBS latency mode';
      } finally {
        obsLatencyMode.disabled = false;
      }
    }
    function showRTSPRiskDialog() {
      if (!rtspRiskDialog || !rtspRiskCancel || !rtspRiskConfirm) {
        return Promise.resolve(false);
      }
      return new Promise((resolve) => {
        let settled = false;
        const finish = (accepted) => {
          if (settled) return;
          settled = true;
          rtspRiskDialog.hidden = true;
          rtspRiskDialog.removeEventListener('click', onBackdrop);
          rtspRiskCancel.removeEventListener('click', onCancel);
          rtspRiskConfirm.removeEventListener('click', onConfirm);
          document.removeEventListener('keydown', onKeyDown);
          if (obsLatencyMode) obsLatencyMode.focus();
          resolve(accepted);
        };
        const onCancel = () => finish(false);
        const onConfirm = () => finish(true);
        const onBackdrop = (event) => {
          if (event.target === rtspRiskDialog) finish(false);
        };
        const onKeyDown = (event) => {
          if (event.key === 'Escape') finish(false);
        };
        rtspRiskDialog.hidden = false;
        rtspRiskDialog.addEventListener('click', onBackdrop);
        rtspRiskCancel.addEventListener('click', onCancel);
        rtspRiskConfirm.addEventListener('click', onConfirm);
        document.addEventListener('keydown', onKeyDown);
        rtspRiskConfirm.focus();
      });
    }
    function showOBSKeyRiskDialog() {
      if (!obsKeyRiskDialog || !obsKeyRiskCancel || !obsKeyRiskConfirm || !obsKeyEditInput) {
        return Promise.resolve('');
      }
      obsKeyEditInput.value = (state.obs && state.obs.streamKey) || '';
      return new Promise((resolve) => {
        let settled = false;
        const finish = (value) => {
          if (settled) return;
          settled = true;
          obsKeyRiskDialog.hidden = true;
          obsKeyRiskDialog.removeEventListener('click', onBackdrop);
          obsKeyRiskCancel.removeEventListener('click', onCancel);
          obsKeyRiskConfirm.removeEventListener('click', onConfirm);
          obsKeyEditInput.removeEventListener('keydown', onInputKeyDown);
          document.removeEventListener('keydown', onKeyDown);
          const key = document.getElementById('obsStreamKey');
          if (key) key.focus();
          resolve(value);
        };
        const validate = () => {
          const value = obsKeyEditInput.value.trim();
          if (!value) {
            toast.textContent = 'OBS Stream Keyを入力してください';
            obsKeyEditInput.focus();
            return '';
          }
          if (/[\\/\s?#]/.test(value)) {
            toast.textContent = 'OBS Stream Keyに空白、/、\\\\、?、# は使えません';
            obsKeyEditInput.focus();
            return '';
          }
          return value;
        };
        const onCancel = () => finish('');
        const onConfirm = () => {
          const value = validate();
          if (value) finish(value);
        };
        const onBackdrop = (event) => {
          if (event.target === obsKeyRiskDialog) finish('');
        };
        const onKeyDown = (event) => {
          if (event.key === 'Escape') finish('');
        };
        const onInputKeyDown = (event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            onConfirm();
          }
        };
        obsKeyRiskDialog.hidden = false;
        obsKeyRiskDialog.addEventListener('click', onBackdrop);
        obsKeyRiskCancel.addEventListener('click', onCancel);
        obsKeyRiskConfirm.addEventListener('click', onConfirm);
        obsKeyEditInput.addEventListener('keydown', onInputKeyDown);
        document.addEventListener('keydown', onKeyDown);
        obsKeyEditInput.focus();
        obsKeyEditInput.select();
      });
    }
    async function updateOBSStreamKey(streamKey) {
      obsKeyRotateButton.disabled = true;
      toast.textContent = 'OBS Stream Keyを保存中...';
      try {
        const res = await fetch('/api/obs/key', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ streamKey })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        obsKeyVisible = true;
        applyState(data);
        announceLocalChange();
        toast.textContent = 'OBS Stream Keyを保存しました。OBS側の設定も同じ値へ変更してください';
      } catch (error) {
        toast.textContent = error.message || 'OBS Stream Keyの保存に失敗しました';
      } finally {
        obsKeyRotateButton.disabled = false;
      }
    }
    function showOBSConnectionsDialog() {
      if (!obsConnectionsDialog || !obsConnectionsClose) return;
      const close = () => {
        obsConnectionsDialog.hidden = true;
        obsConnectionsDialog.removeEventListener('click', onBackdrop);
        obsConnectionsClose.removeEventListener('click', close);
        document.removeEventListener('keydown', onKeyDown);
        if (obsLatencyDetailButton) obsLatencyDetailButton.focus();
      };
      const onBackdrop = (event) => {
        if (event.target === obsConnectionsDialog) close();
      };
      const onKeyDown = (event) => {
        if (event.key === 'Escape') close();
      };
      renderOBSConnections((state.obs && state.obs.connections) || []);
      obsConnectionsDialog.hidden = false;
      obsConnectionsDialog.addEventListener('click', onBackdrop);
      obsConnectionsClose.addEventListener('click', close);
      document.addEventListener('keydown', onKeyDown);
      obsConnectionsClose.focus();
    }
    document.getElementById('tunnelReconnectButton').addEventListener('click', async () => {
      const button = document.getElementById('tunnelReconnectButton');
      button.disabled = true;
      toast.textContent = '再接続を要求しています...';
      try {
        const res = await fetch('/api/tunnel/reconnect', { method: 'POST' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        toast.textContent = data.message || '再接続を要求しました';
      } catch (error) {
        toast.textContent = error.message || '再接続の要求に失敗しました';
      } finally {
        button.disabled = false;
      }
    });
    const quitButton = document.getElementById('quitButton');
    if (quitButton) {
      quitButton.addEventListener('click', async () => {
        if (!confirm('アプリを終了しますか？\n配信中のストリームも停止します。')) return;
        quitButton.disabled = true;
        toast.textContent = 'アプリを終了しています...';
        try {
          const res = await fetch('/api/quit', { method: 'POST' });
          if (!res.ok) throw new Error(await res.text());
          quitButton.classList.add('done');
          quitButton.textContent = '終了しました（この画面は閉じてかまいません）';
          toast.textContent = 'アプリを終了しました';
        } catch (error) {
          quitButton.disabled = false;
          toast.textContent = error.message || 'アプリの終了に失敗しました';
        }
      });
    }
    fileModeButton.addEventListener('click', () => setUploadMode('file'));
    linkModeButton.addEventListener('click', () => setUploadMode('link'));
    obsModeButton.addEventListener('click', () => setUploadMode('obs'));
    imageInput.addEventListener('change', ensureFFmpegForRAWSelection);
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
        announceLocalChange();
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
        announceLocalChange();
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
        announceLocalChange();
        toast.textContent = enabled ? 'ビデオプレーヤー対応を有効にしました' : 'ビデオプレーヤー対応を無効にしました';
      } catch (error) {
        await refreshState();
        toast.textContent = error.message || 'ビデオプレーヤー対応の切り替えに失敗しました';
      } finally {
        videoPlayerPending = false;
        videoPlayerToggle.disabled = false;
      }
    });
    musicModeToggle.addEventListener('change', async () => {
      const enabled = musicModeToggle.checked;
      musicModePending = true;
      musicModeToggle.disabled = true;
      musicModeText.textContent = enabled ? '有効化中...' : '無効化中...';
      try {
        const res = await fetch('/api/music-mode', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ enabled })
        });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        applyVideoPlayer(data);
        await refreshState();
        announceLocalChange();
        toast.textContent = enabled ? 'ミュージックモードを有効にしました' : 'ミュージックモードを無効にしました';
      } catch (error) {
        await refreshState();
        toast.textContent = error.message || 'ミュージックモードの切り替えに失敗しました';
      } finally {
        musicModePending = false;
        musicModeToggle.disabled = !state.videoPlayerEnabled;
      }
    });
    function scheduleRefresh(delay) {
      clearTimeout(refreshTimer);
      refreshTimer = setTimeout(refreshState, delay);
    }

    function announceLocalChange() {
      try {
        if (localChangeChannel) {
          localChangeChannel.postMessage({ type: 'changed', at: Date.now() });
        }
        localStorage.setItem('imagepad:lastChange', String(Date.now()));
      } catch (error) {
      }
      scheduleRefresh(100);
    }

    function setupLiveSync() {
      try {
        if ('BroadcastChannel' in window) {
          localChangeChannel = new BroadcastChannel('imagepad-state');
          localChangeChannel.onmessage = () => scheduleRefresh(100);
        }
      } catch (error) {
      }
      try {
        if ('EventSource' in window) {
          const stateEvents = new EventSource('/api/events');
          stateEvents.onopen = () => scheduleRefresh(50);
          stateEvents.onerror = () => scheduleRefresh(1000);
          stateEvents.addEventListener('state', () => scheduleRefresh(0));
        }
      } catch (error) {
      }
      window.addEventListener('storage', (event) => {
        if (event.key === 'imagepad:lastChange') {
          scheduleRefresh(100);
        }
      });
      window.addEventListener('focus', () => scheduleRefresh(50));
      window.addEventListener('pageshow', () => scheduleRefresh(50));
      window.addEventListener('online', () => scheduleRefresh(50));
      document.addEventListener('visibilitychange', () => {
        if (!document.hidden) {
          scheduleRefresh(50);
        }
      });
    }

    async function checkForUpdates() {
      try {
        const res = await fetch('/api/update-check', { cache: 'no-store' });
        if (!res.ok) throw new Error(await res.text());
        const data = await res.json();
        if (data.ok && data.newer) {
          updateText.innerHTML = '<a href="' + data.url + '" target="_blank" rel="noreferrer">' + escapeHTML(data.latest) + ' があります</a>';
        } else if (data.ok) {
          updateText.textContent = '最新版 ' + (data.current || '');
        } else {
          updateText.textContent = data.message || '確認失敗';
        }
      } catch (error) {
        updateText.textContent = '確認失敗';
      }
    }

    setupLiveSync();
    refreshState();
    checkForUpdates();
  </script>
</body>
</html>`
