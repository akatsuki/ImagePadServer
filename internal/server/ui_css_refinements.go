package server

const dashboardCSSRefinements = `
    /* Visual refinement layer: app-console hierarchy without changing behavior. */
    body {
      background:
        var(--body-wash),
        var(--bg);
    }
    header {
      min-height: 62px;
      padding-block: 10px;
      background: var(--header-bg);
    }
    h1 {
      font-size: 23px;
      font-weight: 850;
    }
    main {
      gap: 18px;
      padding-top: 16px;
    }
    section,
    .hero-panel,
    .modal-card {
      border-radius: var(--radius-panel);
      border-color: color-mix(in srgb, var(--line) 80%, transparent);
      box-shadow: var(--shadow-surface-strong);
    }
    .status-panel {
      padding: 12px;
      border: 0;
      background: color-mix(in srgb, var(--panel) 72%, transparent);
    }
    .status-panel h2 {
      font-size: 16px;
      font-weight: 850;
    }
    .pill,
    .toggle-row,
    .urlbox {
      border-radius: var(--radius-control);
      border-color: color-mix(in srgb, var(--line) 82%, transparent);
      background: var(--panel-strong);
    }
    .status-panel .pill {
      min-height: 48px;
      padding: 8px 10px;
      background: var(--soft-blue);
      color: var(--ink);
    }
    .section-head {
      padding: 12px;
      background: var(--section-head-bg);
    }
    .section-head h2 {
      font-size: 16px;
      font-weight: 850;
    }
    .flow-grid {
      min-height: 350px;
    }
    .flow-primary,
    .flow-secondary {
      padding: 18px;
    }
    .flow-primary {
      background: var(--flow-primary-bg);
    }
    .flow-secondary {
      background: var(--flow-secondary-bg);
    }
    .flow-step {
      color: var(--control-ink);
      font-size: 13px;
    }
    .flow-step::before {
      content: none;
    }
    .step-badge {
      width: 28px;
      height: 28px;
      flex-basis: 28px;
    }
    .mode-tab,
    .wing-tab,
    .advanced-options summary {
      background: var(--control-bg);
      border-color: var(--line);
      color: var(--control-ink);
      font-weight: 850;
    }
    .mode-tab.active,
    .wing-tab.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
    }
    .drop-zone {
      min-height: 190px;
      border-color: var(--line);
      background:
        color-mix(in srgb, var(--panel) 94%, transparent),
        repeating-linear-gradient(135deg, color-mix(in srgb, var(--line) 22%, transparent) 0 10px, transparent 10px 20px);
    }
    input[type="file"] {
      border: 1px solid var(--line);
      background: var(--panel);
    }
    input[type="file"]::file-selector-button {
      min-height: 32px;
      margin-right: 10px;
      border: 0;
      border-radius: var(--radius-small);
      background: var(--control-active-bg);
      color: var(--control-active-ink);
      font: inherit;
      font-weight: 800;
      padding: 0 12px;
      cursor: pointer;
    }
    input[type="url"],
    select,
    input[type="number"] {
      min-height: 38px;
      border-color: var(--line);
    }
    .flow-secondary .upload-actions #uploadButton {
      min-height: 56px;
      border-radius: var(--radius-card);
      background: var(--accent);
      color: var(--on-positive);
      font-size: 15px;
      box-shadow: var(--shadow-positive-soft);
    }
    .flow-secondary .upload-actions #queueUploadButton {
      min-height: 44px;
      border-radius: var(--radius-card);
    }
    .preview {
      height: clamp(300px, 47vh, 510px);
      border-color: var(--line);
      border-radius: var(--radius-card);
      background:
        linear-gradient(45deg, var(--preview-check) 25%, transparent 25% 75%, var(--preview-check) 75%),
        linear-gradient(45deg, var(--preview-check) 25%, transparent 25% 75%, var(--preview-check) 75%),
        var(--preview-bg);
      background-position: 0 0, 12px 12px;
      background-size: 24px 24px;
    }
    .preview-panel {
      display: grid;
      align-content: start;
    }
    .preview-body {
      display: grid;
      gap: 12px;
      padding: 14px;
    }
    .preview-panel .preview {
      height: clamp(280px, 42vh, 430px);
      min-height: 280px;
    }
    .preview-controls {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 10px;
      align-items: stretch;
    }
    .share-box {
      min-width: 0;
      padding: 8px;
    }
    .share-box button {
      min-height: 40px;
      align-self: center;
    }
    .preview-actions {
      display: grid;
      grid-template-columns: repeat(2, auto);
      gap: 8px;
      align-items: stretch;
    }
    .preview-actions button {
      min-height: 44px;
      padding-inline: 14px;
    }
    .preview-panel .video-links {
      margin-top: 0;
      gap: 10px;
    }
    .video-quality-options .pill {
      min-height: 44px;
      align-items: center;
    }
    .video-quality-options #networkCheckButton {
      min-height: 44px;
      padding-inline: 14px;
    }
    /* HIG refinement pass: lighter chrome, calmer empty states, and contained advanced controls. */
    header {
      min-height: 56px;
      background: color-mix(in srgb, var(--panel) 86%, transparent);
      color: var(--ink);
      border-bottom: 1px solid color-mix(in srgb, var(--line) 80%, transparent);
      box-shadow: var(--shadow-surface);
      backdrop-filter: blur(16px);
    }
    header h1 {
      color: var(--ink);
      font-size: 21px;
    }
    header .settings-button {
      background: var(--control-bg);
      color: var(--control-ink);
      border-color: var(--line);
    }
    header .settings-button:hover {
      background: var(--control-bg-strong);
    }
    .media-kind-switch {
      background: var(--control-bg);
      box-shadow: var(--shadow-inset-subtle);
    }
    .media-kind-switch button.active {
      background: var(--tab-active-bg);
      color: var(--tab-active-ink);
      border-color: var(--line);
      box-shadow: var(--shadow-active);
    }
    .advanced-options {
      position: relative;
      overflow: visible;
    }
    .content .hero-panel {
      overflow: hidden;
    }
    .content .hero-panel:has(.advanced-options[open]),
    .content .hero-panel:has(.advanced-options[open]) .flow-grid {
      overflow: hidden;
    }
    .content .hero-panel .flow-grid {
      border-radius: 0 0 var(--radius-panel) var(--radius-panel);
      overflow: hidden;
    }
    .advanced-options[open] .controls {
      position: absolute;
      inset: calc(100% + 6px) 0 auto 0;
      z-index: 7;
      max-height: min(260px, calc(100vh - 330px));
      overflow: auto;
      padding: 12px;
      border: 1px solid var(--line);
      border-radius: var(--radius-card);
      background: var(--panel);
      box-shadow: var(--shadow-overlay);
    }
    .preview {
      border-style: solid;
      border-color: color-mix(in srgb, var(--line) 82%, transparent);
      background:
        linear-gradient(45deg, var(--preview-check) 25%, transparent 25% 75%, var(--preview-check) 75%),
        linear-gradient(45deg, var(--preview-check) 25%, transparent 25% 75%, var(--preview-check) 75%),
        var(--preview-bg);
      background-position: 0 0, 12px 12px;
      background-size: 24px 24px;
    }
    .empty {
      color: var(--muted);
      font-size: 13px;
    }
    .history {
      align-self: stretch;
    }
    .actions {
      margin-top: 10px;
      gap: 8px;
    }
    .history section {
      padding: 12px;
    }
    .wing-tabs {
      gap: 8px;
      margin-bottom: 10px;
    }
    .history-item,
    .queue-item {
      min-height: 64px;
      padding: 8px;
      border-radius: var(--radius-card);
      background: var(--panel-strong);
    }
    .history-thumb {
      border-radius: var(--radius-control);
      background: var(--control-bg);
    }
    .history-title {
      font-size: 13px;
    }
    .modal-backdrop {
      background: var(--scrim-strong);
      backdrop-filter: blur(3px);
    }
    .modal-card {
      border-radius: 18px;
      padding: 18px;
    }
    .settings-row {
      padding: 12px 0;
    }
    .settings-button {
      min-height: 42px;
      border-radius: var(--radius-control);
      font-weight: 850;
    }
    /* Presentation normalization: one radius, shadow, and state vocabulary per component class. */
    section,
    .hero-panel {
      border-radius: var(--radius-panel);
      border-color: color-mix(in srgb, var(--line) 80%, transparent);
      box-shadow: var(--shadow-surface-strong);
    }
    .modal-card,
    .tool-install-card,
    .local-panel,
    .pairing-panel {
      border-radius: var(--radius-overlay);
      border-color: color-mix(in srgb, var(--line) 80%, transparent);
      box-shadow: var(--shadow-overlay);
    }
    button,
    .file-button,
    input[type="file"]::file-selector-button {
      border-radius: var(--radius-control);
      box-shadow: var(--shadow-control);
    }
    button:hover,
    .file-button:hover,
    input[type="file"]::file-selector-button:hover {
      box-shadow: var(--shadow-control-hover);
    }
    button:focus-visible,
    .file-button:focus-visible,
    input:focus-visible,
    select:focus-visible,
    summary:focus-visible {
      outline: 0;
      box-shadow: var(--focus-ring);
    }
    button:disabled,
    button:disabled:hover {
      box-shadow: none !important;
      transform: none;
    }
    .header-actions .settings-button,
    .header-actions .phone-connect-button {
      background: var(--control-bg);
      color: var(--control-ink);
      border-color: var(--line);
      border-radius: var(--radius-control);
      box-shadow: none;
    }
    .header-actions .settings-button:hover,
    .header-actions .phone-connect-button:hover {
      background: var(--control-bg-strong);
      border-color: var(--line);
      box-shadow: none;
    }
    header .app-version {
      color: color-mix(in srgb, var(--ink) 48%, transparent);
    }
    input[type="file"],
    input[type="url"],
    select,
    input[type="number"],
    .urlbox,
    .toggle-row,
    .pill,
    .candidate-item,
    .history-item,
    .queue-item,
    .advanced-options,
    .advanced-options[open] .controls {
      border-radius: var(--radius-control);
      box-shadow: none;
    }
    .drop-zone,
    .preview,
    .phone-dialog-qr,
    .phone-dialog-qr-wrap {
      border-radius: var(--radius-panel);
    }
    .advanced-options {
      overflow: hidden;
    }
    .advanced-options[open] {
      overflow: visible;
    }
    .advanced-options summary {
      border-radius: var(--radius-control);
      overflow: hidden;
    }
    .drop-zone {
      background: var(--panel-strong);
    }
    .drop-zone.dragover,
    input[type="file"]:focus-visible,
    input[type="url"]:focus-visible,
    select:focus-visible,
    input[type="number"]:focus-visible {
      box-shadow: var(--focus-ring);
    }
    .mode-tab.active,
    .wing-tab.active,
    .media-kind-switch button.active {
      box-shadow: var(--shadow-active);
    }
    .mode-tab.active:hover,
    .wing-tab.active:hover,
    .media-kind-switch button.active:hover {
      transform: none;
      box-shadow: var(--shadow-active);
      cursor: auto;
    }
    .media-kind-switch,
    .media-kind-switch button,
    .switch-slider,
    .progress-bar {
      border-radius: var(--radius-pill);
    }
    .switch-slider {
      box-shadow: var(--shadow-inset-subtle);
    }
    .switch-slider::before,
    .flow-step::before,
    .heart-button {
      border-radius: 50%;
    }
    .switch-slider::before {
      box-shadow: var(--shadow-control);
    }
    .history-thumb {
      border-radius: var(--radius-xs);
    }
    .flow-secondary .upload-actions #uploadButton,
    .flow-secondary .upload-actions #queueUploadButton,
    .flow-secondary .upload-actions #obsLatencyDetailButton {
      border-radius: var(--radius-control);
    }
    .flow-secondary .upload-actions #uploadButton {
      box-shadow: var(--shadow-control-hover);
    }
    .content .hero-panel .flow-grid {
      border-radius: 0 0 var(--radius-panel) var(--radius-panel);
    }
    /* Apple Design typography pass: keep Japanese UI on one font family and role-based weights. */
    button,
    .file-button,
    input,
    select,
    textarea,
    summary,
    input[type="file"]::file-selector-button {
      font-family: "Noto Sans JP", sans-serif;
      letter-spacing: 0;
    }
    button,
    .file-button,
    input[type="file"]::file-selector-button {
      font-weight: 700;
      line-height: 1.25;
      text-rendering: optimizeLegibility;
    }
    h1 {
      font-weight: 800;
      line-height: 1.2;
    }
    h2,
    .section-head h2,
    .status-panel h2,
    .pairing-title {
      font-weight: 750;
      line-height: 1.25;
    }
    .app-version {
      font-weight: 650;
    }
    .header-actions .settings-button,
    .header-actions .phone-connect-button,
    .media-kind-switch button,
    .mode-tab,
    .wing-tab,
    .advanced-options summary,
    .settings-button,
    .flow-step,
    .flow-step span {
      font-weight: 750;
    }
    .history-title,
    .queue-title,
    .queue-status,
    .history-detail,
    .urlbox strong,
    #shareURLLabel,
    .history-thumb,
    .pill-action {
      font-size: 12px;
      font-weight: 700;
      line-height: 1.35;
    }
    .pill,
    .toggle-row,
    .drop-hint,
    .section-kicker,
    .about,
    .pairing-detail,
    .mobile-progress {
      line-height: 1.45;
    }`
