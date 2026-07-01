package server

const uiScriptDialog = `    function createDialogController() {
      const root = document.getElementById('appDialogRoot');
      const backdrop = document.getElementById('appDialogBackdrop');
      const dialogWindow = document.getElementById('appDialogWindow');
      const title = document.getElementById('appDialogTitle');
      const message = document.getElementById('appDialogMessage');
      const progress = document.getElementById('appDialogProgress');
      const progressFill = document.getElementById('appDialogProgressFill');
      const progressDetail = document.getElementById('appDialogProgressDetail');
      const actions = document.getElementById('appDialogActions');
      const statusStack = document.getElementById('appStatusStack');
      let resolver = null;
      let lastFocus = null;
      let currentKey = '';

      function setProgress(options) {
        const visible = !!options.progress;
        progress.hidden = !visible;
        if (!visible) return;
        const percent = Math.max(0, Math.min(100, Number(options.percent || 0)));
        progressFill.classList.toggle('indeterminate', !!options.indeterminate);
        progressFill.style.width = options.indeterminate ? '' : Math.max(6, percent) + '%';
        progressDetail.textContent = options.detail || '';
      }

      function openWindow(kind, options) {
        if (!root || !dialogWindow) return;
        currentKey = options.key || kind;
        lastFocus = document.activeElement;
        dialogWindow.classList.toggle('danger', !!options.danger);
        title.textContent = options.title || '';
        message.textContent = options.message || '';
        setProgress(options);
        actions.innerHTML = '';
        backdrop.hidden = false;
        dialogWindow.hidden = false;
        dialogWindow.focus();
      }

      function closeWindow(result) {
        if (!dialogWindow || dialogWindow.hidden) return;
        const done = resolver;
        resolver = null;
        currentKey = '';
        dialogWindow.hidden = true;
        backdrop.hidden = true;
        progress.hidden = true;
        actions.innerHTML = '';
        if (lastFocus && typeof lastFocus.focus === 'function') {
          lastFocus.focus();
        }
        if (done) done(result);
      }

      function addButton(label, className, result) {
        const button = document.createElement('button');
        button.type = 'button';
        button.textContent = label;
        if (className) button.className = className;
        button.addEventListener('click', () => closeWindow(result));
        actions.appendChild(button);
        return button;
      }

      return {
        confirm(options) {
          return new Promise((resolve) => {
            resolver = resolve;
            openWindow('confirm', options || {});
            addButton((options && options.cancelText) || 'キャンセル', 'secondary', false);
            const confirm = addButton((options && options.confirmText) || 'OK', (options && options.danger) ? 'warn' : '', true);
            confirm.focus();
          });
        },
        showProgress(key, options) {
          openWindow('progress', Object.assign({}, options, { key, progress: true }));
          actions.innerHTML = '';
        },
        showStatus(key, options) {
          if (!statusStack) return;
          let card = statusStack.querySelector('[data-status-key="' + key + '"]');
          if (!card) {
            card = document.createElement('div');
            card.className = 'app-status-card';
            card.dataset.statusKey = key;
            card.innerHTML = '<strong></strong><span></span>';
            statusStack.appendChild(card);
          }
          card.className = 'app-status-card ' + ((options && options.state) || '');
          card.querySelector('strong').textContent = (options && options.title) || '';
          card.querySelector('span').textContent = (options && options.message) || '';
        },
        close(key) {
          if (!key || key === currentKey) closeWindow(false);
          if (statusStack && key) {
            const card = statusStack.querySelector('[data-status-key="' + key + '"]');
            if (card) card.remove();
          }
        }
      };
    }

    const dialog = createDialogController();
`
