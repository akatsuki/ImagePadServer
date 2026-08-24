package server

const dashboardScriptInputTabs = `
    function bindInputTabKeyboardNavigation(tablist) {
      if (!tablist) return;
      tablist.addEventListener('keydown', (event) => {
        if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight' && event.key !== 'Home' && event.key !== 'End') return;
        const tabs = Array.from(tablist.children).filter((button) =>
          button.getAttribute('role') === 'tab' && !button.hidden && !button.disabled
        );
        if (tabs.length === 0) return;
        const currentIndex = Math.max(0, tabs.indexOf(document.activeElement));
        let nextIndex = currentIndex;
        if (event.key === 'ArrowLeft') nextIndex = (currentIndex - 1 + tabs.length) % tabs.length;
        if (event.key === 'ArrowRight') nextIndex = (currentIndex + 1) % tabs.length;
        if (event.key === 'Home') nextIndex = 0;
        if (event.key === 'End') nextIndex = tabs.length - 1;
        event.preventDefault();
        const target = tabs[nextIndex];
        target.focus();
        target.click();
      });
    }

    document.querySelectorAll('.mode-tabs, .playlist-input-tabs').forEach(bindInputTabKeyboardNavigation);
`
