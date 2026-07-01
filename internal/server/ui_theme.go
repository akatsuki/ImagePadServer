package server

const uiThemeBootstrap = `  <script>
    (function () {
      var choice = localStorage.getItem('imagepad-theme') || 'auto';
      var dark = choice === 'dark' || (choice === 'auto' && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches);
      document.documentElement.dataset.theme = dark ? 'dark' : 'light';
      document.documentElement.dataset.themeChoice = choice;
    })();
  </script>
`
