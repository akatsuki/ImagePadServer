package server

const dashboardScriptMusicController = `
    const MusicController = (() => {
      let deps = {};
      let mode = 'single';

      function currentMusicMode() {
        return mode;
      }

      function musicModeLabel() {
        if (mode === 'playlist') return 'プレイリスト';
        if (mode === 'party') return 'パーティーモード';
        return 'シングル';
      }

      function setMenuOpen(open) {
        if (!musicModeMenu || !musicIntentButton) return;
        musicModeMenu.hidden = !open;
        musicIntentButton.setAttribute('aria-expanded', String(open));
      }

      function toggleMenu() {
        if (!musicWorkspaceEnabled) return;
        setMenuOpen(!!musicModeMenu && musicModeMenu.hidden);
      }

      function setMusicMode(nextMode, activate) {
        mode = nextMode === 'playlist' || nextMode === 'party' ? nextMode : 'single';
        setMenuOpen(false);
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
        if (!active) setMenuOpen(false);
      }

      function initMusicController(nextDeps) {
        deps = nextDeps || {};
        if (musicIntentButton) {
          musicIntentButton.addEventListener('click', (event) => {
            event.preventDefault();
            event.stopPropagation();
            toggleMenu();
          });
        }
        if (musicModeMenu) {
          musicModeMenu.addEventListener('click', (event) => {
            const button = event.target && event.target.closest ? event.target.closest('[data-music-mode-choice]') : null;
            if (!button) return;
            event.preventDefault();
            event.stopPropagation();
            setMusicMode(button.dataset.musicModeChoice);
          });
        }
        document.addEventListener('click', (event) => {
          if (!musicModeMenu || musicModeMenu.hidden) return;
          if (musicModeMenu.contains(event.target) || musicIntentButton.contains(event.target)) return;
          setMenuOpen(false);
        });
        document.addEventListener('keydown', (event) => {
          if (event.key === 'Escape') setMenuOpen(false);
        });
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
