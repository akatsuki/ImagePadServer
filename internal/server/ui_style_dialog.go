package server

const uiStyleDialog = `    .app-dialog-root {
      position: fixed;
      inset: 0;
      z-index: 10000;
      pointer-events: none;
    }
    .app-dialog-backdrop {
      position: absolute;
      inset: 0;
      background: rgba(15, 28, 25, 0.55);
      backdrop-filter: blur(2px);
      pointer-events: auto;
    }
    .app-dialog-window {
      position: absolute;
      top: 50%;
      left: 50%;
      width: min(92vw, 430px);
      max-height: min(82vh, 520px);
      display: grid;
      grid-template-rows: auto minmax(0, 1fr) auto;
      gap: 12px;
      padding: 18px;
      transform: translate(-50%, -50%);
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--panel);
      color: var(--ink);
      box-shadow: 0 18px 50px rgba(0,0,0,0.25);
      pointer-events: auto;
    }
    .app-dialog-window[hidden],
    .app-dialog-backdrop[hidden] {
      display: none !important;
    }
    .app-dialog-window.danger {
      border-color: var(--danger);
    }
    .app-dialog-header h2 {
      margin: 0;
      font-size: 16px;
    }
    .app-dialog-body {
      display: grid;
      gap: 12px;
      overflow: auto;
    }
    .app-dialog-body p {
      margin: 0;
      color: var(--text);
      font-size: 13px;
      line-height: 1.55;
      white-space: pre-wrap;
    }
    .app-dialog-progress {
      display: grid;
      gap: 8px;
    }
    .app-dialog-actions {
      display: flex;
      justify-content: end;
      gap: 8px;
    }
    .app-status-stack {
      position: absolute;
      right: clamp(12px, 2.4vw, 22px);
      bottom: 12px;
      width: min(92vw, 360px);
      display: grid;
      gap: 8px;
      pointer-events: none;
    }
    .app-status-card {
      display: grid;
      gap: 5px;
      padding: 10px 12px;
      border: 1px solid var(--line);
      border-left: 4px solid var(--accent);
      border-radius: 8px;
      background: var(--panel);
      color: var(--ink);
      box-shadow: 0 10px 28px rgba(0,0,0,.18);
      pointer-events: auto;
    }
    .app-status-card.warning {
      border-left-color: var(--warning);
    }
    .app-status-card.danger {
      border-left-color: var(--danger);
    }
    .app-status-card strong {
      font-size: 13px;
    }
    .app-status-card span {
      color: var(--muted);
      font-size: 12px;
      line-height: 1.4;
      overflow-wrap: anywhere;
    }
`
