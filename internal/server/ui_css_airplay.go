package server

const dashboardCSSAirPlay = `
    .airplay-card {
      margin-top: 1rem;
      padding: 1rem;
      border: 1px solid color-mix(in srgb, var(--accent) 32%, var(--border));
      border-radius: 1rem;
      background: linear-gradient(135deg, color-mix(in srgb, var(--accent) 11%, var(--panel)), var(--panel));
    }
    .airplay-card-heading {
      display: flex;
      align-items: flex-start;
      gap: .8rem;
    }
    .airplay-graphic {
      flex: 0 0 auto;
      width: 3rem;
      height: 3rem;
      color: var(--accent);
    }
    .airplay-card h3 {
      margin: 0;
      font-size: 1rem;
    }
    .airplay-card p {
      margin: .3rem 0 0;
      color: var(--muted);
      font-size: .86rem;
      line-height: 1.45;
    }
    .airplay-status {
      margin: .85rem 0 .25rem !important;
      color: var(--muted);
    }
    .airplay-status.is-running {
      color: var(--success, #41c98a);
    }
    .airplay-status.is-error {
      color: var(--warning, #e5a34a);
    }
    .airplay-receiver-path {
      display: block;
      min-height: 1.1rem;
      overflow-wrap: anywhere;
      color: var(--muted);
      font-size: .72rem;
    }
    .airplay-video-view-option {
      margin-top: .8rem;
    }
    .airplay-video-view-toggle {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      min-height: 44px;
      cursor: pointer;
    }
    .airplay-video-view-toggle input {
      appearance: none;
      position: relative;
      flex: 0 0 44px;
      width: 44px;
      height: 26px;
      min-height: 26px;
      margin: 0;
      padding: 0;
      border: 1px solid var(--line);
      border-radius: 999px;
      background: var(--line);
      cursor: pointer;
    }
    .airplay-video-view-toggle input::before {
      content: '';
      position: absolute;
      width: 18px;
      height: 18px;
      left: 3px;
      top: 3px;
      border-radius: 50%;
      background: #fff;
      transition: transform .16s ease;
    }
    .airplay-video-view-toggle input:checked {
      background: var(--accent);
      border-color: var(--accent);
    }
    .airplay-video-view-toggle input:checked::before {
      transform: translateX(18px);
    }
    .airplay-video-view-toggle input:focus-visible {
      outline: 2px solid var(--accent);
      outline-offset: 3px;
    }
    .airplay-video-view-status {
      margin: .1rem 0 0 !important;
    }
    .airplay-actions {
      display: flex;
      flex-wrap: wrap;
      gap: .55rem;
      margin-top: .8rem;
    }
`
