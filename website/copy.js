// Copy-to-clipboard for every .cmd box. The button content (icon) is injected
// here so the HTML stays free of repeated SVG markup.
(function () {
  'use strict';

  const COPY_ICON =
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<rect width="14" height="14" x="8" y="8" rx="2" ry="2"></rect>' +
    '<path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"></path></svg>';
  const DONE_ICON =
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M20 6 9 17l-5-5"></path></svg>';
  const RESET_MS = 1500;

  function markCopied(button) {
    button.innerHTML = DONE_ICON;
    button.setAttribute('data-copied', '');
    window.setTimeout(function () {
      button.innerHTML = COPY_ICON;
      button.removeAttribute('data-copied');
    }, RESET_MS);
  }

  function copyText(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text);
    }
    return Promise.reject(new Error('Clipboard API unavailable'));
  }

  function wire(box) {
    const button = box.querySelector('button');
    const text = box.getAttribute('data-copy');
    if (!button || !text) return;
    button.innerHTML = COPY_ICON;
    button.addEventListener('click', function () {
      copyText(text).then(
        function () { markCopied(button); },
        function (err) { console.warn('copy failed:', err); }
      );
    });
  }

  document.querySelectorAll('.cmd[data-copy]').forEach(wire);
})();
