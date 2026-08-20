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

      async function selectHistoryItem(id) {
        return selectHistory(id);
      }

      async function toggleFavoriteHistoryItem(id, favorite) {
        return setHistoryFavorite(id, favorite);
      }

      async function queueHistoryItem(id) {
        return queueHistory(id);
      }

      async function setPublishedHistoryItem(id, published) {
        try {
          const res = await apiFetch('/api/history/publish', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id, published, revision: state.publishedRevision || 0 })
          });
          if (!res.ok) throw new Error(await res.text());
          const payload = await res.json();
          state.history = Array.isArray(payload) ? payload : (payload.history || []);
          if (!Array.isArray(payload) && Number.isFinite(payload.publishedRevision)) {
            state.publishedRevision = payload.publishedRevision;
          }
          HistoryController.render(state);
          toast.textContent = published ? '公開しました' : '非公開にしました';
        } catch (error) {
          toast.textContent = error.message || '公開状態の更新に失敗しました';
        }
      }

      async function copyAddressHistoryItem(id) {
        const item = (state.history || []).find((it) => it.id === id);
        if (!item || !item.address) return;
        try {
          const address = new URL(item.address, window.location.href).href;
          await copyText(address, document.body);
          toast.textContent = 'アドレスをコピーしました';
        } catch (error) {
          toast.textContent = 'コピーに失敗しました';
        }
      }

      return {
        init: initHistoryController,
        render: renderHistoryController,
        selectHistoryItem,
        toggleFavoriteHistoryItem,
        queueHistoryItem,
        setPublishedHistoryItem,
        copyAddressHistoryItem
      };
    })();
`
