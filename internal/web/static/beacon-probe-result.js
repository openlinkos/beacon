// A fragment can focus a table's result before the narrow layout finishes shifting its row.
// Keep that result visible without changing focus or interrupting someone who has moved on.
window.addEventListener('load', async () => {
  await document.fonts.ready;
  const result = document.querySelector('.bc-alert-observations > summary:target');
  if (!result || document.activeElement !== result) return;
  const bounds = result.getBoundingClientRect();
  const header = document.querySelector('.ol-topbar');
  if (bounds.top < header.getBoundingClientRect().bottom || bounds.bottom > window.innerHeight) {
    result.scrollIntoView({block: 'start'});
  }
}, {once: true});
