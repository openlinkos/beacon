if (navigator.clipboard) {
  for (const button of document.querySelectorAll('[data-copy-source]')) {
    const source = document.getElementById(button.dataset.copySource);
    const status = document.getElementById(button.dataset.copySource + '-status');
    button.hidden = false;
    button.addEventListener('click', async () => {
      try {
        await navigator.clipboard.writeText(source.textContent);
        status.textContent = button.dataset.copied;
      } catch {
        status.textContent = button.dataset.copyError;
      }
    });
  }
}
