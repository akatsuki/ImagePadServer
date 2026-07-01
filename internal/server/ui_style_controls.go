package server

const uiStyleControls = `    .mode-tabs {
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
      background: var(--surface-subtle);
      color: var(--text);
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
      border: 2px dashed var(--border);
      border-radius: 8px;
      background: var(--surface-subtle);
      text-align: center;
      transition: border-color .15s ease, background .15s ease, box-shadow .15s ease;
    }
    .drop-zone.dragover {
      border-color: var(--accent);
      background: var(--soft);
      box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 24%, transparent);
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
      color: var(--text);
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
      background: var(--progress-track);
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
    .link-input-row {
      display: flex;
      gap: 8px;
      align-items: stretch;
    }
    .link-input-row input[type="url"] {
      flex: 1;
      min-width: 0;
    }
`
