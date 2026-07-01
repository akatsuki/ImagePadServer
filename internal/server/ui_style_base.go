package server

const uiStyleBase = `  <style>
    :root {
      color-scheme: light dark;
      --background: #f5f7f8;
      --surface: #ffffff;
      --surface-subtle: #f8fafb;
      --text: #17202a;
      --text-muted: #607080;
      --border: #d8e0e6;
      --accent: #1b7f6b;
      --accent-strong: #146554;
      --danger: #b83b2f;
      --danger-strong: #963025;
      --warning: #9a6418;
      --success: #1b7f6b;
      --focus-ring: #2a8fda;
      --overlay: rgba(23, 32, 42, .52);
      --soft: #eaf4f1;
      --control: #ffffff;
      --button-secondary: #405564;
      --progress-track: #d6e1e6;
      --bg: var(--background);
      --panel: var(--surface);
      --ink: var(--text);
      --muted: var(--text-muted);
      --line: var(--border);
      --accent-2: var(--danger);
    }
    html[data-theme="dark"] {
      --background: #10161b;
      --surface: #182128;
      --surface-subtle: #202b33;
      --text: #edf3f5;
      --text-muted: #b6c5cc;
      --border: #34434c;
      --accent: #43b59f;
      --accent-strong: #6ed2c0;
      --danger: #e06b5f;
      --danger-strong: #f08a80;
      --warning: #e2b15f;
      --success: #55c9a8;
      --focus-ring: #7bc8ff;
      --overlay: rgba(4, 8, 11, .68);
      --soft: #17342f;
      --control: #111a20;
      --button-secondary: #526977;
      --progress-track: #2a3942;
    }
    * { box-sizing: border-box; }
    [hidden] { display: none !important; }
    body {
      margin: 0;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: var(--bg);
      color: var(--ink);
    }
    header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
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
      background: var(--danger);
      color: #fff;
      font-weight: 700;
    }
    .quit-button:hover { background: var(--danger-strong); }
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
      background: var(--overlay);
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
      background: var(--surface-subtle);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 6px;
    }
    code {
      overflow-wrap: anywhere;
      font-size: 12px;
      color: var(--text);
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
      background: var(--button-secondary);
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
      background: var(--control);
      color: var(--ink);
      padding: 6px 8px;
      font: inherit;
      font-size: 13px;
    }
    button:focus-visible,
    input:focus-visible,
    select:focus-visible,
    .mode-tab:focus-visible,
    .wing-tab:focus-visible,
    .icon-button:focus-visible {
      outline: 3px solid var(--focus-ring);
      outline-offset: 2px;
    }
    .theme-control {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
    }
    .theme-control select {
      width: auto;
      min-height: 30px;
      padding: 4px 8px;
    }
`
