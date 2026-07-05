package server

const dashboardScriptMusicController = `
    const MusicController = (() => {
      let deps = {};
      let mode = 'single';

      function currentMusicMode() {
        return mode;
      }

      function musicModeLabel() {
        return 'シングル';
      }

      function setMusicMode(nextMode, activate) {
        mode = 'single';
        if (activate !== false) {
          setMediaIntent('music');
          return;
        }
        renderMusicController({ active: mediaIntent === 'music' });
      }

      function renderMusicController(context) {
        const active = !!(context && context.active && musicWorkspaceEnabled);
        if (flowGrid) flowGrid.hidden = false;
        if (musicPlaylistPanel) musicPlaylistPanel.hidden = !active || mode === 'single';
        PreviewController.setVisible(!active || mode === 'single');
        (deps.choiceButtons || []).forEach((button) => {
          const selected = button.dataset.musicModeChoice === mode;
          button.classList.toggle('active', selected);
          button.setAttribute('aria-checked', String(selected));
        });
      }

      function initMusicController(nextDeps) {
        deps = nextDeps || {};
        if (musicIntentButton) {
          musicIntentButton.addEventListener('click', (event) => {
            event.preventDefault();
            setMusicMode('single');
          });
        }
        renderMusicController({ active: false });
      }

      return {
        init: initMusicController,
        render: renderMusicController,
        setMode: setMusicMode,
        mode: currentMusicMode,
        label: musicModeLabel
      };
    })();
`
