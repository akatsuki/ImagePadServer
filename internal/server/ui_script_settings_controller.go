package server

const dashboardScriptSettingsController = `
    const SettingsController = (() => {
      let deps = {};

      function initSettingsController(nextDeps) {
        deps = nextDeps || {};
      }

      function openSettingsController() {
        showSettingsModal();
      }

      function closeSettingsController() {
        hideSettingsModal();
      }

      function openPhoneConnectController() {
        showPhoneConnectDialog();
      }

      function closePhoneConnectController() {
        hidePhoneConnectDialog();
      }

      function applyThemePreferenceController(preference, persist) {
        applyThemePreference(preference, persist);
      }

      return {
        init: initSettingsController,
        openSettings: openSettingsController,
        closeSettings: closeSettingsController,
        openPhoneConnect: openPhoneConnectController,
        closePhoneConnect: closePhoneConnectController,
        applyThemePreference: applyThemePreferenceController
      };
    })();
`
