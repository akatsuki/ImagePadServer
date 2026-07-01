package server

const uiDialogMarkup = `  <div class="app-dialog-root" id="appDialogRoot">
    <div class="app-dialog-backdrop" id="appDialogBackdrop" hidden></div>
    <section class="app-dialog-window" id="appDialogWindow" role="dialog" aria-modal="true" aria-labelledby="appDialogTitle" aria-describedby="appDialogMessage" tabindex="-1" hidden>
      <div class="app-dialog-header">
        <h2 id="appDialogTitle">確認</h2>
      </div>
      <div class="app-dialog-body">
        <p id="appDialogMessage"></p>
        <div class="app-dialog-progress" id="appDialogProgress" hidden>
          <div class="progress-track" aria-label="進捗">
            <div class="progress-fill" id="appDialogProgressFill" style="width:6%"></div>
          </div>
          <div class="progress-detail" id="appDialogProgressDetail"></div>
        </div>
      </div>
      <div class="app-dialog-actions" id="appDialogActions"></div>
    </section>
    <div class="app-status-stack" id="appStatusStack" aria-live="polite" aria-atomic="false"></div>
  </div>
`
