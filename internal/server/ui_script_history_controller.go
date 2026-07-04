package server

const dashboardScriptHistoryController = `
    const HistoryController = (() => {
      let deps = {};

      function initHistoryController(nextDeps) {
        deps = nextDeps || {};
      }

      function renderHistoryController(data) {
        const source = data || state || {};
        const items = source.history || [];
        const currentID = source.current ? source.current.id : (source.currentID || '');
        renderHistory(items, currentID);
      }

      async function publishHistoryItem(id) {
        return selectHistory(id);
      }

      async function toggleFavoriteHistoryItem(id, favorite) {
        return setHistoryFavorite(id, favorite);
      }

      async function queueHistoryItem(id) {
        return queueHistory(id);
      }

      return {
        init: initHistoryController,
        render: renderHistoryController,
        publishHistoryItem,
        toggleFavoriteHistoryItem,
        queueHistoryItem
      };
    })();
`
