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
    .airplay-actions {
      display: flex;
      flex-wrap: wrap;
      gap: .55rem;
      margin-top: .8rem;
    }
`
