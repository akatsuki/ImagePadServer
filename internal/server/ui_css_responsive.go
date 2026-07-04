package server

const dashboardCSSResponsive = `
    @media (max-width: 1240px) {
      main {
        grid-template-columns: minmax(0, 1fr);
        grid-template-areas:
          "content"
          "preview";
        height: auto;
        min-height: 0;
        overflow: visible;
      }
      .status-panel {
        grid-template-columns: 1fr;
      }
      .status-panel .status {
        grid-template-columns: repeat(2, minmax(0, 1fr));
      }
      .wing-list {
        max-height: none;
      }
    }
    @media (max-width: 860px) {
      main {
        grid-template-columns: 1fr;
        grid-template-areas:
          "upload"
          "preview"
          "history";
        height: auto;
        min-height: 0;
        overflow: visible;
      }
      .content {
        display: contents;
      }
      .content > .hero-panel {
        grid-area: upload;
      }
      .content > .history {
        grid-area: history;
      }
      .preview-column {
        grid-area: preview;
      }
      .preview-column .hero-panel,
      .history section {
        height: auto;
      }
      .phone-connect-button {
        display: none;
      }
      .controls { grid-template-columns: 1fr; }
      .flow-grid {
        grid-template-columns: 1fr;
      }
      .music-pane-main,
      .music-pane-main.playlist,
      .music-control-grid,
      .music-saved-list {
        grid-template-columns: 1fr;
      }
      .music-mode-tabs {
        grid-template-columns: 1fr;
      }
      .music-track-row {
        grid-template-columns: 42px minmax(0, 1fr);
      }
      .music-track-row button {
        grid-column: 1 / -1;
      }
      .flow-primary {
        border-right: 0;
        border-bottom: 1px solid var(--line);
      }
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
        min-height: 280px;
      }
      .preview.obs-preview {
        min-height: 0;
      }
      .wing-list {
        max-height: none;
      }
      .status-panel .status,
      .assist-dock {
        grid-template-columns: 1fr;
      }
      .settings-row {
        align-items: stretch;
        flex-direction: column;
      }
      .drop-zone {
        min-height: 164px;
      }
      .flow-primary,
      .flow-secondary {
        padding: 16px;
      }
      .flow-secondary .upload-actions #uploadButton {
        min-height: 54px;
      }
      .upload-title-row {
        align-items: flex-start;
        flex-direction: column;
        min-height: 0;
        gap: 8px;
      }
      .upload-title-row h2 {
        line-height: 1.2;
      }
      .section-head {
        align-items: flex-start;
        justify-content: flex-start;
        text-align: left;
        min-height: 0;
        gap: 6px;
        padding: 12px;
      }
      .section-title-copy,
      .section-kicker {
        text-align: left;
      }
      .section-kicker {
        line-height: 1.45;
      }
      .media-kind-switch {
        width: 100%;
        align-self: stretch;
      }
      .preview-body {
        padding: 12px;
      }
      .preview-controls,
      .video-quality-options {
        grid-template-columns: 1fr;
      }
      .preview-actions {
        grid-template-columns: repeat(2, minmax(0, 1fr));
      }
      .preview-panel .preview {
        min-height: 260px;
      }
      .phone-dialog-grid {
        grid-template-columns: 1fr;
      }
      .phone-dialog-qr {
        max-width: 300px;
        justify-self: center;
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
      details:not(.advanced-options)[open] {
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
  `
