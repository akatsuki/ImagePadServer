package server

const uiStyleHistory = `    .wing-tabs {
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
      min-height: 18px;
      color: var(--accent);
      font-weight: 700;
      font-size: 13px;
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
    .pairing-title {
      margin: 0 0 6px;
      color: var(--text);
      font-size: 14px;
      font-weight: 800;
    }
    .pairing-pin {
      display: block;
      margin: 4px 0 8px;
      color: var(--text);
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
`
