package server

const uiStyleResponsive = `    @media (max-width: 860px) {
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
`
