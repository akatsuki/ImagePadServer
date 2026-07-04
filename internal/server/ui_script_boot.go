package server

const themeBootScript = `
    (() => {
      try {
        const pref = localStorage.getItem('imagepad:themePreference') || 'system';
        const dark = pref === 'dark' || (pref !== 'light' && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
        document.documentElement.dataset.theme = dark ? 'dark' : 'light';
        document.documentElement.dataset.themePreference = pref === 'light' || pref === 'dark' ? pref : 'system';
      } catch (error) {
      }
    })();
  `
