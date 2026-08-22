package server

const dashboardScriptMusicController = `
    const MusicController = (() => {
      let mode = "single";

      function currentMusicMode() {
        return mode;
      }

      function musicModeLabel() {
        if (mode === "playlist") return "プレイリスト";
        return "シングル";
      }

      function setMusicMode(nextMode, activate) {
        mode = nextMode === "playlist" ? "playlist" : "single";
        if (activate !== false) {
          setMediaIntent("music");
          return;
        }
        renderMusicController({ active: mediaIntent === "music" });
        if (typeof updateMediaNavigation === "function") updateMediaNavigation();
      }

      function renderMusicController(context) {
        const active = !!(context && context.active && musicWorkspaceEnabled);
        const playlistActive = active && mode === "playlist";
        if (flowGrid) flowGrid.hidden = false;
        if (musicPlaylistPanel) musicPlaylistPanel.hidden = !active || mode === "single";
        PreviewController.setVisible(!active || mode === "single");
        if (!active && typeof updateMediaNavigation === "function") updateMediaNavigation();
        if (typeof PlaylistController !== "undefined") PlaylistController.setActive(playlistActive);
      }

      function initMusicController() {
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
