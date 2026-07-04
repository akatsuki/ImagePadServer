package server

const dashboardCSSLayout = `
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "Noto Sans JP", sans-serif;
      background:
        var(--body-wash),
        var(--bg);
      color: var(--ink);
    }
    header {
      position: sticky;
      top: 0;
      z-index: 10;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      padding: 10px clamp(12px, 2.4vw, 22px);
      border-bottom: 1px solid color-mix(in srgb, var(--on-header) 16%, transparent);
      background: var(--header-bg);
      color: var(--on-header);
      box-shadow: var(--shadow-surface-strong);
    }
    h1 {
      margin: 0;
      font-size: 22px;
      letter-spacing: 0;
    }
    .app-brand {
      display: flex;
      align-items: flex-end;
      gap: 8px;
      min-width: 0;
    }
    .app-brand h1 {
      flex: 0 0 auto;
      min-width: max-content;
      overflow: visible;
      text-overflow: clip;
      white-space: nowrap;
    }
    .app-icon {
      height: 28px;
      width: auto;
      display: block;
      border-radius: var(--radius-xs);
      flex: 0 0 auto;
      align-self: center;
    }
    .app-version {
      color: color-mix(in srgb, var(--on-header) 58%, transparent);
      font-size: 12px;
      font-weight: 750;
      white-space: nowrap;
      line-height: 1.2;
      padding-bottom: 2px;
    }
    main {
      display: grid;
      grid-template-columns: minmax(0, 1.18fr) minmax(360px, .82fr);
      grid-template-areas:
        "content preview";
      align-items: stretch;
      gap: 16px;
      height: calc(100vh - 62px);
      min-height: 620px;
      overflow: hidden;
      max-width: 1600px;
      margin: 0 auto;
      padding: 14px clamp(12px, 2.4vw, 24px) 18px;
    }
    .sidebar, .content, .history, .preview-column {
      display: grid;
      gap: 10px;
      align-content: start;
    }
    .sidebar { grid-area: sidebar; }
    .content {
      grid-area: content;
      grid-template-rows: auto minmax(154px, 1fr);
      min-height: 0;
    }
    .preview-column { grid-area: preview; }
    .preview-column .hero-panel {
      height: 100%;
    }
    .history {
      min-height: 0;
      overflow: hidden;
    }
    .history section {
      height: 100%;
      min-height: 154px;
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
    }
    .quit { display: grid; grid-template-columns: minmax(0, 1fr); gap: 8px; }
    .visually-hidden {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      margin: -1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }
    .quit-button {
      width: 100%;
      min-height: 40px;
      background: var(--danger);
      color: var(--on-negative);
      font-weight: 700;
    }
    .header-actions {
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .header-actions .settings-button,
    .header-actions .quit-header-button,
    .header-actions .phone-connect-button {
      min-height: 40px;
      height: 40px;
      padding: 0 14px;
      background: color-mix(in srgb, var(--on-header) 14%, transparent);
      color: var(--on-header);
      border: 1px solid color-mix(in srgb, var(--on-header) 28%, transparent);
      border-radius: var(--radius-control);
      box-shadow: none;
      font-size: 13px;
      font-weight: 850;
    }
    .header-actions .settings-button:hover,
    .header-actions .quit-header-button:hover,
    .header-actions .phone-connect-button:hover {
      background: color-mix(in srgb, var(--on-header) 22%, transparent);
      border-color: color-mix(in srgb, var(--on-header) 34%, transparent);
      box-shadow: none;
    }
    .header-actions .icon-only-button {
      width: 40px;
      min-width: 40px;
      padding: 0;
      display: inline-flex;
      align-items: center;
      justify-content: center;
    }
    .header-actions .icon-only-button svg {
      width: 20px;
      height: 20px;
      display: block;
    }
    .header-actions .quit-header-button {
      background: var(--danger);
      color: var(--on-negative);
      border-color: color-mix(in srgb, var(--on-negative) 28%, transparent);
    }
    .header-actions .quit-header-button:hover {
      background: color-mix(in srgb, var(--danger) 88%, var(--color-black));
      border-color: color-mix(in srgb, var(--on-negative) 42%, transparent);
    }
    .phone-connect-button {
      display: inline-flex;
      align-items: center;
      gap: 8px;
    }
    .phone-connect-button svg {
      width: 18px;
      height: 18px;
      display: block;
    }
    .quit-button:hover { filter: brightness(.95); }
    .quit-button.done {
      background: var(--disabled-bg);
      cursor: default;
    }
    section {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: var(--radius-control);
      padding: 12px;
      box-shadow: var(--shadow-soft);
    }
    .hero-panel {
      padding: 0;
      overflow: hidden;
      border-color: var(--border-strong);
      box-shadow: var(--shadow);
    }
    .section-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 14px;
      min-height: 0;
      padding: 12px;
      border-bottom: 1px solid var(--line);
      background: var(--section-head-bg);
    }
    .section-title-copy {
      flex: 1 1 auto;
      min-width: 0;
      display: grid;
      gap: 4px;
      align-content: start;
    }
    .section-head h2 {
      margin: 0;
      line-height: 1.2;
    }
    .upload-title-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      width: 100%;
      min-height: 30px;
    }`
